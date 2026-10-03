// Command events serves the one event stream of the web (ADR 0053): it follows every publication of the platform
// on the bus and streams them, with the presence of the users, to the browsers.
package main

import (
	"context"

	"github.com/zimwip/goap/gen/goap/events/v1/eventsv1connect"
	"github.com/zimwip/goap/internal/eventsvc"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/access"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("events")
	defer telemetry.Setup(ctx, log, "events")(ctx)
	bus := platform.OptionalEvents(ctx, log)
	if bus == nil {
		platform.Fatal(log, "events", errNoBus)
	}
	defer bus.Close()
	hc := platform.H2CClient()
	authorizer, err := access.NewAuthorizer(&access.Directory{Graph: graphsvc.NewClient(hc, platform.Env("GOAP_GRAPH_URL", "http://localhost:8081"), telemetry.ClientOptions()...)})
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	hub := eventsvc.NewHub()
	hub.Authz = authorizer
	go hub.Run(ctx)
	// presence is shared between replicas over core NATS, outside the persisted GOAP stream
	hub.Relay = func(subject string, v any) { _ = bus.PublishCore(subject, v) }
	for _, subject := range []string{"goap.>", eventsvc.PresenceSubject + ">"} {
		if err := bus.SubscribeSubject(subject, hub.Ingest); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}
	hub.Hello()
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	srv.Mount(eventsv1connect.NewEventServiceHandler(&eventsvc.Handler{Hub: hub}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}

type busError string

func (e busError) Error() string { return string(e) }

const errNoBus = busError("GOAP_NATS_URL is required: the event stream follows the bus")
