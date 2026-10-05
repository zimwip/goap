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
	// ImpactProposed adds a change impact to the change (State: as proposed). It proposes the modification of an
	// existing node only: a new node is created, never proposed (ADR 0077).
	ImpactProposed ImpactOp = "proposed"
	// ImpactCreated is the creation of a node: one event adds the change impact (State, intent created) and records
	// the first version of the node, written checked out (Post), on Flow. No proposed event precedes it and no
	// checkout follows (ADR 0076, 0077).
	ImpactCreated ImpactOp = "created"
	// ImpactCheckedOut records the working version a checkout writes for the change impact of an existing node (Post),
	// on Flow (ADR 0076).
	ImpactCheckedOut ImpactOp = "checkedOut"
	// ImpactTransitioned records a version written for a change impact (Post), on Flow, by a transition, a merge or an
	// adoption (ADR 0076, 0077); or, with Patch {"state": {"from", "to"}}, a transition of the working version taken
	// in place: no version is written, Post is the working version (ADR 0077, "No explicit check-in").
	ImpactTransitioned ImpactOp = "transitioned"
	// ImpactUpdated records an edit of the working version in place (Patch): properties, owner, links (ADR 0076).
	ImpactUpdated ImpactOp = "updated"
	// ImpactCancelled records a checkout cancelled on Flow: the working version is dropped and Post is the version the
	// impact goes back to (nil: none; a creation cancelled before it is accepted removes the change impact).
	ImpactCancelled ImpactOp = "cancelled"
	// ImpactWithdrawn takes a change impact out of the change, explicitly (its working version, if any, is dropped): the
	// impact leaves the list (ADR 0076 §5b).
	ImpactWithdrawn ImpactOp = "withdrawn"
	// ImpactReviewed records a review (Review).
	// An accepted review freezes the working version of the impact (ADR 0077): no event of its own.
	ImpactReviewed ImpactOp = "reviewed"
	// ImpactDiscarded rejects a candidate whose flow was discarded (Review).
	ImpactDiscarded ImpactOp = "discarded"
	// ImpactAdopted is change level: Flow is adopted, Stale are the runs it replaces (ADR 0025 §5).
	ImpactAdopted ImpactOp = "adopted"
	// ImpactLanded records the version a change applied (Landed) in the baseline it produced (Baseline): on its own
	// branch when it is committed, and again on the branch it is integrated into. The last one is the version on the
	// target branch. A merge change records the same for the versions it writes.
	ImpactLanded ImpactOp = "landed"
	// ImpactRebased moves the pre version of a planned change impact to a newer head, to re-check (Pre).
	ImpactRebased ImpactOp = "rebased"
)

// WritesPost reports whether the operation sets the post version of a change impact: a version written or checked out.
func (o ImpactOp) WritesPost() bool {
	return o == ImpactTransitioned || o == ImpactCheckedOut || o == ImpactCreated
}

// WritesVersion reports whether the event writes a version of the impact's node: a creation, a checkout, a transition
// from a frozen version, a merge or an adoption; not a transition taken in place on the working version (Patch
// "state"), which edits the version it finds (ADR 0077).
func (e ImpactEvent) WritesVersion() bool {
	if !e.Op.WritesPost() {
		return false
	}
	_, inPlace := e.Patch["state"]
	return !(e.Op == ImpactTransitioned && inPlace)
}

// ImpactEvent is one operation on the change impacts of a change.
type ImpactEvent struct {
	ID     string         `json:"id"`
	Change ChangeID       `json:"changeId"`
	Seq    int            `json:"seq"`
	Impact ChangeImpactID `json:"impactId,omitempty"`
	Op     ImpactOp       `json:"op"`
	// The caller: the flow branch the operation was made on ("" = the main flow), the journal record of the action
	// run (process, step, action: ADR 0011) and the principal or component.
	Flow      string    `json:"flow,omitempty"`
	Execution string    `json:"execution,omitempty"`
	By        string    `json:"by,omitempty"`
	At        time.Time `json:"at"`

	State  *ChangeImpact `json:"state,omitempty"` // proposed, created
	Post   *NodeRef      `json:"post,omitempty"`  // written
	Pre    *NodeRef      `json:"pre,omitempty"`   // rebased
	Landed *NodeRef      `json:"landed,omitempty"`
	// Baseline is, on landed, the baseline of the branch the version landed in (ADR 0032).
	Baseline BaselineID `json:"baseline,omitempty"`
	// Branch is, on landed, the branch the version landed on: the change's own branch at commit, the branch it is
	// integrated into after (the main flow's: BranchOf).
	Branch string   `json:"branch,omitempty"`
	Review *Review  `json:"review,omitempty"` // reviewed, discarded
	Stale  []string `json:"stale,omitempty"`  // adopted: the stale executions
	// Patch is what an updated event changed in place: {"props": {...}, "owner": unit, "addLink" / "updateLink" /
	// "removeLink": {...}}; a transitioned event taken in place: {"state": {"from", "to"}}.
	Patch map[string]any `json:"patch,omitempty"`
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
	case ImpactProposed:
		return need(e.State != nil && e.State.ID == e.Impact, "the change impact")
	case ImpactCreated:
		return need(e.State != nil && e.State.ID == e.Impact && e.Post != nil, "the change impact and its first version")
	case ImpactTransitioned, ImpactCheckedOut:
		return need(e.Impact != "" && e.Post != nil, "a change impact and a post version")
	case ImpactReviewed, ImpactDiscarded:
		return need(e.Impact != "" && e.Review != nil, "a change impact and a review")
	case ImpactAdopted:
		return need(e.Impact == "" && e.Flow != "", "a flow and no change impact")
	case ImpactLanded:
		return need(e.Impact != "" && e.Landed != nil, "a change impact and a landed version")
	case ImpactRebased:
		return need(e.Impact != "" && e.Pre != nil, "a change impact and a pre version")
	case ImpactUpdated:
		return need(e.Impact != "" && e.Post != nil && len(e.Patch) > 0, "a change impact, its working version and a patch")
	case ImpactCancelled, ImpactWithdrawn:
		return need(e.Impact != "", "a change impact")
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
	if e.Op == ImpactProposed || e.Op == ImpactCreated {
		cn := cloneImpact(*e.State)
		if e.Op == ImpactCreated {
			p := *e.Post
			cn.Post = &p
		}
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
	if e.Op == ImpactWithdrawn {
		return slices.Delete(out, at, at+1)
	}
	cn := cloneImpact(out[at])
	switch e.Op {
	case ImpactTransitioned, ImpactCheckedOut:
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
	case ImpactCancelled:
		if e.Flow != "" && e.Flow != cn.Flow {
			break // the stored post is the main flow's (see written)
		}
		if e.Post == nil && cn.Intent == IntentCreated {
			return slices.Delete(out, at, at+1) // a creation cancelled before it is accepted: nothing is left
		}
		cn.Post = nil
		if e.Post != nil {
			p := *e.Post
			cn.Post = &p
		}
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
		if (!e.Op.WritesPost() && e.Op != ImpactCancelled) || e.Impact != id {
			continue
		}
		switch {
		case e.Flow == "" && !stale(e.Execution):
			last[""] = e.Post
		case e.Flow != "" && slices.Contains(chain, e.Flow) && !stale(e.Execution):
			// a flow inside a flow may relaunch a step of its parent flow: that step's writes are stale too
			last[e.Flow] = e.Post
		}
		if e.Op == ImpactCancelled && e.Post == nil {
			delete(last, e.Flow) // back to what the flow saw before its checkout
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
