// Command credentials keeps the local sign-in password of subjects that connect without an external
// identity provider (ADR 0040).
package main

import (
	"context"
	"errors"

	"github.com/zimwip/goap/contracts/gen/goap/credentials/v1/credentialsv1connect"
	"github.com/zimwip/goap/gateway/credsvc"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("credentials")
	defer telemetry.Setup(context.Background(), log, "credentials")(context.Background())
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	var store credsvc.Store = credsvc.NewMemoryStore()
	if db := platform.OptionalSQL(ctx, log, "credentials", credsvc.Migrations, credsvc.SQLiteMigrations); db != nil {
		defer db.Close()
		store = credsvc.SQLStore{DB: db.DB, Dollar: db.Dollar}
		srv.Readiness(db.Ping)
	}
	// the gateway is the only caller: it presents a shared service credential; without one nobody may call
	token, err := platform.NewSecrets().Get(ctx, "goap/credentials#service_token", "GOAP_CREDENTIALS_TOKEN")
	if err != nil || token == "" {
		platform.Fatal(log, "credentials service token (GOAP_CREDENTIALS_TOKEN)", errors.Join(err, errors.New("not set")))
	}
	// its events (an account registered, a password changed, a session started or ended) go through its outbox to the
	// bus (ADR 0136)
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()
	svc := &credsvc.Service{Store: store}
	outbox.Start(ctx, svc.Emit(outbox.NATS(events)), log)
	srv.Mount(credentialsv1connect.NewCredentialsServiceHandler(&credsvc.Handler{Service: svc, Token: token}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
