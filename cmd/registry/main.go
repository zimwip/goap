// Command registry stores methodology definitions.
package main

import (
	"context"

	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/internal/iamsvc"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/pkg/authz"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("registry")
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	var store registrysvc.Store = registrysvc.NewMemoryStore()
	if pool := platform.OptionalPostgres(ctx, log, registrysvc.Migrations); pool != nil {
		defer pool.Close()
		store = registrysvc.PostgresStore{Pool: pool}
		srv.Readiness(pool.Ping)
	}
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()

	svc := &registrysvc.Service{Store: store, Authz: iamsvc.NewClient(platform.H2CClient(), platform.Env("GOAP_IAM_URL", "http://localhost:8086")), Events: events}
	if dir := platform.Env("GOAP_METHODOLOGIES_DIR", ""); dir != "" {
		// bootstrap: import the YAML files of versions not stored yet
		system := authz.With(ctx, authz.Principal{Subject: "system:registry", Roles: []string{"admin"}})
		seed := &registrysvc.Service{Store: store, Events: events}
		loaded, err := seed.Seed(system, dir)
		if err != nil {
			platform.Fatal(log, "import methodologies", err)
		}
		log.Info("methodologies imported", "dir", dir, "methodologies", loaded)
	}
	srv.Mount(registryv1connect.NewRegistryServiceHandler(&registrysvc.Handler{Service: svc}))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
