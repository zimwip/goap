package registrysvc

import (
	"context"
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
)

// ActivityGoalsMet resolves pkg/graph.Graph.ActivityGoalsMet (architecture plan "Activity concept"): given an
// Activity's node key (methodology@Process/Step/Method/MethodStep, "MV:<name>@<version>/<kind>/<item>"), it loads
// and compiles that activity's own methodology version, then evaluates the activity's own goal condition against
// bb (built by the caller from the change's own impacts, ADR 0024). Wire it from main: g.ActivityGoalsMet =
// reg.ActivityGoalsMet, where reg is the *Service* backing the registry (it needs Store and Types, exactly what
// Service.Methodology already uses to compile by name - this does the same, pinned to a specific version).
func (s *Service) ActivityGoalsMet(ctx context.Context, activityRef string, bb domain.Blackboard) (bool, error) {
	name, version, kind, item, ok := parseActivityRef(activityRef)
	if !ok {
		return false, fmt.Errorf("%s: not an activity key: %w", activityRef, ErrNotFound)
	}
	r, err := s.Store.Get(ctx, name, version)
	if err != nil {
		return false, err
	}
	cat, err := s.Types(ctx)
	if err != nil {
		return false, err
	}
	c, err := r.Methodology.Resolve(cat).Compile()
	if err != nil {
		return false, err
	}
	cond, err := activityCondition(c, kind, item)
	if err != nil {
		return false, fmt.Errorf("%s: %w", activityRef, err)
	}
	state := c.Conditions.Evaluate(bb).State
	for k, want := range cond {
		if state[k] != want {
			return false, nil
		}
	}
	return true, nil
}

// activityCondition is the goal condition of a compiled Activity: a process's or a steps-composed method's own
// generated goal, a plain-agent method's named goal, or a step's/method-step's own exit criteria - the same
// condition map shape (CEL condition names to required values) in every case.
func activityCondition(c *methodology.Compiled, kind, item string) (map[string]bool, error) {
	switch kind {
	case "process":
		g, ok := c.Goal(item)
		if !ok {
			return nil, fmt.Errorf("process %q: no such goal", item)
		}
		return g.Pre, nil
	case "method":
		g, ok := c.Goal(c.MethodGoal(item))
		if !ok {
			return nil, fmt.Errorf("method %q: no goal", item)
		}
		return g.Pre, nil
	case "step", "methodStep":
		_, exit, ok := c.StepCriteria(item)
		if !ok {
			return nil, fmt.Errorf("step %q: unknown", item)
		}
		return exit, nil
	default:
		return nil, fmt.Errorf("unknown activity kind %q", kind)
	}
}

// parseActivityRef splits an Activity node key ("MV:<name>@<version>/<kind>/<item>", built by versionEdits /
// stepChildren) back into its methodology name, version, element kind and item name or step path.
func parseActivityRef(ref string) (name, version, kind, item string, ok bool) {
	rest, ok := strings.CutPrefix(ref, "MV:")
	if !ok {
		return "", "", "", "", false
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 {
		return "", "", "", "", false
	}
	name, version, ok = strings.Cut(parts[0], "@")
	if !ok {
		return "", "", "", "", false
	}
	return name, version, parts[1], parts[2], true
}
