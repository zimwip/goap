// Command guardian is the guardian of the changes (ADR 0168): what the graphs ask of the methodologies of their
// changes (registry.v1.ChangeGuardianService). It stores nothing: it reads the methodologies the registry stores in the
// platform graph, compiles them against the domains in force with its cache, and keeps them until a commit of the
// methodology namespace moves their head. Stateless, it runs as many replicas as the graphs' load asks; the registry
// keeps the storage and the validation.
package main

import (
	"context"
	"time"

	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/domainclient"
	"github.com/zimwip/goap/lib/graphclient"
	"github.com/zimwip/goap/lib/graphsnap"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
	"github.com/zimwip/goap/platform/registrysvc"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("guardian")
	defer telemetry.Setup(context.Background(), log, "guardian")(context.Background())
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()

	graphClient := graphclient.NewClient(platform.H2CClient(), platform.Env("GOAP_GRAPH_URL", "http://localhost:8081"), telemetry.ClientOptions()...)
	reader := graphsnap.NewReader(graphClient)
	// read only: this service never writes a methodology (the registry's store does)
	store := registrysvc.NewGraphStore(graphClient).ShareReader(reader)
	domains := domainclient.NewClient(platform.H2CClient(), platform.Env("GOAP_DOMAINS_URL", "http://localhost:8096"), telemetry.ClientOptions()...)
	directory := &access.Directory{Graph: graphClient, Reader: reader}
	if events != nil {
		// the organisation (the methodologies applicable to a project) and the methodologies, each read again when a
		// commit moves its namespace: a guard asked inside a graph's write transaction is answered from what is held
		if err := directory.FollowEvents(events); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
		if err := store.FollowEvents(events); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}
	// no Events: the registry announces the suspensions, this service only honours them
	svc := &registrysvc.Service{Store: store, Domains: domains, Log: log, Applicable: applicable(directory)}
	// a domain published or archived: the compiled methodologies go, the ones it breaks are suspended (ADR 0125 §3)
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
		for delay := time.Second; ; delay = min(2*delay, time.Minute) {
			if _, err := svc.Recheck(ctx); err == nil {
				return
			}
			time.Sleep(delay)
		}
	}()
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
