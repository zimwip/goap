// Command registry stores methodology definitions.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/internal/graphsvc"
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

	// methodologies and domains are nodes of the graph: the registry needs no database, only the graph
	graphClient := graphsvc.NewClient(platform.H2CClient(), platform.Env("GOAP_GRAPH_URL", "http://localhost:8081"), telemetry.ClientOptions()...)
	store := registrysvc.NewGraphStore(graphClient)
	authorizer, err := access.NewAuthorizer(&access.Directory{Graph: graphClient})
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	svc := &registrysvc.Service{Store: store, Authz: authorizer, Events: events}
	if dir := platform.Env("GOAP_METHODOLOGIES_DIR", ""); dir != "" {
		// bootstrap: import the YAML files of versions not stored yet, once the graph answers
		go func() {
			system := authz.With(ctx, authz.Principal{Subject: "system:registry", Roles: []string{"admin"}})
			seed := &registrysvc.Service{Store: store, Events: events}
			for delay := time.Second; ; delay = min(2*delay, time.Minute) {
				err := func() error {
					if ddir := platform.Env("GOAP_DOMAINS_DIR", ""); ddir != "" {
						doms, err := seed.SeedDomains(system, ddir)
						if err != nil {
							return fmt.Errorf("import domains: %w", err)
						}
						log.Info("domains imported", "dir", ddir, "domains", doms)
					}
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
	srv.Mount(registryv1connect.NewRegistryServiceHandler(&registrysvc.Handler{Service: svc}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
