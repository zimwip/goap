// Command graph serves the domain/change graph.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/metamodel"
)

// syncMethodologies projects every published methodology at startup,
// retrying while the registry is not reachable. Every synced methodology is
// then backfilled (ADR 0012 phase 3: instanceOf edges for nodes created
// before its metadata layer existed) and, when the projection changed
// the graph, reported on events so the engine invalidates its ancestor cache.
func syncMethodologies(ctx context.Context, log *slog.Logger, g *graph.Graph, reg metamodel.Published, events engine.Publisher) {
	for delay := time.Second; ; delay = min(2*delay, time.Minute) {
		n, err := syncAll(ctx, log, g, reg, events)
		if err == nil {
			log.Info("methodologies projected onto the graph", "methodologies", n)
			return
		}
		log.Warn("methodology projection", "err", err, "retry", delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// syncAll projects every published methodology (and the shared domains they
// reference), backfills instanceOf edges and reports graph changes.
func syncAll(ctx context.Context, log *slog.Logger, g *graph.Graph, reg metamodel.Published, events engine.Publisher) (int, error) {
	rs, err := metamodel.SyncAll(ctx, g, reg)
	if err != nil {
		return 0, err
	}
	for _, res := range rs {
		backfillInstanceOf(ctx, log, g, res.Methodology)
		if res.Changed() {
			publishNodeTypeChanged(ctx, events, res.Methodology)
		}
	}
	return len(rs), nil
}

// backfillInstanceOf links pre-existing domain nodes to their node type
// (ADR 0012 phase 3). It is idempotent and best-effort: a failure is logged,
// never fatal, and retried on the next sync.
func backfillInstanceOf(ctx context.Context, log *slog.Logger, g *graph.Graph, methodology string) {
	res, err := metamodel.BackfillInstanceOf(ctx, g, methodology)
	if err != nil {
		log.Warn("instanceOf backfill", "methodology", methodology, "err", err)
		return
	}
	if res.Changed() {
		log.Info("instanceOf backfill", "methodology", methodology, "links", res.Links)
	}
}

// publishNodeTypeChanged reports that a methodology's metadata layer may have
// changed (ADR 0012): the engine invalidates its NodeType ancestor cache for
// it. Published on every graph-changing Sync, not only when the NodeType
// elements themselves changed — over-invalidating is harmless.
func publishNodeTypeChanged(ctx context.Context, events engine.Publisher, methodology string) {
	_ = events.Publish(ctx, "goap.graph.nodetype.changed", map[string]string{"methodology": methodology})
}

func main() {
	ctx := context.Background()
	log := platform.Logger("graph")
	defer telemetry.Setup(context.Background(), log, "graph")(context.Background())
	var repo graph.Repo = graph.NewMemory()
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	if pool := platform.OptionalPostgres(ctx, log, graph.Migrations); pool != nil {
		defer pool.Close()
		repo = graph.NewPostgres(pool)
		srv.Readiness(pool.Ping)
	}
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()
	srv.Readiness(events.Ready)

	g := graph.New(repo)
	if platform.Env("GOAP_GRAPH_SEED", "") == "demo" {
		seeded, err := graphsvc.SeedDemo(ctx, g)
		if err != nil {
			platform.Fatal(log, "seed", err)
		}
		log.Info("demo seed", "loaded", seeded)
	}
	if _, err := graphsvc.SeedAccess(ctx, g); err != nil {
		platform.Fatal(log, "seed access", err)
	}
	if seeded, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		platform.Fatal(log, "seed defaults", err)
	} else if seeded {
		log.Info("default organisation created")
	}
	// the graph's own metadata and the node types of the registry's stored versions, before anything is stored
	if _, err := metamodel.SeedMeta(ctx, g); err != nil {
		platform.Fatal(log, "seed metadata", err)
	}
	if _, err := graphsvc.SeedNamespaces(ctx, g); err != nil {
		platform.Fatal(log, "seed namespaces", err)
	}
	// the model gateway configuration (providers, models, aliases) is graph data: seeded when the graph has none
	if cfg, err := modelgw.InitialConfig(ctx, platform.Env("GOAP_MODELS_CONFIG", ""), platform.NewSecrets()); err != nil {
		platform.Fatal(log, "models config", err)
	} else if provs, models, aliases, err := cfg.Objects(); err != nil {
		platform.Fatal(log, "models config", err)
	} else if seeded, err := graphsvc.SeedModels(ctx, g, provs, models, aliases); err != nil {
		platform.Fatal(log, "seed models", err)
	} else if seeded {
		log.Info("model gateway configuration created", "providers", len(provs), "models", len(models), "aliases", len(aliases))
	}
	// the published methodologies are projected onto the graph as versioned elements
	if url := platform.Env("GOAP_REGISTRY_URL", ""); url != "" {
		reg := registrysvc.NewClient(platform.H2CClient(), url, telemetry.ClientOptions()...)
		go syncMethodologies(ctx, log, g, reg, events)
		if err := events.Subscribe("goap.registry.methodology.published", func(data []byte) {
			var ev struct{ Name, Version string }
			if json.Unmarshal(data, &ev) != nil || ev.Name == "" {
				return
			}
			m, err := reg.Methodology(context.Background(), ev.Name)
			if err != nil {
				log.Error("published methodology", "name", ev.Name, "err", err)
				return
			}
			if res, err := metamodel.Sync(context.Background(), g, m.Methodology); err != nil {
				log.Error("methodology projection", "name", ev.Name, "err", err)
			} else {
				if res.Changed() {
					log.Info("methodology projected onto the graph", "name", ev.Name, "version", ev.Version, "change", res.Change)
					publishNodeTypeChanged(context.Background(), events, ev.Name)
				}
				backfillInstanceOf(context.Background(), log, g, ev.Name)
			}
		}); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}
	// a published domain reaches the methodologies that follow its latest
	// version: project everything again (idempotent)
	if url := platform.Env("GOAP_REGISTRY_URL", ""); url != "" {
		reg := registrysvc.NewClient(platform.H2CClient(), url, telemetry.ClientOptions()...)
		if err := events.Subscribe("goap.registry.domain.published", func([]byte) {
			ctx := context.Background()
			if n, err := syncAll(ctx, log, g, reg, events); err != nil {
				log.Error("domain projection", "err", err)
			} else {
				log.Info("domain published: methodologies projected again", "methodologies", n)
			}
		}); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}
	authorizer, err := access.NewAuthorizer(&access.Directory{Graph: g})
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	g.Authorizer = graphsvc.TransitionAuthorizer(authorizer)
	srv.Mount(graphv1connect.NewGraphServiceHandler(&graphsvc.Handler{Graph: g, Events: events, Authz: authorizer, Floor: authorizer.Floor()}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
