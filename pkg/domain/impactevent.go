package domain

import (
	"fmt"
	"slices"
	"time"
)

// The change impacts of a change are event-sourced (ADR 0029): every operation on them is an ImpactEvent appended to
// the change's log, with its caller. Their state, for the main flow or for a flow, is a fold of the log.

// ImpactOp is the operation an ImpactEvent records.
type ImpactOp string

const (
	// ImpactDeclared adds a change impact (Impact: as declared).
	ImpactDeclared ImpactOp = "declared"
	// ImpactWritten records the version written for a change impact (Post), on Flow.
	ImpactWritten ImpactOp = "written"
	// ImpactReviewed records a review (Review).
	ImpactReviewed ImpactOp = "reviewed"
	// ImpactDiscarded rejects a candidate whose flow was discarded (Review).
	ImpactDiscarded ImpactOp = "discarded"
	// ImpactAdopted is change level: Flow is adopted, Stale are the runs it replaces (ADR 0025 §5).
	ImpactAdopted ImpactOp = "adopted"
	// ImpactLanded records the version on the target branch once the change is applied (Landed).
	ImpactLanded ImpactOp = "landed"
	// ImpactRebased moves the pre version of a planned change impact to a newer head, to re-check (Pre).
	ImpactRebased ImpactOp = "rebased"
	// ImpactImported is the whole state of a change impact recorded before the log existed (Impact).
	ImpactImported ImpactOp = "imported"
)

// ImpactEvent is one operation on the change impacts of a change.
type ImpactEvent struct {
	ID     string         `json:"id"`
	Change ChangeID       `json:"change"`
	Seq    int            `json:"seq"`
	Impact ChangeImpactID `json:"impact,omitempty"`
	Op     ImpactOp       `json:"op"`
	// The caller: the flow branch the operation was made on ("" = the main flow), the journal record of the action
	// run (process, step, action: ADR 0011) and the principal or component.
	Flow      string    `json:"flow,omitempty"`
	Execution string    `json:"execution,omitempty"`
	By        string    `json:"by,omitempty"`
	At        time.Time `json:"at"`

	State  *ChangeImpact `json:"state,omitempty"` // declared, imported
	Post   *NodeRef      `json:"post,omitempty"`  // written
	Pre    *NodeRef      `json:"pre,omitempty"`   // rebased
	Landed *NodeRef      `json:"landed,omitempty"`
	Review *Review       `json:"review,omitempty"` // reviewed, discarded
	Stale  []string      `json:"stale,omitempty"`  // adopted: the stale executions
}

// Validate checks that an event carries what its operation needs.
func (e ImpactEvent) Validate() error {
	need := func(ok bool, what string) error {
		if !ok {
			return fmt.Errorf("event %s needs %s", e.Op, what)
		}
		return nil
	}
	switch e.Op {
	case ImpactDeclared, ImpactImported:
		return need(e.State != nil && e.State.ID == e.Impact, "the change impact")
	case ImpactWritten:
		return need(e.Impact != "" && e.Post != nil, "a change impact and a post version")
	case ImpactReviewed, ImpactDiscarded:
		return need(e.Impact != "" && e.Review != nil, "a change impact and a review")
	case ImpactAdopted:
		return need(e.Impact == "" && e.Flow != "", "a flow and no change impact")
	case ImpactLanded:
		return need(e.Impact != "" && e.Landed != nil, "a change impact and a landed version")
	case ImpactRebased:
		return need(e.Impact != "" && e.Pre != nil, "a change impact and a pre version")
	}
	return fmt.Errorf("unknown event operation %q", e.Op)
}

// FoldImpacts replays a log: the change impacts of the change, in declaration order.
func FoldImpacts(events []ImpactEvent) []ChangeImpact {
	var out []ChangeImpact
	for _, e := range events {
		out = ApplyImpactEvent(out, e)
	}
	return out
}

// ApplyImpactEvent returns the change impacts after an event (the list is not modified).
func ApplyImpactEvent(impacts []ChangeImpact, e ImpactEvent) []ChangeImpact {
	out := slices.Clone(impacts)
	at := slices.IndexFunc(out, func(c ChangeImpact) bool { return c.ID == e.Impact })
	if e.Op == ImpactDeclared || e.Op == ImpactImported {
		cn := cloneImpact(*e.State)
		if at >= 0 {
			out[at] = cn
		} else {
			out = append(out, cn)
		}
		return out
	}
	if e.Op == ImpactAdopted {
		for i := range out {
			out[i] = adoptImpact(out[i], e.Flow, e.Stale)
		}
		return out
	}
	if at < 0 {
		return out
	}
	cn := cloneImpact(out[at])
	switch e.Op {
	case ImpactWritten:
		// the stored post is the main flow's, or the one of the flow that declared the change impact
		if e.Flow == "" || e.Flow == cn.Flow {
			p := *e.Post
			cn.Post = &p
		}
	case ImpactReviewed:
		cn.Reviews = append(cn.Reviews, *e.Review)
		if e.Review.Flow == "" {
			cn.Review = e.Review.Status
		}
	case ImpactDiscarded:
		cn.Reviews = append(cn.Reviews, *e.Review)
		cn.Review = ReviewRejected
	case ImpactLanded:
		l := *e.Landed
		cn.Landed = &l
	case ImpactRebased:
		p := *e.Pre
		cn.Pre, cn.Recheck = &p, true
	}
	out[at] = cn
	return out
}

func cloneImpact(cn ChangeImpact) ChangeImpact {
	cn.Reviews = slices.Clone(cn.Reviews)
	cn.DerivedFrom = slices.Clone(cn.DerivedFrom)
	cn.Items = slices.Clone(cn.Items)
	for _, r := range []**NodeRef{&cn.Pre, &cn.Post, &cn.Landed} {
		if *r != nil {
			v := **r
			*r = &v
		}
	}
	return cn
}

// adoptImpact is ADR 0025 §5.3 for one change impact: the flow's change impacts and reviews become the main flow's,
// the stale ones are superseded, and the review is recomputed.
func adoptImpact(cn ChangeImpact, flow string, staleExecutions []string) ChangeImpact {
	cn = cloneImpact(cn)
	stale := func(e string) bool { return e != "" && slices.Contains(staleExecutions, e) }
	switch {
	case cn.Flow == flow:
		cn.Flow = ""
	case cn.Flow == "" && !cn.Superseded && stale(cn.Execution):
		cn.Superseded = true
	}
	for i, r := range cn.Reviews {
		switch {
		case r.Flow == flow:
			cn.Reviews[i].Flow = ""
		case r.Flow == "" && stale(r.Execution):
			cn.Reviews[i].Superseded = true
		}
	}
	if !cn.Superseded {
		cn.Review = ReviewProposed
		for _, r := range cn.Reviews {
			if !r.Superseded && r.Flow == "" {
				cn.Review = r.Status
			}
		}
	}
	return cn
}

// ImpactsSeenBy is what a process on a flow sees of the change impacts (ADR 0025 §3, ADR 0029 §3): impacts is the
// stored state (FoldImpacts), chain the flow and its ancestors (innermost first, empty for the main flow) and stale
// the runs the chain invalidates. The change impacts declared by another flow or by a stale run are left out; the
// post is the last version written by a run that is not stale on the innermost flow of the chain that wrote one,
// else on the main flow; the review is the last one on the chain or the main flow that is neither
// superseded nor stale.
func ImpactsSeenBy(impacts []ChangeImpact, events []ImpactEvent, chain []string, stale map[string]bool) []ChangeImpact {
	isStale := func(e string) bool { return e != "" && stale[e] }
	inChain := func(f string) bool { return slices.Contains(chain, f) }
	var out []ChangeImpact
	for _, cn := range impacts {
		if cn.Superseded || (cn.Flow != "" && !inChain(cn.Flow)) || (len(chain) > 0 && isStale(cn.Execution)) {
			continue
		}
		if len(chain) == 0 {
			out = append(out, cn)
			continue
		}
		cn = cloneImpact(cn)
		cn.Post = flowPost(events, cn.ID, chain, isStale)
		cn.Review = ReviewProposed
		for _, r := range cn.Reviews {
			if r.Superseded || (r.Flow != "" && !inChain(r.Flow)) || isStale(r.Execution) {
				continue
			}
			cn.Review = r.Status
		}
		out = append(out, cn)
	}
	return out
}

func flowPost(events []ImpactEvent, id ChangeImpactID, chain []string, stale func(string) bool) *NodeRef {
	last := map[string]*NodeRef{} // by flow
	for _, e := range events {
		if e.Op != ImpactWritten || e.Impact != id {
			continue
		}
		switch {
		case e.Flow == "" && !stale(e.Execution):
			last[""] = e.Post
		case e.Flow != "" && slices.Contains(chain, e.Flow) && !stale(e.Execution):
			// a flow inside a flow may relaunch a step of its parent flow: that step's writes are stale too
			last[e.Flow] = e.Post
		}
	}
	for _, f := range append(slices.Clone(chain), "") {
		if p := last[f]; p != nil {
			v := *p
			return &v
		}
	}
	return nil
}
