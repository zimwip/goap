package enginee2e

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/engine/engine"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/llm"
)

func TestCancelInterruptsARunInFlight(t *testing.T) {
	hub := &fakeHub{bound: map[string][]string{"acme": {"document-repository"}}}
	called := make(chan struct{})
	var seen error
	client := llm.ClientFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
		close(called)
		<-ctx.Done() // a long model call: it ends with its context
		seen = ctx.Err()
		return llm.Response{}, ctx.Err()
	})
	e, base := mcpEngine(t, hub, client)
	e.Methodologies.(engine.StaticMethodologies)["docs"] = parseDocs(t, docsAsLLM)
	alice := authz.With(context.Background(), authz.Principal{Subject: "alice", Org: "acme", Roles: []string{"admin"}})
	p, err := e.Start(alice, engine.StartRequest{Methodology: "docs", Agent: "writer", Goal: "done", ForkCommit: base, Intent: "document it", OwnerOrg: "acme", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *engine.Process)
	go func() {
		got, _ := e.Run(alice, p.ID)
		done <- got
	}()
	<-called
	c, err := e.Cancel(alice, p.ID, "wrong change")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not stop")
	}
	if c.Status != engine.StatusCancelled || c.Error != "cancelled by alice: wrong change" || !errors.Is(seen, context.Canceled) {
		t.Fatalf("cancelled: %s %q, the model call saw %v", c.Status, c.Error, seen)
	}
	stored, _ := e.Store.Get(alice, p.ID)
	if stored.Status != engine.StatusCancelled || stored.Pending != nil {
		t.Fatalf("stored %s %+v", stored.Status, stored.Pending)
	}
	// it stays cancelled: a later run does nothing, a second cancellation is refused
	if again, _ := e.Run(alice, p.ID); again.Status != engine.StatusCancelled {
		t.Fatalf("run after cancel: %s", again.Status)
	}
	if _, err := e.Cancel(alice, p.ID, ""); !errors.Is(err, engine.ErrInvalidState) {
		t.Fatalf("second cancel: %v", err)
	}
}

func TestCancelAWaitingRunOnlyByWhoAnswersForIt(t *testing.T) {
	e, _, p := deliverUntilReviewed(t, contributor)
	other := authz.With(context.Background(), authz.Principal{Subject: "dave", Org: "globex"})
	if _, err := e.Cancel(other, p.ID, "no"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("forbidden: %v", err)
	}
	c, err := e.Cancel(authz.With(context.Background(), contributor), p.ID, "")
	if err != nil || c.Status != engine.StatusCancelled || c.Pending != nil || !strings.HasPrefix(c.Error, "cancelled by "+contributor.Subject) {
		t.Fatalf("cancel: %v %s %+v %q", err, c.Status, c.Pending, c.Error)
	}
}

func TestCancelTakesTheLiveSubProcesses(t *testing.T) {
	e, _, _ := setup(t)
	ctx := authz.With(context.Background(), authz.Principal{Subject: "alice"})
	parent := &engine.Process{ID: "parent", Status: engine.StatusWaiting, Initiator: authz.Principal{Subject: "alice"}, Pending: &engine.HumanTask{Kind: engine.TaskAgent, ChildProcessID: "child"}}
	child := &engine.Process{ID: "child", ParentID: "parent", Status: engine.StatusWaiting, Initiator: authz.Principal{Subject: "alice"}}
	done := &engine.Process{ID: "done", ParentID: "parent", Status: engine.StatusCompleted, Initiator: authz.Principal{Subject: "alice"}}
	for _, p := range []*engine.Process{parent, child, done} {
		if err := e.Store.Put(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Cancel(ctx, "parent", "stop"); err != nil {
		t.Fatal(err)
	}
	e.Drain()
	for id, want := range map[string]engine.Status{"parent": engine.StatusCancelled, "child": engine.StatusCancelled, "done": engine.StatusCompleted} {
		if p, _ := e.Store.Get(ctx, id); p.Status != want {
			t.Errorf("%s: %s, want %s", id, p.Status, want)
		}
	}
	if c, _ := e.Store.Get(ctx, "child"); c.Error != "cancelled by alice: its parent process was cancelled" {
		t.Errorf("child error %q", c.Error)
	}
}
