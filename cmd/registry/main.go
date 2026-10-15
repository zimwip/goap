// Command registry is the methodology registry: it stores the methodologies (in the graph) and compiles them against the
// domains of the domain registry, served by the graph (ADR 0125 §3).
package main

import (
	"connectrpc.com/connect"
	"context"
	"fmt"
	"time"

	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/internal/domainclient"
	"github.com/zimwip/goap/internal/eventsvc"
	"github.com/zimwip/goap/internal/graphclient"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("registry")
	defer telemetry.Setup(context.Background(), log, "registry")(context.Background())
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()

	// methodologies are nodes of the graph; the domains are the graph's (ADR 0125 §3), read from its domain registry
	graphURL := platform.Env("GOAP_GRAPH_URL", "http://localhost:8081")
	graphClient := graphclient.NewClient(platform.H2CClient(), graphURL, telemetry.ClientOptions()...)
	store := registrysvc.NewGraphStore(graphClient)
	domains := domainclient.NewClient(platform.H2CClient(), graphURL, telemetry.ClientOptions()...)
	directory := &access.Directory{Graph: graphClient}
	authorizer, err := access.NewAuthorizer(directory)
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	svc := &registrysvc.Service{Store: store, Domains: domains, Authz: authorizer, Events: events,
		MCPScopes:  (&mcpsvc.Directory{Graph: graphClient}).Scopes, // the scope of the MCPs (ADR 0028)
		Applicable: applicable(directory)}                          // the integrators in force in a project (ADR 0106)
	if dir := platform.Env("GOAP_METHODOLOGIES_DIR", ""); dir != "" {
		// bootstrap: import the YAML files of versions not stored yet, once the graph answers
		go func() {
			system := authz.With(ctx, authz.System("registry", access.RoleAdmin))
			seed := &registrysvc.Service{Store: store, Domains: domains, Events: events}
			for delay := time.Second; ; delay = min(2*delay, time.Minute) {
				err := func() error {
					loaded, err := seed.Seed(system, dir)
					if err != nil {
						return fmt.Errorf("import methodologies: %w", err)
					}
					log.Info("methodologies imported", "dir", dir, "methodologies", loaded)
					return nil
				}()
				if err == nil {
					return
				}
				log.Warn("registry bootstrap", "err", err, "retry", delay)
				time.Sleep(delay)
			}
		}()
	}
	// a domain published or archived: the methodologies are checked again, the ones it breaks are suspended (ADR 0125 §3)
	for _, subject := range []string{"goap.registry.domain.published", "goap.registry.domain.deleted"} {
		if err := events.Subscribe(subject, func([]byte) {
			domains.Invalidate()
			if _, err := svc.DomainsChanged(context.Background()); err != nil {
				log.Error("check the methodologies", "err", err)
			}
		}); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}
	go func() {
		// the domains of a new start may break a methodology too (a built-in domain changes with the platform)
		for delay := time.Second; ; delay = min(2*delay, time.Minute) {
			if _, err := svc.Recheck(ctx); err == nil {
				return
			}
			time.Sleep(delay)
		}
	}()
	srv.Mount(registryv1connect.NewRegistryServiceHandler(&registrysvc.Handler{Service: svc}, append(telemetry.HandlerOptions(), connect.WithInterceptors(eventsvc.CommandInterceptor()))...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}

// applicable lists the methodologies applicable to a project from the snapshot of the organisation (ADR 0106: where
// the guardian chain of a change finds the methodologies integrating its own).
func applicable(d *access.Directory) func(ctx context.Context, project string) ([]string, error) {
	return func(ctx context.Context, project string) ([]string, error) {
		s, err := d.Snapshot(ctx)
		if err != nil || s == nil {
			return nil, err
		}
		return s.ApplicableMethodologies(project), nil
	}
}
