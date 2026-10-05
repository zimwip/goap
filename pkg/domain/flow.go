package domain

import (
	"fmt"
	"maps"
	"slices"
	"time"
)

// The blackboard of a change is an append-only log of items (event sourcing).
// Relaunching a step of the action flow does not rewrite it: it opens a flow
// branch. The items the relaunched step (and what followed) produced, and
// everything derived from them, are marked stale; the replanned run appends
// its items to the new branch, where they are candidates. A human then adopts
// the branch (the stale items become superseded, the candidates count) or
// discards it (the candidates are rejected, the stale items count again).
// Every transition is a KindFlow item of the same log, the state is a replay.

// FlowStatus is the state of a flow branch.
type FlowStatus string

const (
	FlowOpen      FlowStatus = "open"
	FlowAdopted   FlowStatus = "adopted"
	FlowDiscarded FlowStatus = "discarded"
)

// Flow event operations. activate / deactivate / evaluate apply to options (ADR 0032 §6).
const (
	FlowOpenOp       = "open"
	FlowAdoptOp      = "adopt"
	FlowDiscardOp    = "discard"
	FlowActivateOp   = "activate"
	FlowDeactivateOp = "deactivate"
	FlowEvaluateOp   = "evaluate"
)

// MainFlow names the main flow explicitly. An empty flow is the active option of the change when it has one,
// else the main flow (ADR 0032 §6).
const MainFlow = "main"

// OptionIntent names why an option exists relative to its parent flow (derive: a new alternative, forked from
// main; revise: a correction/adjustment to an already-active option or landed state; refine: a sub-branch of
// another option, isolating narrower work within it).
type OptionIntent string

const (
	IntentDerive OptionIntent = "derive"
	IntentRevise OptionIntent = "revise"
	IntentRefine OptionIntent = "refine"
)

// ValidOptionIntent reports whether s is a known OptionIntent ("" included: unspecified).
func ValidOptionIntent(s OptionIntent) bool {
	switch s {
	case "", IntentDerive, IntentRevise, IntentRefine:
		return true
	}
	return false
}

// OptionSpec describes an option of a change (ADR 0009 §3): a hypothesis explored on a flow branch of its own.
type OptionSpec struct {
	Name       string       `json:"name"`
	Hypothesis string       `json:"hypothesis,omitempty"`
	Intent     OptionIntent `json:"intent,omitempty"`
}

// Option statuses (ADR 0009 §3), derived from the flow of the option.
const (
	OptionExploring = "exploring"
	OptionEvaluated = "evaluated"
	OptionSelected  = "selected"
	OptionRejected  = "rejected"
)

// FlowEvent is the payload of a KindFlow item.
type FlowEvent struct {
	Op   string `json:"op"` // open | adopt | discard
	Flow string `json:"flow"`
	// open: the branch this one is forked from ("" = the main flow), the last
	// item of that line before the fork, and the items it invalidates
	// (the seeds and their dependents).
	Parent    string   `json:"parent,omitempty"`
	ForkAfter ItemID   `json:"forkAfter,omitempty"`
	Stale     []ItemID `json:"stale,omitempty"`
	// StaleRuns are opaque producer ids (the Execution of the change impacts and node versions): what these
	// produced is stale until the branch is adopted (ADR 0025). The graph only compares them, it never
	// interprets them.
	StaleRuns []string `json:"staleRuns,omitempty"`
	// Origin is an opaque record of why and from where the flow was opened (for a relaunch: the step, the
	// run, the process, the reason). Its owner is whoever opened the flow: the graph stores and returns it and
	// never reads it (ADR 0067).
	Origin map[string]any `json:"origin,omitempty"`
	// By is the principal that adopted or discarded the branch.
	By string `json:"by,omitempty"`
	// Option makes the opened flow an option of the change (open only).
	Option *OptionSpec `json:"option,omitempty"`
	// Comment is the evaluation of an option (evaluate).
	Comment string `json:"comment,omitempty"`
}

func (e *FlowEvent) validate() error {
	if e == nil || e.Flow == "" {
		return fmt.Errorf("flow item requires flowEvent.flow")
	}
	switch e.Op {
	case FlowOpenOp, FlowAdoptOp, FlowDiscardOp, FlowActivateOp, FlowDeactivateOp, FlowEvaluateOp:
		return nil
	}
	return fmt.Errorf("unknown flow op %q", e.Op)
}

// Flow is a flow branch, replayed from its events.
type Flow struct {
	ID        string     `json:"id"`
	Parent    string     `json:"parent,omitempty"`
	ForkAfter ItemID     `json:"forkAfter,omitempty"`
	Status    FlowStatus `json:"status"`
	Stale     []ItemID   `json:"stale,omitempty"`
	// StaleRuns and Origin: see FlowEvent.
	StaleRuns []string       `json:"staleRuns,omitempty"`
	Origin    map[string]any `json:"origin,omitempty"`
	OpenedAt  time.Time      `json:"openedAt"`
	DecidedAt time.Time      `json:"decidedAt,omitempty"`
	DecidedBy string         `json:"decidedBy,omitempty"`
	// CompetesWith lists the flows adopted after this one was opened that replace
	// the same items, or whose replaced items its candidates build on: an open flow
	// that competes cannot be adopted any more, it is relaunched or discarded.
	CompetesWith []string `json:"competesWith,omitempty"`
	// Option is set when the flow is an option of the change (ADR 0009 §3); Evaluation is its last evaluation.
	Option     *OptionSpec `json:"option,omitempty"`
	Evaluation string      `json:"evaluation,omitempty"`
	Evaluated  bool        `json:"evaluated,omitempty"`
	// Active tells the option is the one the change works on (ADR 0032 §6).
	Active bool `json:"active,omitempty"`
}

// OptionStatus is the status of an option: exploring, evaluated, selected (adopted) or rejected (discarded).
func (f Flow) OptionStatus() string {
	switch {
	case f.Status == FlowAdopted:
		return OptionSelected
	case f.Status == FlowDiscarded:
		return OptionRejected
	case f.Evaluated:
		return OptionEvaluated
	}
	return OptionExploring
}

type flowIndex struct {
	list []Flow
	byID map[string]int
	// active is the open option the change works on ("" = the main flow)
	active string
}

func (fx *flowIndex) get(id string) (Flow, bool) {
	i, ok := fx.byID[id]
	if !ok {
		return Flow{}, false
	}
	return fx.list[i], true
}

// effective is the state of a flow taking its ancestors into account: a
// branch is only adopted when the whole chain is; a discarded ancestor
// discards it.
func (fx *flowIndex) effective(id string) FlowStatus {
	st := FlowAdopted
	for hops := 0; id != "" && hops < 64; hops++ {
		f, ok := fx.get(id)
		if !ok {
			return FlowDiscarded
		}
		switch f.Status {
		case FlowDiscarded:
			return FlowDiscarded
		case FlowOpen:
			st = FlowOpen
		}
		id = f.Parent
	}
	return st
}

// flows replays the flow events of the log (cached per log length).
func (c *Change) flows() *flowIndex {
	if c.flx != nil && c.flxN == len(c.Items) {
		return c.flx
	}
	fx := &flowIndex{byID: map[string]int{}}
	openAt, decidedAt := map[string]int{}, map[string]int{}
	for pos, it := range c.Items {
		e := it.FlowEvent
		if it.Kind != KindFlow || e == nil {
			continue
		}
		switch e.Op {
		case FlowOpenOp:
			if _, dup := fx.byID[e.Flow]; dup {
				continue
			}
			fx.byID[e.Flow] = len(fx.list)
			openAt[e.Flow] = pos
			f := Flow{ID: e.Flow, Parent: e.Parent, ForkAfter: e.ForkAfter, Status: FlowOpen,
				Stale: slices.Clone(e.Stale), StaleRuns: slices.Clone(e.StaleRuns), Origin: maps.Clone(e.Origin), OpenedAt: it.CreatedAt}
			if e.Option != nil {
				o := *e.Option
				f.Option = &o
			}
			fx.list = append(fx.list, f)
		case FlowActivateOp:
			if i, ok := fx.byID[e.Flow]; ok && fx.list[i].Status == FlowOpen && fx.list[i].Option != nil {
				fx.active = e.Flow
			}
		case FlowDeactivateOp:
			if fx.active == e.Flow {
				fx.active = ""
			}
		case FlowEvaluateOp:
			if i, ok := fx.byID[e.Flow]; ok && fx.list[i].Status == FlowOpen {
				fx.list[i].Evaluated, fx.list[i].Evaluation = true, e.Comment
			}
		case FlowAdoptOp, FlowDiscardOp:
			if i, ok := fx.byID[e.Flow]; ok && fx.list[i].Status == FlowOpen {
				f := &fx.list[i]
				f.Status, f.DecidedAt, f.DecidedBy = FlowAdopted, it.CreatedAt, e.By
				decidedAt[e.Flow] = pos
				if e.Op == FlowDiscardOp {
					f.Status = FlowDiscarded
				}
				if fx.active == e.Flow {
					fx.active = "" // a decided option is not worked on any more
				}
			}
		}
	}
	// competition: a flow adopted after another was opened that replaces the same items
	for i := range fx.list {
		f := &fx.list[i]
		if f.Status != FlowOpen {
			continue
		}
		for _, a := range fx.list {
			if a.Status != FlowAdopted || a.ID == f.ID || decidedAt[a.ID] < openAt[f.ID] {
				continue
			}
			compete := slices.ContainsFunc(f.Stale, func(id ItemID) bool { return slices.Contains(a.Stale, id) }) ||
				slices.ContainsFunc(f.StaleRuns, func(e string) bool { return slices.Contains(a.StaleRuns, e) })
			for _, it := range c.Items {
				if compete {
					break
				}
				if it.Flow == f.ID && slices.ContainsFunc(it.DerivedFrom, func(d ItemID) bool { return slices.Contains(a.Stale, d) }) {
					compete = true
				}
			}
			if compete {
				f.CompetesWith = append(f.CompetesWith, a.ID)
			}
		}
	}
	if i, ok := fx.byID[fx.active]; ok {
		fx.list[i].Active = true
	}
	c.flx, c.flxN = fx, len(c.Items)
	return fx
}

// ActiveOption is the open option the change works on, "" when it works on its main flow (ADR 0032 §6). It is read
// from the flow events: on the whole change, not on the view of a flow (which carries none).
func (c *Change) ActiveOption() string { return c.flows().active }

// Options lists the options of the change (the flows opened as options), oldest first.
func (c *Change) Options() []Flow {
	return slices.DeleteFunc(c.Flows(), func(f Flow) bool { return f.Option == nil })
}

// ResolveFlow is the flow a call works on: "main" names the main flow, an empty flow is the active option (else the
// main flow), any other is itself.
func (c *Change) ResolveFlow(flow string) string {
	switch flow {
	case MainFlow:
		return ""
	case "":
		return c.ActiveOption()
	}
	return flow
}

// Flows lists the flow branches of the change, oldest first.
func (c *Change) Flows() []Flow { return slices.Clone(c.flows().list) }

// Flow returns a flow branch.
func (c *Change) Flow(id string) (Flow, bool) { return c.flows().get(id) }

// FlowStatusOf is the effective state of a flow, ancestors included.
func (c *Change) FlowStatusOf(id string) FlowStatus { return c.flows().effective(id) }

// View returns the change as seen by the process running on a flow. The main
// flow ("") sees the log without the items of branches that are not adopted
// (the flow events stay, so statuses keep their derivation). A branch sees
// its parent's view without the items it invalidated, plus its own items: the
// board a replanned run works on. The result carries no flow events, its
// items are plain.
func (c Change) View(flow string) Change {
	out := c
	out.Items, out.flx, out.flxN = nil, nil, 0
	fx := c.flows()
	if flow == "" {
		for _, it := range c.Items {
			if it.Flow != "" && fx.effective(it.Flow) != FlowAdopted {
				continue
			}
			out.Items = append(out.Items, it)
		}
		return out
	}
	f, ok := fx.get(flow)
	if !ok {
		return out
	}
	base := c.View(f.Parent)
	for _, it := range base.Items {
		if it.Kind == KindFlow || slices.Contains(f.Stale, it.ID) {
			continue
		}
		out.Items = append(out.Items, it)
	}
	for _, it := range c.Items {
		if it.Flow == flow && it.Kind != KindFlow {
			it.Flow = "" // the view carries no flow events: its items are plain
			out.Items = append(out.Items, it)
		}
	}
	return out
}

// StaleClosure returns the items that depend on the seeds among the items of a
// view: the seeds, then any item derived from a stale item or deciding on one,
// until nothing else is reached.
func StaleClosure(items []ChangeItem, seeds []ItemID) []ItemID {
	stale := map[ItemID]bool{}
	for _, s := range seeds {
		stale[s] = true
	}
	for changed := true; changed; {
		changed = false
		for _, it := range items {
			if stale[it.ID] || it.Kind == KindFlow {
				continue
			}
			dep := slices.ContainsFunc(it.DerivedFrom, func(d ItemID) bool { return stale[d] })
			if d := it.Decision; !dep && d != nil {
				dep = stale[d.Item]
			}
			if dep {
				stale[it.ID], changed = true, true
			}
		}
	}
	var out []ItemID
	for _, it := range items {
		if stale[it.ID] {
			out = append(out, it.ID)
		}
	}
	return out
}
