// Package startingpoint composes the possible steps of a change (ADR 0097) for their readers: the engine binds the
// steps of the compiled view of the methodology to the change (engine.v1.ListStartingPoints: which step, the method
// that applies, why it is possible, how to start it, whether the caller may), the view says what each step is
// (registry.v1.MethodologyView: name, description, guidance, exit, roles). Compose puts the two together, by the path
// of the step (ADR 0126 step 5). It is pure and names neither the engine nor the registry: the goap-scheduler tool and
// the assistant call it with what the two services answered, the web does the same in web/src/lib/startingPoints.ts.
package startingpoint

import (
	"slices"

	enginev1 "github.com/zimwip/goap/contracts/gen/goap/engine/v1"
	registryv1 "github.com/zimwip/goap/contracts/gen/goap/registry/v1"
)

// Kinds of points.
const (
	KindStep   = "step"   // a step of a process
	KindMethod = "method" // the method a step names as a capability, or a step of that method
)

// Launch is what starts a point: the fields of StartProcess running exactly it.
type Launch struct {
	Methodology string `json:"methodology"`
	Agent       string `json:"agent"`
	Goal        string `json:"goal"`
	ChangeID    string `json:"changeId"`
}

// Point is a possible step as it reads.
type Point struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Process is the process (or method) the step belongs to; Parent, for a step of a method, the step of the process
	// that names the capability.
	Process string `json:"process"`
	Parent  string `json:"parent,omitempty"`
	Name    string `json:"name"`
	// Method and Capability: the method that applies and the capability it provides.
	Method      string `json:"method,omitempty"`
	Capability  string `json:"capability,omitempty"`
	Description string `json:"description,omitempty"`
	Guidance    string `json:"guidance,omitempty"`
	// Why are the entry conditions that hold ("name" / "!name"); Produces the exit criteria the step reaches.
	Why      []string `json:"why,omitempty"`
	Produces []string `json:"produces,omitempty"`
	// Responsible and Accountable are the roles in force for the step (ADR 0035 §2).
	Responsible string   `json:"responsible,omitempty"`
	Accountable string   `json:"accountable,omitempty"`
	Launch      Launch   `json:"launch"`
	MayRun      bool     `json:"mayRun"`
	NeedRoles   []string `json:"needRoles,omitempty"`
	Running     bool     `json:"running,omitempty"`
}

// Possible reports whether the caller may start the point now.
func (p Point) Possible() bool { return p.MayRun && !p.Running }

// Blocked is a step towards the goal that waits for conditions.
type Blocked struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Missing []string `json:"missing"`
}

// Points is the composed answer.
type Points struct {
	Methodology  string    `json:"methodology,omitempty"`
	Version      string    `json:"version,omitempty"`
	Goal         string    `json:"goal,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	Points       []Point   `json:"points"`
	Blocked      []Blocked `json:"blocked,omitempty"`
	BlockedCount int       `json:"blockedCount,omitempty"`
}

// Find returns the point of an id.
func (p Points) Find(id string) (Point, bool) {
	for _, pt := range p.Points {
		if pt.ID == id {
			return pt, true
		}
	}
	return Point{}, false
}

// Compose reads the points the engine bound (b) in the view of their methodology version (v, nil: the points keep
// their ids and bindings only, a step the view does not hold too).
func Compose(v *registryv1.MethodologyView, b *enginev1.ListStartingPointsResponse) Points {
	steps := map[string]*registryv1.ViewStep{}
	methods := map[string]*registryv1.ViewActivity{}
	var index func([]*registryv1.ViewStep)
	index = func(list []*registryv1.ViewStep) {
		for _, s := range list {
			steps[s.GetPath()] = s
			index(s.GetSteps())
		}
	}
	for _, a := range v.GetProcesses() {
		index(a.GetSteps())
	}
	for _, a := range v.GetMethods() {
		methods[a.GetName()] = a
		index(a.GetSteps())
	}
	out := Points{Methodology: b.GetMethodology(), Version: b.GetVersion(), Goal: b.GetGoal(), Reason: b.GetReason(), Points: []Point{},
		BlockedCount: int(b.GetBlockedCount())}
	for _, p := range b.GetPoints() {
		l := p.GetLaunch()
		pt := Point{ID: p.GetId(), Kind: p.GetKind(), Parent: p.GetParent(), Method: p.GetMethod(), Why: p.GetWhy(), MayRun: p.GetMayRun(),
			NeedRoles: p.GetNeedRoles(), Running: p.GetRunning(),
			Launch: Launch{Methodology: l.GetMethodology(), Agent: l.GetAgent(), Goal: l.GetGoal(), ChangeID: l.GetChangeId()}}
		s := steps[pt.ID]
		me := methods[pt.Method]
		if s != nil {
			pt.Process, pt.Name, pt.Description, pt.Guidance = s.GetOwner(), s.GetName(), s.GetDescription(), s.GetGuidance()
			pt.Produces = conditions(s.GetExit())
		}
		switch {
		case pt.Kind == KindMethod && pt.Parent == "" && s != nil:
			pt.Capability = s.GetCapability() // the step naming the capability, proposed through its method
		case me != nil:
			pt.Capability = me.GetFor()
		}
		// the roles of the method win over those of the step (ADR 0035 §2)
		roles := s.GetRoles()
		if me.GetRoles() != nil {
			roles = me.GetRoles()
		}
		pt.Responsible, pt.Accountable = roles.GetResponsible(), roles.GetAccountable()
		out.Points = append(out.Points, pt)
	}
	for _, bl := range b.GetBlocked() {
		out.Blocked = append(out.Blocked, Blocked{ID: bl.GetId(), Name: steps[bl.GetId()].GetName(), Missing: bl.GetMissing()})
	}
	return out
}

// conditions lists a set of conditions as "name" / "!name", sorted.
func conditions(m map[string]bool) []string {
	var out []string
	for k, v := range m {
		if v {
			out = append(out, k)
		} else {
			out = append(out, "!"+k)
		}
	}
	slices.Sort(out)
	return out
}
