// Command graph serves the domain/change graph and the domain registry, the definition of the graph (ADR 0125 §3).
package main

import (
	"connectrpc.com/connect"

	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/contracts/assess"
	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/criticality"
	"github.com/zimwip/goap/contracts/decision"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/contracts/gen/goap/importer/v1/importerv1connect"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/contracts/gen/goap/smarttag/v1/smarttagv1connect"
	"github.com/zimwip/goap/contracts/risk"
	"github.com/zimwip/goap/contracts/verify"
	"github.com/zimwip/goap/graph/domainsvc"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/graph/importsvc"
	"github.com/zimwip/goap/graph/smarttagsvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/domainclient"
	"github.com/zimwip/goap/lib/graphclient"
	"github.com/zimwip/goap/lib/graphsnap"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/jobkit"
	"github.com/zimwip/goap/lib/llmcfg"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
	"github.com/zimwip/goap/lib/typecat"
	"github.com/zimwip/goap/platform/devseed"
	"github.com/zimwip/goap/platform/modelgw"
	"github.com/zimwip/goap/platform/registrysvc"
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
	// two graph spaces (ADR 0154 §9): the platform graph (the default) holds the platform, the organisation and the
	// methodologies, serves the domain registry and forwards the calls on the namespaces of GOAP_GRAPH_ROUTES to the
	// graphs holding them; a content graph (GOAP_GRAPH_SPACE=content) holds the domains' content and reads the
	// organisation, the projects and the domains from the platform graph at GOAP_PLATFORM_GRAPH_URL
	content := platform.Env("GOAP_GRAPH_SPACE", "platform") == "content"
	platformURL := os.Getenv("GOAP_PLATFORM_GRAPH_URL")
	if content && platformURL == "" {
		platform.Fatal(log, "graph space", fmt.Errorf("a content graph needs GOAP_PLATFORM_GRAPH_URL"))
	}
	// `graph bootstrap` (ADR 0154 §9): the platform bootstrap of graphsvc.Boot (the roots of the organisation and the
	// projects, the default policies, the built-in MCPs, the models, the sources of GOAP_GRAPH_SOURCES, the dev seeds of
	// GOAP_GRAPH_SEED) run once against the database, then the command exits; the service itself bootstraps nothing
	// unless GOAP_GRAPH_BOOTSTRAP=1 (and always on a memory store, which no other process reaches). A content graph has
	// nothing to bootstrap: its roots are the platform graph's.
	bootstrapOnly := len(os.Args) > 1 && os.Args[1] == "bootstrap"
	sources, err := graphsvc.ParseSources(os.Getenv("GOAP_GRAPH_SOURCES"))
	if err != nil {
		platform.Fatal(log, "graph sources", err)
	}
	var repo graph.Repo = graph.NewMemory()
	memory := true
	var domainStore domainsvc.Store = domainsvc.NewMemoryStore()
	var smartTagStore smarttagsvc.Store = smarttagsvc.NewMemoryStore()
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	if pool := platform.OptionalPostgres(ctx, log, graph.Migrations); pool != nil {
		defer pool.Close()
		// the domain registry keeps its versions next to the graph they define
		if err := platform.Migrate(ctx, log, pool, domainsvc.Migrations, "migrations"); err != nil {
			platform.Fatal(log, "migrate domains", err)
		}
		// the smart tags select versions of this graph, in a store of their own (ADR 0143 §7)
		if err := platform.Migrate(ctx, log, pool, smarttagsvc.Migrations, "migrations"); err != nil {
			platform.Fatal(log, "migrate smart tags", err)
		}
		repo = graph.NewPostgres(pool)
		memory = false
		domainStore = domainsvc.SQLStore{DB: stdlib.OpenDBFromPool(pool), Dollar: true}
		smartTagStore = smarttagsvc.SQLStore{DB: stdlib.OpenDBFromPool(pool), Dollar: true}
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
	g.Resolvers = graphsvc.Resolvers()   // the sources of the logical URLs beyond its namespaces (ADR 0154)
	// the events of the graph (changes, nodes, baselines) go through its outbox to the bus (ADR 0136)
	outbox.Start(ctx, g.Observe(outbox.NATS(events)), log)
	// the graph judges nodes by the types of the published domains (ADR 0012): the domain registry served here is the
	// reference (ADR 0125 §3), the graph holds a copy reloaded when a version is published or archived; the methodology
	// registry follows the same publications through the events
	// the domain registry's events (a version saved, published, deleted) go through its outbox to the bus (ADR 0136)
	domains := &domainsvc.Service{Store: domainStore}
	outbox.Start(ctx, domains.Emit(outbox.NATS(events)), log)
	types := typecat.NewLive(domains.Domains)
	domains.Changed = func(ctx context.Context, r def.Record) {
		if err := types.Reload(ctx); err != nil {
			log.Error("type catalogue", "domain", r.Domain.Name, "err", err)
		}
	}
	if content {
		// the domains are the platform graph's: read from its registry, reloaded on its events
		types = typecat.NewLive(domainclient.NewClient(platform.H2CClient(), platformURL, telemetry.ClientOptions()...).Domains)
		for _, subject := range []string{"goap.registry.domain.published", "goap.registry.domain.deleted"} {
			if err := events.Subscribe(subject, func([]byte) {
				if err := types.Reload(context.Background()); err != nil {
					log.Error("type catalogue", "err", err)
				}
			}); err != nil {
				platform.Fatal(log, "subscribe", err)
			}
		}
	}
	g.Types = func() graph.TypeCatalog { return types.Get() }
	// every start publishes the domain versions of GOAP_DOMAINS_DIR not stored yet (ADR 0113)
	if dir := platform.Env("GOAP_DOMAINS_DIR", "domains"); dir != "" && !content {
		system := authz.With(ctx, authz.System("graph", access.RoleAdmin))
		if doms, err := domains.SeedDomains(system, dir); err != nil {
			log.Error("import domains", "dir", dir, "err", err)
		} else {
			log.Info("domains imported", "dir", dir, "domains", doms)
		}
	}
	// the hooks of the graph are set before the seeds start (they run in the background, and write through them)
	directory := &access.Directory{Graph: g}
	if content {
		// the organisation and the projects are the platform graph's: its directory places the changes and the versions
		// of this graph, read at each use from the head of the platform's organisation (ADR 0145)
		platformGraph := followedGraph{Client: graphclient.NewClient(platform.H2CClient(), platformURL, telemetry.ClientOptions()...), heads: &graphsnap.Heads{}}
		if events != nil {
			// read once, then again when the platform's main moves (its baseline events); a lost event is caught up later
			platformGraph.heads.MaxAge = 30 * time.Second
			if err := events.Subscribe(fmt.Sprintf(domain.SubjectBaselineAdvanced, domain.MainBranch), func([]byte) { platformGraph.heads.Moved() }); err != nil {
				platform.Fatal(log, "subscribe", err)
			}
		} else {
			platformGraph.heads.MaxAge = time.Nanosecond // no bus: asked at each read
		}
		directory = &access.Directory{Graph: platformGraph}
		g.Directory = directory
	}
	authorizer, err := access.NewAuthorizer(directory)
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	g.Authorizer = graphsvc.TransitionAuthorizer(authorizer)
	g.ChangeAuthorizer = graphsvc.ChangeTransitionAuthorizer(authorizer)
	g.ProjectMoveGate = graphsvc.ProjectMoveGate(authorizer, directory)
	g.MethodologyGate = graphsvc.MethodologyGate(directory)
	g.ProjectSources = directory.ProjectSources // where each project reads the sources of its URLs (ADR 0154 §8)
	g.ItemAuthorizer = graphsvc.ItemAuthorizer(authorizer, directory.CriticalityResolver())
	g.SeatAuthorizer = graphsvc.SeatAuthorizer(authorizer)
	g.ItemPolicy = criticality.ItemPolicy(directory.CriticalityResolver()) // the oracle and the lifetime a level accepts (ADR 0075 §3)
	g.Facets = map[string]graph.BlackboardFacet{domain.FacetCriticalityPolicy: directory.CriticalityFacet()}
	// the history of a change (ADR 0109): no registry here, so no step relies on another for its standing
	g.LogFacets = graphsvc.LogFacets(nil)
	g.Validators = []graph.NodeValidator{access.AdminFloorValidator{}, access.ProjectScopeValidator{}, access.ProjectOwnerValidator{},
		llmcfg.ProtectedAliasValidator{}, graphsvc.PlatformSourceValidator{}}
	// the roles a methodology allows each operation to (ADR 0103, 0138), asked of the methodology registry: without
	// them, an operation on a change would be the administrators' only
	registry := registrysvc.NewClient(&http.Client{Timeout: 30 * time.Second}, platform.Env("GOAP_REGISTRY_URL", "http://localhost:8082"), telemetry.ClientOptions()...)
	g.OperationAuthorizer = graphsvc.OperationAuthorizer(authorizer, registryOperationRoles(registry))
	// SubChangeValidator and Lifecycles, the guardian of the changes (ADR 0058, 0102), need the registry service itself
	// (its methodology store): only goap-dev, which holds it in process, wires them; the registry client has no RPC for
	// them, so this graph merges by the committable states of the domains only
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
	opts.Sources = sources
	boot := func() error {
		// the model gateway configuration (providers, models, aliases) is graph data: seeded when the graph has none
		var err error
		if opts.Models, err = modelgw.BootConfig(ctx, platform.Env("GOAP_MODELS_CONFIG", ""), platform.NewSecrets()); err != nil {
			log.Error("models config", "err", err)
		}
		// the same bootstrap as every composition (ADR 0071), once the type catalogue holds the domains the seeds write to
		r, err := graphsvc.Boot(ctx, g, opts)
		if err == nil {
			log.Info("platform bootstrapped", "access", r.Access, "builtins", r.Builtins, "models", r.Models, "sources", r.Sources)
		}
		return err
	}
	switch {
	case bootstrapOnly && content:
		log.Info("a content graph holds no roots nor platform data: nothing to bootstrap")
		return
	case bootstrapOnly:
		loadTypes(ctx, log, types, need, func() {
			if err := boot(); err != nil {
				platform.Fatal(log, "boot", err)
			}
		})
		// the events of the bootstrap stay in the outbox: the service delivers them
		return
	case content:
		// nothing to seed: the platform graph holds the platform data
		go loadTypes(ctx, log, types, nil, func() {})
	case memory || platform.Env("GOAP_GRAPH_BOOTSTRAP", "") == "1":
		go loadTypes(ctx, log, types, need, func() {
			if err := boot(); err != nil {
				log.Error("boot", "err", err)
			}
		})
	default:
		// bootstrapped by `graph bootstrap`; the catalogue is loaded all the same
		go loadTypes(ctx, log, types, nil, func() {})
	}
	domains.Authz = authorizer
	// the job service (ADR 0149): the exports, the snapshot imports and the imports from a source are its jobs; this
	// process creates them with the services' token and runs the graph's kinds as a worker
	jobs := jobkit.NewClient(platform.H2CClient(), platform.Env("GOAP_JOBS_URL", "http://localhost:8091"), os.Getenv("GOAP_JOB_TOKEN"), telemetry.ClientOptions()...)
	graphHandler := &graphsvc.Handler{Graph: g, Events: events, Authz: authorizer, Floor: authorizer.Floor(), Projects: directory.MayAccessProject, Stopped: directory.ProjectStopped,
		Jobs: jobs}
	path, graphService := graphv1connect.NewGraphServiceHandler(graphHandler, append(telemetry.HandlerOptions(), connect.WithInterceptors(graphHandler.Identify(), graphHandler.Positions(), identity.CommandInterceptor(), graphHandler.PersonalScope(), graphHandler.EnsureCaller()))...)
	if content {
		// the jobs, the domain registry, the smart tags and the imports are the platform graph's (a job of a content
		// namespace, a smart tag of its versions: steps to come of ADR 0154)
		srv.Mount(path, graphService)
		if err := srv.Run(); err != nil {
			platform.Fatal(log, "server", err)
		}
		return
	}
	graphHandler.Demo = devseed.DemoLoader(directory) // the "Load demo data" setting; the alm domain must be published
	go (&jobkit.Worker{Queue: jobs, ID: jobkit.WorkerID("graph"), Kinds: graphHandler.JobRunners(), Log: log}).Run(ctx)
	// the calls on the namespaces other graphs hold go to them, the declared graph sources driving them (ADR 0154 §9)
	graphService = &graphsvc.Router{Local: graphService, Sources: g.Sources, LocalSource: graphsvc.PlatformSource, Client: platform.H2CClient(), HasChange: g.HasChange}
	srv.Mount(path, graphService)
	srv.Mount(registryv1connect.NewDomainServiceHandler(&domainsvc.Handler{Service: domains}, append(telemetry.HandlerOptions(), connect.WithInterceptors(identity.CommandInterceptor()))...))
	smartTags := &smarttagsvc.Service{Store: smartTagStore, Exists: smarttagsvc.VersionsExist(g.Node)}
	srv.Mount(smarttagv1connect.NewSmartTagServiceHandler(&smarttagsvc.Handler{Service: smartTags}, append(telemetry.HandlerOptions(), connect.WithInterceptors(identity.CommandInterceptor()))...))
	// the import registry (ADR 0148): a run is a job of the job service, an importer available when a worker claims it
	imports := &importsvc.Service{Domains: domains, Authz: authorizer, Jobs: jobs}
	srv.Mount(importerv1connect.NewImportServiceHandler(&importsvc.Handler{Service: imports}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}

// followedGraph is the platform graph as a content graph reads its organisation (ADR 0154 §9): the heads of its
// branches are kept until its events say one moved, the rest is read from it.
type followedGraph struct {
	*graphclient.Client
	heads *graphsnap.Heads
}

func (f followedGraph) BranchHead(ctx context.Context, namespace, name string) (domain.Baseline, error) {
	return f.heads.Get(ctx, namespace, name, f.Client.BranchHead)
}

// registryOperationRoles reads the roles that allow an operation from the methodology of the change, compiled by the
// registry (registrysvc.Service.OperationRoles in process): a change of no methodology, or of one the registry does
// not know, is not restricted by roles (the ABAC rules and the gates still apply).
func registryOperationRoles(c *registrysvc.Client) graphsvc.OperationRoles {
	return func(ctx context.Context, ref, op string) ([]string, bool, error) {
		if ref == "" {
			return nil, false, nil
		}
		m, err := c.Methodology(ctx, ref)
		if err != nil {
			if connect.CodeOf(err) == connect.CodeNotFound {
				return nil, false, nil
			}
			return nil, false, err
		}
		roles, restricted := m.Definition().OperationRoles(op)
		return roles, restricted, nil
	}
}
