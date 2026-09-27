// Command graph serves the domain/change graph.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/typecat"
)

// loadTypes loads the type catalogue until it holds the domains the seeds write to (the registry may start after
// the graph, and seeds its domains once the graph answers), then runs the seeds.
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
	g.Observe(events) // node and baseline events feed the node index (ADR 0026)
	// the graph judges nodes by the types of the published domains (ADR 0012): the registry is the reference, the
	// graph holds a copy reloaded on its domain events; without a registry, the domain files are the source
	var source typecat.Source = func(context.Context) ([]*methodology.Domain, error) {
		return methodology.LoadDomains(platform.Env("GOAP_DOMAINS_DIR", "domains"))
	}
	if url := platform.Env("GOAP_REGISTRY_URL", ""); url != "" {
		source = registrysvc.NewClient(platform.H2CClient(), url, telemetry.ClientOptions()...).Domains
	}
	types := typecat.NewLive(source)
	g.Types = func() graph.TypeCatalog { return types.Get() }
	for _, subject := range []string{"goap.registry.domain.published", "goap.registry.domain.deleted"} {
		if err := events.Subscribe(subject, func([]byte) {
			if err := types.Reload(context.Background()); err != nil {
				log.Error("type catalogue", "err", err)
			}
		}); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}
	// the built-in domains (organisation, platform) are always there; the demo seed needs alm
	var need []string
	demo := platform.Env("GOAP_GRAPH_SEED", "") == "demo"
	if demo {
		need = append(need, "alm")
	}
	go loadTypes(ctx, log, types, need, func() {
		if demo {
			seeded, err := graphsvc.SeedDemo(ctx, g)
			if err != nil {
				log.Error("seed", "err", err)
			}
			log.Info("demo seed", "loaded", seeded)
		}
		if _, err := graphsvc.SeedAccess(ctx, g); err != nil {
			log.Error("seed access", "err", err)
		}
		if seeded, err := graphsvc.SeedDefaults(ctx, g); err != nil {
			log.Error("seed defaults", "err", err)
		} else if seeded {
			log.Info("default organisation created")
		}
		// the built-in MCPs (ADR 0028) follow the platform; the default organisation lends them to every unit
		if seeded, err := graphsvc.SeedBuiltins(ctx, g); err != nil {
			log.Error("seed built-in MCPs", "err", err)
		} else if seeded {
			log.Info("built-in MCPs updated")
		}
		// the model gateway configuration (providers, models, aliases) is graph data: seeded when the graph has none
		if cfg, err := modelgw.InitialConfig(ctx, platform.Env("GOAP_MODELS_CONFIG", ""), platform.NewSecrets()); err != nil {
			log.Error("models config", "err", err)
		} else if provs, models, aliases, err := cfg.Objects(); err != nil {
			log.Error("models config", "err", err)
		} else if seeded, err := graphsvc.SeedModels(ctx, g, provs, models, aliases); err != nil {
			log.Error("seed models", "err", err)
		} else if seeded {
			log.Info("model gateway configuration created", "providers", len(provs), "models", len(models), "aliases", len(aliases))
		}
	})
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
