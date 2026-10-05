package registrysvc

import (
	"context"
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
)

// DataActivity is the key of Change.Data holding the Activity a change is scoped to (architecture plan "Activity
// concept"): the node key of a methodology@Process/Step/Method/MethodStep ("MV:<name>@<version>/<kind>/<item>"), the
// activity whose goal condition the change must satisfy to land. The graph never reads it: Service.LandingGate and
// Service.SubChangeValidator do. Absent: no activity-relative gating beyond a node type's own lifecycle.
const DataActivity = "activityRef"

// ActivityOf is the Activity a change is scoped to ("" when it has none).
func ActivityOf(c domain.Change) string {
	ref, _ := c.Data[DataActivity].(string)
	return ref
}

// LandingGate resolves pkg/graph.Graph.LandingGate: a change scoped to an Activity (DataActivity) is gated at
// landing by that activity's own goal condition instead of the node-type lifecycle's landable-state floor; any other
// change is not decided. Wire it from main: g.LandingGate = reg.LandingGate, where reg is the *Service* backing the
// registry.
func (s *Service) LandingGate(ctx context.Context, c domain.Change, bb domain.Blackboard) (decided, ok bool, err error) {
	ref := ActivityOf(c)
	if ref == "" {
		return false, false, nil
	}
	ok, err = s.ActivityGoalsMet(ctx, ref, bb)
	return err == nil, ok, err
}

// ActivityGoalsMet: given an Activity's node key, it loads and compiles that activity's own methodology version,
// then evaluates the activity's own goal condition against bb (built by the graph from the change's own impacts,
// ADR 0024). It needs Store and Types, exactly what Service.Methodology already uses to compile by name - this does
// the same, pinned to a specific version.
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
	c, err := resolve(r.Methodology, cat).Compile()
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
