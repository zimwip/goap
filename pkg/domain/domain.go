// Package domain defines the two-axis knowledge graph used by GOAP.
//
// The domain axis holds versioned content nodes linked version-to-version and
// grouped into baselines. The change axis holds Changes: the description of
// a modification starting from a reference baseline (impacts) and proposing the
// target graph (proposals). A Change is the blackboard of an agent process.
package domain

import (
	"fmt"
	"slices"
	"time"
)

// NodeID is the stable identity of a node across all its versions.
type NodeID string

// Version is a per-node monotonically increasing version number, starting at 1.
type Version int

// NodeRef points to an exact version of a node.
type NodeRef struct {
	ID      NodeID  `json:"id"`
	Version Version `json:"version"`
}

func (r NodeRef) String() string { return fmt.Sprintf("%s@v%d", r.ID, r.Version) }

// IsZero reports whether the reference is unset.
func (r NodeRef) IsZero() bool { return r.ID == "" }

// DefaultNamespace is the namespace of nodes and changes that name none (an untyped graph, in tests). With a type
// catalogue (ADR 0012) no node lives there: a node lives in the namespace of its type, and a change names the
// namespace it acts on.
const DefaultNamespace = "default"

// NamespacePlatform is the namespace of the platform configuration: the MCPs, the adapter definitions and the model
// configuration (built-in domain platform).
const NamespacePlatform = "platform"

// NamespaceMethodology is the namespace of the meta-domain (built-in domain methodology): the types of the definition
// nodes of the methodologies (ADR 0023). Its nodes describe how work is done, not what is worked on, so the views of
// the graph leave it out by default.
const NamespaceMethodology = "methodology"

// The node types of the adapter, the meeting point of organisation, MCP and connector (pkg/adapter, ADR 0019): the
// definition is a node of the platform namespace, the instance a node of the organisation namespace held by a unit.
const (
	TypeAdapterDef = "platform@AdapterDef"
	// TypeMCP is the type of the node of an MCP, a generic tool definition (pkg/mcp).
	TypeMCP     = "platform@MCP"
	TypeAdapter = "organisation@Adapter"
)

// NamespaceOf returns ns, defaulting to DefaultNamespace.
func NamespaceOf(ns string) string {
	if ns == "" {
		return DefaultNamespace
	}
	return ns
}

// ChangeBranchOrigin is the Branch.Origin of the branch owned by a change.
func ChangeBranchOrigin(id ChangeID) string { return "change:" + string(id) }

// MainBranch is the default branch.
const MainBranch = "main"

// Version reasons.
const (
	ReasonCreate = "create" // first version of a node
	ReasonRevise = "revise" // successor on the same branch
	ReasonDerive = "derive" // first version on a parallel branch
	ReasonMerge  = "merge"  // merge of versions of two branches
	ReasonAdopt  = "adopt"  // a version that makes a change branch equal to an adopted flow (ADR 0025)
)

// BranchOf returns the branch name, defaulting to main.
func BranchOf(b string) string {
	if b == "" {
		return MainBranch
	}
	return b
}

// Node is one immutable version of a domain node. Versions are numbered per
// node across all branches; Parents link a version to the one(s) it comes from.
// A version is written on one branch (Branch, never changed) and may join other
// branches when a merge lands it there as is (Joined, ADR 0032).
type Node struct {
	ID      NodeID  `json:"id"`
	Version Version `json:"version"`
	Branch  string  `json:"branch,omitempty"`
	// Joined lists the other branches the version is part of (filled by Versions only).
	Joined     []string       `json:"joined,omitempty"`
	Parents    []Version      `json:"parents,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	Namespace  string         `json:"namespace,omitempty"`
	Key        string         `json:"key"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"props,omitempty"`
	Deleted    bool           `json:"deleted,omitempty"`
	// State in the lifecycle of the node type (empty: the type has none).
	State string `json:"state,omitempty"`
	// ChangeID is the change that wrote the version: every version has one (ADR 0049, 0054).
	ChangeID ChangeID `json:"changeId,omitempty"`
	// Owner is the organisational unit responsible for the version (a node of the organisation structure, ADR
	// 0054): the unit holding the change that created the node, carried over by the next versions unless a change
	// transfers it.
	Owner NodeID `json:"owner,omitempty"`
	// Project is the project the node was created in (a node of the project structure, ADR 0054): the project of the
	// change that created it, the same for every version.
	Project NodeID `json:"project,omitempty"`
	// ChangeImpact is the change impact that produced this version, Comment the acceptance
	// comment (else the rationale): the origin of the version (ADR 0024).
	ChangeImpact ChangeImpactID `json:"changeImpact,omitempty"`
	Comment      string         `json:"comment,omitempty"`
	// Execution is the journal execution (action run) that wrote this version: what a
	// relaunch of a step marks stale (ADR 0025).
	Execution string `json:"execution,omitempty"`
	// CheckedOut marks the working version of a change (ADR 0076): written by CreateNode or CheckoutNode, edited in
	// place until its check-in; every other version is immutable.
	CheckedOut bool      `json:"checkedOut,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Ref returns the exact reference of this node version.
func (n Node) Ref() NodeRef { return NodeRef{ID: n.ID, Version: n.Version} }

// On reports whether the version is part of a branch: written there, or joined (Joined is filled by Versions).
func (n Node) On(branch string) bool {
	branch = BranchOf(branch)
	return BranchOf(n.Branch) == branch || slices.Contains(n.Joined, branch)
}

// LinkID identifies a link.
type LinkID string

// Link is a typed relation between two exact node versions.
type Link struct {
	ID         LinkID         `json:"id"`
	Type       string         `json:"type"`
	From       NodeRef        `json:"from"`
	To         NodeRef        `json:"to"`
	Properties map[string]any `json:"props,omitempty"`
	ChangeID   ChangeID       `json:"changeId,omitempty"`
}

// BaselineID identifies a baseline.
type BaselineID string

// Baseline is a consistent snapshot of the domain graph: which version of each
// node is part of it. Links belong to a baseline when both endpoints do.
type Baseline struct {
	ID        BaselineID `json:"id"`
	Name      string     `json:"name"`
	Namespace string     `json:"namespace,omitempty"`
	Branch    string     `json:"branch,omitempty"`
	ParentID  BaselineID `json:"parentId,omitempty"`
	// MergedFrom is the head baseline of the branch merged in, when this baseline is the result of a merge
	// (ADR 0032): a baseline has at most one such second parent, ParentID being the first (the target branch's
	// previous head).
	MergedFrom BaselineID `json:"mergedFrom,omitempty"`
	ChangeID   ChangeID   `json:"changeId,omitempty"`
	// Kind says how the state of the baseline follows from the change that produced it (ADR 0056): BaselineCommit,
	// BaselineMerge, BaselineFastForward or BaselineSnapshot.
	Kind string `json:"kind,omitempty"`
	// Gap is how far the baseline is from a snapshot of its state (ADR 0056): 0, the state is materialised (its
	// entries are stored); n > 0, only its header is stored and the state is computed from the baseline n steps back
	// and what the changes in between did (see Kind). The graph hides it: a baseline it returns always holds its nodes.
	Gap       int                `json:"gap,omitempty"`
	Nodes     map[NodeID]Version `json:"nodes"`
	CreatedAt time.Time          `json:"createdAt"`
}

// Kinds of baseline.
const (
	// BaselineCommit is the state a change leaves on its own branch when it is committed.
	BaselineCommit = "commit"
	// BaselineMerge is the state of a branch after a 3-way merge into it.
	BaselineMerge = "merge"
	// BaselineFastForward is the state of a branch after a change was fast-forwarded into it.
	BaselineFastForward = "fast-forward"
	// BaselineSnapshot is a state nothing derives (the bootstrap, a fixture): always stored.
	BaselineSnapshot = "snapshot"
)

// DerivedKind tells whether the state of a baseline of this kind follows from what its change did: a commit and a merge
// from the `landed` events naming the baseline in the log of its change, a fast-forward from the versions its change
// joined to the branch (ADR 0056).
func DerivedKind(kind string) bool {
	return kind == BaselineCommit || kind == BaselineMerge || kind == BaselineFastForward
}

// TagID identifies a tag.
type TagID string

// Tag is a name given to the state a change leaves (ADR 0056): it labels the change that produced the state, and
// the materialised baseline of that change when one is kept, never a baseline of its own. Tags are not unique: one
// name may label several changes (a "release" per product line), and a change may carry several names.
type Tag struct {
	ID        TagID  `json:"id"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// ChangeID is the change whose resulting state is named.
	ChangeID ChangeID `json:"changeId"`
	// BaselineID is the materialised snapshot of that state, empty while it is only computed.
	BaselineID BaselineID `json:"baselineId,omitempty"`
	By         string     `json:"by,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// TagFilter selects tags; an empty field matches every value.
type TagFilter struct {
	Namespace string
	Name      string
	Change    ChangeID
}

// Contains reports whether the exact node version is part of the baseline.
func (b Baseline) Contains(r NodeRef) bool {
	v, ok := b.Nodes[r.ID]
	return ok && v == r.Version
}

// Branch statuses.
const (
	BranchOpen      = "open"
	BranchMerged    = "merged"
	BranchAbandoned = "abandoned"
)

// Branch is a line of versions parallel to main (an option of a change, a
// maintenance line…). Its head is its most recent baseline.
type Branch struct {
	Name         string     `json:"name"`
	Namespace    string     `json:"namespace,omitempty"`
	Parent       string     `json:"parent"`
	ForkBaseline BaselineID `json:"forkBaseline"`
	Head         BaselineID `json:"head,omitempty"`   // latest baseline of the branch
	Origin       string     `json:"origin,omitempty"` // change / option that opened it
	// Intent says why the branch exists relative to its parent (derive/revise/refine, same vocabulary as an
	// Option's, architecture plan "exploration branches"): derive, a new independent line of work (a sub-change
	// of a sub-activity, the common case); revise, a correction of what the parent already has; refine, a
	// sub-branch narrowing an already-open sibling's work. "" on a branch opened before this existed.
	Intent      OptionIntent `json:"intent,omitempty"`
	Status      string       `json:"status"`
	CreatedAt   time.Time    `json:"createdAt"`
	Description string       `json:"description,omitempty"`
}

// AttributeCheck is what the graph needs of an attribute of a node type to judge the value of a property: its
// type (empty: unchecked) and, for an enum, the allowed values.
type AttributeCheck struct {
	Name   string
	Type   string
	Enum   string
	Values []string
}
