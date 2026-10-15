package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/llm"
)

// sharedLocks is the lock service of several replicas in memory (the SQL store's twin, ADR 0123).
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
	*MemoryStore
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
		if l, ok := s.locks.locks[p.ID]; p.Status == StatusRunning && p.UpdatedAt.Before(before) && (!ok || !l.until.After(time.Now())) {
			out = append(out, p.ID)
		}
	}
	return out, nil
}

// twoReplicas are two engines on one store and one lock service, running the docs methodology with client.
func twoReplicas(t *testing.T, client llm.Client) (a, b *Engine, start func() *Process, locks *sharedLocks) {
	t.Helper()
	hub := &fakeHub{bound: map[string][]string{"acme": {"document-repository"}}}
	a, base := mcpEngine(t, hub, client)
	a.Methodologies.(StaticMethodologies)["docs"] = parseDocs(t, docsAsLLM)
	locks = &sharedLocks{locks: map[string]*sharedLock{}}
	a.Store = orphanStore{MemoryStore: a.Store.(*MemoryStore), locks: locks}
	a.Locks, a.LockTTL = replica{s: locks, holder: "a"}, 200*time.Millisecond
	b = &Engine{Graph: a.Graph, Methodologies: a.Methodologies, Executors: a.Executors, Store: a.Store, Scope: a.Scope,
		Locks: replica{s: locks, holder: "b"}, LockTTL: a.LockTTL}
	start = func() *Process {
		p, err := a.Start(alice(), StartRequest{Methodology: "docs", Agent: "writer", Goal: "done", BaselineID: base, Intent: "document it", OwnerOrg: "acme", ProjectID: testProject})
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
	done := make(chan *Process, 2)
	go func() { got, _ := a.Run(alice(), p.ID); done <- got }()
	<-called
	go func() { got, _ := b.Run(alice(), p.ID); done <- got }()
	time.Sleep(300 * time.Millisecond) // b waits for a's lock, renewed past its lease
	close(release)
	for range 2 {
		select {
		case got := <-done:
			if got == nil || got.Status != StatusCompleted {
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
	done := make(chan *Process)
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
	if c.Status != StatusCancelled || c.Error != "cancelled by alice: wrong change" {
		t.Fatalf("cancelled: %s %q", c.Status, c.Error)
	}
	if stored, _ := a.Store.Get(alice(), p.ID); stored.Status != StatusCancelled {
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
		if got.Status == StatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not resumed: %s %q", got.Status, got.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	b.Drain()
}
