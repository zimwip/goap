package domain

import (
	"fmt"
	"slices"
	"sync"
	"time"
)

// ChangeID identifies a Change.
type ChangeID string

// ItemID identifies a ChangeItem.
type ItemID string

// ChangeStatus is the lifecycle state of a Change.
type ChangeStatus string

const (
	ChangeDraft  ChangeStatus = "draft"
	ChangeActive ChangeStatus = "active"
	// ChangeCommitted: validated, and the state it leaves is recorded on its own branch; it is not integrated into the
	// branch it was forked from yet (a conflict of the merge waits for a resolution, ADR 0056). No impact can be
	// added to it any more.
	ChangeCommitted ChangeStatus = "committed"
	// ChangeApplied: committed and integrated into the branch it was forked from.
	ChangeApplied   ChangeStatus = "applied"
	ChangeAbandoned ChangeStatus = "abandoned"
)

// DataAdministrative is the key of Change.Data (value true) the engine sets on a change of a methodology that
// manages organisation/project/policy/adapter data, the admin surface itself (ADR 0039): such work is exempt from
// the project selector gate of the web. Only a name: the graph never reads it, and a sub-change does not inherit it.
const DataAdministrative = "administrative"

// Change describes a modification of the domain graph. It starts from a
// reference baseline and accumulates items. It is the blackboard of a process.
type Change struct {
	ID          ChangeID `json:"id"`
	Title       string   `json:"title"`
	Intent      string   `json:"intent"`
	Methodology string   `json:"methodology,omitempty"`
	// Namespace the change acts on: only its nodes can be linked to the change.
	Namespace string `json:"namespace,omitempty"`
	// ParentID is set on a sub-change: a part of the parent change, split along an
	// organisational boundary. OwnerOrg is the key of the unit of the organisation structure
	// responsible for it (ADR 0054): the unit holding the change, resolved when the change is
	// created (a sub-change inherits its parent's, else the root unit) and never empty once
	// stored. It decides which MCP adapters its actions resolve (ADR 0019) and owns the nodes
	// the change creates.
	ParentID ChangeID `json:"parentId,omitempty"`
	OwnerOrg string   `json:"ownerOrg,omitempty"`
	// ProjectID is the key of the project (project structure, ADR 0039, 0054) the change acts in,
	// resolved when the change is created (a sub-change inherits its parent's, else the default
	// project, pkg/graph.DefaultProject) and never empty once stored: the nodes the change creates
	// are created in it.
	ProjectID string `json:"projectId,omitempty"`
	// ActivityRef scopes the change to one Activity (a methodology@Process/Step/Method/MethodStep node key,
	// architecture plan "Activity concept"): the activity whose goal condition the change must satisfy to apply.
	// Empty: no activity-relative gating beyond a node type's own lifecycle.
	ActivityRef string `json:"activityRef,omitempty"`
	// Lifecycle names the lifecycle the change follows (ADR 0058), set by CreateChange from its methodology, and State
	// is the state it is in; both are empty when the methodology names no lifecycle. State is moved only by
	// TransitionChange, which journals it as a KindTransition item.
	Lifecycle  string       `json:"lifecycle,omitempty"`
	State      string       `json:"state,omitempty"`
	Goal       string       `json:"goal,omitempty"`
	Status     ChangeStatus `json:"status"`
	BaselineID BaselineID   `json:"baselineId"`
	// Branch the change is applied to (default main).
	Branch           string     `json:"branch,omitempty"`
	ResultBaselineID BaselineID `json:"resultBaselineId,omitempty"`
	// Data is the free-form data of the change: the marks the services give it (the graph reads none, see
	// DataAdministrative, "trigger").
	Data  map[string]any `json:"data,omitempty"`
	Items []ChangeItem   `json:"items"`
	// Nodes are the node versions the change reads, modifies or creates (ADR 0024).
	Nodes     []ChangeImpact `json:"nodes,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`

	flx  *flowIndex // replay of the flow events, valid for flxN items
	flxN int
}

// ChangeEvent is published by the graph service on change lifecycle events
// (change.created, change.item_added, change.applied).
type ChangeEvent struct {
	Type     string       `json:"type"`
	Change   Change       `json:"change"`
	Baseline *Baseline    `json:"baseline,omitempty"`
	Items    []ChangeItem `json:"items,omitempty"`
}

// ItemKind classifies the facts of the blackboard (the nodes a change acts on are its change impacts).
type ItemKind string

const (
	KindDecision ItemKind = "decision"
	KindArtifact ItemKind = "artifact"
	// KindMerge records a merge of the change and its resolution.
	KindMerge ItemKind = "merge"
	// KindFlow is an event of the action flow: a step is relaunched on a new flow
	// branch, and the branch is adopted or discarded (see flow.go).
	KindFlow ItemKind = "flow"
	// KindSignal is a named notification other agents or a live parent may react
	// to (Type is the signal's name, Target addresses a process, "" = broadcast).
	KindSignal ItemKind = "signal"
	// KindTransition records a move of the lifecycle of the change (ADR 0058): Data holds transition, from, to and,
	// when a decision point gated it, decision. Written only by TransitionChange.
	KindTransition ItemKind = "transition"
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

// ChangeItem is one fact on the blackboard: an artifact, a decision, a merge or a flow event.
type ChangeItem struct {
	ID     ItemID     `json:"id"`
	Kind   ItemKind   `json:"kind"`
	Type   string     `json:"type,omitempty"`
	Status ItemStatus `json:"status"`
	// Flow is the flow branch that produced the item ("" = the main flow);
	// FlowEvent is set on KindFlow items.
	Flow      string     `json:"flow,omitempty"`
	FlowEvent *FlowEvent `json:"flowEvent,omitempty"`
	// DecisionEvent is set on KindDecisionPoint items (ADR 0009 §4).
	DecisionEvent *DecisionEvent `json:"decisionEvent,omitempty"`
	Decision      *Decision      `json:"decision,omitempty"` // decision only
	// Target addresses a signal item to a process id ("" = broadcast).
	Target      string         `json:"target,omitempty"`
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

// Decision accepts or rejects another item.
type Decision struct {
	Item    ItemID `json:"item"`
	Accept  bool   `json:"accept"`
	Comment string `json:"comment,omitempty"`
}

// Validate checks the structural consistency of an item.
func (it ChangeItem) Validate() error {
	switch it.Kind {
	case KindFlow:
		if err := it.FlowEvent.validate(); err != nil {
			return err
		}
	case KindDecisionPoint:
		if err := it.DecisionEvent.validate(); err != nil {
			return err
		}
	case KindDecision:
		if it.Decision == nil || it.Decision.Item == "" {
			return fmt.Errorf("decision item requires decision.item")
		}
	case KindArtifact, KindMerge:
	case KindSignal:
		if it.Type == "" {
			return fmt.Errorf("signal item requires type")
		}
	case KindTransition:
		if s, _ := it.Data["to"].(string); s == "" {
			return fmt.Errorf("transition item requires data.to")
		}
	default:
		validate := itemKinds.get(it.Kind)
		if validate == nil {
			return fmt.Errorf("unknown item kind %q", it.Kind)
		}
		return validate(it)
	}
	return nil
}

// itemKinds holds the kinds of item the use cases add to the built-in ones (RegisterItemKind).
var itemKinds registry

type registry struct {
	mu sync.RWMutex
	m  map[ItemKind]func(ChangeItem) error
}

func (r *registry) get(k ItemKind) func(ChangeItem) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.m[k]
}

// RegisterItemKind makes a kind of item known to ChangeItem.Validate: a use case adds the facts it writes to a
// change (pkg/risk: risks, actions, waivers) without the core naming them. Registration is explicit, done by the
// composition root (the cmd/* mains) or by a test, never by init; it is idempotent (the last validation of a kind
// wins) and safe for concurrent use. A nil validate accepts every item of the kind.
func RegisterItemKind(kind ItemKind, validate func(ChangeItem) error) {
	if validate == nil {
		validate = func(ChangeItem) error { return nil }
	}
	itemKinds.mu.Lock()
	defer itemKinds.mu.Unlock()
	if itemKinds.m == nil {
		itemKinds.m = map[ItemKind]func(ChangeItem) error{}
	}
	itemKinds.m[kind] = validate
}

// ItemsOfKind returns the items of the given kind.
func (c *Change) ItemsOfKind(k ItemKind) []ChangeItem {
	var out []ChangeItem
	for _, it := range c.Items {
		if it.Kind == k {
			out = append(out, it)
		}
	}
	return out
}

// Item returns the item with the given id.
func (c *Change) Item(id ItemID) (ChangeItem, bool) {
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
func (c *Change) EffectiveStatus(id ItemID) ItemStatus {
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
func (c *Change) InEffect(id ItemID) bool {
	switch c.EffectiveStatus(id) {
	case ItemRejected, ItemSuperseded, ItemCandidate:
		return false
	}
	return true
}

// Active reports whether an item is not superseded.
func (c *Change) Active(id ItemID) bool {
	st := c.EffectiveStatus(id)
	return st != ItemSuperseded && st != ItemCandidate
}
