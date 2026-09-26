// Package domain defines the two-axis knowledge graph used by GOAP.
//
// The domain axis holds versioned content nodes linked version-to-version and
// grouped into baselines. The change axis holds ChangeSets: the description of
// a modification starting from a reference baseline (impacts) and proposing the
// target graph (proposals). A ChangeSet is the blackboard of an agent process.
package domain

import (
	"fmt"
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

// DefaultOrg is the organisation of changes and processes that name none. It is
// the backfill value of pre-existing rows and the organisation seeded by iam.
const DefaultOrg = "default"

// OrgOf returns org, or DefaultOrg when empty.
func OrgOf(org string) string {
	if org == "" {
		return DefaultOrg
	}
	return org
}

// DefaultNamespace is the namespace of nodes and changes that name none.
const DefaultNamespace = "sdlc"

// NamespaceMetadata is the namespace of the meta model bound to the objects the
// code manipulates (methodologies, node types, domains).
const NamespaceMetadata = "metadata"

// NamespaceOf returns ns, defaulting to DefaultNamespace.
func NamespaceOf(ns string) string {
	if ns == "" {
		return DefaultNamespace
	}
	return ns
}

// LinkInstanceOf is the link from a data node to its NodeType (metadata layer).
const LinkInstanceOf = "instanceOf"

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
type Node struct {
	ID         NodeID         `json:"id"`
	Version    Version        `json:"version"`
	Branch     string         `json:"branch,omitempty"`
	Parents    []Version      `json:"parents,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	Namespace  string         `json:"namespace,omitempty"`
	Key        string         `json:"key"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"props,omitempty"`
	Deleted    bool           `json:"deleted,omitempty"`
	// State in the lifecycle of the node type (empty: the type has none).
	State     string    `json:"state,omitempty"`
	ChangeID  ChangeID  `json:"changeId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Ref returns the exact reference of this node version.
func (n Node) Ref() NodeRef { return NodeRef{ID: n.ID, Version: n.Version} }

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
	ID        BaselineID         `json:"id"`
	Name      string             `json:"name"`
	Branch    string             `json:"branch,omitempty"`
	ParentID  BaselineID         `json:"parentId,omitempty"`
	ChangeID  ChangeID           `json:"changeId,omitempty"`
	Nodes     map[NodeID]Version `json:"nodes"`
	CreatedAt time.Time          `json:"createdAt"`
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
	Parent       string     `json:"parent"`
	ForkBaseline BaselineID `json:"forkBaseline"`
	Head         BaselineID `json:"head,omitempty"`   // latest baseline of the branch
	Origin       string     `json:"origin,omitempty"` // change / option that opened it
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"createdAt"`
}
