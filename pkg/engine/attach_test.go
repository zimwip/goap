package engine

import (
	"context"
	"testing"

	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/graph/graphtest"
	"github.com/zimwip/goap/pkg/journal"
)

// The default (eager) path is unchanged for every existing methodology: a
// process binds its change immediately, before Run's own first cycle, and
// that bind is now visible as its own journal.attach record (ADR 0031, gap 5).
func TestEagerBindJournalsAttach(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	p, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", BaselineID: base, ProjectID: testProject, Intent: "The PSP changes its API, what does this break?"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ChangeID == "" || p.Status != StatusRunning {
		t.Fatalf("eager bind: process = %+v", p)
	}
	recs, err := journal.Read(ctx, g, journal.Filter{ChangeID: p.ChangeID, ProcessIDs: []string{p.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var attaches []journal.Record
	for _, r := range recs {
		if r.Kind == journal.KindAttach {
			attaches = append(attaches, r)
		}
	}
	if len(attaches) != 1 {
		t.Fatalf("expected exactly one attach record, got %d: %+v", len(attaches), attaches)
	}
	if attaches[0].Data["reused"] != false || attaches[0].Data["changeId"] != string(p.ChangeID) {
		t.Fatalf("attach record: %+v", attaches[0])
	}
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != StatusCompleted {
		t.Fatalf("run: %v %+v", err, p)
	}
}

// An agent that declares its own change_bound-effect action defers the bind:
// the process starts and plans with no change at all (only its own Vars),
// and only becomes bound once that action (here, a direct AttachChange call,
// standing in for the goap-scheduler/attach tool of a later pass) runs.
func TestDeferredBindStartsUnbound(t *testing.T) {
	ctx := context.Background()
	e, _, base := setup(t)
	statics := e.Methodologies.(StaticMethodologies)
	statics["deferred-bind"] = loadMethodology(t, "deferred-bind.yaml")

	p, err := e.Start(ctx, StartRequest{Methodology: "deferred-bind", Agent: "intake", Goal: "bound", BaselineID: base, ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if p.ChangeID != "" || p.Status != StatusRunning {
		t.Fatalf("expected an unbound, running process, got %+v", p)
	}
	m, err := e.Methodologies.Methodology(ctx, "deferred-bind")
	if err != nil {
		t.Fatal(err)
	}
	bb, err := e.observe(ctx, p, m)
	if err != nil {
		t.Fatal(err)
	}
	if bb.Change.ID != "" || len(bb.Nodes) != 0 {
		t.Fatalf("unbound process should see no change-backed blackboard, got %+v", bb)
	}
	if p.World["change_bound"] {
		t.Fatalf("change_bound should be false while unbound: %+v", p.World)
	}

	if err := e.AttachChange(ctx, p.ID, AttachRequest{}); err != nil {
		t.Fatal(err)
	}
	p, err = e.Store.Get(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.ChangeID == "" {
		t.Fatalf("AttachChange should have bound a change: %+v", p)
	}

	recs, err := journal.Read(ctx, e.Graph, journal.Filter{ChangeID: p.ChangeID, ProcessIDs: []string{p.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var attaches []journal.Record
	for _, r := range recs {
		if r.Kind == journal.KindAttach {
			attaches = append(attaches, r)
		}
	}
	if len(attaches) != 1 || attaches[0].Data["reused"] != false {
		t.Fatalf("expected exactly one non-reused attach record, got %+v", attaches)
	}

	// change_bound is now true and the goal was only ever that: the process
	// completes on its very next cycle without replanning "bind".
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != StatusCompleted {
		t.Fatalf("run after attach: %v %+v", err, p)
	}
}

// Reusing an existing change (the "change" argument of the future
// goap-scheduler/attach op) is the other AttachChange path.
func TestAttachChangeReusesExistingChange(t *testing.T) {
	ctx := context.Background()
	e, _, base := setup(t)
	statics := e.Methodologies.(StaticMethodologies)
	statics["deferred-bind"] = loadMethodology(t, "deferred-bind.yaml")

	existing, err := e.Graph.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: "existing", Intent: "pre-existing change to reuse", Namespace: "alm", BaselineID: base})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Start(ctx, StartRequest{Methodology: "deferred-bind", Agent: "intake", Goal: "bound", BaselineID: base, ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AttachChange(ctx, p.ID, AttachRequest{ChangeID: existing.ID}); err != nil {
		t.Fatal(err)
	}
	p, err = e.Store.Get(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.ChangeID != existing.ID {
		t.Fatalf("expected the process to be bound to the existing change %s, got %s", existing.ID, p.ChangeID)
	}
	recs, err := journal.Read(ctx, e.Graph, journal.Filter{ChangeID: existing.ID, ProcessIDs: []string{p.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Kind != journal.KindAttach || recs[0].Data["reused"] != true {
		t.Fatalf("expected one reused attach record, got %+v", recs)
	}
}

// A change is never created without a project (ADR 0091): a run naming none works in the root project (an empty claim
// means the root), and a run on an existing change works where the change does, also after the change moved.
func TestStartProjectOfTheChange(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	st, err := g.Structures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", BaselineID: base, Intent: "The PSP changes its API, what does this break?"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.Change(ctx, p.ChangeID)
	if err != nil || c.ProjectID != st.Project().Root || p.Project != st.Project().Root {
		t.Fatalf("no project: change in %q, run in %q, %v", c.ProjectID, p.Project, err)
	}

	own, err := g.CreateChange(ctx, graph.NewChange{Title: "t", Namespace: c.Namespace, BaselineID: base, ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	q, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", ChangeID: own.ID, ProjectID: "PROJ-OTHER", Intent: "what does this break?"})
	if err != nil {
		t.Fatal(err)
	}
	if q.Project != testProject {
		t.Fatalf("a run on a change works in its project: %q", q.Project)
	}
	if _, err := graphtest.Project(ctx, g, "PROJ-MOVED", "Moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.MoveChange(ctx, own.ID, "PROJ-MOVED"); err != nil {
		t.Fatal(err)
	}
	m, err := e.Methodologies.Methodology(ctx, "impact-analysis")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.observe(ctx, q, m); err != nil || q.Project != "PROJ-MOVED" {
		t.Fatalf("a run follows the move of its change: %q, %v", q.Project, err)
	}
}
