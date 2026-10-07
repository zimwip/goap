// Command conversations keeps the conversations of the users with the assistant, outside the graph (ADR 0085), and
// hosts the assistant that answers them (ADR 0087): it is the one process that holds both the conversations and, through
// clients acting for the caller, the graph, the model gateway and the registry.
package main

import (
	"context"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/gen/goap/assistant/v1/assistantv1connect"
	"github.com/zimwip/goap/gen/goap/conversations/v1/conversationsv1connect"
	"github.com/zimwip/goap/internal/assistantsvc"
	"github.com/zimwip/goap/internal/convsvc"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/access"
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
	convs := &convsvc.Service{Store: store}
	srv.Mount(conversationsv1connect.NewConversationServiceHandler(&convsvc.Handler{Service: convs}, telemetry.HandlerOptions()...))

	// the assistant calls the platform for the caller: every client carries the principal of its context
	hc := platform.H2CClient()
	copts := append(telemetry.ClientOptions(), identity.Forward())
	graphClient := graphsvc.NewClient(hc, platform.Env("GOAP_GRAPH_URL", "http://localhost:8081"), telemetry.ClientOptions()...)
	assistant := &assistantsvc.Service{
		Convs:         convs,
		Model:         modelgw.NewClient(hc, platform.Env("GOAP_MODELGW_URL", "http://localhost:8084"), copts...),
		Graph:         graphClient,
		Methodologies: registrysvc.NewClient(hc, platform.Env("GOAP_REGISTRY_URL", "http://localhost:8082"), copts...),
		Projects:      assistantsvc.Directory{Directory: &access.Directory{Graph: graphClient}},
		Log:           log,
	}
	srv.Mount(assistantv1connect.NewAssistantServiceHandler(&assistantsvc.Handler{Service: assistant}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
