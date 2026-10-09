package main

import (
	"context"

	"github.com/zimwip/goap/internal/devseed"
	"github.com/zimwip/goap/internal/eventsvc"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/events"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/typecat"
)

// registryPart is the registry, the type catalogue the graph judges by, and the one event stream of the web.
type registryPart struct {
	reg   *registrysvc.Service
	types *typecat.Live
	bus   *eventsvc.Hub
}

// buildRegistry creates the registry, wires its hooks on the graph, then runs the seeds in the order of
// graphsvc/boot.go: domains, type catalogue, the platform bootstrap (graphsvc.Boot), methodologies.
func buildRegistry(e *env, gp *graphPart, st stores) (*registryPart, error) {
	g := gp.g
	// methodologies and domains are nodes of the graph: the registry needs no database
	// the scope of the MCPs (ADR 0028) is checked where methodologies declare them
	reg := &registrysvc.Service{Store: registrysvc.NewGraphStore(g), DomainStore: st.domains, Authz: gp.authorizer,
		MCPScopes: (&mcpsvc.Directory{Graph: g}).Scopes}
	// the graph judges nodes by the types of the published domains (ADR 0012): its catalogue follows the registry
	types := typecat.NewLive(reg.Domains)
	g.Types = func() graph.TypeCatalog { return types.Get() }
	// the guardian of the changes (ADR 0098), the engine once it exists (buildEngine): the lifecycle of the methodology
	// of a change and what it freezes, then the rules of the registry: a change scoped to an Activity is gated by its own
	// goal condition at Apply, not the node-type lifecycle's landable-state floor (architecture plan "Activity concept"),
	// its sub-changes stay in its activity and it moves only between projects applying its methodology
	e.guardian.set(registrysvc.Guardian{Service: reg, Directory: gp.directory})
	g.Guardians = map[string]graph.Guardian{engine.GuardianName: e.guardian}
	g.DefaultGuardian = engine.GuardianName
	// a change starts with the main goal of its methodology (ADR 0096)
	g.Defaults = reg
	// the one event stream of the web (ADR 0053): every publication of the platform also feeds it
	bus := eventsvc.NewHub()
	go bus.Run(e.ctx)
	// publications reload the triggers and the type catalogue
	reg.Events = engine.Publishers{bus, registryEvents{
		methodology: func(ctx context.Context, name, version string) {
			e.triggers.handle(ctx, engine.TriggerEvent{Type: events.MethodologyPublished, Methodology: name, Version: version})
		},
		domain: func(ctx context.Context, name, version string) {
			if err := types.Reload(ctx); err != nil {
				e.log.Error("type catalogue", "domain", name, "err", err)
				return
			}
			e.log.Info("domain published: type catalogue reloaded", "domain", name, "version", version)
		},
	}}
	system := authz.With(e.ctx, authz.System("registry", access.RoleAdmin))
	if _, err := reg.SeedDomains(system, e.cfg.DomainsDir); err != nil {
		return nil, wrap("domains", err)
	}
	if err := types.Reload(e.ctx); err != nil {
		return nil, wrap("type catalogue", err)
	}
	// the one platform bootstrap (graphsvc.Boot, ADR 0071) with the development data of this composition: the ALM demo,
	// the document-repository MCP and, with GOAP_DEV_FS_ROOT, a directory as the default organisation's repository.
	// The gateway configuration is seeded before the methodologies below: publishing a methodology stubs any alias it
	// references that the platform namespace doesn't have yet (registrysvc.ensureAliasStubs), and that stub would
	// otherwise collide with the alias Boot seeds.
	bootModels, err := modelgw.BootConfig(e.ctx, e.cfg.ModelsConfig, e.secrets)
	if err != nil {
		return nil, wrap("models config", err)
	}
	if _, err := graphsvc.Boot(e.ctx, g, graphsvc.Options{Models: bootModels, RequireHooks: true, Log: e.log,
		Dev: func(ctx context.Context, g *graph.Graph) error {
			if _, err := devseed.Demo(ctx, g); err != nil {
				return err
			}
			if _, err := devseed.DocumentRepository(ctx, g); err != nil {
				return err
			}
			if root := e.cfg.DevFSRoot; root != "" {
				return devseed.LocalFS(ctx, g, root)
			}
			return nil
		}}); err != nil {
		return nil, wrap("boot", err)
	}
	if _, err := reg.Seed(system, e.cfg.MethodologiesDir); err != nil {
		return nil, wrap("methodologies", err)
	}
	return &registryPart{reg: reg, types: types, bus: bus}, nil
}
