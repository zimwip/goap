// Command credentials keeps the local sign-in password of subjects that connect without an external
// identity provider (ADR 0040).
package main

import (
	"context"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/gen/goap/credentials/v1/credentialsv1connect"
	"github.com/zimwip/goap/internal/credsvc"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/telemetry"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("credentials")
	defer telemetry.Setup(context.Background(), log, "credentials")(context.Background())
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	var store credsvc.Store = credsvc.NewMemoryStore()
	if pool := platform.OptionalPostgres(ctx, log, credsvc.Migrations); pool != nil {
		defer pool.Close()
		store = credsvc.SQLStore{DB: stdlib.OpenDBFromPool(pool), Dollar: true}
		srv.Readiness(pool.Ping)
	}
	srv.Mount(credentialsv1connect.NewCredentialsServiceHandler(&credsvc.Handler{Service: &credsvc.Service{Store: store}}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
