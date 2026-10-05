// Command registry stores the methodologies (in the graph) and the domains (in its database).
package main

import (
	"connectrpc.com/connect"
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/internal/eventsvc"
	"github.com/zimwip/goap/internal/graphsvc"
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

	// methodologies are nodes of the graph; domains are kept in the registry's database (ADR 0023)
	graphClient := graphsvc.NewClient(platform.H2CClient(), platform.Env("GOAP_GRAPH_URL", "http://localhost:8081"), telemetry.ClientOptions()...)
	store := registrysvc.NewGraphStore(graphClient)
	var domains registrysvc.DomainStore = registrysvc.NewMemoryStore()
	if pool := platform.OptionalPostgres(ctx, log, registrysvc.Migrations); pool != nil {
		defer pool.Close()
		domains = registrysvc.SQLDomainStore{DB: stdlib.OpenDBFromPool(pool), Dollar: true}
		srv.Readiness(pool.Ping)
	}
	authorizer, err := access.NewAuthorizer(&access.Directory{Graph: graphClient})
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	svc := &registrysvc.Service{Store: store, DomainStore: domains, Authz: authorizer, Events: events,
		MCPScopes: (&mcpsvc.Directory{Graph: graphClient}).Scopes} // the scope of the MCPs (ADR 0028)
	if dir := platform.Env("GOAP_METHODOLOGIES_DIR", ""); dir != "" {
		// bootstrap: import the YAML files of versions not stored yet, once the graph answers
		go func() {
			system := authz.With(ctx, authz.System("registry", access.RoleAdmin))
			seed := &registrysvc.Service{Store: store, DomainStore: domains, Events: events}
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
	srv.Mount(registryv1connect.NewRegistryServiceHandler(&registrysvc.Handler{Service: svc}, append(telemetry.HandlerOptions(), connect.WithInterceptors(eventsvc.CommandInterceptor()))...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
