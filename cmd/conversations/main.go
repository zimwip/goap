// Command conversations keeps the conversations of the users with the assistant, outside the graph (ADR 0085).
package main

import (
	"context"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/gen/goap/conversations/v1/conversationsv1connect"
	"github.com/zimwip/goap/internal/convsvc"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/telemetry"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("conversations")
	defer telemetry.Setup(context.Background(), log, "conversations")(context.Background())
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	var store convsvc.Store = convsvc.NewMemoryStore()
	if pool := platform.OptionalPostgres(ctx, log, convsvc.Migrations); pool != nil {
		defer pool.Close()
		store = convsvc.SQLStore{DB: stdlib.OpenDBFromPool(pool), Dollar: true}
		srv.Readiness(pool.Ping)
	}
	srv.Mount(conversationsv1connect.NewConversationServiceHandler(&convsvc.Handler{Service: &convsvc.Service{Store: store}}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
