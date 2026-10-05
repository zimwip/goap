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
	// ImpactCreated is the creation of a node: one event adds the change impact (State, intent created) and the draft of
	// the node (Draft: its initial state; Post is the draft reference, ADR 0079), on Flow. No proposed event precedes
	// it and no checkout follows (ADR 0076, 0077). The merge change (graph.merge) records the versions it writes itself
	// the same way, with a version in Post and no Draft.
	ImpactCreated ImpactOp = "created"
	// ImpactCheckedOut records the draft a checkout makes for the change impact of an existing node (Draft: a copy of
	// the version the flow sees, Post the draft reference), on Flow (ADR 0076, 0079).
	ImpactCheckedOut ImpactOp = "checkedOut"
	// ImpactTransitioned records a lifecycle transition taken on the draft (Patch {"state": {"from", "to"}}, with the
	// properties its action algorithms set); or, carrying Draft, a draft installed on the flow by an adoption (Patch
	// {"adopted": flow}), or a version a merge writes in the merge change (Post a version, no Draft). ADR 0079.
	ImpactTransitioned ImpactOp = "transitioned"
	// ImpactUpdated records an edit of the draft (Patch): properties, owner, links (ADR 0076, 0079).
	ImpactUpdated ImpactOp = "updated"
	// ImpactCancelled records a checkout cancelled on Flow: the draft is dropped and Post is the draft the flow falls
	// back to (nil: none; a creation cancelled removes the change impact).
	ImpactCancelled ImpactOp = "cancelled"
	// ImpactWithdrawn takes a change impact out of the change, explicitly (its draft, if any, is dropped): the
	// impact leaves the list (ADR 0076 §5b).
	ImpactWithdrawn ImpactOp = "withdrawn"
	// ImpactReviewed records a review (Review).
	// An accepted review is a gate on the draft (ADR 0079): the checks of a version run, nothing is written.
	ImpactReviewed ImpactOp = "reviewed"
	// ImpactDiscarded rejects a candidate whose flow was discarded (Review).
	ImpactDiscarded ImpactOp = "discarded"
	// ImpactAdopted is change level: Flow is adopted, Stale are the runs it replaces (ADR 0025 §5).
	ImpactAdopted ImpactOp = "adopted"
	// ImpactLanded records the version a change applied (Landed) in the baseline it produced (Baseline): on its own
	// branch when it is committed (the version is written from the draft then, which goes: Post becomes that version,
	// ADR 0079), and again on the branch it is integrated into. The last one is the version on the target branch. A
	// merge change records the same for the versions it writes.
	ImpactLanded ImpactOp = "landed"
	// ImpactRebased moves the pre version of a planned change impact to a newer head, to re-check (Pre).
	ImpactRebased ImpactOp = "rebased"
)

// WritesPost reports whether the operation sets the post of a change impact: the draft of its node (or, in the merge
// change, a version).
func (o ImpactOp) WritesPost() bool {
	return o == ImpactTransitioned || o == ImpactCheckedOut || o == ImpactCreated
}

// StartsDraft reports whether the event installs a draft for the impact on its flow: a creation, a checkout or an
// adoption (a transition without Draft edits the one it finds, ADR 0079). A change goes through its lifecycle states
// while it works: the state an impact was written in is the one of the event that started its draft (ADR 0058).
func (e ImpactEvent) StartsDraft() bool {
	return e.Draft != nil && e.Op.WritesPost()
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

	State *ChangeImpact `json:"state,omitempty"` // proposed, created
	// Post is the draft reference of the node (Version 0) the event leaves the impact with, or a version (merge change).
	Post *NodeRef `json:"post,omitempty"`
	// Draft is, on created, checkedOut and an adopting transitioned, the draft the event installs for (impact, flow):
	// its initial state, so the log alone rebuilds the drafts (ADR 0079).
	Draft  *Draft   `json:"draft,omitempty"`
	Pre    *NodeRef `json:"pre,omitempty"` // rebased
	Landed *NodeRef `json:"landed,omitempty"`
	// Baseline is, on landed, the baseline of the branch the version landed in (ADR 0032).
	Baseline BaselineID `json:"baseline,omitempty"`
	// Branch is, on landed, the branch the version landed on: the change's own branch at commit, the branch it is
	// integrated into after (the main flow's: BranchOf).
	Branch string   `json:"branch,omitempty"`
	Review *Review  `json:"review,omitempty"` // reviewed, discarded
	Stale  []string `json:"stale,omitempty"`  // adopted: the stale executions
	// Patch is what an updated event changed on the draft: {"props": {...}, "unset": [...], "owner": unit key, "ownerId":
	// unit node, "addLink": {id, type, to, toId, toVersion, props}, "updateLink": {id, props}, "removeLink": {id, type,
	// to}}; a transitioned event: {"state": {"from", "to"}} and the "props" / "unset" its actions made. ApplyDraftEvent
	// reads it.
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
		if err := need(e.State != nil && e.State.ID == e.Impact && e.Post != nil, "the change impact and its draft"); err != nil {
			return err
		}
		return need(!e.Post.IsDraft() || e.Draft != nil, "the initial state of the draft")
	case ImpactTransitioned, ImpactCheckedOut:
		if err := need(e.Impact != "" && e.Post != nil, "a change impact and a post"); err != nil {
			return err
		}
		return need(e.Op == ImpactTransitioned || !e.Post.IsDraft() || e.Draft != nil, "the initial state of the draft")
	case ImpactReviewed, ImpactDiscarded:
		return need(e.Impact != "" && e.Review != nil, "a change impact and a review")
	case ImpactAdopted:
		return need(e.Impact == "" && e.Flow != "", "a flow and no change impact")
	case ImpactLanded:
		return need(e.Impact != "" && e.Landed != nil, "a change impact and a landed version")
	case ImpactRebased:
		return need(e.Impact != "" && e.Pre != nil, "a change impact and a pre version")
	case ImpactUpdated:
		return need(e.Impact != "" && e.Post != nil && len(e.Patch) > 0, "a change impact, its draft and a patch")
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
		if cn.Post == nil || cn.Post.IsDraft() {
			p := l // the draft is written as this version (ADR 0079)
			cn.Post = &p
		}
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
