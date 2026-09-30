package engine

import (
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/goap"
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
