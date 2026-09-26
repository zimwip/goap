// Command iam serves ABAC access decisions (Casbin, policies in PostgreSQL)
// and policy administration. Organization and user management: milestone M2.
package main

import (
	"context"
	"time"

	"github.com/casbin/casbin/v2/persist"

	"github.com/zimwip/goap/gen/goap/iam/v1/iamv1connect"
	"github.com/zimwip/goap/internal/iamsvc"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/authz"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("iam")
	defer telemetry.Setup(context.Background(), log, "iam")(context.Background())
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	var adapter persist.Adapter
	var orgs iamsvc.OrgStore
	if pool := platform.OptionalPostgres(ctx, log, iamsvc.Migrations); pool != nil {
		defer pool.Close()
		adapter = &iamsvc.Adapter{Pool: pool}
		orgs = iamsvc.PGOrgStore{Pool: pool}
		srv.Readiness(pool.Ping)
	}
	enforcer, err := authz.NewCasbin(adapter)
	if err != nil {
		platform.Fatal(log, "casbin", err)
	}
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()
	// replicas reload on change events, and periodically as a safety net
	reload := func() {
		if err := enforcer.Reload(); err != nil {
			log.Error("policy reload", "err", err)
		}
	}
	if adapter != nil {
		if err := events.Subscribe(iamsvc.PolicyChangedSubject, func([]byte) { reload() }); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
		go func() {
			for range time.Tick(platform.EnvDuration("GOAP_POLICY_RELOAD", 30*time.Second)) {
				reload()
			}
		}()
	}
	srv.Mount(iamv1connect.NewIamServiceHandler(&iamsvc.Handler{Enforcer: enforcer, Orgs: orgs, Events: events}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
