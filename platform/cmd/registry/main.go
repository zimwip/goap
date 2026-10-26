// Command registry is the methodology registry: it stores the methodologies (in the graph) and compiles them against the
// domains of the domain registry, served by the graph (ADR 0125 §3).
package main

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/domainclient"
	"github.com/zimwip/goap/lib/graphclient"
	"github.com/zimwip/goap/lib/graphsnap"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
	"github.com/zimwip/goap/mcp/mcpsvc"
	"github.com/zimwip/goap/platform/registrysvc"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("registry")
	defer telemetry.Setup(context.Background(), log, "registry")(context.Background())
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()

	// methodologies are nodes of the graph; the domains are the domain registry's (ADR 0125 §3, ADR 0167)
	graphURL := platform.Env("GOAP_GRAPH_URL", "http://localhost:8081")
	graphClient := graphclient.NewClient(platform.H2CClient(), graphURL, telemetry.ClientOptions()...)
	// one reader of the graph for the methodologies, the access and the scopes of the MCPs (ADR 0145)
	reader := graphsnap.NewReader(graphClient)
	store := registrysvc.NewGraphStore(graphClient).ShareReader(reader)
	domains := domainclient.NewClient(platform.H2CClient(), platform.Env("GOAP_DOMAINS_URL", "http://localhost:8096"), telemetry.ClientOptions()...)
	directory := &access.Directory{Graph: graphClient, Reader: reader}
	// the policies, the organisation and the projects the platform graph delivers, read again when its baseline
	// events say the organisation moved (ADR 0154 §9)
	if events != nil {
		if err := directory.FollowEvents(events); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
		// the methodologies, compiled once, until a commit of them moves their head (ADR 0168): the graphs asking about
		// a change are answered without the registry reading them back
		if err := store.FollowEvents(events); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}
	authorizer, err := access.NewAuthorizer(directory)
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	svc := &registrysvc.Service{Store: store, Domains: domains, Authz: authorizer, Events: events,
		MCPScopes:  (&mcpsvc.Directory{Graph: graphClient, Reader: reader}).Scopes, // the scope of the MCPs (ADR 0028)
		Applicable: applicable(directory)}                                          // the integrators in force in a project (ADR 0106)
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
	srv.Mount(registryv1connect.NewRegistryServiceHandler(&registrysvc.Handler{Service: svc}, append(telemetry.HandlerOptions(), connect.WithInterceptors(identity.CommandInterceptor()))...))
	// what the graphs ask of the methodologies of their changes (ADR 0168)
	srv.Mount(registryv1connect.NewChangeGuardianServiceHandler(&registrysvc.GuardianHandler{Service: svc}, telemetry.HandlerOptions()...))
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
