package enginee2e

import (
	"context"
	"testing"

	"github.com/zimwip/goap/engine/engine"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
)

const applyOnly = `
name: apply-only
version: 1.0.0
namespace: alm
goal: done
conditions:
  - name: applied
    description: The change has been applied
    expr: change.status == "applied"
actions:
  - name: apply_change
    kind: builtin
    builtin: graph.apply
    description: Apply the change
    effects: {applied: true}
    cost: 1
agents:
  - name: applier
    description: Applies the change
    planner: goap
    actions: [apply_change]
    goals: [done]
goals:
  - name: done
    description: The change is applied
    pre: {applied: true}
    value: 1
`

// Two changes forked from the same baseline edit the same property: the run applying the second waits on the
// conflicts of the rebase (ADR 0101), a person settles them, and the run goes on and lands the change.
func TestRunWaitsOnTheConflictsOfTheRebase(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	m, err := methodology.Parse([]byte(applyOnly))
	if err != nil {
		t.Fatal(err)
	}
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies = engine.StaticMethodologies{cm.Definition().Name: cm}
	req, err := g.NodeByKey(ctx, "alm", "REQ-1")
	if err != nil {
		t.Fatal(err)
	}
	edited := func(title, value string) domain.Change {
		c, err := g.CreateChange(ctx, graph.NewChange{ProjectID: testProject, Namespace: "alm", Title: title, BaselineID: base, OwnBranch: true})
		if err != nil {
			t.Fatal(err)
		}
		cn, err := g.ImpactNodeCheckout(ctx, c.ID, graph.NodeCheckout{Node: req.ID, Rationale: title})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.ImpactNodeUpdate(ctx, c.ID, cn.ID, graph.NodeUpdate{Properties: map[string]any{"title": value}}); err != nil {
			t.Fatal(err)
		}
		if _, err := g.ImpactNodeReview(ctx, c.ID, cn.ID, domain.ReviewAccepted, "u", "ok"); err != nil {
			t.Fatal(err)
		}
		return c
	}
	first, second := edited("first", "One"), edited("second", "Two")
	if _, err := g.Apply(ctx, first.ID, ""); err != nil {
		t.Fatal(err)
	}

	p, err := e.Start(ctx, engine.StartRequest{Methodology: "apply-only", ChangeID: second.ID, ProjectID: testProject, Agent: "applier", Goal: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if p.Status != engine.StatusWaiting || p.Pending == nil || p.Pending.Kind != engine.TaskRebase || p.Pending.Rebase == nil {
		t.Fatalf("the run waits on the rebase: %s %s %+v", p.Status, p.Error, p.Pending)
	}
	if im := p.Pending.Rebase.Impacts; len(im) != 1 || im[0].Key != "REQ-1" || len(im[0].Conflicts) != 1 || im[0].Conflicts[0] != "props.title" {
		t.Fatalf("the task lists the conflict: %+v", p.Pending.Rebase)
	}
	if _, err := e.ResolveRebase(ctx, p.ID, ""); err == nil {
		t.Fatal("nothing is settled yet")
	}

	st, err := g.RebaseState(ctx, second.ID)
	if err != nil || len(st.Conflicts) != 1 {
		t.Fatalf("state: %+v %v", st, err)
	}
	impact := st.Conflicts[0].Impact
	if _, err := g.ImpactNodeResolve(ctx, second.ID, impact, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ImpactNodeReview(ctx, second.ID, impact, domain.ReviewAccepted, "u", "ok"); err != nil {
		t.Fatal(err)
	}
	if p, err = e.ResolveRebase(ctx, p.ID, "settled"); err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if p.Status != engine.StatusCompleted {
		t.Fatalf("the run completes: %s %s %+v", p.Status, p.Error, p.Pending)
	}
	c, err := g.Change(ctx, second.ID)
	if err != nil || c.Status != domain.ChangeApplied {
		t.Fatalf("the change is applied: %+v %v", c, err)
	}
	if n, err := g.NodeByKey(ctx, "alm", "REQ-1"); err != nil || n.Properties["title"] != "Two" {
		t.Fatalf("the change keeps its value: %+v %v", n, err)
	}
}
