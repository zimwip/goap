package condition

import (
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/guard"
)

// CheckGuard evaluates the guard of a transition of the lifecycle of a change (ADR 0058) in the environment of the
// conditions: the guard sees the change (`change`, with the transition asked for and, when a decision point gates it,
// `change.decision`), its decision points, actions and risks (`decisionPoints`, `actions`, `risks`) and the world state (`world`, the value of each
// condition of its methodology).
func CheckGuard(expr string, bb domain.Blackboard, world map[string]bool, transition, decision string) (bool, error) {
	gd, err := guard.Compile(expr)
	if err != nil {
		return false, err
	}
	act := Activation(bb)
	change, _ := act["change"].(map[string]any)
	change["transition"], change["decision"] = transition, decision
	f := guard.Facts{}
	f.DecisionPoints, _ = Resolve(act["decisionPoints"]).([]any)
	f.Actions, _ = Resolve(act["actions"]).([]any)
	f.Risks, _ = Resolve(act["risks"]).([]any)
	return gd.CheckChange(change, f, world)
}
