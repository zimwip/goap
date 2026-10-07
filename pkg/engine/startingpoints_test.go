package engine

import (
	"context"
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
)

func pointIDs(sp *StartingPoints) []string {
	var ids []string
	for _, p := range sp.Points {
		ids = append(ids, p.ID)
	}
	return ids
}

func pointChange(t *testing.T, e *Engine, in graph.NewChange) domain.ChangeID {
	t.Helper()
	in.ProjectID, in.Title = testProject, "points"
	c, err := e.Graph.CreateChange(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// Only the steps whose entry conditions all hold are proposed, as steps and methods (never actions); the others
// are reported as blocked, and a step carried out or done leaves the list (ADR 0097).
func TestStartingPointsArePossibleStepsOnly(t *testing.T) {
	ctx := authz.With(context.Background(), authz.Principal{Subject: "w", Roles: []string{"writer@ORG-DEFAULT"}})
	e := stagedEngine(t)
	id := pointChange(t, e, graph.NewChange{Methodology: "staged", Goal: "delivery"})

	sp, err := e.StartingPoints(ctx, id, StartingPointsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// "prepare" is possible as a whole step (its sub-step "note" is the scheduler's business); "verify" needs the
	// note and "approve" the check: not proposed, however ready their actions would be
	if got := pointIDs(sp); !slices.Equal(got, []string{"delivery/prepare"}) {
		t.Fatalf("points %v", got)
	}
	pt := sp.Points[0]
	if pt.Kind != PointStep || pt.Launch.Agent != "delivery" || pt.Launch.Goal != "delivery/prepare" || pt.Launch.ChangeID != id ||
		pt.Launch.Methodology != "staged" || !pt.MayRun || pt.Running || pt.Responsible != "writer" || pt.Accountable != "reviewer" {
		t.Fatalf("point %+v", pt)
	}
	if sp.BlockedCount != 2 {
		t.Fatalf("blocked %+v", sp.Blocked)
	}
	for _, b := range sp.Blocked {
		if (b.ID == "delivery/verify" && !slices.Equal(b.Missing, []string{"noted"})) || (b.ID == "delivery/approve" && !slices.Equal(b.Missing, []string{"checked"})) {
			t.Fatalf("blocked %+v", b)
		}
	}

	// starting the point runs the step alone: the goal is its exit, the run carries the step
	p, err := e.Start(ctx, StartRequest{Methodology: pt.Launch.Methodology, Agent: pt.Launch.Agent, Goal: pt.Launch.Goal, ChangeID: pt.Launch.ChangeID})
	if err != nil {
		t.Fatal(err)
	}
	if p.Step == nil || p.Step.Path != "delivery/prepare" {
		t.Fatalf("the run carries the step: %+v", p.Step)
	}
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != StatusWaiting || p.Pending.Action != "delivery/prepare/note" {
		t.Fatalf("run %v %+v %v", err, p, p.Pending)
	}
	if sp, _ = e.StartingPoints(ctx, id, StartingPointsOptions{}); len(sp.Points) != 1 || !sp.Points[0].Running {
		t.Fatalf("a step carried out is running: %+v", sp.Points)
	}

	// the note is written: the step is done and leaves; verify is now possible, through the method that applies
	if _, err := e.Submit(ctx, p.ID, []ItemInput{{Kind: "artifact", Type: "note"}}); err != nil {
		t.Fatal(err)
	}
	e.schedule(p.ID)
	e.Drain()
	sp, _ = e.StartingPoints(ctx, id, StartingPointsOptions{})
	if got := pointIDs(sp); !slices.Equal(got, []string{"delivery/verify"}) {
		t.Fatalf("points after the note %v (%s)", got, sp.Reason)
	}
	pt = sp.Points[0]
	if pt.Kind != PointMethod || pt.Method != "peer_check" || pt.Capability != "verification" || pt.Launch.Agent != "peer_check" || pt.Launch.Goal != "peer_check" ||
		!slices.Equal(pt.Why, []string{"noted"}) {
		t.Fatalf("method point %+v", pt)
	}
	// capped blocked list
	if sp, _ = e.StartingPoints(ctx, id, StartingPointsOptions{MaxBlocked: 0}); sp.BlockedCount != 1 || len(sp.Blocked) != 1 || sp.Blocked[0].ID != "delivery/approve" {
		t.Fatalf("blocked %+v", sp.Blocked)
	}
	if sp, _ = e.StartingPoints(ctx, id, StartingPointsOptions{MaxPoints: 0}); len(sp.Points) != 1 {
		t.Fatal("default cap")
	}
}

func TestStartingPointsWithoutMethodologyOrGoal(t *testing.T) {
	ctx := context.Background()
	e := stagedEngine(t)
	for name, in := range map[string]graph.NewChange{
		"no methodology": {},
		"no goal":        {Methodology: "staged"},
	} {
		id := pointChange(t, e, in)
		if name == "no goal" {
			if _, err := e.Graph.UpdateChange(ctx, id, graph.ChangePatch{Goal: ptr("")}); err != nil {
				t.Fatal(err)
			}
		}
		sp, err := e.StartingPoints(ctx, id, StartingPointsOptions{})
		if err != nil || len(sp.Points) != 0 || sp.Reason == "" {
			t.Fatalf("%s: %+v %v", name, sp, err)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// The shipped sdlc methodology: its main goal `deliver` is the end point; at the start only the framing of the change is
// possible, every other phase waits for conditions.
func TestStartingPointsOfSDLC(t *testing.T) {
	ctx := context.Background()
	e, _, _ := setup(t)
	m, err := methodology.LoadFile("../../methodologies/sdlc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(StaticMethodologies)["sdlc"] = c
	id := pointChange(t, e, graph.NewChange{Methodology: "sdlc", Goal: "deliver"})
	sp, err := e.StartingPoints(ctx, id, StartingPointsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := pointIDs(sp); !slices.Equal(got, []string{"software_delivery/framing"}) {
		t.Fatalf("points %v (%s) blocked %+v", got, sp.Reason, sp.Blocked)
	}
	if sp.BlockedCount == 0 {
		t.Fatal("the other phases wait")
	}
	if sp.Points[0].Launch.Agent != "software_delivery" || sp.Points[0].Launch.Goal != "software_delivery/framing" {
		t.Fatalf("%+v", sp.Points[0])
	}
}
