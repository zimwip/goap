package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/events"
	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/graph/indexersvc"
	"github.com/zimwip/goap/lib/llmcfg"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
	"github.com/zimwip/goap/mcp/connectorkit"
	"github.com/zimwip/goap/mcp/connectors/claudeagent"
	"github.com/zimwip/goap/mcp/connectors/localfs"
	"github.com/zimwip/goap/mcp/connectors/workspace"
	"github.com/zimwip/goap/mcp/mcpsvc"
	"github.com/zimwip/goap/platform/modelgw"
	"github.com/zimwip/goap/platform/registrysvc"
)

// platformPart is what the engine and the handlers reach out with: the model gateway, the node index and the MCP hub.
type platformPart struct {
	gw         *modelgw.Service
	indexer    *indexersvc.Service
	indexSink  *indexersvc.Sink
	hub        *mcpsvc.Service
	connectors map[string]connectorkit.Connector
	connToken  string
	// models is the gateway's Anthropic endpoint, where the Claude coding agent's sessions call the model (ADR 0123 §6)
	models *modelgw.AnthropicProxy
}

// buildPlatform creates the model gateway, the node index following the graph and the MCP hub. The built-in
// connectors join the hub's map once the engine exists (buildEngine).
func buildPlatform(e *env, st stores, gp *graphPart, rp *registryPart) (*platformPart, error) {
	g := gp.g
	// devlocal (ADR 0010): no Vault running, so a raw key an admin asks to be vaulted falls back to a local
	// file next to the SQLite database (platform.Secrets.Dir), the same convenience as the generated JWT/connector
	// secrets (localSecret); ignored once a real Vault is configured (VAULT_ADDR).
	e.secrets.Dir = st.dir
	gw := modelgw.NewService(&llmcfg.Directory{Graph: g, Reader: gp.reader}, st.models, e.secrets.Resolve, e.log)
	gw.Vault = e.secrets.Put
	genai := telemetry.NewGenAI()
	gw.Router.Instrument, gw.Router.InstrumentEmbed = genai.Instrument, genai.InstrumentEmbed
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
	// the events of the graph go through its outbox (ADR 0136), delivered in process: to the index, the event stream of
	// the web and the triggers (late bound: they need the engine)
	relay := g.Observe(outbox.Sinks{
		// the organisation moved: the directory reads its snapshot again (ADR 0154 §9)
		outbox.SinkFunc(func(_ context.Context, m outbox.Message) error {
			if m.Subject == baselineMain {
				gp.directory.Moved(m.Data)
			}
			return nil
		}),
		indexSink,
		outbox.SinkFunc(func(ctx context.Context, m outbox.Message) error {
			rp.bus.Ingest(m.Subject, m.Data, platform.StampOf(ctx))
			return nil
		}),
		engine.ChangeEvents(e.triggers.onChange),
		// a methodology published reloads the triggers: its event rides on the graph commit storing it (ADR 0136)
		outbox.SinkFunc(func(ctx context.Context, m outbox.Message) error {
			if m.Subject == "goap.registry.methodology.published" {
				var ev registrysvc.MethodologyEvent
				if json.Unmarshal(m.Data, &ev) == nil {
					e.triggers.handle(ctx, engine.TriggerEvent{Type: events.MethodologyPublished, Methodology: ev.Name, Version: ev.Version})
				}
			}
			return nil
		})})
	e.emit(relay)
	go func() {
		if n, err := indexer.Republish(e.ctx); err != nil {
			e.log.Error("index: initial publication", "err", err)
		} else {
			e.log.Info("node index: graph published", "versions", n)
		}
	}()
	// MCP hub in-process; the local file system, workspace (ADR 0118) and Claude coding agent (ADR 0123) connectors and
	// the built-in connectors (ADR 0028, added once the engine exists) run inside too, other connectors register over
	// HTTP like in the distributed platform
	connectors := map[string]connectorkit.Connector{"localfs": localfs.Connector{}, workspace.ID: workspace.New(),
		workspace.DockerID: workspace.NewDocker(e.cfg.DockerHost), claudeagent.ID: claudeagent.New(e.cfg.ClaudeAgentDir)}
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
		Directory: &mcpsvc.Directory{Graph: g, Reader: gp.reader},
		Invoker:   mcpsvc.InprocInvoker{Connectors: connectors, Remote: &mcpsvc.ConnectInvoker{Token: connectorToken}},
		Secrets:   mcpsvc.ResolveSecret(e.secrets),
		Lease:     platform.EnvDuration("GOAP_CONNECTOR_LEASE", mcpsvc.DefaultLease),
	}
	// the hub's events (a connector registered, lost, back) and the gateway's (a call recorded, a quota exceeded) go
	// through their outboxes to the event stream (ADR 0136)
	e.emit(hub.Emit(busSink(rp.bus)))
	go hub.Watch(e.ctx, 0)
	e.emit(gw.Emit(busSink(rp.bus)))
	// the sessions of the Claude coding agent call the model through the gateway, each call a row of the ledger (ADR
	// 0123 §6): the hub gives each call a token the gateway checks, signed with a secret generated like the others
	proxySecret := e.cfg.ModelProxySecret
	if proxySecret == "" {
		var err error
		if proxySecret, err = localSecret(st.dir, "model_proxy_secret"); err != nil {
			return nil, wrap("model proxy secret", err)
		}
	}
	proxyURL := e.cfg.ModelProxyURL
	if proxyURL == "" {
		proxyURL = localURL(e.cfg.Addr) + strings.TrimSuffix(modelgw.AnthropicPath, "/")
	}
	hub.Models = &mcpsvc.ModelEndpoint{URL: proxyURL, Secret: []byte(proxySecret)}
	models := &modelgw.AnthropicProxy{Service: gw, Secret: []byte(proxySecret)}
	return &platformPart{gw: gw, indexer: indexer, indexSink: indexSink, hub: hub, connectors: connectors, connToken: connectorToken, models: models}, nil
}

// baselineMain is the subject of the baseline events of the main branches.
var baselineMain = fmt.Sprintf(domain.SubjectBaselineAdvanced, domain.MainBranch)
