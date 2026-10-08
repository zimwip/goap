package engine

import (
	"context"
	"testing"

	"github.com/zimwip/goap/pkg/engine/blackboard"
	"github.com/zimwip/goap/pkg/graph"
)

// A run records itself on its change (ADR 0098): the methodology the change carries (execution@Methodology, primary,
// with its main goal) and the run with its status (execution@Run), read back through the execution view.
func TestRunsAreRecordedOnTheirChange(t *testing.T) {
	ctx := context.Background()
	e, _ := agentsSetup(t)
	tm := &TriggerManager{Engine: e}
	if err := tm.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := tm.Fire(ctx, "test-design", "test-designer", "nightly")
	if err != nil {
		t.Fatal(err)
	}
	e.Drain()
	p, _ = e.Store.Get(ctx, p.ID)
	if p.Status != StatusCompleted || p.Recorded == nil || p.Recorded.Status != StatusCompleted {
		t.Fatalf("run %s recorded %+v", p.Status, p.Recorded)
	}
	bb, err := e.Graph.(*graph.Graph).Blackboard(ctx, p.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	v := blackboard.Of(bb)
	ms := v.Methodologies()
	if len(ms) != 1 || ms[0].Name != "test-design" || ms[0].Role != blackboard.RolePrimary || ms[0].Version == "" {
		t.Fatalf("methodologies %+v", ms)
	}
	runs := v.Runs()
	if len(runs) != 1 || runs[0].ID != p.ID || runs[0].Status != string(StatusCompleted) || runs[0].Agent != "test-designer" || runs[0].StartedAt.IsZero() {
		t.Fatalf("runs %+v", runs)
	}
	if o := v.OfType(blackboard.TypeRun)[0]; o.Version < 2 || o.Labels["process"] != p.ID {
		t.Fatalf("the run record follows its status: %+v", o)
	}
}
