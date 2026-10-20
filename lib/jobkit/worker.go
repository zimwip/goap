package jobkit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/lib/pbconv"
)

// Runner runs one job and returns when it is over. ctx carries the principal of the job (when it has one) and is
// cancelled when the job is cancelled; an error fails the job.
type Runner func(ctx context.Context, job *Job) error

// ReportEvery is how often a running job sends its progress when it changed; Heartbeat how often it reports when it
// did not, renewing its lease and learning of a cancel.
const (
	ReportEvery = 500 * time.Millisecond
	Heartbeat   = 2 * time.Second
)

// Worker claims the jobs of its kinds and runs them.
type Worker struct {
	Queue Queue
	// ID names the worker process (default: <name>@<host>:<pid>, see WorkerID).
	ID    string
	Kinds map[string]Runner
	// Parallel is how many jobs run at once (default 2).
	Parallel int
	// Wait is how long a claim waits for a job (default 20 s).
	Wait time.Duration
	Log  *slog.Logger
}

// WorkerID is a worker id for a process: <name>@<host>:<pid>.
func WorkerID(name string) string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s@%s:%d", name, host, os.Getpid())
}

func (w *Worker) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}

// Run claims and runs jobs until ctx is done. It retries while the job service is unreachable, so start order does not
// matter; the jobs running when ctx ends are cancelled.
func (w *Worker) Run(ctx context.Context) {
	if w.ID == "" {
		w.ID = WorkerID("worker")
	}
	kinds := slices.Sorted(maps.Keys(w.Kinds))
	n := w.Parallel
	if n <= 0 {
		n = 2
	}
	wait := w.Wait
	if wait <= 0 {
		wait = 20 * time.Second
	}
	slots := make(chan struct{}, n)
	var wg sync.WaitGroup
	defer wg.Wait()
	failing := false
	for ctx.Err() == nil {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		c, err := w.Queue.Claim(ctx, w.ID, kinds, wait)
		if err != nil {
			<-slots
			if ctx.Err() != nil {
				return
			}
			if !failing {
				w.log().Warn("job claim failed, retrying", "worker", w.ID, "err", err)
				failing = true
			}
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if failing {
			w.log().Info("job service reached", "worker", w.ID, "kinds", kinds)
			failing = false
		}
		if c == nil || c.Job == nil {
			<-slots
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			w.run(ctx, c)
		}()
	}
}

// run runs a claimed job: a reporter sends its progress and renews its lease while the runner works.
func (w *Worker) run(parent context.Context, c *Claim) {
	pb := c.Job
	j := &Job{ID: pb.Id, Kind: pb.Kind, Label: pb.Label, Target: pb.Target, Params: pbconv.Map(pb.Params), ProjectID: pb.ProjectId,
		OwnerOrg: pb.OwnerOrg, By: pb.By, Principal: c.Principal, queue: w.Queue, worker: w.ID}
	if j.Params == nil {
		j.Params = map[string]any{}
	}
	log := w.log().With("job", j.ID, "kind", j.Kind, "by", j.By)
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	defer cancel()
	if !c.Principal.Anonymous() {
		ctx = authz.With(ctx, c.Principal)
	}
	lease := c.Lease
	if lease <= 0 {
		lease = DefaultLease
	}
	var cancelled sync.Once
	stop := make(chan struct{})
	reported := make(chan struct{})
	go func() {
		defer close(reported)
		tick := time.NewTicker(ReportEvery)
		defer tick.Stop()
		last := time.Now()
		ended := parent.Done()
		for {
			select {
			case <-stop:
				return
			case <-ended:
				// the worker stops: its jobs are cancelled
				ended = nil
				cancelled.Do(cancel)
				continue
			case <-tick.C:
			}
			r, dirty := j.report()
			if !dirty && time.Since(last) < min(Heartbeat, lease/3) {
				continue
			}
			stopIt, err := w.Queue.Report(context.WithoutCancel(ctx), j.ID, w.ID, r)
			if err != nil {
				log.Warn("job report failed", "err", err)
				if errors.Is(err, ErrLost) {
					cancelled.Do(cancel)
				}
				continue
			}
			last = time.Now()
			if stopIt {
				cancelled.Do(cancel)
			}
		}
	}()
	log.Info("job started")
	err := safeRun(ctx, w.Kinds[j.Kind], j)
	close(stop)
	<-reported
	r, _ := j.report()
	switch {
	case err == nil:
		r.Status = StatusDone
	case ctx.Err() != nil:
		r.Status, r.Error = StatusCancelled, err.Error()
	default:
		r.Status, r.Error = StatusFailed, err.Error()
	}
	if _, rerr := w.Queue.Report(context.WithoutCancel(parent), j.ID, w.ID, r); rerr != nil {
		log.Warn("job end not recorded", "status", r.Status, "err", rerr)
	}
	if err != nil {
		log.Warn("job ended", "status", r.Status, "err", err)
	} else {
		log.Info("job done")
	}
}

func safeRun(ctx context.Context, run Runner, j *Job) (err error) {
	if run == nil {
		return fmt.Errorf("no runner of kind %s", j.Kind)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("job panicked: %v", r)
		}
	}()
	return run(ctx, j)
}
