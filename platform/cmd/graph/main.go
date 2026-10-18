// Command graph serves the domain/change graph and the domain registry, the definition of the graph (ADR 0125 §3).
package main

import (
	"connectrpc.com/connect"

	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/contracts/assess"
	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/criticality"
	"github.com/zimwip/goap/contracts/decision"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/contracts/risk"
	"github.com/zimwip/goap/contracts/verify"
	"github.com/zimwip/goap/graph/domainsvc"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/llmcfg"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
	"github.com/zimwip/goap/lib/typecat"
	"github.com/zimwip/goap/platform/devseed"
	"github.com/zimwip/goap/platform/modelgw"
)

// loadTypes loads the type catalogue until it holds the domains the seeds write to, then runs the seeds.
func loadTypes(ctx context.Context, log *slog.Logger, types *typecat.Live, need []string, seed func()) {
	for delay := time.Second; ; delay = min(2*delay, time.Minute) {
		err := types.Reload(ctx)
		if err == nil {
			missing := slices.DeleteFunc(slices.Clone(need), func(ns string) bool { _, ok := types.Get().Domains()[ns]; return ok })
			if len(missing) == 0 {
				log.Info("type catalogue loaded", "domains", len(types.Get().Domains()))
				seed()
				return
			}
			err = fmt.Errorf("domains %v not published yet", missing)
		}
		log.Warn("type catalogue", "err", err, "retry", delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func main() {
	ctx := context.Background()
	// the facts of the risk register are items of a change (ADR 0065)
	risk.Register()
	assess.Register()
	verify.Register()
	log := platform.Logger("graph")
	defer telemetry.Setup(context.Background(), log, "graph")(context.Background())
	var repo graph.Repo = graph.NewMemory()
	var domainStore domainsvc.Store = domainsvc.NewMemoryStore()
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	if pool := platform.OptionalPostgres(ctx, log, graph.Migrations); pool != nil {
		defer pool.Close()
		// the domain registry keeps its versions next to the graph they define
		if err := platform.Migrate(ctx, log, pool, domainsvc.Migrations, "migrations"); err != nil {
			platform.Fatal(log, "migrate domains", err)
		}
		repo = graph.NewPostgres(pool)
		domainStore = domainsvc.SQLStore{DB: stdlib.OpenDBFromPool(pool), Dollar: true}
		srv.Readiness(pool.Ping)
	}
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()
	srv.Readiness(events.Ready)

	g := graph.New(repo)
	// the principal behind each event of the impact logs (ADR 0029)
	g.Caller = graphsvc.Caller
	g.EndUser = graphsvc.EndUser         // the person the events of the changes are for (ADR 0114)
	g.DecisionPolicy = decision.Policy{} // confidence, rounds and deadline settle the decision points (ADR 0067)
	g.ReviewPolicy = verify.Policy{}     // a verifier is not the producer (ADR 0075)
	// the events of the graph (changes, nodes, baselines) go through its outbox to the bus (ADR 0136)
	if relay := g.Observe(outbox.NATS(events)); relay != nil {
		relay.Log, relay.Poll, relay.Retention = log, platform.EnvDuration("GOAP_OUTBOX_POLL", time.Second), platform.EnvDuration("GOAP_OUTBOX_RETENTION", 24*time.Hour)
		go relay.Run(ctx)
	}
	// baselines written whole before they were stored as deltas are compacted, once, in the background (ADR 0032)
	go func() {
		if n, err := g.CompactBaselines(ctx); err != nil {
			log.Error("compact baselines", "err", err)
		} else if n > 0 {
			log.Info("baselines compacted", "rewritten", n)
		}
	}()
	// the graph judges nodes by the types of the published domains (ADR 0012): the domain registry served here is the
	// reference (ADR 0125 §3), the graph holds a copy reloaded when a version is published or archived; the methodology
	// registry follows the same publications through the events
	domains := &domainsvc.Service{Store: domainStore, Events: events}
	types := typecat.NewLive(domains.Domains)
	domains.Changed = func(ctx context.Context, r def.Record) {
		if err := types.Reload(ctx); err != nil {
			log.Error("type catalogue", "domain", r.Domain.Name, "err", err)
		}
	}
	g.Types = func() graph.TypeCatalog { return types.Get() }
	// every start publishes the domain versions of GOAP_DOMAINS_DIR not stored yet (ADR 0113)
	if dir := platform.Env("GOAP_DOMAINS_DIR", "domains"); dir != "" {
		system := authz.With(ctx, authz.System("graph", access.RoleAdmin))
		if doms, err := domains.SeedDomains(system, dir); err != nil {
			log.Error("import domains", "dir", dir, "err", err)
		} else {
			log.Info("domains imported", "dir", dir, "domains", doms)
		}
	}
	// the roots of the organisation and of the projects, before serving (ADR 0054): the structures are tagged by the
	// built-in organisation domain, known before the catalogue loads the others (graphsvc.Boot repeats it, a no-op)
	if err := g.Bootstrap(ctx); err != nil {
		platform.Fatal(log, "bootstrap", err)
	}
	// the hooks of the graph are set before the seeds start (they run in the background, and write through them)
	directory := &access.Directory{Graph: g}
	authorizer, err := access.NewAuthorizer(directory)
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	g.Authorizer = graphsvc.TransitionAuthorizer(authorizer)
	g.ChangeAuthorizer = graphsvc.ChangeTransitionAuthorizer(authorizer)
	g.ProjectMoveGate = graphsvc.ProjectMoveGate(authorizer, directory)
	g.MethodologyGate = graphsvc.MethodologyGate(directory)
	g.ItemAuthorizer = graphsvc.ItemAuthorizer(authorizer, directory.CriticalityResolver())
	g.SeatAuthorizer = graphsvc.SeatAuthorizer(authorizer)
	g.ItemPolicy = criticality.ItemPolicy(directory.CriticalityResolver()) // the oracle and the lifetime a level accepts (ADR 0075 §3)
	g.Facets = map[string]graph.BlackboardFacet{domain.FacetCriticalityPolicy: directory.CriticalityFacet()}
	// the history of a change (ADR 0109): no registry here, so no step relies on another for its standing
	g.LogFacets = graphsvc.LogFacets(nil)
	g.Validators = []graph.NodeValidator{access.AdminFloorValidator{}, llmcfg.ProtectedAliasValidator{}}
	// SubChangeValidator and Lifecycles, the guardian of the changes (ADR 0058, 0102), need the registry service itself
	// (its methodology store): only goap-dev, which holds it in process, wires them; the registry client has no RPC for
	// them, so this graph merges by the landable states of the domains only
	// the built-in domains (organisation, platform) are always there; the demo seed needs alm. GOAP_GRAPH_SEED=dev seeds
	// the development data (the document-repository MCP), =demo the demo data too; otherwise an administrator loads the
	// demo from the settings (LoadDemoData)
	var need []string
	seed := platform.Env("GOAP_GRAPH_SEED", "")
	demo := seed == "demo"
	if demo {
		need = append(need, "alm")
	}
	opts := graphsvc.Options{RequireHooks: true, Log: log}
	if demo || seed == "dev" {
		opts.Dev = func(ctx context.Context, g *graph.Graph) error {
			if demo {
				seeded, err := devseed.Demo(ctx, g)
				log.Info("demo seed", "loaded", seeded)
				if err != nil {
					return err
				}
			}
			_, err := devseed.DocumentRepository(ctx, g)
			return err
		}
	}
	go loadTypes(ctx, log, types, need, func() {
		// the model gateway configuration (providers, models, aliases) is graph data: seeded when the graph has none
		var err error
		if opts.Models, err = modelgw.BootConfig(ctx, platform.Env("GOAP_MODELS_CONFIG", ""), platform.NewSecrets()); err != nil {
			log.Error("models config", "err", err)
		}
		// the same bootstrap as every composition (ADR 0071), once the type catalogue holds the domains the seeds write to
		if r, err := graphsvc.Boot(ctx, g, opts); err != nil {
			log.Error("boot", "err", err)
		} else {
			log.Info("platform bootstrapped", "access", r.Access, "builtins", r.Builtins, "models", r.Models)
		}
	})
	domains.Authz = authorizer
	graphHandler := &graphsvc.Handler{Graph: g, Events: events, Authz: authorizer, Floor: authorizer.Floor(),
		Demo: devseed.DemoLoader(directory)} // the "Load demo data" setting; the alm domain must be published
	srv.Mount(graphv1connect.NewGraphServiceHandler(graphHandler, append(telemetry.HandlerOptions(), connect.WithInterceptors(graphHandler.Identify(), identity.CommandInterceptor(), graphHandler.PersonalScope(), graphHandler.EnsureCaller()))...))
	srv.Mount(registryv1connect.NewDomainServiceHandler(&domainsvc.Handler{Service: domains}, append(telemetry.HandlerOptions(), connect.WithInterceptors(identity.CommandInterceptor()))...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
