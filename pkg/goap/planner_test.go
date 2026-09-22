package goap

import (
	"errors"
	"testing"
)

var actions = []Action{
	{Name: "identify", Pre: map[string]bool{"has_impacts": false}, Effects: map[string]bool{"has_impacts": true}, Cost: 2},
	{Name: "propagate", Pre: map[string]bool{"has_impacts": true}, Effects: map[string]bool{"propagated": true}, Cost: 1},
	{Name: "propose", Pre: map[string]bool{"propagated": true}, Effects: map[string]bool{"proposed": true}, Cost: 3},
	{Name: "propose_quick", Pre: map[string]bool{"has_impacts": true}, Effects: map[string]bool{"proposed": true}, Cost: 10},
	{Name: "review", Pre: map[string]bool{"proposed": true}, Effects: map[string]bool{"reviewed": true}, Cost: 1},
}

func names(p *Plan) []string {
	var out []string
	for _, a := range p.Actions {
		out = append(out, a.Name)
	}
	return out
}

func TestPlanCheapest(t *testing.T) {
	start := WorldState{"has_impacts": false, "propagated": false, "proposed": false, "reviewed": false}
	p, err := Planner{}.Plan(start, actions, Goal{Name: "g", Pre: map[string]bool{"reviewed": true}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"identify", "propagate", "propose", "review"}
	if got := names(p); len(got) != len(want) || got[0] != want[0] || got[2] != want[2] {
		t.Fatalf("got %v want %v", got, want)
	}
	if p.Cost != 7 {
		t.Fatalf("cost %v", p.Cost)
	}
}

func TestPlanReplanFromMidState(t *testing.T) {
	start := WorldState{"has_impacts": true, "propagated": true, "proposed": false}
	p, err := Planner{}.Plan(start, actions, Goal{Name: "g", Pre: map[string]bool{"reviewed": true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(p); len(got) != 2 || got[0] != "propose" {
		t.Fatalf("got %v", got)
	}
}

func TestPlanAlreadySatisfied(t *testing.T) {
	p, err := Planner{}.Plan(WorldState{"reviewed": true}, actions, Goal{Pre: map[string]bool{"reviewed": true}})
	if err != nil || len(p.Actions) != 0 {
		t.Fatalf("expected empty plan, got %v %v", p, err)
	}
}

func TestUnknownConditionBlocks(t *testing.T) {
	// has_impacts unknown: "identify" requires has_impacts=false and cannot run.
	_, err := Planner{}.Plan(WorldState{}, actions, Goal{Pre: map[string]bool{"reviewed": true}})
	if !errors.Is(err, ErrNoPlan) {
		t.Fatalf("expected ErrNoPlan, got %v", err)
	}
}

func TestBestGoal(t *testing.T) {
	start := WorldState{"has_impacts": false, "propagated": false, "proposed": false, "reviewed": false}
	goals := []Goal{
		{Name: "assess", Pre: map[string]bool{"propagated": true}, Value: 10},
		{Name: "full", Pre: map[string]bool{"reviewed": true}, Value: 12},
	}
	p, err := Planner{}.Best(start, actions, goals)
	if err != nil {
		t.Fatal(err)
	}
	// assess: 10-3=7 ; full: 12-7=5
	if p.Goal.Name != "assess" {
		t.Fatalf("got %s", p)
	}
}
