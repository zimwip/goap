// Command registry serves methodology definitions.
package main

import (
	"context"

	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
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

	h := &registrysvc.Handler{Store: store, Events: events}
	if dir := platform.Env("GOAP_METHODOLOGIES_DIR", ""); dir != "" {
		loaded, err := h.LoadDir(ctx, dir)
		if err != nil {
			platform.Fatal(log, "load methodologies", err)
		}
		log.Info("methodologies loaded", "dir", dir, "methodologies", loaded)
	}
	srv.Mount(registryv1connect.NewRegistryServiceHandler(h))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
