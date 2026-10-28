package domain

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zimwip/goap/contracts/lnk"
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
	// ChangeCommitted: its integration into the branch it was forked from waits for the resolution of a three-way merge
	// (ADR 0056); no impact can be added to it any more. A root change that committed on its own branch stays active
	// (ADR 0102: ResultBaselineID names its last commit) until it is merged; a sub-change is committed, then integrated.
	ChangeCommitted ChangeStatus = "committed"
	// ChangeApplied: committed and integrated into the branch it was forked from.
	ChangeApplied   ChangeStatus = "applied"
	ChangeAbandoned ChangeStatus = "abandoned"
)

// DataAdministrative is the key of Change.Data (value true) the engine sets on a change of a methodology that
// manages organisation/project/policy/adapter data, the admin surface itself (ADR 0039): such work is exempt from
// the project selector gate of the web. Only a name: the graph never reads it, and a sub-change does not inherit it.
const DataAdministrative = "administrative"

// DataCriticality is the key of Change.Data holding the criticality of the change (C1, C2 or C3, ADR 0075 §3): the
// weight of its effect, from which the policy of the organisation says what verification and derogations it asks. Only
// a name: the graph and the domain model never read it (pkg/criticality interprets it).
const DataCriticality = "criticality"

// Change describes a modification of the domain graph. It starts from a
// reference baseline and accumulates items. It is the blackboard of a process.
type Change struct {
	ID          ChangeID `json:"id"`
	Title       string   `json:"title"`
	Intent      string   `json:"intent"`
	Methodology string   `json:"methodology,omitempty"`
	// MethodologyVersion is the version of its methodology the change follows (ADR 0113): the latest published one
	// when it was created (a sub-change of the same methodology takes its parent's), kept while it runs, moved only by
	// Graph.UpgradeChange. Empty: the latest published one, whatever it is when asked (a graph without a registry).
	MethodologyVersion string `json:"methodologyVersion,omitempty"`
	// Namespace the change acts on: only its nodes can be linked to the change.
	Namespace string `json:"namespace,omitempty"`
	// ParentID is set on a sub-change: a part of the parent change, split along an
	// organisational boundary. OwnerOrg is the unit of the organisation structure responsible
	// for it (ADR 0054): the unit holding the change, resolved when the change is created (a
	// sub-change inherits its parent's, else the root unit) and never empty once stored. It
	// decides which MCP adapters its actions resolve (ADR 0019) and owns the nodes the change
	// creates. A logical URL with no version (ADR 0154 §9): a reference to a node is a link.
	ParentID ChangeID `json:"parentId,omitempty"`
	OwnerOrg lnk.URL  `json:"ownerOrg,omitzero"`
	// ProjectID is the project (project structure, ADR 0039, 0054) the change acts in, a logical URL
	// as OwnerOrg, named when the change is created (a sub-change inherits its parent's; none is
	// defaulted, ADR 0091) and never empty once stored: the nodes the change creates are created in it.
	// It changes only through Graph.MoveChange, before the change commits (existing nodes keep their project).
	ProjectID lnk.URL `json:"projectId,omitzero"`
	// Lifecycle names the lifecycle the change follows (ADR 0058), set by CreateChange from its methodology, and State
	// is the state it is in; both are empty when the methodology names no lifecycle. State is moved only by
	// TransitionChange, which journals it as a KindTransition item.
	Lifecycle string `json:"lifecycle,omitempty"`
	State     string `json:"state,omitempty"`
	Goal      string `json:"goal,omitempty"`
	// Guardians is the guardian chain of the change (ADR 0106): the methodologies that frame it, outermost first, its
	// own last. Computed when the change is created (or attached to, or detached from, a parent) and saved, so the
	// change keeps the rules it started with; each guardian has its say on its transitions and its merge. Empty: its
	// own methodology only.
	Guardians []GuardianRef `json:"guardians,omitempty"`
	// DependsOn are the changes whose work must have reached the branch this change merges into before it merges
	// (ADR 0108): set by Graph.SetChangeDependencies, never a cycle, never an ancestor or a sub-change of the change.
	DependsOn  []ChangeID   `json:"dependsOn,omitempty"`
	Status     ChangeStatus `json:"status"`
	BaselineID BaselineID   `json:"baselineId"`
	// Branch the change is applied to (default main).
	Branch           string     `json:"branch,omitempty"`
	ResultBaselineID BaselineID `json:"resultBaselineId,omitempty"`
	// Stats are the counters of what the change committed (ChangeStats.Committed of StatsOf), kept on its header at each
	// commit and each merge of a sub-change into it: what a statistic of the changes reads with no fold of their logs.
	Stats ChangeStats `json:"stats,omitzero"`
	// Data is the free-form data of the change: the marks the services give it (the graph reads none, see
	// DataAdministrative, "trigger").
	Data map[string]any `json:"data,omitempty"`
	// Seq is the position of the log of the change its impacts and items were read at (ADR 0153): the view of the
	// change the reader holds. Never stored.
	Seq   int64        `json:"seq,omitempty"`
	Items []ChangeItem `json:"items"`
	// Nodes are the node versions the change reads, modifies or creates (ADR 0024).
	Nodes     []ChangeImpact `json:"nodes,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`

	flx  *flowIndex // replay of the flow events, valid for flxN items
	flxN int
	// shared are the impacts of the view the change was read from, when it shares them (ShareImpacts)
	shared Impacts
}

// MethodologyRef is the reference of the methodology of the change at the version it follows (MethodologyRef).
func (c Change) MethodologyRef() string { return MethodologyRef(c.Methodology, c.MethodologyVersion) }

// MethodologyRef names a methodology at a version, "<name>@<version>" (ADR 0113); the name alone when the version is
// empty: the latest published one. The readers of a methodology that take a name take a reference too.
func MethodologyRef(name, version string) string {
	if version == "" || name == "" {
		return name
	}
	return name + "@" + version
}

// SplitMethodologyRef splits a reference made by MethodologyRef: the version is empty for a bare name.
func SplitMethodologyRef(ref string) (name, version string) {
	if i := strings.LastIndexByte(ref, '@'); i > 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// GuardianRef is a methodology of the guardian chain of a change (ADR 0106), at the version in force when the chain
// was computed ("" when unknown: the latest).
type GuardianRef struct {
	Methodology string `json:"methodology"`
	Version     string `json:"version,omitempty"`
}

// OuterGuardians are the guardians of the chain of a change outside its own methodology, innermost first: the order
// they are asked in after its own.
func (c Change) OuterGuardians() []GuardianRef {
	var out []GuardianRef
	for i := len(c.Guardians) - 1; i >= 0; i-- {
		if g := c.Guardians[i]; g.Methodology != c.Methodology {
			out = append(out, g)
		}
	}
	return out
}

// ChangeEvent is published by the graph service on change lifecycle events
// (change.created, change.item_added, change.applied).
type ChangeEvent struct {
	Type     string       `json:"type"`
	Change   Change       `json:"change"`
	Baseline *Baseline    `json:"baseline,omitempty"`
	Items    []ChangeItem `json:"items,omitempty"`
	// Fields are the fields of the header a change.edited event changed (ADR 0110).
	Fields map[string]HeaderValue `json:"fields,omitempty"`
	// EndUser is the person whose action raised the event, directly or from one consequence to the next
	// (authz.Principal.EndUser of the writer, ADR 0114): what the event sets off is attributed to them.
	EndUser string `json:"endUser,omitempty"`
	// Branch is the branch a change.committed event committed on, the one a change.merged event merged into (ADR 0159).
	Branch string `json:"branch,omitempty"`
	// Nodes are the node versions a change.committed or change.merged event left on Branch (ADR 0159): the versions
	// the commit wrote, the versions the merge wrote or joined there.
	Nodes []EventNode `json:"nodes,omitempty"`
}

// EventNode is a node version an event of a change names (ADR 0159): enough for a filter or a reaction to select on
// (type, state) and to read it (id and version).
type EventNode struct {
	ID        NodeID  `json:"id"`
	Version   Version `json:"version"`
	Namespace string  `json:"namespace,omitempty"`
	Key       string  `json:"key,omitempty"`
	Type      string  `json:"type,omitempty"`
	State     string  `json:"state,omitempty"`
	Deleted   bool    `json:"deleted,omitempty"`
}

// EventNodeOf is the version n as an event names it.
func EventNodeOf(n Node) EventNode {
	return EventNode{ID: n.ID, Version: n.Version, Namespace: n.Namespace, Key: n.Key, Type: n.Type, State: n.State, Deleted: n.Deleted}
}

// EditedFields returns the fields of the header a person reads that differ between two versions of a change: title,
// intent, goal and the keys of data ("data.<key>"). Nil when none differs.
func EditedFields(before, after Change) map[string]HeaderValue {
	out := map[string]HeaderValue{}
	for name, v := range map[string][2]string{"title": {before.Title, after.Title}, "intent": {before.Intent, after.Intent}, "goal": {before.Goal, after.Goal}} {
		if v[0] != v[1] {
			out[name] = HeaderValue{From: v[0], To: v[1]}
		}
	}
	for k, v := range after.Data {
		if old, ok := before.Data[k]; !ok || !reflect.DeepEqual(old, v) {
			out["data."+k] = HeaderValue{From: old, To: v}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ItemType classifies the facts of the blackboard (the nodes a change acts on are its change impacts).
type ItemType string

const (
	TypeDecision ItemType = "decision"
	TypeArtifact ItemType = "artifact"
	// TypeFlow is an event of the action flow: a step is relaunched on a new flow
	// branch, and the branch is adopted or discarded (see flow.go).
	TypeFlow ItemType = "flow"
	// TypeSignal is a named notification other agents or a live parent may react
	// to (Type is the signal's name, Target addresses a process, "" = broadcast).
	TypeSignal ItemType = "signal"
	// TypeTransition records a move of the lifecycle of the change (ADR 0058): Data holds transition, from, to and,
	// when a decision point gated it, decision. Written only by TransitionChange.
	TypeTransition ItemType = "transition"
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
	Type   ItemType   `json:"type"`
	Target string     `json:"target,omitempty"`
	Status ItemStatus `json:"status"`
	// Flow is the flow branch that produced the item ("" = the main flow);
	// FlowEvent is set on KindFlow items.
	Flow      string     `json:"flow,omitempty"`
	FlowEvent *FlowEvent `json:"flowEvent,omitempty"`
	// DecisionEvent is set on KindDecisionPoint items (ADR 0009 §4).
	DecisionEvent *DecisionEvent `json:"decisionEvent,omitempty"`
	Decision      *Decision      `json:"decision,omitempty"` // decision only
	// Recipient addresses a signal item to a process id ("" = broadcast).
	Recipient   string         `json:"recipient,omitempty"`
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

// Validate checks the structural consistency of an item and runs the validator its use case registered for its type
// (RegisterItemType). Whether a domain declares the type is the graph's question (ADR 0128): it knows the domains.
func (it ChangeItem) Validate() error {
	switch it.Type {
	case TypeFlow:
		if err := it.FlowEvent.validate(); err != nil {
			return err
		}
	case TypeDecisionPoint:
		if err := it.DecisionEvent.validate(); err != nil {
			return err
		}
	case TypeDecision:
		if it.Decision == nil || it.Decision.Item == "" {
			return fmt.Errorf("decision item requires decision.item")
		}
	case TypeArtifact:
	case TypeSignal:
		if it.Target == "" {
			return fmt.Errorf("signal item requires target, the name of the signal")
		}
	case TypeTransition:
		if s, _ := it.Data["to"].(string); s == "" {
			return fmt.Errorf("transition item requires data.to")
		}
	case TypeReview:
		return validateReview(it)
	default:
		if validate := itemTypes.get(it.Type); validate != nil {
			return validate(it)
		}
	}
	return nil
}

// itemTypes holds the validators the use cases give their types of item (RegisterItemType).
var itemTypes registry

type registry struct {
	mu sync.RWMutex
	m  map[ItemType]func(ChangeItem) error
	// perms are the permissions a kind of item asks of its writer (RequireItemPermission).
	perms map[ItemType]ItemPermission
}

func (r *registry) get(k ItemType) func(ChangeItem) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.m[k]
}

// RegisterItemType gives the items of a type the validation of its use case, run by ChangeItem.Validate: a use case adds
// the meaning of the facts it writes to a change (pkg/risk: risks, actions, derogations) without the core naming them.
// The type itself is declared by a domain (ADR 0128). Registration is explicit, done by the
// composition root (the cmd/* mains) or by a test, never by init; it is idempotent (the last validation of a kind
// wins) and safe for concurrent use. A nil validate accepts every item of the kind.
func RegisterItemType(kind ItemType, validate func(ChangeItem) error) {
	if validate == nil {
		validate = func(ChangeItem) error { return nil }
	}
	itemTypes.mu.Lock()
	defer itemTypes.mu.Unlock()
	if itemTypes.m == nil {
		itemTypes.m = map[ItemType]func(ChangeItem) error{}
	}
	itemTypes.m[kind] = validate
}

// ItemPermission is what writing an item of a kind asks of the writer (RequireItemPermission).
type ItemPermission struct {
	// Permission is "<resource>:<action>", checked by the authorizer of the services on the project of the change.
	Permission string
	// SubjectField, when set, is the data field naming the principal who answers for the item: it must be the writer's
	// (a platform service acting by itself excepted), as someone signs for themselves.
	SubjectField string
}

// RequireItemPermission makes the write of the items of a kind subject to a permission (ADR 0075): the graph asks its
// ItemAuthorizer before it stores them and knows nothing of what the kind means. Registration is explicit and
// idempotent, like RegisterItemType.
func RequireItemPermission(kind ItemType, p ItemPermission) {
	itemTypes.mu.Lock()
	defer itemTypes.mu.Unlock()
	if itemTypes.perms == nil {
		itemTypes.perms = map[ItemType]ItemPermission{}
	}
	itemTypes.perms[kind] = p
}

// ItemPermissionOf returns the permission the items of a kind ask of their writer, if any.
func ItemPermissionOf(kind ItemType) (ItemPermission, bool) {
	itemTypes.mu.RLock()
	defer itemTypes.mu.RUnlock()
	p, ok := itemTypes.perms[kind]
	return p, ok
}

// ItemsOfType returns the items of the given kind.
func (c *Change) ItemsOfType(k ItemType) []ChangeItem {
	var out []ChangeItem
	for _, it := range c.Items {
		if it.Type == k {
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
		if it.Type == TypeDecision && it.Decision != nil && it.Decision.Item == id {
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
