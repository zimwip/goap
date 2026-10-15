package main

import (
	"context"

	"github.com/zimwip/goap/internal/devseed"
	"github.com/zimwip/goap/internal/domainsvc"
	"github.com/zimwip/goap/internal/eventsvc"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/events"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/typecat"
)

// registryPart is the two registries (the domains, the graph's; the methodologies, the engine's: ADR 0125 §3), the type
// catalogue the graph judges by, and the one event stream of the web.
type registryPart struct {
	domains *domainsvc.Service
	reg     *registrysvc.Service
	types   *typecat.Live
	bus     *eventsvc.Hub
}

// buildRegistry creates the registries, wires their hooks on the graph, then runs the seeds in the order of
// graphsvc/boot.go: domains, type catalogue, the platform bootstrap (graphsvc.Boot), methodologies.
func buildRegistry(e *env, gp *graphPart, st stores) (*registryPart, error) {
	g := gp.g
	// methodologies are nodes of the graph; the domains are kept in the local database (domainsvc)
	// the scope of the MCPs (ADR 0028) is checked where methodologies declare them
	domains := &domainsvc.Service{Store: st.domains, Authz: gp.authorizer}
	reg := &registrysvc.Service{Store: registrysvc.NewGraphStore(g), Domains: domains, Authz: gp.authorizer,
		MCPScopes: (&mcpsvc.Directory{Graph: g}).Scopes, Applicable: applicable(gp.directory)}
	// the graph judges nodes by the types of the published domains (ADR 0012): its catalogue follows the domain registry
	types := typecat.NewLive(domains.Domains)
	g.Types = func() graph.TypeCatalog { return types.Get() }
	g.SubChangeValidator = reg.SubChangeValidator
	// a change follows the lifecycle its methodology names, its gates read the conditions of the methodology (ADR 0058)
	g.Lifecycles = reg
	// the history of a change, read by the conditions and guards the graph evaluates; its steps rely on one another as
	// their methodology says (ADR 0109)
	g.LogFacets = graphsvc.LogFacets(func(ctx context.Context, name string) map[string]map[string]bool {
		m, err := reg.Methodology(ctx, name)
		if err != nil {
			return nil
		}
		return m.StepDependencies()
	})
	// who may perform each operation on a change: the roles of its methodology (ADR 0103)
	g.OperationAuthorizer = graphsvc.OperationAuthorizer(gp.authorizer, reg.OperationRoles)
	// the one event stream of the web (ADR 0053): every publication of the platform also feeds it
	bus := eventsvc.NewHub()
	go bus.Run(e.ctx)
	// publications reload the triggers; a domain published or archived reloads the type catalogue and checks the
	// methodologies again, the ones it breaks being suspended (ADR 0125 §3)
	reg.Events = engine.Publishers{bus, registryEvents{
		methodology: func(ctx context.Context, name, version string) {
			e.triggers.handle(ctx, engine.TriggerEvent{Type: events.MethodologyPublished, Methodology: name, Version: version})
		},
	}}
	domains.Events = bus
	domains.Changed = func(ctx context.Context, r def.Record) {
		if err := types.Reload(ctx); err != nil {
			e.log.Error("type catalogue", "domain", r.Domain.Name, "err", err)
			return
		}
		e.log.Info("domain published: type catalogue reloaded", "domain", r.Domain.Name, "version", r.Domain.Version, "status", r.Status)
		if _, err := reg.DomainsChanged(ctx); err != nil {
			e.log.Error("check the methodologies", "domain", r.Domain.Name, "err", err)
		}
	}
	system := authz.With(e.ctx, authz.System("registry", access.RoleAdmin))
	// every start publishes the versions of the files not stored yet (ADR 0113): a new version of a shipped domain or
	// methodology needs no reset of the database; the changes already open keep the version they follow
	doms, err := domains.SeedDomains(system, e.cfg.DomainsDir)
	if err != nil {
		return nil, wrap("domains", err)
	}
	if len(doms) > 0 {
		e.log.Info("domain versions published", "domains", doms)
	}
	if err := types.Reload(e.ctx); err != nil {
		return nil, wrap("type catalogue", err)
	}
	// the one platform bootstrap (graphsvc.Boot, ADR 0071) with the development data of this composition: the ALM demo,
	// the document-repository MCP and, with GOAP_DEV_FS_ROOT, a directory as the default organisation's repository;
	// with GOAP_DEV_WORKSPACE_ROOT, the workspaces of the changes (ADR 0118), their commands in containers of
	// GOAP_DEV_WORKSPACE_IMAGE when it is set.
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
				if err := devseed.LocalFS(ctx, g, root); err != nil {
					return err
				}
			}
			if root := e.cfg.WorkspaceRoot; root != "" {
				return devseed.Workspace(ctx, g, root, e.cfg.WorkspaceImage)
			}
			return nil
		}}); err != nil {
		return nil, wrap("boot", err)
	}
	loaded, err := reg.Seed(system, e.cfg.MethodologiesDir)
	if err != nil {
		return nil, wrap("methodologies", err)
	}
	if len(loaded) > 0 {
		e.log.Info("methodology versions published", "methodologies", loaded)
	}
	// a new start may break a methodology too (a built-in domain changes with the platform)
	if _, err := reg.Recheck(e.ctx); err != nil {
		return nil, wrap("check the methodologies", err)
	}
	return &registryPart{domains: domains, reg: reg, types: types, bus: bus}, nil
}

// applicable lists the methodologies applicable to a project from the snapshot of the organisation (ADR 0106: where
// the guardian chain of a change finds the methodologies integrating its own).
func applicable(d *access.Directory) func(ctx context.Context, project string) ([]string, error) {
	return func(ctx context.Context, project string) ([]string, error) {
		s, err := d.Snapshot(ctx)
		if err != nil || s == nil {
			return nil, err
		}
		return s.ApplicableMethodologies(project), nil
	}
}
