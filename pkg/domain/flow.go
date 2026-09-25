package domain

import (
	"fmt"
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

// Flow event operations.
const (
	FlowOpenOp    = "open"
	FlowAdoptOp   = "adopt"
	FlowDiscardOp = "discard"
)

// FlowEvent is the payload of a KindFlow item.
type FlowEvent struct {
	Op   string `json:"op"` // open | adopt | discard
	Flow string `json:"flow"`
	// open: the branch this one is forked from ("" = the main flow), the last
	// item of that line before the relaunched step, the step and the journal
	// record it restarts, the process that relaunched it, why, and the items
	// it invalidates (relaunched outputs and their dependents).
	Parent    string   `json:"parent,omitempty"`
	ForkAfter ItemID   `json:"forkAfter,omitempty"`
	FromStep  int      `json:"fromStep,omitempty"`
	Execution string   `json:"execution,omitempty"`
	Process   string   `json:"process,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Stale     []ItemID `json:"stale,omitempty"`
	// By is the principal that adopted or discarded the branch.
	By string `json:"by,omitempty"`
}

func (e *FlowEvent) validate() error {
	if e == nil || e.Flow == "" {
		return fmt.Errorf("flow item requires flowEvent.flow")
	}
	switch e.Op {
	case FlowOpenOp, FlowAdoptOp, FlowDiscardOp:
		return nil
	}
	return fmt.Errorf("unknown flow op %q", e.Op)
}

// Flow is a flow branch, replayed from its events.
type Flow struct {
	ID        string     `json:"id"`
	Parent    string     `json:"parent,omitempty"`
	ForkAfter ItemID     `json:"forkAfter,omitempty"`
	FromStep  int        `json:"fromStep"`
	Execution string     `json:"execution,omitempty"`
	Process   string     `json:"process,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	Status    FlowStatus `json:"status"`
	Stale     []ItemID   `json:"stale,omitempty"`
	OpenedAt  time.Time  `json:"openedAt"`
	DecidedAt time.Time  `json:"decidedAt,omitempty"`
	DecidedBy string     `json:"decidedBy,omitempty"`
}

type flowIndex struct {
	list []Flow
	byID map[string]int
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
func (c *ChangeSet) flows() *flowIndex {
	if c.flx != nil && c.flxN == len(c.Items) {
		return c.flx
	}
	fx := &flowIndex{byID: map[string]int{}}
	for _, it := range c.Items {
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
			fx.list = append(fx.list, Flow{ID: e.Flow, Parent: e.Parent, ForkAfter: e.ForkAfter, FromStep: e.FromStep, Execution: e.Execution,
				Process: e.Process, Reason: e.Reason, Status: FlowOpen, Stale: slices.Clone(e.Stale), OpenedAt: it.CreatedAt})
		case FlowAdoptOp, FlowDiscardOp:
			if i, ok := fx.byID[e.Flow]; ok && fx.list[i].Status == FlowOpen {
				f := &fx.list[i]
				f.Status, f.DecidedAt, f.DecidedBy = FlowAdopted, it.CreatedAt, e.By
				if e.Op == FlowDiscardOp {
					f.Status = FlowDiscarded
				}
			}
		}
	}
	c.flx, c.flxN = fx, len(c.Items)
	return fx
}

// Flows lists the flow branches of the change, oldest first.
func (c *ChangeSet) Flows() []Flow { return slices.Clone(c.flows().list) }

// Flow returns a flow branch.
func (c *ChangeSet) Flow(id string) (Flow, bool) { return c.flows().get(id) }

// FlowStatusOf is the effective state of a flow, ancestors included.
func (c *ChangeSet) FlowStatusOf(id string) FlowStatus { return c.flows().effective(id) }

// View returns the change as seen by the process running on a flow. The main
// flow ("") sees the log without the items of branches that are not adopted
// (the flow events stay, so statuses keep their derivation). A branch sees
// its parent's view without the items it invalidated, plus its own items: the
// board a replanned run works on. The result carries no flow events, its
// items are plain.
func (c ChangeSet) View(flow string) ChangeSet {
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
			out.Items = append(out.Items, it)
		}
	}
	return out
}

// StaleClosure returns the items that depend on the seeds among the items of a
// view: the seeds, then any item derived from a stale item, proposing a link
// to one, or deciding on one, until nothing else is reached.
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
			if p := it.Proposal; !dep && p != nil && p.Link != nil {
				dep = (p.Link.From.Item != "" && stale[p.Link.From.Item]) || (p.Link.To.Item != "" && stale[p.Link.To.Item])
			}
			if d := it.Decision; !dep && d != nil {
				dep = stale[d.Item]
			}
			if p := it.Post; !dep && p != nil {
				dep = p.Item != "" && stale[p.Item]
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
