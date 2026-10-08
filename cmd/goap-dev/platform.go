package main

import (
	"context"
	"path/filepath"
	"time"

	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/internal/connectors/localfs"
	"github.com/zimwip/goap/internal/indexersvc"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/llmcfg"
)

// platformPart is what the engine and the handlers reach out with: the model gateway, the node index and the MCP hub.
type platformPart struct {
	gw         *modelgw.Service
	indexer    *indexersvc.Service
	indexSink  *indexersvc.Sink
	hub        *mcpsvc.Service
	connectors map[string]connectorkit.Connector
	connToken  string
}

// buildPlatform creates the model gateway, the node index following the graph and the MCP hub. The built-in
// connectors join the hub's map once the engine exists (buildEngine).
func buildPlatform(e *env, st stores, gp *graphPart, rp *registryPart) (*platformPart, error) {
	g := gp.g
	// devlocal (ADR 0010): no Vault running, so a raw key an admin asks to be vaulted falls back to a local
	// file next to the SQLite database (platform.Secrets.Dir), the same convenience as the generated JWT/connector
	// secrets (localSecret); ignored once a real Vault is configured (VAULT_ADDR).
	e.secrets.Dir = st.dir
	gw := modelgw.NewService(&llmcfg.Directory{Graph: g}, st.models, e.secrets.Resolve, e.log)
	gw.Vault = e.secrets.Put
	gw.Router.Instrument = telemetry.NewGenAI().Instrument
	gw.Authz = gp.authorizer // the roles a model requires are held on the caller's project (ADR 0043)
	if err := gw.Reload(e.ctx); err != nil {
		return nil, wrap("models", err)
	}
	gw.CostTTL = time.Duration(platform.EnvInt("GOAP_LLM_BEHAVIOR_COST_TTL_DAYS", int(modelgw.DefaultCostTTL/(24*time.Hour)))) * 24 * time.Hour // a measured behaviour cost is measured again after this (ADR 0093)
	gw.CallPrompts = platform.EnvBool("GOAP_LLM_CALL_PROMPTS", true)                                                                            // the exchange of the calls no change log keeps (ADR 0089)
	go gw.KeepCalls(e.ctx, platform.EnvInt("GOAP_LLM_CALL_RETENTION_DAYS", modelgw.DefaultCallRetentionDays))                                   // the ledger of calls (ADR 0089)
	// the node index follows the graph in-process (ADR 0026); embeddings go through the gateway, semantic search
	// needs an "embed" alias. The graph is published again at start: an index kept in SQLite catches up, a new one fills.
	indexer := indexersvc.New(st.index, gw, gp.authorizer, e.log)
	indexer.Access = gp.directory // the projects a caller may see, and a project with its sub-projects (ADR 0095)
	indexSink := indexersvc.NewSink(e.ctx, indexer)
	indexer.Republish = func(ctx context.Context) (int, error) { return g.Republish(ctx, indexSink) }
	g.Observe(engine.Publishers{indexSink, rp.bus})
	go func() {
		if n, err := indexer.Republish(e.ctx); err != nil {
			e.log.Error("index: initial publication", "err", err)
		} else {
			e.log.Info("node index: graph published", "versions", n)
		}
	}()
	// MCP hub in-process; the local file system connector and the built-in connectors (ADR 0028,
	// added once the engine exists) run inside too, other connectors register over HTTP like in
	// the distributed platform
	connectors := map[string]connectorkit.Connector{"localfs": localfs.Connector{}}
	// connectors register with a shared token (a service without one cannot register): generated like the JWT
	// secret when none is configured, and shown so that a connector started by hand can use it
	connectorToken := e.cfg.ConnectorToken
	if connectorToken == "" {
		var err error
		if connectorToken, err = localSecret(st.dir, "connector_token"); err != nil {
			return nil, wrap("connector token", err)
		}
		e.log.Info("connector token generated: start connectors with GOAP_CONNECTOR_TOKEN set to it", "file", filepath.Join(st.dir, "connector_token"))
	}
	hub := &mcpsvc.Service{
		Store:     st.mcp,
		Directory: &mcpsvc.Directory{Graph: g},
		Invoker:   mcpsvc.InprocInvoker{Connectors: connectors, Remote: &mcpsvc.ConnectInvoker{Token: connectorToken}},
		Secrets:   mcpsvc.ResolveSecret(e.secrets),
		Lease:     platform.EnvDuration("GOAP_CONNECTOR_LEASE", mcpsvc.DefaultLease),
	}
	return &platformPart{gw: gw, indexer: indexer, indexSink: indexSink, hub: hub, connectors: connectors, connToken: connectorToken}, nil
}
