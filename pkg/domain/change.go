package domain

import (
	"fmt"
	"slices"
	"time"
)

// ChangeID identifies a ChangeSet.
type ChangeID string

// ItemID identifies a ChangeItem.
type ItemID string

// ChangeStatus is the lifecycle state of a ChangeSet.
type ChangeStatus string

const (
	ChangeDraft  ChangeStatus = "draft"
	ChangeActive ChangeStatus = "active"
	// ChangeMergePending: applied on its own branch, waiting for a merge into the target branch.
	ChangeMergePending ChangeStatus = "merge_pending"
	ChangeApplied      ChangeStatus = "applied"
	ChangeAbandoned    ChangeStatus = "abandoned"
)

// ChangeSet describes a modification of the domain graph. It starts from a
// reference baseline and accumulates items. It is the blackboard of a process.
type ChangeSet struct {
	ID          ChangeID `json:"id"`
	Title       string   `json:"title"`
	Intent      string   `json:"intent"`
	Methodology string   `json:"methodology,omitempty"`
	// Namespace the change acts on: only its nodes can be linked to the change.
	Namespace string `json:"namespace,omitempty"`
	// ParentID is set on a sub-change: a part of the parent change, split along an
	// organisational boundary. OwnerOrg is the key of the OrgUnit ("organisation"
	// namespace) responsible for it.
	ParentID   ChangeID     `json:"parentId,omitempty"`
	OwnerOrg   string       `json:"ownerOrg,omitempty"`
	Goal       string       `json:"goal,omitempty"`
	Status     ChangeStatus `json:"status"`
	BaselineID BaselineID   `json:"baselineId"`
	// Branch the change is applied to (default main).
	Branch           string         `json:"branch,omitempty"`
	ResultBaselineID BaselineID     `json:"resultBaselineId,omitempty"`
	Data             map[string]any `json:"data,omitempty"`
	Items            []ChangeItem   `json:"items"`
	CreatedAt        time.Time      `json:"createdAt"`

	flx  *flowIndex // replay of the flow events, valid for flxN items
	flxN int
}

// ChangeEvent is published by the graph service on change lifecycle events
// (change.created, change.item_added, change.applied).
type ChangeEvent struct {
	Type     string       `json:"type"`
	Change   ChangeSet    `json:"change"`
	Baseline *Baseline    `json:"baseline,omitempty"`
	Items    []ChangeItem `json:"items,omitempty"`
}

// ItemKind classifies change items.
type ItemKind string

const (
	KindImpact   ItemKind = "impact"
	KindProposal ItemKind = "proposal"
	KindDecision ItemKind = "decision"
	KindArtifact ItemKind = "artifact"
	// KindMerge records a divergence of a proposal from the head of its branch
	// and the proposed resolution (validated by a human before the rebase).
	KindMerge ItemKind = "merge"
	// KindFlow is an event of the action flow: a step is relaunched on a new flow
	// branch, and the branch is adopted or discarded (see flow.go).
	KindFlow ItemKind = "flow"
)

// ItemStatus is the review state of an item.
type ItemStatus string

const (
	ItemProposed ItemStatus = "proposed"
	ItemAccepted ItemStatus = "accepted"
	ItemRejected ItemStatus = "rejected"
	// ItemSuperseded marks an item replaced by another one (rebase, merge).
	ItemSuperseded ItemStatus = "superseded"
	// ItemCandidate is an item of a flow branch that is neither adopted nor discarded yet.
	ItemCandidate ItemStatus = "candidate"
	// ItemStale is an item that a relaunched step may invalidate: it stays in
	// effect until the relaunched flow is adopted (then it is superseded).
	ItemStale ItemStatus = "stale"
)

// ChangeItem is one fact on the blackboard.
type ChangeItem struct {
	ID     ItemID     `json:"id"`
	Kind   ItemKind   `json:"kind"`
	Type   string     `json:"type,omitempty"`
	Status ItemStatus `json:"status"`
	Target *NodeRef   `json:"target,omitempty"` // impact: the "pre" version, in the reference graph; decision: n/a
	// Post is the "post" side of an impact: the proposal (Item) that produces the
	// new version on the change branch, or that version once known (Node).
	Post *Endpoint `json:"post,omitempty"`
	// Flow is the flow branch that produced the item ("" = the main flow);
	// FlowEvent is set on KindFlow items.
	Flow        string         `json:"flow,omitempty"`
	FlowEvent   *FlowEvent     `json:"flowEvent,omitempty"`
	Proposal    *Proposal      `json:"proposal,omitempty"` // proposal only
	Decision    *Decision      `json:"decision,omitempty"` // decision only
	Data        map[string]any `json:"data,omitempty"`
	ProducedBy  string         `json:"producedBy,omitempty"`
	DerivedFrom []ItemID       `json:"derivedFrom,omitempty"`
	// Supersedes lists the items this one replaces.
	Supersedes []ItemID `json:"supersedes,omitempty"`
	// Execution is the journal record of the action execution that produced
	// the item (provenance down to the model / tool calls).
	Execution string    `json:"execution,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// ProposalOp is the kind of modification proposed for the target graph.
type ProposalOp string

const (
	OpCreateNode ProposalOp = "create_node"
	OpUpdateNode ProposalOp = "update_node"
	OpDeleteNode ProposalOp = "delete_node"
	OpAddLink    ProposalOp = "add_link"
	OpRemoveLink ProposalOp = "remove_link"
	// OpMergeNode creates a version merging Node.Base (target branch, may be
	// nil when the node is new there) with Node.From (source branch).
	OpMergeNode ProposalOp = "merge_node"
	// OpTransitionNode moves Node.Base to the lifecycle state Node.State. It is
	// how a released node is reopened for edition (a transition into an
	// editable state) and how it leaves the editable states again (ADR 0014).
	OpTransitionNode ProposalOp = "transition_node"
)

// Proposal is a modification of the target graph.
type Proposal struct {
	Op   ProposalOp `json:"op"`
	Node *NodeDraft `json:"node,omitempty"`
	Link *LinkDraft `json:"link,omitempty"`
}

// NodeDraft describes a node to create (Base unset) or a new version of an
// existing node (Base = the version it modifies, used for conflict detection).
type NodeDraft struct {
	Base *NodeRef `json:"base,omitempty"`
	// From and Ancestor are set for merge_node (Properties is then the full merged map).
	From     *NodeRef `json:"from,omitempty"`
	Ancestor *NodeRef `json:"ancestor,omitempty"`
	// Namespace of a created node (empty: the change namespace).
	Namespace  string         `json:"namespace,omitempty"`
	Key        string         `json:"key,omitempty"`
	Type       string         `json:"type,omitempty"`
	Properties map[string]any `json:"props,omitempty"`
	// State is the target lifecycle state of a transition_node.
	State string `json:"state,omitempty"`
}

// Endpoint designates a link endpoint: an existing node version or a node
// proposed by another item of the same change.
type Endpoint struct {
	Node *NodeRef `json:"node,omitempty"`
	Item ItemID   `json:"item,omitempty"`
}

func (e Endpoint) String() string {
	if e.Node != nil {
		return e.Node.String()
	}
	return "item:" + string(e.Item)
}

// LinkDraft describes a link to add, or (with LinkID) a link to remove.
type LinkDraft struct {
	LinkID     LinkID         `json:"linkId,omitempty"`
	Type       string         `json:"type,omitempty"`
	From       Endpoint       `json:"from"`
	To         Endpoint       `json:"to"`
	Properties map[string]any `json:"props,omitempty"`
}

// Decision accepts or rejects another item.
type Decision struct {
	Item    ItemID `json:"item"`
	Accept  bool   `json:"accept"`
	Comment string `json:"comment,omitempty"`
}

// Validate checks the structural consistency of an item.
func (it ChangeItem) Validate() error {
	switch it.Kind {
	case KindImpact:
		if (it.Target == nil || it.Target.IsZero()) && it.Post == nil {
			return fmt.Errorf("impact item requires a target (pre) or a post")
		}
		if p := it.Post; p != nil && (p.Node == nil) == (p.Item == "") {
			return fmt.Errorf("impact post requires exactly one of node and item")
		}
	case KindProposal:
		p := it.Proposal
		if p == nil {
			return fmt.Errorf("proposal item requires a proposal")
		}
		switch p.Op {
		case OpCreateNode:
			if p.Node == nil || p.Node.Type == "" {
				return fmt.Errorf("create_node requires node.type")
			}
		case OpUpdateNode, OpDeleteNode:
			if p.Node == nil || p.Node.Base == nil {
				return fmt.Errorf("%s requires node.base", p.Op)
			}
		case OpTransitionNode:
			if p.Node == nil || p.Node.Base == nil || p.Node.State == "" {
				return fmt.Errorf("transition_node requires node.base and node.state")
			}
		case OpMergeNode:
			if p.Node == nil || p.Node.From == nil {
				return fmt.Errorf("merge_node requires node.from")
			}
		case OpAddLink:
			if p.Link == nil || p.Link.Type == "" {
				return fmt.Errorf("add_link requires link.type")
			}
			if (p.Link.From.Node == nil && p.Link.From.Item == "") || (p.Link.To.Node == nil && p.Link.To.Item == "") {
				return fmt.Errorf("add_link requires both endpoints")
			}
		case OpRemoveLink:
			if p.Link == nil || p.Link.LinkID == "" {
				return fmt.Errorf("remove_link requires link.linkId")
			}
		default:
			return fmt.Errorf("unknown proposal op %q", p.Op)
		}
	case KindFlow:
		if err := it.FlowEvent.validate(); err != nil {
			return err
		}
	case KindDecision:
		if it.Decision == nil || it.Decision.Item == "" {
			return fmt.Errorf("decision item requires decision.item")
		}
	case KindArtifact, KindMerge:
	default:
		return fmt.Errorf("unknown item kind %q", it.Kind)
	}
	return nil
}

// ItemsOfKind returns the items of the given kind.
func (c *ChangeSet) ItemsOfKind(k ItemKind) []ChangeItem {
	var out []ChangeItem
	for _, it := range c.Items {
		if it.Kind == k {
			out = append(out, it)
		}
	}
	return out
}

// Item returns the item with the given id.
func (c *ChangeSet) Item(id ItemID) (ChangeItem, bool) {
	for _, it := range c.Items {
		if it.ID == id {
			return it, true
		}
	}
	return ChangeItem{}, false
}

// EffectiveStatus returns the status of an item after applying the latest
// decision targeting it and the flow events of the change: an item of a flow
// branch is a candidate until the branch is adopted (and rejected when it is
// discarded), an item invalidated by a relaunched step is stale until the new
// flow is adopted (then superseded).
func (c *ChangeSet) EffectiveStatus(id ItemID) ItemStatus {
	st := ItemProposed
	var own string
	for _, it := range c.Items {
		if it.ID == id {
			st, own = it.Status, it.Flow
		}
		for _, s := range it.Supersedes {
			if s == id {
				return ItemSuperseded
			}
		}
	}
	for _, it := range c.Items {
		if it.Kind == KindDecision && it.Decision != nil && it.Decision.Item == id {
			if it.Decision.Accept {
				st = ItemAccepted
			} else {
				st = ItemRejected
			}
		}
	}
	fx := c.flows()
	if own != "" {
		switch fx.effective(own) {
		case FlowOpen:
			return ItemCandidate
		case FlowDiscarded:
			return ItemRejected
		}
	}
	for _, f := range fx.list {
		if !slices.Contains(f.Stale, id) {
			continue
		}
		switch fx.effective(f.ID) {
		case FlowAdopted:
			return ItemSuperseded
		case FlowOpen:
			if st == ItemProposed || st == ItemAccepted {
				st = ItemStale
			}
		}
	}
	return st
}

// InEffect reports whether an item counts for the change: it is neither
// rejected, superseded nor the candidate of a flow branch.
func (c *ChangeSet) InEffect(id ItemID) bool {
	switch c.EffectiveStatus(id) {
	case ItemRejected, ItemSuperseded, ItemCandidate:
		return false
	}
	return true
}

// Active reports whether an item is not superseded.
func (c *ChangeSet) Active(id ItemID) bool {
	st := c.EffectiveStatus(id)
	return st != ItemSuperseded && st != ItemCandidate
}
