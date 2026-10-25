// Command conversations keeps the conversations of the users with the assistant, outside the graph (ADR 0085), and
// hosts the assistant that answers them (ADR 0087): it is the one process that holds both the conversations and, through
// clients acting for the caller, the graph, the model gateway and the registry.
package main

import (
	"context"

	"github.com/zimwip/goap/contracts/gen/goap/assistant/v1/assistantv1connect"
	"github.com/zimwip/goap/contracts/gen/goap/conversations/v1/conversationsv1connect"
	"github.com/zimwip/goap/contracts/gen/goap/engine/v1/enginev1connect"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/graphclient"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
	"github.com/zimwip/goap/platform/assistantsvc"
	"github.com/zimwip/goap/platform/convsvc"
	"github.com/zimwip/goap/platform/modelgw"
	"github.com/zimwip/goap/platform/registrysvc"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("conversations")
	defer telemetry.Setup(context.Background(), log, "conversations")(context.Background())
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	var store convsvc.Store = convsvc.NewMemoryStore()
	if db := platform.OptionalSQL(ctx, log, "conversations", convsvc.Migrations, convsvc.SQLiteMigrations); db != nil {
		defer db.Close()
		store = convsvc.SQLStore{DB: db.DB, Dollar: db.Dollar}
		srv.Readiness(db.Ping)
	}
	convs := &convsvc.Service{Store: store}
	// its events (a conversation created, renamed or deleted, a message appended or updated, an action decided) go
	// through its outbox to the bus (ADR 0136)
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()
	outbox.Start(ctx, convs.Emit(outbox.NATS(events)), log)
	srv.Mount(conversationsv1connect.NewConversationServiceHandler(&convsvc.Handler{Service: convs}, telemetry.HandlerOptions()...))

	// the assistant calls the platform for the caller: every client carries the principal of its context
	hc := platform.H2CClient()
	copts := append(telemetry.ClientOptions(), identity.Forward())
	graphClient := graphclient.NewClient(hc, platform.Env("GOAP_GRAPH_URL", "http://localhost:8081"), telemetry.ClientOptions()...)
	directory := &access.Directory{Graph: graphClient}
	// the policies, the organisation and the projects the platform graph delivers, read again when its baseline
	// events say the organisation moved (ADR 0154 §9)
	if events != nil {
		if err := directory.FollowEvents(events); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}
	registryURL := platform.Env("GOAP_REGISTRY_URL", "http://localhost:8082")
	assistant := &assistantsvc.Service{
		Convs:         convs,
		Model:         modelgw.NewClient(hc, platform.Env("GOAP_MODELGW_URL", "http://localhost:8084"), copts...),
		Graph:         graphClient,
		Methodologies: registrysvc.NewClient(hc, registryURL, copts...),
		Projects:      assistantsvc.Directory{Directory: directory},
		Engine: assistantsvc.EngineClient{API: enginev1connect.NewEngineServiceClient(hc, platform.Env("GOAP_ENGINE_URL", "http://localhost:8083"), copts...),
			Views: registryv1connect.NewRegistryServiceClient(hc, registryURL, copts...)},
		Log: log,
	}
	srv.Mount(assistantv1connect.NewAssistantServiceHandler(&assistantsvc.Handler{Service: assistant}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
