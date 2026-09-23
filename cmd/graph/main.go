// Command graph serves the domain/change graph.
package main

import (
	"context"

	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/graph"
)

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
	srv.Mount(graphv1connect.NewGraphServiceHandler(&graphsvc.Handler{Graph: g, Events: events}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
