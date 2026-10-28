// Command graph serves the domain/change graph and the domain registry, the definition of the graph (ADR 0125 §3).
package main

import (
	"connectrpc.com/connect"
	"encoding/json"

	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/contracts/assess"
	"github.com/zimwip/goap/contracts/criticality"
	"github.com/zimwip/goap/contracts/decision"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/contracts/gen/goap/importer/v1/importerv1connect"
	"github.com/zimwip/goap/contracts/gen/goap/smarttag/v1/smarttagv1connect"
	"github.com/zimwip/goap/contracts/risk"
	"github.com/zimwip/goap/contracts/verify"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/graph/importsvc"
	"github.com/zimwip/goap/graph/smarttagsvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/domainclient"
	"github.com/zimwip/goap/lib/graphclient"
	"github.com/zimwip/goap/lib/guardianclient"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/jobkit"
	"github.com/zimwip/goap/lib/llmcfg"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
	"github.com/zimwip/goap/lib/typecat"
	"github.com/zimwip/goap/platform/devseed"
	"github.com/zimwip/goap/platform/modelgw"
)

// registryTimeout bounds a read of the domain registry, at start, on its events and on a type the catalogue misses: a
// registry that does not answer is one that is not there, the catalogue kept stays in force and the start listens for
// its announcement (ADR 0169).
const registryTimeout = 3 * time.Second

// missedEvery spaces the reads of the registry a type the catalogue does not hold asks for (Handler.KnownTypes): a
// client sending types no domain declares reads it once a second at most.
const missedEvery = time.Second

// loadTypes loads the type catalogue until it holds the domains the seeds write to, then runs the seeds: tried at
// once, then again when the domain registry announces it serves or publishes a domain (ADR 0169), else after a delay.
func loadTypes(ctx context.Context, log *slog.Logger, events *platform.Events, types *typecat.Live, invalidate func(), need []string, seed func()) {
	err := events.Await(ctx, []string{platform.ReadyDomains, "goap.registry.domain.published"}, func(ctx context.Context) error {
		invalidate() // what the client kept says nothing of the registry now
		// a registry that does not answer is one that is not there: the announcement brings the next try
		ctx, cancel := context.WithTimeout(ctx, registryTimeout)
		defer cancel()
		if err := types.Reload(ctx); err != nil {
			return err
		}
		if missing := slices.DeleteFunc(slices.Clone(need), func(ns string) bool { _, ok := types.Get().Domains()[ns]; return ok }); len(missing) > 0 {
			return fmt.Errorf("domains %v not published yet", missing)
		}
		return nil
	}, func(err error, delay time.Duration) { log.Warn("type catalogue", "err", err, "retry", delay) })
	if err != nil {
		return
	}
	log.Info("type catalogue loaded", "domains", len(types.Get().Domains()))
	seed()
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
	// methodologies and forwards the calls on the namespaces of GOAP_GRAPH_ROUTES to the
	// graphs holding them; a content graph (GOAP_GRAPH_SPACE=content) holds the domains' content and reads the
	// organisation and the projects from the platform graph at GOAP_PLATFORM_GRAPH_URL. Both read their domains from the
	// domain registry, a service of its own (GOAP_DOMAINS_URL, ADR 0167)
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
	var smartTagStore smarttagsvc.Store = smarttagsvc.NewMemoryStore()
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	if path := os.Getenv("GOAP_SQLITE_PATH"); path != "" && os.Getenv("GOAP_DB_DSN") == "" {
		// a SQLite file (the local stack of goap-local): the graph and its smart tags in it, the
		// reads on read-only connections
		db, err := platform.OpenSQLite(ctx, path)
		if err != nil {
			platform.Fatal(log, "sqlite", err)
		}
		defer db.Close()
		for _, m := range []struct {
			component string
			fs        fs.FS
		}{{"graph", graph.SQLiteMigrations}, {"smarttags", smarttagsvc.SQLiteMigrations}} {
			if err := platform.MigrateSQLite(ctx, db, m.component, m.fs, "migrations_sqlite"); err != nil {
				platform.Fatal(log, "migrate "+m.component, err)
			}
		}
		if err := graph.CheckReferences(ctx, db); err != nil {
			platform.Fatal(log, "database", err)
		}
		read, err := platform.OpenSQLiteReader(ctx, path, 4)
		if err != nil {
			platform.Fatal(log, "sqlite readers", err)
		}
		defer read.Close()
		repo = graph.NewSQLite(db).WithReader(read)
		memory = false
		smartTagStore = smarttagsvc.SQLStore{DB: db}
		srv.Readiness(db.PingContext)
		log.Info("storage", "sqlite", path)
	} else if pool := platform.OptionalPostgres(ctx, log, graph.Migrations); pool != nil {
		defer pool.Close()
		// the smart tags select versions of this graph, in a store of their own (ADR 0143 §7)
		if err := platform.Migrate(ctx, log, pool, smarttagsvc.Migrations, "migrations"); err != nil {
			platform.Fatal(log, "migrate smart tags", err)
		}
		pg := graph.NewPostgres(pool)
		// the requests that only read run on read-only connections, with no BEGIN and COMMIT around them
		read, err := platform.PostgresReader(ctx, pool)
		if err != nil {
			platform.Fatal(log, "postgres readers", err)
		}
		defer read.Close()
		repo = pg.WithReader(read)
		if err := graph.CheckReferences(ctx, stdlib.OpenDBFromPool(pool)); err != nil {
			platform.Fatal(log, "database", err)
		}
		memory = false
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
	// the events of the graph (changes, nodes, baselines) go through its outbox to the bus (ADR 0136), named apart
	// from the platform graph's in a content graph
	if content {
		g.Origin = "graph-content"
	}
	outbox.Start(ctx, g.Observe(outbox.NATS(events)), log)
	// the graph judges nodes by the types of the published domains (ADR 0012), read from the domain registry, a service
	// of its own (ADR 0167): at start (loadTypes), then again on its events (a version published or archived, the
	// registry announcing it serves again); with no bus, every minute. The requests read the catalogue kept meanwhile
	client := domainclient.NewClient(platform.H2CClient(), platform.Env("GOAP_DOMAINS_URL", "http://localhost:8096"), telemetry.ClientOptions()...)
	types := typecat.NewLive(client.Domains)
	reload := func() {
		// the client keeps what it read for its TTL: the event says it moved
		client.Invalidate()
		ctx, cancel := context.WithTimeout(context.Background(), registryTimeout)
		defer cancel()
		if err := types.Reload(ctx); err != nil {
			log.Error("type catalogue", "err", err)
		}
	}
	if events != nil {
		for _, subject := range []string{"goap.registry.domain.published", "goap.registry.domain.deleted"} {
			if err := events.Subscribe(subject, func([]byte) { reload() }); err != nil {
				platform.Fatal(log, "subscribe", err)
			}
		}
		if err := events.Subscribe(platform.ReadyDomains, func([]byte) { reload() }); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	} else {
		go func() {
			for range time.Tick(time.Minute) {
				reload()
			}
		}()
	}
	g.Types = func() graph.TypeCatalog { return types.Get() }
	// the hooks of the graph are set before the seeds start (they run in the background, and write through them).
	// The platform graph delivers the policies, the organisation and the projects to every graph, itself included, the
	// same way (ADR 0154 §9): a directory over the platform graph (this one in process, else its client) keeps its
	// snapshot until the platform's baseline events say the organisation moved
	var source access.Graph = g
	if content {
		// the organisation and the projects are the platform graph's: its directory places the changes and the versions
		// of this graph
		source = graphclient.NewClient(platform.H2CClient(), platformURL, telemetry.ClientOptions()...)
	}
	directory := &access.Directory{Graph: source}
	if events != nil {
		// the heads of the branches this graph serves are kept in memory, moved by its commits and by the baseline
		// events of the other processes sharing its store (each carries its header and the versions it moved)
		g.FollowHeads()
		if err := events.Subscribe(fmt.Sprintf(domain.SubjectBaselineAdvanced, "*"), func(data []byte) {
			var ev domain.BaselineEvent
			if json.Unmarshal(data, &ev) == nil {
				g.HeadEvent(ev)
			}
		}); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
		// read once, then again when the organisation moves; a lost event is caught up after access.FollowAge
		if err := directory.FollowEvents(events); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	} else {
		// no bus: read again once a commit of this graph moved the organisation, else after access.CommitAge
		directory.FollowCommits()
	}
	if !content {
		// this graph holds the organisation: its own commits are seen at once, before their event comes back
		g.OnBranchMoved = directory.MovedNamespace
	}
	if content {
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
	g.ProjectSources = directory.ProjectSources      // where each project reads the sources of its URLs (ADR 0154 §8)
	g.ReachOf = graphsvc.ReachOf(directory.ReachSet) // a caller reads the versions whose scope reaches it (ADR 0161 §5)
	g.OwnedWrites = graphsvc.OwnedWrites             // a change writes the nodes its project owns (ADR 0161 §3)
	g.ItemAuthorizer = graphsvc.ItemAuthorizer(authorizer, directory.CriticalityResolver())
	g.SeatAuthorizer = graphsvc.SeatAuthorizer(authorizer)
	g.ItemPolicy = criticality.ItemPolicy(directory.CriticalityResolver()) // the oracle and the lifetime a level accepts (ADR 0075 §3)
	g.Facets = map[string]graph.BlackboardFacet{domain.FacetCriticalityPolicy: directory.CriticalityFacet()}
	g.Validators = []graph.NodeValidator{access.AdminFloorValidator{}, access.ProjectScopeValidator{}, access.ProjectOwnerValidator{},
		llmcfg.ProtectedAliasValidator{}, graphsvc.PlatformSourceValidator{}}
	// the graph knows no methodology (ADR 0168): what it asks of the methodology of a change (its lifecycle, guards and
	// gates, ADR 0058; the guardian of its commits and its chain of guardians, ADR 0106, 0146; the cascade of its
	// sub-changes, ADR 0066; the standing of its steps, ADR 0109; the roles of its operations, ADR 0103) its hooks ask
	// the guardian service, which compiles the methodologies the registry stores and answers on the blackboard sent
	guardian := guardianclient.NewClient(platform.H2CClient(), platform.Env("GOAP_GUARDIAN_URL", "http://localhost:8097"),
		telemetry.ClientOptions()...)
	g.Lifecycles = guardian
	g.SubChangeValidator = guardian.SubChangeValidator
	g.LogFacets = graphsvc.LogFacets(guardian.StepDependencies)
	g.OperationAuthorizer = graphsvc.OperationAuthorizer(authorizer, guardian.OperationRoles)
	// the bootstrap writes to the built-in domains (the registry's, ADR 0167: the graph waits for them); the demo seed
	// needs alm. GOAP_GRAPH_SEED=dev seeds
	// the development data (the document-repository MCP), =demo the demo data too; otherwise an administrator loads the
	// demo from the settings (LoadDemoData)
	need := []string{"methodology", "organisation", "platform"}
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
		loadTypes(ctx, log, events, types, client.Invalidate, need, func() {
			if err := boot(); err != nil {
				platform.Fatal(log, "boot", err)
			}
		})
		// the events of the bootstrap stay in the outbox: the service delivers them
		return
	case content:
		// nothing to seed: the platform graph holds the platform data
		go loadTypes(ctx, log, events, types, client.Invalidate, need, func() {})
	case memory || platform.Env("GOAP_GRAPH_BOOTSTRAP", "") == "1":
		go loadTypes(ctx, log, events, types, client.Invalidate, need, func() {
			if err := boot(); err != nil {
				log.Error("boot", "err", err)
				return
			}
			// the services waiting for the platform data try again now (ADR 0169)
			_ = events.PublishCore(platform.ReadyGraph, map[string]string{"service": platform.ServiceName()})
		})
	default:
		// bootstrapped by `graph bootstrap`; the catalogue is loaded all the same
		go loadTypes(ctx, log, events, types, client.Invalidate, need, func() {})
	}
	// the job service (ADR 0149): the exports, the snapshot imports and the imports from a source are its jobs; this
	// process creates them with the services' token and runs the graph's kinds as a worker
	jobs := jobkit.NewClient(platform.H2CClient(), platform.Env("GOAP_JOBS_URL", "http://localhost:8091"), os.Getenv("GOAP_JOB_TOKEN"), telemetry.ClientOptions()...)
	graphHandler := &graphsvc.Handler{Graph: g, Events: events, Authz: authorizer, Floor: authorizer.Floor(), Projects: directory.MayAccessProject, Stopped: directory.ProjectStopped,
		Jobs: jobs}
	// a type the catalogue does not hold is looked for in the registry before the request is refused: a domain
	// published since the last reload is known at its first use, its event lost or with no bus
	graphHandler.TypesMissed = func(ctx context.Context) bool {
		client.Invalidate()
		ctx, cancel := context.WithTimeout(ctx, registryTimeout)
		defer cancel()
		return types.Refresh(ctx, missedEvery)
	}
	if content {
		graphHandler.Space = "content" // its exports and imports are jobs of kinds of its own
	}
	path, graphService := graphv1connect.NewGraphServiceHandler(graphHandler, append(telemetry.HandlerOptions(), connect.WithInterceptors(graphHandler.Identify(), graphHandler.Positions(), identity.CommandInterceptor(), graphHandler.PersonalScope(), graphHandler.EnsureCaller(), graphHandler.KnownTypes(), graphHandler.LockedWrites()))...)
	// the exports and imports of the graph's namespaces, a worker of the job service
	go (&jobkit.Worker{Queue: jobs, ID: jobkit.WorkerID("graph"), Kinds: graphHandler.JobRunners(), Log: log}).Run(ctx)
	if content {
		// the smart tags and the imports are the platform graph's (a smart tag of a content
		// graph's versions: a step to come of ADR 0154)
		srv.Mount(path, platform.ReadOnlyRequests(graphService))
		srv.Announce(events, platform.ReadyGraphContent)
		if err := srv.Run(); err != nil {
			platform.Fatal(log, "server", err)
		}
		return
	}
	graphHandler.Demo = devseed.DemoLoader(directory) // the "Load demo data" setting; the alm domain must be published
	// the calls on the namespaces other graphs hold go to them, the declared graph sources driving them (ADR 0154 §9)
	graphService = &graphsvc.Router{Local: graphService, Sources: g.Sources, LocalSource: graphsvc.PlatformSource, Client: platform.H2CClient(), HasChange: g.HasChange}
	// the requests that only read (Get…, List…: platform.ReadOnlyRequests) take the readers of the store
	srv.Mount(path, platform.ReadOnlyRequests(graphService))
	smartTags := &smarttagsvc.Service{Store: smartTagStore, Exists: smarttagsvc.VersionsExist(g.Node)}
	srv.Mount(smarttagv1connect.NewSmartTagServiceHandler(&smarttagsvc.Handler{Service: smartTags}, append(telemetry.HandlerOptions(), connect.WithInterceptors(identity.CommandInterceptor()))...))
	// the import registry (ADR 0148): a run is a job of the job service, an importer available when a worker claims it
	imports := &importsvc.Service{Domains: client, Authz: authorizer, Jobs: jobs}
	srv.Mount(importerv1connect.NewImportServiceHandler(&importsvc.Handler{Service: imports}, telemetry.HandlerOptions()...))
	srv.Announce(events, platform.ReadyGraph)
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
