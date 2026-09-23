// Command graph serves the domain/change graph.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/iamsvc"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/metamodel"
)

// syncMethodologies projects every published methodology at startup,
// retrying while the registry is not reachable. Every methodology whose
// projection changes the graph (which seeds its NodeType nodes the first
// time) is reported on events, so the engine invalidates its ancestor cache.
func syncMethodologies(ctx context.Context, log *slog.Logger, g *graph.Graph, reg metamodel.Published, events engine.Publisher) {
	for delay := time.Second; ; delay = min(2*delay, time.Minute) {
		rs, err := metamodel.SyncAll(ctx, g, reg)
		if err == nil {
			for _, res := range rs {
				if res.Changed() {
					publishNodeTypeChanged(ctx, events, res.Methodology)
				}
			}
			log.Info("methodologies projected onto the graph", "methodologies", len(rs))
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
			} else if res.Changed() {
				log.Info("methodology projected onto the graph", "name", ev.Name, "version", ev.Version, "change", res.Change)
				publishNodeTypeChanged(context.Background(), events, ev.Name)
			}
		}); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}
	authorizer := iamsvc.NewClient(platform.H2CClient(), platform.Env("GOAP_IAM_URL", "http://localhost:8086"))
	srv.Mount(graphv1connect.NewGraphServiceHandler(&graphsvc.Handler{Graph: g, Events: events, Authz: authorizer}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
