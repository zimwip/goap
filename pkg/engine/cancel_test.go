package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/llm"
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
	e.Methodologies.(StaticMethodologies)["docs"] = parseDocs(t, docsAsLLM)
	alice := authz.With(context.Background(), authz.Principal{Subject: "alice", Org: "acme", Roles: []string{"admin"}})
	p, err := e.Start(alice, StartRequest{Methodology: "docs", Agent: "writer", Goal: "done", BaselineID: base, Intent: "document it", OwnerOrg: "acme", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *Process)
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
	if c.Status != StatusCancelled || c.Error != "cancelled by alice: wrong change" || !errors.Is(seen, context.Canceled) {
		t.Fatalf("cancelled: %s %q, the model call saw %v", c.Status, c.Error, seen)
	}
	stored, _ := e.Store.Get(alice, p.ID)
	if stored.Status != StatusCancelled || stored.Pending != nil {
		t.Fatalf("stored %s %+v", stored.Status, stored.Pending)
	}
	// it stays cancelled: a later run does nothing, a second cancellation is refused
	if again, _ := e.Run(alice, p.ID); again.Status != StatusCancelled {
		t.Fatalf("run after cancel: %s", again.Status)
	}
	if _, err := e.Cancel(alice, p.ID, ""); !errors.Is(err, ErrInvalidState) {
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
	if err != nil || c.Status != StatusCancelled || c.Pending != nil || !strings.HasPrefix(c.Error, "cancelled by "+contributor.Subject) {
		t.Fatalf("cancel: %v %s %+v %q", err, c.Status, c.Pending, c.Error)
	}
}

func TestCancelTakesTheLiveSubProcesses(t *testing.T) {
	e, _, _ := setup(t)
	ctx := authz.With(context.Background(), authz.Principal{Subject: "alice"})
	parent := &Process{ID: "parent", Status: StatusWaiting, Initiator: authz.Principal{Subject: "alice"}, Pending: &HumanTask{Kind: TaskAgent, ChildProcessID: "child"}}
	child := &Process{ID: "child", ParentID: "parent", Status: StatusWaiting, Initiator: authz.Principal{Subject: "alice"}}
	done := &Process{ID: "done", ParentID: "parent", Status: StatusCompleted, Initiator: authz.Principal{Subject: "alice"}}
	for _, p := range []*Process{parent, child, done} {
		if err := e.Store.Put(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Cancel(ctx, "parent", "stop"); err != nil {
		t.Fatal(err)
	}
	e.Drain()
	for id, want := range map[string]Status{"parent": StatusCancelled, "child": StatusCancelled, "done": StatusCompleted} {
		if p, _ := e.Store.Get(ctx, id); p.Status != want {
			t.Errorf("%s: %s, want %s", id, p.Status, want)
		}
	}
	if c, _ := e.Store.Get(ctx, "child"); c.Error != "cancelled by alice: its parent process was cancelled" {
		t.Errorf("child error %q", c.Error)
	}
}
