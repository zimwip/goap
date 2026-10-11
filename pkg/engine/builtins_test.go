package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/builtins"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

func TestBuiltinRegister(t *testing.T) {
	b := DefaultBuiltins()
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
	b.Register(builtins.GraphApply, ApplyChange)
}

// applyStub is a GraphPort whose Apply left the change in the status given, and whose merge into main has one
// conflict: both branches changed the title.
type applyStub struct {
	GraphPort
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
	run := func(st domain.ChangeStatus) (ActionResult, error) {
		return ApplyChange(context.Background(), ActionContext{Graph: &applyStub{status: st}, Blackboard: domain.Blackboard{Change: domain.Change{ID: "c1"}}})
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
	task, err := mergeTaskOf(ctx, g, domain.Change{ID: "c1", Namespace: "alm", Branch: "change-ab12cd34"})
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Graph: g, Store: NewMemoryStore()}
	p := &Process{ID: "p1", ChangeID: "c1", Status: StatusWaiting, Steps: []Step{{Index: 0, Action: "apply"}},
		Pending: &HumanTask{Kind: TaskMerge, Action: "apply", Step: 0, Merge: task}}
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
	if got.Status != StatusRunning || got.Pending != nil || g.integrated["n1"].Props["title"] != "c" {
		t.Fatalf("the merge completes and the run goes on: %s %+v %+v", got.Status, got.Pending, g.integrated)
	}
	if _, err := e.ResolveMerge(ctx, "p1", nil, ""); err == nil {
		t.Fatal("nothing pending any more")
	}
}
