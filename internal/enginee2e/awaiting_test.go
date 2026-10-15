package enginee2e

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/engine"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/journal"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/risk"
)

func TestAPersonCanAlwaysUnblockAStuckRun(t *testing.T) {
	e, g, p := deliverUntilReviewed(t, contributor)
	ctx := authz.With(context.Background(), approver)
	p, err := e.Approve(ctx, p.ID, false, "not now")
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(ctx, p.ID)
	if p.Status != engine.StatusStuck || p.Pending == nil || p.Pending.Kind != engine.TaskUnblock || p.Status.Terminal() {
		t.Fatalf("stuck with a task to unblock it: %s %+v", p.Status, p.Pending)
	}
	// someone who does not answer for the run may not
	other := authz.Principal{Subject: "dave", Org: "globex"}
	if _, err := e.Unblock(authz.With(context.Background(), other), p.ID, engine.UnblockRequest{Decision: engine.UnblockRetry}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("forbidden: %v", err)
	}
	// its initiator retries what it gave up on: the application asks for its approval again
	owner := authz.With(context.Background(), contributor)
	if p, err = e.Unblock(owner, p.ID, engine.UnblockRequest{Decision: engine.UnblockRetry}); err != nil || p.Status != engine.StatusRunning || len(p.Disabled) != 0 {
		t.Fatalf("retry: %v %s %v", err, p.Status, p.Disabled)
	}
	if p, _ = e.Run(owner, p.ID); p.Status != engine.StatusWaiting || p.Pending.Kind != engine.TaskApproval {
		t.Fatalf("approval asked again: %s %+v", p.Status, p.Pending)
	}
	// rejected again: the approver of the organisation abandons it, with its reason
	if p, err = e.Approve(ctx, p.ID, false, "still not"); err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(ctx, p.ID)
	if _, err := e.Unblock(ctx, p.ID, engine.UnblockRequest{Decision: engine.UnblockAbandon}); !errors.Is(err, engine.ErrInvalidState) {
		t.Fatalf("a reason is required: %v", err)
	}
	if p, err = e.Unblock(ctx, p.ID, engine.UnblockRequest{Decision: engine.UnblockAbandon, Reason: "postponed to the next release"}); err != nil || p.Status != engine.StatusFailed {
		t.Fatalf("abandon: %v %s", err, p.Status)
	}
	recs, _ := journal.Read(ctx, g, journal.Filter{ProcessIDs: []string{p.ID}})
	if n := len(slices.DeleteFunc(recs, func(r journal.Record) bool { return r.Kind != journal.KindUnblock })); n != 2 {
		t.Fatalf("each decision is journaled: %d", n)
	}
}

func TestAPersonEstablishesWhatARunWaitsFor(t *testing.T) {
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
	e.Methodologies.(engine.StaticMethodologies)[c.Name] = c
	p, err := e.Start(ctx, engine.StartRequest{Methodology: "choreo", Goal: "flow", Intent: "deliver", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(ctx, p.ID)
	if _, err = e.Submit(ctx, p.ID, []engine.ItemInput{{Type: "artifact", Target: "note"},
		{Type: "risk", Data: map[string]any{"key": "RSK-1", "title": "wrong note", "probability": 4.0, "impact": 4.0}}}); err != nil {
		t.Fatal(err)
	}
	if p, _ = e.Run(ctx, p.ID); !p.WaitsForConditions() {
		t.Fatalf("waits for the risks: %s %+v", p.Status, p.Pending)
	}
	// nobody declares the risks under control (ADR 0109 §6): a person mitigates the risk on the change, and the run,
	// asked to retry, sees it
	if _, err := e.Graph.AddItems(ctx, p.ChangeID, []domain.ChangeItem{{Type: risk.TypeAction,
		Data: map[string]any{"key": "ACT-1", "title": "review the note", "status": "open", "for": "RSK-1"}}}); err != nil {
		t.Fatal(err)
	}
	if p, err = e.Unblock(ctx, p.ID, engine.UnblockRequest{Decision: engine.UnblockRetry}); err != nil {
		t.Fatal(err)
	}
	if p, _ = e.Run(ctx, p.ID); p.Pending == nil || p.Pending.Action != "flow/approve" {
		t.Fatalf("resumes to its approval: %s %+v", p.Status, p.Pending)
	}
}
