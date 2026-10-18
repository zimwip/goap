// Command preferences keeps the personal preferences of the users, outside the graph (ADR 0038).
package main

import (
	"context"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/contracts/gen/goap/preferences/v1/preferencesv1connect"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
	"github.com/zimwip/goap/platform/prefssvc"
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
	// its events (a user's preferences updated or reset) go through its outbox to the bus (ADR 0136)
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()
	svc := &prefssvc.Service{Store: store}
	outbox.Start(ctx, svc.Emit(outbox.NATS(events)), log)
	srv.Mount(preferencesv1connect.NewPreferencesServiceHandler(&prefssvc.Handler{Service: svc}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
