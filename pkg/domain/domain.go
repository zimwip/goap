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

// Node is one immutable version of a domain node.
type Node struct {
	ID         NodeID         `json:"id"`
	Version    Version        `json:"version"`
	Key        string         `json:"key"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"props,omitempty"`
	Deleted    bool           `json:"deleted,omitempty"`
	ChangeID   ChangeID       `json:"changeId,omitempty"`
	CreatedAt  time.Time      `json:"createdAt"`
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
