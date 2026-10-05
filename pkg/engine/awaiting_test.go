package engine

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/goap"
	"github.com/zimwip/goap/pkg/journal"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/risk"
)

func TestWaitingIsToldFromStuck(t *testing.T) {
	e := &Engine{}
	write := goap.Action{Name: "write", Effects: map[string]bool{"written": true}, Cost: 1}
	release := goap.Action{Name: "release", Pre: map[string]bool{"written": true, "approved": true, "frozen": false}, Effects: map[string]bool{"released": true}, Cost: 1}
	goal := goap.Goal{Name: "shipped", Pre: map[string]bool{"released": true}}
	all := []goap.Action{write, release}
	world := goap.WorldState{"frozen": true}

	// approved and !frozen are established outside the agent: it waits for them
	if got := e.awaited(world, all, all, goal); !slices.Equal(got, []string{"!frozen", "approved"}) {
		t.Fatalf("awaited %v", got)
	}
	// once they hold, nothing is awaited any more (the planner finds a plan)
	if got := e.awaited(goap.WorldState{"approved": true, "frozen": false}, all, all, goal); got != nil {
		t.Fatalf("nothing to wait for: %v", got)
	}
	// the agent's own action that would establish what is missing is not admissible (disabled, unbound): stuck
	if got := e.awaited(goap.WorldState{"approved": true, "frozen": false}, all, []goap.Action{release}, goal); got != nil {
		t.Fatalf("stuck, not waiting: %v", got)
	}
	// what is established outside would not be enough: stuck
	if got := e.awaited(world, all, []goap.Action{release}, goal); got != nil {
		t.Fatalf("stuck, not waiting: %v", got)
	}
}

func TestAPersonCanAlwaysUnblockAStuckRun(t *testing.T) {
	e, g, p := deliverUntilReviewed(t, contributor)
	ctx := authz.With(context.Background(), approver)
	p, err := e.Approve(ctx, p.ID, false, "not now")
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(ctx, p.ID)
	if p.Status != StatusStuck || p.Pending == nil || p.Pending.Kind != TaskUnblock || p.Status.Terminal() {
		t.Fatalf("stuck with a task to unblock it: %s %+v", p.Status, p.Pending)
	}
	// someone who does not answer for the run may not
	other := authz.Principal{Subject: "dave", Org: "globex"}
	if _, err := e.Unblock(authz.With(context.Background(), other), p.ID, UnblockRequest{Decision: UnblockRetry}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("forbidden: %v", err)
	}
	// its initiator retries what it gave up on: the application asks for its approval again
	owner := authz.With(context.Background(), contributor)
	if p, err = e.Unblock(owner, p.ID, UnblockRequest{Decision: UnblockRetry}); err != nil || p.Status != StatusRunning || len(p.Disabled) != 0 {
		t.Fatalf("retry: %v %s %v", err, p.Status, p.Disabled)
	}
	if p, _ = e.Run(owner, p.ID); p.Status != StatusWaiting || p.Pending.Kind != TaskApproval {
		t.Fatalf("approval asked again: %s %+v", p.Status, p.Pending)
	}
	// rejected again: the approver of the organisation abandons it, with its reason
	if p, err = e.Approve(ctx, p.ID, false, "still not"); err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(ctx, p.ID)
	if _, err := e.Unblock(ctx, p.ID, UnblockRequest{Decision: UnblockAbandon}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("a reason is required: %v", err)
	}
	if p, err = e.Unblock(ctx, p.ID, UnblockRequest{Decision: UnblockAbandon, Reason: "postponed to the next release"}); err != nil || p.Status != StatusFailed {
		t.Fatalf("abandon: %v %s", err, p.Status)
	}
	recs, _ := journal.Read(ctx, g, journal.Filter{ProcessIDs: []string{p.ID}})
	if n := len(slices.DeleteFunc(recs, func(r journal.Record) bool { return r.Kind != journal.KindUnblock })); n != 2 {
		t.Fatalf("each decision is journaled: %d", n)
	}
}

func TestAPersonCanWaiveWhatARunWaitsFor(t *testing.T) {
	ctx := authz.With(context.Background(), contributor)
	e := stagedEngine(t)
	m, err := methodology.Parse([]byte(choreoYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(StaticMethodologies)[c.Name] = c
	p, err := e.Start(ctx, StartRequest{Methodology: "choreo", Goal: "flow", Intent: "deliver", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(ctx, p.ID)
	if _, err = e.Submit(ctx, p.ID, []ItemInput{{Kind: "artifact", Type: "note"},
		{Kind: "risk", Data: map[string]any{"key": "RSK-1", "title": "wrong note", "probability": 4.0, "impact": 4.0}}}); err != nil {
		t.Fatal(err)
	}
	if p, _ = e.Run(ctx, p.ID); !p.WaitsForConditions() {
		t.Fatalf("waits for the risks: %s %+v", p.Status, p.Pending)
	}
	// no one manages the risks of this change: its owner takes them, and says why
	if _, err := e.Unblock(ctx, p.ID, UnblockRequest{Decision: UnblockWaive, Conditions: []string{"risks_under_control"}}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("a reason is required: %v", err)
	}
	if _, err := e.Unblock(ctx, p.ID, UnblockRequest{Decision: UnblockWaive, Conditions: []string{"nonsense"}, Reason: "x"}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("a condition of the methodology: %v", err)
	}
	if p, err = e.Unblock(ctx, p.ID, UnblockRequest{Decision: UnblockWaive, Conditions: []string{"risks_under_control"}, Reason: "low-risk change"}); err != nil {
		t.Fatal(err)
	}
	if p, _ = e.Run(ctx, p.ID); p.Pending == nil || p.Pending.Action != "flow/approve" {
		t.Fatalf("resumes to its approval: %s %+v", p.Status, p.Pending)
	}
	bb, _ := e.Graph.Blackboard(ctx, p.ChangeID)
	if w := bb.Change.ItemsOfKind(risk.KindWaiver); len(w) != 1 || w[0].Data["reason"] != "low-risk change" {
		t.Fatalf("the waiver is a fact of the change: %+v", w)
	}
}
