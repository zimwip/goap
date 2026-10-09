package engine

import (
	"github.com/zimwip/goap/pkg/goap"
	"github.com/zimwip/goap/pkg/planning"
)

// awaited is planning.Awaited with the planner of the engine: what a process no plan reaches waits for (ADR 0036 §3).
func (e *Engine) awaited(world goap.WorldState, all, admissible []goap.Action, goal goap.Goal) []string {
	return planning.Awaited(e.Planner, world, all, admissible, goal)
}
