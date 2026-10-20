package jobsvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	jobsv1 "github.com/zimwip/goap/contracts/gen/goap/jobs/v1"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/jobkit"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/pbconv"
)

// DefaultRetention is how long an ended job is kept.
const DefaultRetention = 7 * 24 * time.Hour

// MaxInputBytes bounds the input of a job.
const MaxInputBytes = 256 << 20

// readChunk is the most ReadInput answers at once.
const readChunk = blobChunk

// OutboxOrigin names the job service in the ids of its events (ADR 0136).
const OutboxOrigin = "jobs"

// The events of a job, on goap.job.<id>.<event>.
const (
	EventCreated = "created"
	EventStarted = "started"
	EventEnded   = "ended"
)

// Event is the payload of an event of a job.
type Event struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
	Target string    `json:"target,omitempty"`
	Status string    `json:"status"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
}

// Subject is the bus subject of an event of a job.
func Subject(id, event string) string { return "goap.job." + outbox.Token(id) + "." + event }

// Service is the job service. It is a jobkit.Queue for the services and the workers of its process.
type Service struct {
	Store Store
	// Lease is the lease of a claimed job (default jobkit.DefaultLease); Retention how long an ended job is kept.
	Lease     time.Duration
	Retention time.Duration
	Now       func() time.Time
	Log       *slog.Logger

	relay *outbox.Relay

	mu      sync.Mutex
	wake    chan struct{}
	workers map[string]*jobsv1.Worker
	subs    map[*subscriber]struct{}
}

var _ jobkit.Queue = (*Service)(nil)

// Emit makes the service write its events in the outbox of its store (ADR 0136) and returns the relay delivering them
// to sink (nil sink: no event, nil relay). Call it before serving.
func (s *Service) Emit(sink outbox.Sink) *outbox.Relay {
	if sink == nil {
		return nil
	}
	s.relay = &outbox.Relay{Origin: OutboxOrigin, Store: s.Store, Sink: sink}
	return s.relay
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) lease() time.Duration {
	if s.Lease > 0 {
		return s.Lease
	}
	return jobkit.DefaultLease
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) event(ctx context.Context, r Record, event string) []outbox.Message {
	if s.relay == nil {
		return nil
	}
	at := s.now()
	m, err := outbox.New(ctx, Subject(r.ID, event), Event{ID: r.ID, Kind: r.Kind, Target: r.Target, Status: r.Status, By: r.By, At: at}, at)
	if err != nil {
		return nil
	}
	return []outbox.Message{m}
}

// written hands a write to the readers: the relay of the outbox, the streams of the person.
func (s *Service) written(ctx context.Context, r Record, emitted bool) {
	if emitted && s.relay != nil {
		relay := s.relay
		go func() { _ = relay.Drain(context.WithoutCancel(ctx)) }()
	}
	s.publish(r)
}

// ToPB is the protocol form of a job.
func ToPB(r Record) *jobsv1.Job {
	j := &jobsv1.Job{Id: r.ID, Kind: r.Kind, Label: r.Label, Target: r.Target, Status: r.Status, CancelRequested: r.CancelRequested,
		Params: pbconv.Struct(r.Params), By: r.By, ProjectId: r.ProjectID, OwnerOrg: r.OwnerOrg, Progress: r.Progress, Done: r.Done,
		Total: r.Total, Stats: maps.Clone(r.Stats), Output: pbconv.Struct(r.Output), Error: r.Error, Worker: r.Worker,
		CreatedAt: pbconv.Time(r.Created), UpdatedAt: pbconv.Time(r.Updated)}
	if !r.Started.IsZero() {
		j.StartedAt = pbconv.Time(r.Started)
	}
	if !r.Ended.IsZero() {
		j.EndedAt = pbconv.Time(r.Ended)
	}
	if r.ResultName != "" {
		j.Result = &jobsv1.Result{Name: r.ResultName, MediaType: r.ResultType, Size: r.ResultSize}
	}
	return j
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "job-" + hex.EncodeToString(b)
}

// Create implements jobkit.Queue: it queues a job for a person (the caller authorized it).
func (s *Service) Create(ctx context.Context, sp jobkit.Spec) (*jobsv1.Job, error) {
	if !jobkit.KindRE.MatchString(sp.Kind) {
		return nil, fmt.Errorf("kind %q: dot-separated lowercase words: %w", sp.Kind, jobkit.ErrInvalid)
	}
	if sp.By == "" {
		return nil, fmt.Errorf("by: the person the job runs for is required: %w", jobkit.ErrInvalid)
	}
	if len(sp.Input) > MaxInputBytes {
		return nil, fmt.Errorf("input of %d bytes, at most %d: %w", len(sp.Input), MaxInputBytes, jobkit.ErrInvalid)
	}
	now := s.now()
	label := sp.Label
	if label == "" {
		label = sp.Kind
	}
	r := Record{ID: newID(), Kind: sp.Kind, Label: label, Target: sp.Target, Status: jobkit.StatusQueued, Params: sp.Params,
		By: sp.By, ProjectID: sp.ProjectID, OwnerOrg: sp.OwnerOrg, Principal: jobkit.EncodePrincipal(sp.Principal), Created: now, Updated: now}
	if r.Params == nil {
		r.Params = map[string]any{}
	}
	msgs := s.event(ctx, r, EventCreated)
	if err := s.Store.Create(ctx, r, sp.Input, msgs); err != nil {
		return nil, err
	}
	s.written(ctx, r, len(msgs) > 0)
	s.kick()
	return ToPB(r), nil
}

// kick wakes the claims waiting for a job.
func (s *Service) kick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wake != nil {
		close(s.wake)
	}
	s.wake = make(chan struct{})
}

func (s *Service) waiter() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wake == nil {
		s.wake = make(chan struct{})
	}
	return s.wake
}

// seen records a worker as live with its kinds (nil kinds: keeps the ones it claimed).
func (s *Service) seen(worker string, kinds []string) {
	if worker == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workers == nil {
		s.workers = map[string]*jobsv1.Worker{}
	}
	w := s.workers[worker]
	if w == nil {
		if kinds == nil {
			return
		}
		w = &jobsv1.Worker{Id: worker}
		s.workers[worker] = w
	}
	if kinds != nil {
		w.Kinds = slices.Sorted(slices.Values(kinds))
	}
	w.LastSeen = pbconv.Time(s.now())
}

// Workers implements jobkit.Queue: the workers seen within a lease and a claim wait.
func (s *Service) Workers(context.Context) ([]*jobsv1.Worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	horizon := s.now().Add(-(s.lease() + jobkit.MaxWait))
	var out []*jobsv1.Worker
	for id, w := range s.workers {
		if w.LastSeen.AsTime().Before(horizon) {
			delete(s.workers, id)
			continue
		}
		out = append(out, &jobsv1.Worker{Id: w.Id, Kinds: slices.Clone(w.Kinds), LastSeen: w.LastSeen})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
	return out, nil
}

// Claim implements jobkit.Queue.
func (s *Service) Claim(ctx context.Context, worker string, kinds []string, wait time.Duration) (*jobkit.Claim, error) {
	if worker == "" || len(kinds) == 0 {
		return nil, fmt.Errorf("a claim names its worker and its kinds: %w", jobkit.ErrInvalid)
	}
	wait = min(max(wait, 0), jobkit.MaxWait)
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		s.seen(worker, kinds)
		wake := s.waiter()
		r, ok, err := s.Store.Claim(ctx, kinds, func(r *Record) []outbox.Message {
			now := s.now()
			r.Status, r.Worker, r.Started, r.Updated, r.LeaseUntil = jobkit.StatusRunning, worker, now, now, now.Add(s.lease())
			return s.event(ctx, *r, EventStarted)
		})
		if err != nil {
			return nil, err
		}
		if ok {
			s.written(ctx, r, s.relay != nil)
			return &jobkit.Claim{Job: ToPB(r), Principal: jobkit.DecodePrincipal(r.Principal), Lease: s.lease()}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, nil
		case <-wake:
		}
	}
}

// Report implements jobkit.Queue.
func (s *Service) Report(ctx context.Context, id, worker string, rep jobkit.Report) (bool, error) {
	if rep.Status != "" && !jobkit.Final(rep.Status) {
		return false, fmt.Errorf("status %q: done, failed or cancelled: %w", rep.Status, jobkit.ErrInvalid)
	}
	s.seen(worker, nil)
	ended := false
	r, err := s.Store.Update(ctx, id, func(r *Record) ([]outbox.Message, error) {
		if r.Status != jobkit.StatusRunning || r.Worker != worker {
			return nil, fmt.Errorf("job %s is %s, not running for %s: %w", id, r.Status, worker, jobkit.ErrLost)
		}
		now := s.now()
		r.Progress, r.Done, r.Total, r.Updated, r.LeaseUntil = rep.Progress, rep.Done, rep.Total, now, now.Add(s.lease())
		if rep.Stats != nil {
			r.Stats = maps.Clone(rep.Stats)
		}
		if rep.Output != nil {
			r.Output = maps.Clone(rep.Output)
		}
		if rep.Status == "" {
			return nil, nil
		}
		ended = true
		r.Status, r.Error, r.Ended, r.LeaseUntil = rep.Status, rep.Error, now, time.Time{}
		if r.Status == jobkit.StatusDone && r.Total > 0 {
			r.Done = r.Total
		}
		return s.event(ctx, *r, EventEnded), nil
	})
	if err != nil {
		return false, err
	}
	s.written(ctx, r, ended && s.relay != nil)
	return r.CancelRequested, nil
}

// running is the job of a worker, refused when it no longer runs for it.
func (s *Service) running(ctx context.Context, id, worker string) (Record, error) {
	r, err := s.Store.Get(ctx, id)
	if err != nil {
		return r, err
	}
	if r.Status != jobkit.StatusRunning || r.Worker != worker {
		return r, fmt.Errorf("job %s is %s, not running for %s: %w", id, r.Status, worker, jobkit.ErrLost)
	}
	return r, nil
}

// ReadInput implements jobkit.Queue.
func (s *Service) ReadInput(ctx context.Context, id, worker string, offset int64) ([]byte, bool, error) {
	if _, err := s.running(ctx, id, worker); err != nil {
		return nil, false, err
	}
	return s.Store.ReadBlob(ctx, id, BlobInput, max(offset, 0), readChunk)
}

// WriteResult implements jobkit.Queue.
func (s *Service) WriteResult(ctx context.Context, id, worker, name, mediaType string, data []byte) (int64, error) {
	if _, err := s.running(ctx, id, worker); err != nil {
		return 0, err
	}
	size, err := s.Store.AppendBlob(ctx, id, BlobResult, data)
	if err != nil {
		return 0, err
	}
	r, err := s.Store.Update(ctx, id, func(r *Record) ([]outbox.Message, error) {
		if r.ResultName == "" {
			if name == "" {
				name = r.ID
			}
			r.ResultName, r.ResultType = name, mediaType
		}
		if r.ResultType == "" {
			r.ResultType = "application/octet-stream"
		}
		r.ResultSize, r.Updated = size, s.now()
		return nil, nil
	})
	if err != nil {
		return 0, err
	}
	s.publish(r)
	return size, nil
}

// mayRead says whether a person reads a job: who it runs for, or an administrator.
func mayRead(p authz.Principal, r Record) bool {
	return (p.EndUser() != "" && r.By == p.EndUser()) || p.HasRole(access.RoleAdmin)
}

// Get returns a job the caller may read.
func (s *Service) Get(ctx context.Context, id string) (Record, error) {
	p := authz.From(ctx)
	if p.Anonymous() {
		return Record{}, fmt.Errorf("anonymous: %w", jobkit.ErrForbidden)
	}
	r, err := s.Store.Get(ctx, id)
	if err != nil {
		return r, err
	}
	if !mayRead(p, r) {
		return Record{}, fmt.Errorf("job %s: %w", id, jobkit.ErrNotFound)
	}
	return r, nil
}

// ListRequest selects the caller's jobs.
type ListRequest struct {
	KindPrefix, Target string
	// All: every person's (administrators only).
	All   bool
	Limit int
}

// List lists the caller's jobs, newest first.
func (s *Service) List(ctx context.Context, req ListRequest) ([]Record, error) {
	p := authz.From(ctx)
	if p.Anonymous() {
		return nil, fmt.Errorf("anonymous: %w", jobkit.ErrForbidden)
	}
	f := Filter{By: p.EndUser(), KindPrefix: req.KindPrefix, Target: req.Target, Limit: req.Limit}
	if req.All {
		if !p.HasRole(access.RoleAdmin) {
			return nil, fmt.Errorf("every person's jobs: administrators only: %w", jobkit.ErrForbidden)
		}
		f.By = ""
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	f.Limit = min(f.Limit, 200)
	return s.Store.List(ctx, f)
}

// Cancel cancels a job of the caller: a queued one at once, a running one when its worker next reports.
func (s *Service) Cancel(ctx context.Context, id string) (Record, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return Record{}, err
	}
	ended := false
	r, err := s.Store.Update(ctx, id, func(r *Record) ([]outbox.Message, error) {
		switch r.Status {
		case jobkit.StatusQueued:
			now := s.now()
			ended = true
			r.Status, r.Ended, r.Updated, r.Error = jobkit.StatusCancelled, now, now, "cancelled before it started"
			return s.event(ctx, *r, EventEnded), nil
		case jobkit.StatusRunning:
			r.CancelRequested, r.Updated = true, s.now()
		}
		return nil, nil
	})
	if err != nil {
		return r, err
	}
	s.written(ctx, r, ended && s.relay != nil)
	return r, nil
}

// Sweep fails the running jobs whose lease lapsed (their worker is lost) and forgets the jobs ended past the retention.
func (s *Service) Sweep(ctx context.Context) error {
	now := s.now()
	ids, err := s.Store.Expired(ctx, now)
	if err != nil {
		return err
	}
	for _, id := range ids {
		r, err := s.Store.Update(ctx, id, func(r *Record) ([]outbox.Message, error) {
			if r.Status != jobkit.StatusRunning || !r.LeaseUntil.Before(now) {
				return nil, nil
			}
			r.Status, r.Error = jobkit.StatusFailed, "the worker running it was lost ("+r.Worker+")"
			if r.CancelRequested {
				r.Status, r.Error = jobkit.StatusCancelled, "cancelled"
			}
			r.Ended, r.Updated, r.LeaseUntil = now, now, time.Time{}
			return s.event(ctx, *r, EventEnded), nil
		})
		if err != nil {
			return err
		}
		s.log().Warn("job lease lapsed", "job", id, "worker", r.Worker, "status", r.Status)
		s.written(ctx, r, s.relay != nil)
	}
	retention := s.Retention
	if retention <= 0 {
		retention = DefaultRetention
	}
	if n, err := s.Store.Purge(ctx, now.Add(-retention)); err != nil {
		return err
	} else if n > 0 {
		s.log().Info("jobs purged", "count", n)
	}
	return nil
}

// Run sweeps every few seconds until ctx is done.
func (s *Service) Run(ctx context.Context) {
	every := min(s.lease()/3, 10*time.Second)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
				s.log().Warn("job sweep", "err", err)
			}
		}
	}
}

// subscriber is a stream of jobs of a person.
type subscriber struct {
	by  string
	all bool
	job string
	ch  chan Record
}

func (s *subscriber) sees(r Record) bool {
	return (s.all || r.By == s.by) && (s.job == "" || r.ID == s.job)
}

// Subscribe opens a stream of the jobs the caller may read (one job when id is set): every write of one is sent, the
// channel is closed when the reader falls too far behind (it reconnects). cancel ends it.
func (s *Service) Subscribe(p authz.Principal, id string, all bool) (<-chan Record, func()) {
	sub := &subscriber{by: p.EndUser(), all: all && p.HasRole(access.RoleAdmin), job: id, ch: make(chan Record, 64)}
	s.mu.Lock()
	if s.subs == nil {
		s.subs = map[*subscriber]struct{}{}
	}
	s.subs[sub] = struct{}{}
	s.mu.Unlock()
	return sub.ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subs[sub]; ok {
			delete(s.subs, sub)
			close(sub.ch)
		}
	}
}

func (s *Service) publish(r Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs {
		if !sub.sees(r) {
			continue
		}
		select {
		case sub.ch <- r.clone():
		default:
			// too slow: it reconnects and starts from a snapshot
			delete(s.subs, sub)
			close(sub.ch)
		}
	}
}

// Result opens the result of a job the caller may read: its record, and a reader of its chunks from an offset.
func (s *Service) Result(ctx context.Context, id string) (Record, func(offset int64) ([]byte, bool, error), error) {
	r, err := s.Get(ctx, id)
	if err != nil {
		return r, nil, err
	}
	if r.ResultName == "" {
		return r, nil, fmt.Errorf("job %s has no result: %w", id, jobkit.ErrNotFound)
	}
	return r, func(offset int64) ([]byte, bool, error) {
		return s.Store.ReadBlob(ctx, id, BlobResult, offset, readChunk)
	}, nil
}
