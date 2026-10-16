package startingpoint

import (
	"slices"
	"testing"

	enginev1 "github.com/zimwip/goap/contracts/gen/goap/engine/v1"
	registryv1 "github.com/zimwip/goap/contracts/gen/goap/registry/v1"
)

func TestCompose(t *testing.T) {
	view := &registryv1.MethodologyView{Methodology: "m", Version: "1.0.0",
		Processes: []*registryv1.ViewActivity{{Name: "delivery", Kind: "process", Steps: []*registryv1.ViewStep{
			{Path: "delivery/prepare", Name: "prepare", Owner: "delivery", Description: "Prepare", Guidance: "Read first",
				Exit: map[string]bool{"noted": true, "stale": false}, Roles: &registryv1.Responsibilities{Responsible: "writer", Accountable: "reviewer"}},
			{Path: "delivery/verify", Name: "verify", Owner: "delivery", Capability: "verification", Roles: &registryv1.Responsibilities{Responsible: "writer"}},
			{Path: "delivery/approve", Name: "approve", Owner: "delivery"},
		}}},
		Methods: []*registryv1.ViewActivity{{Name: "peer_check", Kind: "method", For: "verification", Roles: &registryv1.Responsibilities{Responsible: "peer"},
			Steps: []*registryv1.ViewStep{{Path: "peer_check/read", Name: "read", Owner: "peer_check"}}}},
	}
	b := &enginev1.ListStartingPointsResponse{Methodology: "m", Version: "1.0.0", Goal: "ship", BlockedCount: 3, Points: []*enginev1.StartingPoint{
		{Id: "delivery/prepare", Kind: KindStep, Why: []string{"framed"}, MayRun: true, Launch: &enginev1.StartLaunch{Methodology: "m", Agent: "delivery", Goal: "delivery/prepare", ChangeId: "C"}},
		{Id: "delivery/verify", Kind: KindMethod, Method: "peer_check", Launch: &enginev1.StartLaunch{Agent: "peer_check", Goal: "peer_check"}},
		{Id: "peer_check/read", Kind: KindMethod, Parent: "delivery/verify", Method: "peer_check", Running: true},
		{Id: "gone/step", Kind: KindStep},
	}, Blocked: []*enginev1.BlockedStep{{Id: "delivery/approve", Missing: []string{"checked"}}}}

	got := Compose(view, b)
	if got.Methodology != "m" || got.Version != "1.0.0" || got.Goal != "ship" || got.BlockedCount != 3 || len(got.Points) != 4 {
		t.Fatalf("%+v", got)
	}
	p := got.Points[0]
	if p.Name != "prepare" || p.Process != "delivery" || p.Description != "Prepare" || p.Guidance != "Read first" || p.Responsible != "writer" ||
		p.Accountable != "reviewer" || !slices.Equal(p.Produces, []string{"!stale", "noted"}) || !p.Possible() || p.Launch.ChangeID != "C" {
		t.Fatalf("step %+v", p)
	}
	// a step proposed through its method: the capability of the step, the roles of the method
	if p := got.Points[1]; p.Capability != "verification" || p.Responsible != "peer" || p.Name != "verify" {
		t.Fatalf("method %+v", p)
	}
	// a step of the method: the capability the method provides
	if p := got.Points[2]; p.Capability != "verification" || p.Process != "peer_check" || p.Possible() {
		t.Fatalf("step of a method %+v", p)
	}
	// a step the view does not hold keeps its binding
	if p := got.Points[3]; p.ID != "gone/step" || p.Name != "" {
		t.Fatalf("unknown %+v", p)
	}
	if bl := got.Blocked; len(bl) != 1 || bl[0].Name != "approve" || bl[0].Missing[0] != "checked" {
		t.Fatalf("blocked %+v", bl)
	}
	if pt, ok := got.Find("delivery/verify"); !ok || pt.Method != "peer_check" {
		t.Fatal("find")
	}
	// no view: the bindings only
	if bare := Compose(nil, b); len(bare.Points) != 4 || bare.Points[0].Name != "" || bare.Points[0].Launch.Agent != "delivery" {
		t.Fatalf("bare %+v", bare)
	}
}
