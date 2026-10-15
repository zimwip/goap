package enginee2e

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zimwip/goap/engine/engine"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/journal"
	"github.com/zimwip/goap/contracts/llm"
)

// sharedLocks is the lock service of several replicas in memory (the SQL store's twin, ADR 0124).
type sharedLocks struct {
	mu    sync.Mutex
	locks map[string]*sharedLock
}

type sharedLock struct {
	holder, interrupt string
	until             time.Time
}

// replica is the view of one replica on the shared locks.
type replica struct {
	s      *sharedLocks
	holder string
}

func (r replica) Acquire(_ context.Context, key string, ttl time.Duration) (bool, string, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	now := time.Now()
	l, ok := r.s.locks[key]
	switch {
	case !ok || !l.until.After(now):
		r.s.locks[key] = &sharedLock{holder: r.holder, until: now.Add(ttl)}
		return true, "", nil
	case l.holder == r.holder:
		l.until = now.Add(ttl)
		return true, l.interrupt, nil
	}
	return false, "", nil
}

func (r replica) Release(_ context.Context, key string) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if l, ok := r.s.locks[key]; ok && l.holder == r.holder {
		delete(r.s.locks, key)
	}
	return nil
}

func (r replica) Interrupt(_ context.Context, key, by string) (bool, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if l, ok := r.s.locks[key]; ok && l.holder != r.holder && l.until.After(time.Now()) {
		l.interrupt = by
		return true, nil
	}
	return false, nil
}

// orphanStore tells the running processes whose lock nobody holds (the SQL store's twin).
type orphanStore struct {
	*engine.MemoryStore
	locks *sharedLocks
}

func (s orphanStore) Orphans(ctx context.Context, before time.Time) ([]string, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	s.locks.mu.Lock()
	defer s.locks.mu.Unlock()
	var out []string
	for _, p := range all {
		if l, ok := s.locks.locks[p.ID]; p.Status == engine.StatusRunning && p.UpdatedAt.Before(before) && (!ok || !l.until.After(time.Now())) {
			out = append(out, p.ID)
		}
	}
	return out, nil
}

// twoReplicas are two engines on one store and one lock service, running the docs methodology with client.
func twoReplicas(t *testing.T, client llm.Client) (a, b *engine.Engine, start func() *engine.Process, locks *sharedLocks) {
	t.Helper()
	hub := &fakeHub{bound: map[string][]string{"acme": {"document-repository"}}}
	a, base := mcpEngine(t, hub, client)
	a.Methodologies.(engine.StaticMethodologies)["docs"] = parseDocs(t, docsAsLLM)
	locks = &sharedLocks{locks: map[string]*sharedLock{}}
	a.Store = orphanStore{MemoryStore: a.Store.(*engine.MemoryStore), locks: locks}
	a.Locks, a.LockTTL = replica{s: locks, holder: "a"}, 200*time.Millisecond
	b = &engine.Engine{Graph: a.Graph, Methodologies: a.Methodologies, Executors: a.Executors, Store: a.Store, Scope: a.Scope,
		Locks: replica{s: locks, holder: "b"}, LockTTL: a.LockTTL}
	start = func() *engine.Process {
		p, err := a.Start(alice(), engine.StartRequest{Methodology: "docs", Agent: "writer", Goal: "done", BaselineID: base, Intent: "document it", OwnerOrg: "acme", ProjectID: testProject})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	return a, b, start, locks
}

func alice() context.Context {
	return authz.With(context.Background(), authz.Principal{Subject: "alice", Org: "acme", Roles: []string{"admin"}})
}

func TestReplicasRunAProcessOnceAtATime(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	called := make(chan struct{}, 4)
	client := llm.ClientFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
		calls.Add(1)
		called <- struct{}{}
		<-release
		return llm.Response{Text: docItems}, nil
	})
	a, b, start, _ := twoReplicas(t, client)
	p := start()
	done := make(chan *engine.Process, 2)
	go func() { got, _ := a.Run(alice(), p.ID); done <- got }()
	<-called
	go func() { got, _ := b.Run(alice(), p.ID); done <- got }()
	time.Sleep(300 * time.Millisecond) // b waits for a's lock, renewed past its lease
	close(release)
	for range 2 {
		select {
		case got := <-done:
			if got == nil || got.Status != engine.StatusCompleted {
				t.Fatalf("run: %+v", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a run did not end")
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d model calls, want 1: the second replica ran the process again", n)
	}
}

func TestCancelReachesTheRunOfAnotherReplica(t *testing.T) {
	called := make(chan struct{})
	client := llm.ClientFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
		close(called)
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	})
	a, b, start, _ := twoReplicas(t, client)
	p := start()
	done := make(chan *engine.Process)
	go func() { got, _ := a.Run(alice(), p.ID); done <- got }()
	<-called
	c, err := b.Cancel(alice(), p.ID, "wrong change")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run on the other replica did not stop")
	}
	if c.Status != engine.StatusCancelled || c.Error != "cancelled by alice: wrong change" {
		t.Fatalf("cancelled: %s %q", c.Status, c.Error)
	}
	if stored, _ := a.Store.Get(alice(), p.ID); stored.Status != engine.StatusCancelled {
		t.Fatalf("stored %s %q", stored.Status, stored.Error)
	}
}

func TestRecoverResumesTheRunOfAStoppedReplica(t *testing.T) {
	client := llm.ClientFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
		return llm.Response{Text: docItems}, nil
	})
	_, b, start, locks := twoReplicas(t, client)
	p := start()
	// a replica took the process and stopped: its lease ends, nobody renews it
	locks.locks[p.ID] = &sharedLock{holder: "gone", until: time.Now().Add(100 * time.Millisecond)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Recover(ctx, 50*time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := b.Store.Get(alice(), p.ID)
		if got.Status == engine.StatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not resumed: %s %q", got.Status, got.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	b.Drain()
}

func TestAStepLeftInFlightIsNotWrittenTwice(t *testing.T) {
	artifact := func(t *testing.T, e *engine.Engine, p *engine.Process, exec string) {
		if _, err := e.Graph.AddItems(alice(), p.ChangeID, []domain.ChangeItem{{Type: domain.TypeArtifact, Target: "doc",
			Data: map[string]any{"seen": "ok"}, ProducedBy: "fetch_doc", Execution: exec}}); err != nil {
			t.Fatal(err)
		}
	}
	record := func(t *testing.T, e *engine.Engine, p *engine.Process, exec string) {
		if err := journal.Append(alice(), e.Graph, []journal.Record{{ID: exec, ChangeID: p.ChangeID, ProcessID: p.ID,
			Kind: journal.KindAction, Action: "fetch_doc", StartedAt: time.Now()}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		wrote []func(*testing.T, *engine.Engine, *engine.Process, string)
		calls int32
		check func(*testing.T, *engine.Process)
	}{
		{"nothing written: run again", nil, 1, func(t *testing.T, p *engine.Process) {
			if p.Status != engine.StatusCompleted || len(p.Steps) != 1 || p.Steps[0].Error != "" {
				t.Fatalf("%s %+v", p.Status, p.Steps)
			}
		}},
		{"ended: closed as it is", []func(*testing.T, *engine.Engine, *engine.Process, string){artifact, record}, 0, func(t *testing.T, p *engine.Process) {
			if p.Status != engine.StatusCompleted || len(p.Steps) != 1 || p.Steps[0].Error != "" || p.Steps[0].EndedAt.IsZero() {
				t.Fatalf("%s %+v", p.Status, p.Steps)
			}
		}},
		{"cut halfway: failed, not run again", []func(*testing.T, *engine.Engine, *engine.Process, string){artifact}, 0, func(t *testing.T, p *engine.Process) {
			if len(p.Steps) == 0 || !strings.Contains(p.Steps[0].Error, "interrupted after writing part") || !p.Disabled["fetch_doc"] {
				t.Fatalf("%s %+v", p.Status, p.Steps)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := llm.ClientFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
				calls.Add(1)
				return llm.Response{Text: docItems}, nil
			})
			_, b, start, _ := twoReplicas(t, client)
			p := start()
			// a replica checkpointed the step and stopped while it ran
			const exec = "exec-1"
			p.Steps = append(p.Steps, engine.Step{Action: "fetch_doc", Execution: exec, StartedAt: time.Now()})
			if err := b.Store.Put(alice(), p); err != nil {
				t.Fatal(err)
			}
			for _, w := range tc.wrote {
				w(t, b, p, exec)
			}
			got, err := b.Run(alice(), p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if n := calls.Load(); n != tc.calls {
				t.Fatalf("%d model calls, want %d", n, tc.calls)
			}
			tc.check(t, got)
		})
	}
}

func TestAReplicaAtCapacityLeavesTheRunToThePool(t *testing.T) {
	client := llm.ClientFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
		return llm.Response{Text: docItems}, nil
	})
	a, b, start, _ := twoReplicas(t, client)
	// a is full; b has room and is woken by a's signal
	a.MaxRuns, b.MaxRuns = 1, 1
	engine.InternalsOf(a).SetInflight(1)
	a.Signals = wakeOnly{b}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Recover(ctx, time.Hour)
	p := start()
	engine.InternalsOf(a).Schedule(p.ID)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := b.Store.Get(alice(), p.ID)
		if got.Status == engine.StatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not run by the replica with room: %s", got.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	b.Drain()
}

// wakeOnly delivers the signals of a replica to another, in process.
type wakeOnly struct{ to *engine.Engine }

func (w wakeOnly) Wake(context.Context) error { w.to.Woken(); return nil }
func (w wakeOnly) Interrupt(_ context.Context, id, by string) error {
	w.to.Interrupted(id, by)
	return nil
}

func TestAnInterruptSignalStopsTheRunAtOnce(t *testing.T) {
	called := make(chan struct{})
	client := llm.ClientFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
		close(called)
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	})
	a, b, start, _ := twoReplicas(t, client)
	a.LockTTL, b.LockTTL = time.Minute, time.Minute // renewals too rare to carry the ask in time
	b.Signals = wakeOnly{a}
	p := start()
	done := make(chan struct{})
	go func() { _, _ = a.Run(alice(), p.ID); close(done) }()
	<-called
	began := time.Now()
	c, err := b.Cancel(alice(), p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if c.Status != engine.StatusCancelled || time.Since(began) > 3*time.Second {
		t.Fatalf("cancelled %s after %s", c.Status, time.Since(began))
	}
}
