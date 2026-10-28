package graphapi

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
)

// NodeEdit is one node created or modified by a Commit.
type NodeEdit struct {
	// Key and Type name a created node (Pre is nil).
	Key, Type string
	// Pre is the version a modified node starts from (in the reference commit).
	Pre *domain.NodeRef
	// Props are merged over the current properties (a nil value clears one).
	Props map[string]any
	// State moves the node to a lifecycle state, after its other edits: a transition of its draft, the node checked out
	// first when it has none (ImpactNodeTransition, ADR 0076, 0079): a retired entry is edited and restored in the same change.
	State string
	// Responsible transfers the node to another organisational unit (its key, ADR 0054); empty: unchanged, or the unit
	// holding the commit for a created node.
	Responsible string
	// Rationale says why; the title of the commit when empty.
	Rationale string
	Links     []LinkEdit
	// RemoveLinks are outgoing links of the Pre version the new version leaves out (removing a child is a
	// modification of its parent, ADR 0024 §4).
	RemoveLinks []domain.LinkID
}

// LinkEdit is an outgoing link added by an edit: to an existing version (To) or
// to a node created by another edit of the commit (ToKey). Nodes created by the commit may link each other in
// any direction, cycles included: a link to a node not written yet is added, in the same change, once it is.
type LinkEdit struct {
	Type  string
	To    *domain.NodeRef
	ToKey string
	Props map[string]any
}

// Edits describes a change made of node edits, applied as one unit.
type Edits struct {
	Namespace, Title, Intent, Methodology string
	Data                                  map[string]any
	Commit                                domain.CommitID
	// Branch the commit writes on (default main).
	Branch string
	// By is who accepts the edits: the producer.
	By         string
	CommitName string
	Edits      []NodeEdit
	// OwnerOrg is the unit holding the commit and ProjectID the project it acts in (ADR 0054); empty: the root unit.
	// The project is required (ADR 0091).
	OwnerOrg, ProjectID string
	// Events are the writer's own events, stored in the graph's outbox in the transaction of the commit and
	// delivered with the graph's (ADR 0136): what a service keeping its facts in the graph emits (the methodology
	// registry). Nothing committed (nothing to commit, a refusal): no event. A subject of the graph's own families is
	// refused.
	Events []CarriedEvent
}

// CarriedEvent is an event a writer owns that rides on a graph write (ADR 0136).
type CarriedEvent struct {
	Subject string
	Payload json.RawMessage
}

// CommitResult is the outcome of a Commit.
type CommitResult struct {
	Change domain.ChangeID
	Commit domain.Commit
}

// RebasedImpact is what a rebase did to a change impact.
type RebasedImpact struct {
	Impact domain.ChangeImpactID `json:"impactId"`
	Key    string                `json:"key"`
	// Changed is set when its review went back to proposed: the rebase left conflicts on a decided impact (a clean
	// rebase keeps the reviews, ADR 0101 §2).
	Changed bool `json:"changed"`
	// Merged is set when the node was merged three-way with the target's version.
	Merged bool `json:"merged,omitempty"`
	// Conflicts are the fields changed on both sides (domain.ConflictProp, ...), kept as the change had them.
	Conflicts []string `json:"conflicts,omitempty"`
}

// RebaseResult is what RebaseChange did.
type RebaseResult struct {
	// Parent is the parent change of a sub-change, empty for a root change.
	Parent domain.ChangeID `json:"parentId"`
	// Branch is the target branch of a root change; From the commit it was forked from and Onto the head it moved to
	// (equal: it was not behind).
	Branch  string          `json:"branch,omitempty"`
	From    domain.CommitID `json:"from,omitempty"`
	Onto    domain.CommitID `json:"onto,omitempty"`
	Impacts []RebasedImpact `json:"impacts,omitempty"`
}

// TransitionRequest asks for a transition of the lifecycle of a change.
type TransitionRequest struct {
	// Transition is the name of the transition out of the current state.
	Transition string
	// Decision is the decision point that gates the transition, when its guard asks for one: it is seen by the guard
	// as change.decision, and is consumed by the move (a point gates one transition only).
	Decision string
	// By is the caller, recorded in the journal.
	By string
}

// Changed reports a rebase that left conflicts: the change has impacts to settle and review again.
func (r RebaseResult) Changed() bool {
	for _, i := range r.Impacts {
		if i.Changed || len(i.Conflicts) > 0 {
			return true
		}
	}
	return false
}

// Summary says what a rebase left to do: the impacts to review again, the conflicts to settle.
func (r RebaseResult) Summary() string {
	var review, conflicts []string
	for _, i := range r.Impacts {
		if i.Changed || len(i.Conflicts) > 0 {
			review = append(review, i.Key)
		}
		for _, c := range i.Conflicts {
			conflicts = append(conflicts, i.Key+" "+c)
		}
	}
	out := fmt.Sprintf("rebased onto branch %s: review again %s", r.Branch, strings.Join(review, ", "))
	if len(conflicts) > 0 {
		out += "; settle the conflicts " + strings.Join(conflicts, ", ")
	}
	return out
}
