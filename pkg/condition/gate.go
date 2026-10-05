package condition

import (
	"time"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/guard"
	"github.com/zimwip/goap/pkg/risk"
)

// CheckGuard evaluates the guard of a transition of the lifecycle of a change (ADR 0058) in the environment of the
// conditions: the guard sees the change (`change`, with the transition asked for and, when a decision point gates it,
// `change.decision`), its decision points, actions, risks, verifications and derogations (`decisionPoints`, `actions`,
// `risks`, `verifications`, `derogations`), what its organisation requires of its criticality (`criticalityPolicy`, and
// `change.criticality`) and the world state (`world`, the value of each condition of its methodology).
func CheckGuard(expr string, bb domain.Blackboard, world map[string]bool, transition, decision string) (bool, error) {
	gd, err := guard.Compile(expr)
	if err != nil {
		return false, err
	}
	change, f := guardInputs(bb, transition, decision)
	return gd.CheckChange(change, f, world)
}

func guardInputs(bb domain.Blackboard, transition, decision string) (map[string]any, guard.Facts) {
	act := Activation(bb)
	change, _ := act["change"].(map[string]any)
	change["transition"], change["decision"] = transition, decision
	f := guard.Facts{}
	f.DecisionPoints, _ = Resolve(act["decisionPoints"]).([]any)
	f.Actions, _ = Resolve(act["actions"]).([]any)
	f.Risks, _ = Resolve(act["risks"]).([]any)
	f.Verifications, _ = Resolve(act["verifications"]).([]any)
	f.Derogations, _ = Resolve(act["derogations"]).([]any)
	f.CriticalityPolicy, _ = Resolve(act["criticalityPolicy"]).(map[string]any)
	return change, f
}

// CheckGate evaluates the vetos and the objectives of a transition (ADR 0075 §3) in the environment of CheckGuard. A
// veto or an objective that does not hold is listed; an objective not met is covered when a derogation in force at the
// instant of the blackboard names it (its rule is the name of the objective): the first one in the register is kept. A
// criterion that cannot be evaluated is an error, not an unmet one.
func CheckGate(t domain.Transition, bb domain.Blackboard, world map[string]bool, decision string) (domain.GateResult, error) {
	var res domain.GateResult
	change, f := guardInputs(bb, t.Name, decision)
	eval := func(c domain.Criterion) (bool, error) {
		gd, err := guard.Compile(c.Expr)
		if err != nil {
			return false, err
		}
		return gd.CheckChange(change, f, world)
	}
	for _, c := range t.Vetos {
		ok, err := eval(c)
		if err != nil {
			return res, err
		}
		if !ok {
			res.Vetoed = append(res.Vetoed, c.Name)
		}
	}
	at := bb.At
	for _, c := range t.Objectives {
		ok, err := eval(c)
		if err != nil {
			return res, err
		}
		if ok {
			continue
		}
		res.Unmet = append(res.Unmet, c.Name)
		if at.IsZero() {
			at = time.Now()
		}
		for _, d := range risk.Derogations(bb.Change) {
			if d.Rule == c.Name && d.InForce(at) {
				if res.Reserve == nil {
					res.Reserve = map[string]string{}
				}
				res.Reserve[c.Name] = d.Key
				break
			}
		}
	}
	return res, nil
}
