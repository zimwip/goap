package domain

import (
	"fmt"
	"time"
)

// ChangeID identifies a ChangeSet.
type ChangeID string

// ItemID identifies a ChangeItem.
type ItemID string

// ChangeStatus is the lifecycle state of a ChangeSet.
type ChangeStatus string

const (
	ChangeDraft     ChangeStatus = "draft"
	ChangeActive    ChangeStatus = "active"
	ChangeApplied   ChangeStatus = "applied"
	ChangeAbandoned ChangeStatus = "abandoned"
)

// ChangeSet describes a modification of the domain graph. It starts from a
// reference baseline and accumulates items. It is the blackboard of a process.
type ChangeSet struct {
	ID               ChangeID       `json:"id"`
	Title            string         `json:"title"`
	Intent           string         `json:"intent"`
	Methodology      string         `json:"methodology,omitempty"`
	Goal             string         `json:"goal,omitempty"`
	Status           ChangeStatus   `json:"status"`
	BaselineID       BaselineID     `json:"baselineId"`
	ResultBaselineID BaselineID     `json:"resultBaselineId,omitempty"`
	Data             map[string]any `json:"data,omitempty"`
	Items            []ChangeItem   `json:"items"`
	CreatedAt        time.Time      `json:"createdAt"`
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
)

// ItemStatus is the review state of an item.
type ItemStatus string

const (
	ItemProposed ItemStatus = "proposed"
	ItemAccepted ItemStatus = "accepted"
	ItemRejected ItemStatus = "rejected"
)

// ChangeItem is one fact on the blackboard.
type ChangeItem struct {
	ID          ItemID         `json:"id"`
	Kind        ItemKind       `json:"kind"`
	Type        string         `json:"type,omitempty"`
	Status      ItemStatus     `json:"status"`
	Target      *NodeRef       `json:"target,omitempty"`   // impact: node of the reference graph; decision: n/a
	Proposal    *Proposal      `json:"proposal,omitempty"` // proposal only
	Decision    *Decision      `json:"decision,omitempty"` // decision only
	Data        map[string]any `json:"data,omitempty"`
	ProducedBy  string         `json:"producedBy,omitempty"`
	DerivedFrom []ItemID       `json:"derivedFrom,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
}

// ProposalOp is the kind of modification proposed for the target graph.
type ProposalOp string

const (
	OpCreateNode ProposalOp = "create_node"
	OpUpdateNode ProposalOp = "update_node"
	OpDeleteNode ProposalOp = "delete_node"
	OpAddLink    ProposalOp = "add_link"
	OpRemoveLink ProposalOp = "remove_link"
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
	Base       *NodeRef       `json:"base,omitempty"`
	Key        string         `json:"key,omitempty"`
	Type       string         `json:"type,omitempty"`
	Properties map[string]any `json:"props,omitempty"`
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
		if it.Target == nil || it.Target.IsZero() {
			return fmt.Errorf("impact item requires a target")
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
	case KindDecision:
		if it.Decision == nil || it.Decision.Item == "" {
			return fmt.Errorf("decision item requires decision.item")
		}
	case KindArtifact:
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
// decision targeting it.
func (c *ChangeSet) EffectiveStatus(id ItemID) ItemStatus {
	st := ItemProposed
	for _, it := range c.Items {
		if it.ID == id {
			st = it.Status
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
	return st
}
