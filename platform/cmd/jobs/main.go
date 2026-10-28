// Command jobs is the job service (ADR 0149): the long runs a person starts (an import from a source, the import of a
// graph snapshot, an export), claimed by the workers of their kinds and followed over Server-Sent Events
// (GET /api/jobs/events, routed by the gateway). GOAP_JOB_TOKEN is the services' token (the services creating jobs and
// the workers present it); GOAP_JOB_RETENTION how long an ended job is kept (default 7 days). One replica: the stream
// follows the writes of its process.
package main

import (
	"context"
	"os"

	"github.com/zimwip/goap/contracts/gen/goap/jobs/v1/jobsv1connect"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
	"github.com/zimwip/goap/platform/jobsvc"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("jobs")
	defer telemetry.Setup(context.Background(), log, "jobs")(context.Background())
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	var store jobsvc.Store = jobsvc.NewMemoryStore()
	if db := platform.OptionalSQL(ctx, log, "jobs", jobsvc.Migrations, jobsvc.SQLiteMigrations); db != nil {
		defer db.Close()
		store = jobsvc.SQLStore{DB: db.DB, Dollar: db.Dollar}
		srv.Readiness(db.Ping)
	}
	token := os.Getenv("GOAP_JOB_TOKEN")
	if token == "" {
		log.Warn("GOAP_JOB_TOKEN is not set: no service may create a job, no worker claim one")
	}
	svc := &jobsvc.Service{Store: store, Log: log, Retention: platform.EnvDuration("GOAP_JOB_RETENTION", jobsvc.DefaultRetention)}
	// its events (a job created, started, ended) go through its outbox to the bus (ADR 0136)
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()
	outbox.Start(ctx, svc.Emit(outbox.NATS(events)), log)
	go svc.Run(ctx)
	h := &jobsvc.Handler{Service: svc, Token: token}
	srv.Mount(jobsv1connect.NewJobServiceHandler(h, telemetry.HandlerOptions(telemetry.LongPoll(jobsv1connect.JobServiceClaimJobProcedure))...))
	h.Mount(srv.Echo)
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
