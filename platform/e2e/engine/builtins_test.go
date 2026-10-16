package enginee2e

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zimwip/goap/engine/engine"

	"github.com/zimwip/goap/contracts/builtins"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/graph/graph"
)

func TestBuiltinRegister(t *testing.T) {
	b := engine.DefaultBuiltins()
	for _, n := range []string{builtins.GraphPropagate, builtins.GraphApply, builtins.DecisionInvestigate, builtins.ProcessStep} {
		if !b.HasBuiltin(n) {
			t.Errorf("%s not registered", n)
		}
	}
	if b.HasBuiltin(builtins.ObserveAnalyze) {
		t.Error("the engine registers no self-improvement builtin")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a duplicate registration must panic")
		}
	}()
	b.Register(builtins.GraphApply, engine.ApplyChange)
}

// applyStub is a GraphPort whose Apply left the change in the status given, and whose merge into main has one
// conflict: both branches changed the title.
type applyStub struct {
	engine.GraphPort
	status     domain.ChangeStatus
	integrated map[domain.NodeID]graph.Resolution
}

func (s *applyStub) AppendLog(context.Context, []domain.LogEntry) error { return nil }

func (s *applyStub) Apply(context.Context, domain.ChangeID, string) (domain.Baseline, error) {
	return domain.Baseline{ID: "b1", Name: "n"}, nil
}

func (s *applyStub) Change(_ context.Context, id domain.ChangeID) (domain.Change, error) {
	return domain.Change{ID: id, Namespace: "alm", Branch: "change-ab12cd34", Status: s.status}, nil
}

func (s *applyStub) Branch(_ context.Context, ns, name string) (domain.Branch, error) {
	return domain.Branch{Name: name, Namespace: ns, Parent: "main"}, nil
}

func (s *applyStub) PlanMerge(_ context.Context, _, from, into string) (graph.MergePlan, error) {
	return graph.MergePlan{From: from, Into: into, Candidates: []graph.MergeCandidate{
		{Node: "n1", Key: "REQ-1", Kind: "merge", Conflicts: []string{"title"},
			Base: map[string]any{"title": "a"}, OursProps: map[string]any{"title": "b"}, TheirsProps: map[string]any{"title": "c"}},
		{Node: "n2", Key: "REQ-2", Kind: "merge", Merged: map[string]any{"title": "x"}}, // merged by itself: not listed
	}}, nil
}

func (s *applyStub) IntegrateChange(_ context.Context, id domain.ChangeID, rs map[domain.NodeID]graph.Resolution) (domain.Change, error) {
	s.integrated = rs
	s.status = domain.ChangeApplied
	return domain.Change{ID: id, Status: domain.ChangeApplied}, nil
}

func TestApplyChangeAsksForTheConflictsOfTheMerge(t *testing.T) {
	run := func(st domain.ChangeStatus) (engine.ActionResult, error) {
		return engine.ApplyChange(context.Background(), engine.ActionContext{Graph: &applyStub{status: st}, Blackboard: domain.Blackboard{Change: domain.Change{ID: "c1"}}})
	}
	r, err := run(domain.ChangeCommitted)
	if err != nil || r.Merge == nil {
		t.Fatalf("a committed change waits for its merge: %v %+v", err, r)
	}
	if r.Merge.From != "change-ab12cd34" || r.Merge.Into != "main" || len(r.Merge.Conflicts) != 1 {
		t.Fatalf("the merge task: %+v", r.Merge)
	}
	pr := r.Merge.Conflicts[0].Properties[0]
	if pr.Name != "title" || pr.Ours != "b" || pr.Theirs != "c" || pr.Proposed != "c" || pr.Base != "a" {
		t.Fatalf("both values and the proposal are shown: %+v", pr)
	}
	if r, err := run(domain.ChangeApplied); err != nil || r.Merge != nil || !strings.Contains(r.Output, "created") {
		t.Fatalf("an applied change: %v %+v", err, r)
	}
}

func TestResolveMergeCompletesTheMergeAndResumes(t *testing.T) {
	ctx := context.Background()
	g := &applyStub{status: domain.ChangeCommitted}
	task, err := engine.Internals{}.MergeTaskOf(ctx, g, domain.Change{ID: "c1", Namespace: "alm", Branch: "change-ab12cd34"})
	if err != nil {
		t.Fatal(err)
	}
	e := &engine.Engine{Graph: g, Store: engine.NewMemoryStore()}
	p := &engine.Process{ID: "p1", ChangeID: "c1", Status: engine.StatusWaiting, Steps: []engine.Step{{Index: 0, Action: "apply"}},
		Pending: &engine.HumanTask{Kind: engine.TaskMerge, Action: "apply", Step: 0, Merge: task}}
	if err := e.Store.Put(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ResolveMerge(ctx, "p1", map[domain.NodeID]graph.Resolution{}, ""); err == nil {
		t.Fatal("every conflict needs a resolution")
	}
	got, err := e.ResolveMerge(ctx, "p1", map[domain.NodeID]graph.Resolution{"n1": {Props: map[string]any{"title": "c"}}}, "ok")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != engine.StatusRunning || got.Pending != nil || g.integrated["n1"].Props["title"] != "c" {
		t.Fatalf("the merge completes and the run goes on: %s %+v %+v", got.Status, got.Pending, g.integrated)
	}
	if _, err := e.ResolveMerge(ctx, "p1", nil, ""); err == nil {
		t.Fatal("nothing pending any more")
	}
}

// rebaseStub is a GraphPort whose Apply stops on the rebase: conflicts left to settle, impacts still to review.
type rebaseStub struct {
	engine.GraphPort
	applyErr  error
	conflicts []graph.ImpactConflicts
	proposed  bool
}

func (s *rebaseStub) AppendLog(context.Context, []domain.LogEntry) error { return nil }

func (s *rebaseStub) Apply(context.Context, domain.ChangeID, string) (domain.Baseline, error) {
	return domain.Baseline{}, s.applyErr
}

func (s *rebaseStub) RebaseState(context.Context, domain.ChangeID) (graph.RebaseState, error) {
	return graph.RebaseState{Branch: "main", Conflicts: s.conflicts}, nil
}

func (s *rebaseStub) Change(_ context.Context, id domain.ChangeID) (domain.Change, error) {
	c := domain.Change{ID: id, Status: domain.ChangeActive, Nodes: []domain.ChangeImpact{{ID: "i1", Key: "REQ-1", Review: domain.ReviewAccepted}}}
	if s.proposed {
		c.Nodes[0].Review = domain.ReviewProposed
	}
	return c, nil
}

func TestApplyChangeWaitsOnTheConflictsOfTheRebase(t *testing.T) {
	conflict := fmt.Errorf("change c1 rebased: %w", graph.ErrConflict)
	run := func(s *rebaseStub) (engine.ActionResult, error) {
		return engine.ApplyChange(context.Background(), engine.ActionContext{Graph: s, Blackboard: domain.Blackboard{Change: domain.Change{ID: "c1"}}})
	}
	r, err := run(&rebaseStub{applyErr: conflict, conflicts: []graph.ImpactConflicts{{Impact: "i1", Key: "REQ-1", Conflicts: []string{"props.title", "owner"}}}})
	if err != nil || r.Rebase == nil {
		t.Fatalf("a rebase conflict is a task: %v %+v", err, r)
	}
	if r.Rebase.Branch != "main" || len(r.Rebase.Impacts) != 1 || r.Rebase.Impacts[0].Key != "REQ-1" || len(r.Rebase.Impacts[0].Conflicts) != 2 {
		t.Fatalf("the task: %+v", r.Rebase)
	}
	// a conflict without any rebase conflict (impacts awaiting their review, say) stays an error
	if r, err := run(&rebaseStub{applyErr: conflict}); !errors.Is(err, graph.ErrConflict) || r.Rebase != nil {
		t.Fatalf("an ordinary conflict: %v %+v", err, r)
	}
	if _, err := run(&rebaseStub{applyErr: errors.New("boom"), conflicts: []graph.ImpactConflicts{{Impact: "i1"}}}); err == nil {
		t.Fatal("another error stays an error")
	}
}

func TestResolveRebaseNeedsTheConflictsSettledAndReviewed(t *testing.T) {
	ctx := context.Background()
	g := &rebaseStub{conflicts: []graph.ImpactConflicts{{Impact: "i1", Key: "REQ-1", Conflicts: []string{"props.title"}}}}
	e := &engine.Engine{Graph: g, Store: engine.NewMemoryStore()}
	p := &engine.Process{ID: "p1", ChangeID: "c1", Status: engine.StatusWaiting, Steps: []engine.Step{{Index: 0, Action: "apply"}},
		Pending: &engine.HumanTask{Kind: engine.TaskRebase, Action: "apply", Step: 0, Rebase: &engine.RebaseTask{Branch: "main", Impacts: []engine.RebaseConflict{{Impact: "i1", Key: "REQ-1", Conflicts: []string{"props.title"}}}}}}
	if err := e.Store.Put(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ResolveRebase(ctx, "p1", ""); !errors.Is(err, engine.ErrInvalidState) || !strings.Contains(err.Error(), "REQ-1") {
		t.Fatalf("a conflict remains: %v", err)
	}
	g.conflicts, g.proposed = nil, true
	if _, err := e.ResolveRebase(ctx, "p1", ""); !errors.Is(err, engine.ErrInvalidState) || !strings.Contains(err.Error(), "review") {
		t.Fatalf("an impact awaits its review: %v", err)
	}
	g.proposed = false
	got, err := e.ResolveRebase(ctx, "p1", "ok")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != engine.StatusRunning || got.Pending != nil {
		t.Fatalf("the run goes on: %s %+v", got.Status, got.Pending)
	}
	if _, err := e.ResolveRebase(ctx, "p1", ""); !errors.Is(err, engine.ErrInvalidState) {
		t.Fatalf("nothing pending any more: %v", err)
	}
}
