// Command preferences keeps the personal preferences of the users, outside the graph (ADR 0038).
package main

import (
	"context"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/gen/goap/preferences/v1/preferencesv1connect"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/prefssvc"
	"github.com/zimwip/goap/internal/telemetry"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("preferences")
	defer telemetry.Setup(context.Background(), log, "preferences")(context.Background())
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	var store prefssvc.Store = prefssvc.NewMemoryStore()
	if pool := platform.OptionalPostgres(ctx, log, prefssvc.Migrations); pool != nil {
		defer pool.Close()
		store = prefssvc.SQLStore{DB: stdlib.OpenDBFromPool(pool), Dollar: true}
		srv.Readiness(pool.Ping)
	}
	srv.Mount(preferencesv1connect.NewPreferencesServiceHandler(&prefssvc.Handler{Service: &prefssvc.Service{Store: store}}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
