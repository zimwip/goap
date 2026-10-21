// Package domain defines the two-axis knowledge graph used by GOAP.
//
// The domain axis holds versioned content nodes linked version-to-version and
// grouped into baselines. The change axis holds Changes: the description of
// a modification starting from a reference baseline (impacts) and proposing the
// target graph (proposals). A Change is the blackboard of an agent process.
package domain

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/zimwip/goap/contracts/codetext"
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
	ReasonMerge  = "merge"  // merge of versions of two branches, written on the branch merged into
	// ReasonRebase: a merge seen from the other side (ADR 0133): the version a rebase writes on the branch of a change,
	// merging into it the version of the branch it was forked from (its parent branch). Parents: the version the
	// change had committed, then the parent branch's.
	ReasonRebase = "rebase"
	// ReasonRevert: the version the commit of a change writes on its branch for a node it changed and no longer does
	// (its impact withdrawn): the properties of the version the branch was forked from. The branch reads it as the
	// version of its fork (ADR 0140), and it never commits beyond the branch.
	ReasonRevert = "revert"
)

// BranchOf returns the branch name, defaulting to main.
func BranchOf(b string) string {
	if b == "" {
		return MainBranch
	}
	return b
}

// Node is one immutable version of a domain node (or, with Version 0, the draft of a node in a change, ADR 0079: see
// Draft.AsNode). Versions are numbered per
// node across all branches; Parents link a version to the one(s) it comes from.
// A version is written on one branch (Branch, never changed) and may join other
// branches when a merge commits it there as is (Joined, ADR 0032).
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
	// Origins are the nodes the node derives from (ADR 0077, "Merge and split"): set on the first version of the
	// successor of a merge or a split, pure lineage across nodes, never structural (Parents are the versions of one
	// node).
	Origins   []NodeRef `json:"origins,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	// Committed is the order (Baseline.Seq) of the baseline the version committed in on its branch (ADR 0140): the graph sets
	// it when it writes the version, and the state of a baseline follows from it.
	Committed int64 `json:"-"`
}

// Ref returns the exact reference of this node version.
func (n Node) Ref() NodeRef { return NodeRef{ID: n.ID, Version: n.Version} }

// IsDraft reports the view of the draft of a node in a change (no version, ADR 0079).
func (n Node) IsDraft() bool { return n.Version == 0 }

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
	// RebasedFrom is the baseline the change was forked from before it was rebased onto the head of its target (ADR 0101),
	// on the commit baseline of a rebased change: the first fork, when it was rebased several times.
	RebasedFrom BaselineID `json:"rebasedFrom,omitempty"`
	ChangeID    ChangeID   `json:"changeId,omitempty"`
	// Kind says how the state of the baseline follows from the change that produced it (ADR 0056): BaselineCommit,
	// BaselineMerge, BaselineFastForward or BaselineSnapshot.
	Kind string `json:"kind,omitempty"`
	// Seq orders the baselines (ADR 0140): a baseline has a greater one than every baseline before it on its branch, and
	// its state is the selection of the versions committed on its branch up to it (ADR 0143); the graph hides it.
	Seq       int64              `json:"-"`
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
	// BaselineRebase is the state of the branch of a committed change moved onto the head of its target (ADR 0102): that
	// head with what the change committed.
	BaselineRebase = "rebase"
	// BaselineRevert is a commit that also puts back, on the branch of a change, the nodes it no longer changes (an impact
	// withdrawn after its commit, ADR 0102).
	BaselineRevert = "revert"
	// BaselineSnapshot is the state the bootstrap leaves (ADR 0143: an ordinary commit).
	BaselineSnapshot = "snapshot"
)

// TagID identifies a tag.
type TagID string

// Tag names one exact node version (ADR 0143 §6): a release, a configuration, a document set, whose version links to
// the exact versions of what it is made of (a link resolves to a version at commit, ADR 0079), so a reader follows
// them as far as it needs; the tag stores no closure. Tags are not unique: one name may label several versions (a
// "release" per product line), and a version may carry several names. Nothing names a whole namespace at a point.
type Tag struct {
	ID   TagID  `json:"id"`
	Name string `json:"name"`
	// Namespace is the namespace of the node, kept to list the tags of a namespace.
	Namespace string    `json:"namespace"`
	Node      NodeID    `json:"node"`
	Version   Version   `json:"version"`
	By        string    `json:"by,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Ref is the version the tag names.
func (t Tag) Ref() NodeRef { return NodeRef{ID: t.Node, Version: t.Version} }

// TagFilter selects tags; an empty field matches every value.
type TagFilter struct {
	Namespace string
	Name      string
	Node      NodeID
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
	Head         BaselineID `json:"head,omitempty"` // latest baseline of the branch
	// RebasedFrom is the first fork baseline of a change branch moved by a rebase (ADR 0101), set once.
	RebasedFrom BaselineID `json:"rebasedFrom,omitempty"`
	Origin      string     `json:"origin,omitempty"` // change / option that opened it
	// Intent says why the branch exists relative to its parent (derive/revise/refine, same vocabulary as an
	// Option's, architecture plan "exploration branches"): derive, a new independent line of work (a sub-change
	// of a sub-activity, the common case); revise, a correction of what the parent already has; refine, a
	// sub-branch narrowing an already-open sibling's work. "" on a branch opened before this existed.
	Intent      OptionIntent `json:"intent,omitempty"`
	Status      string       `json:"status"`
	CreatedAt   time.Time    `json:"createdAt"`
	Description string       `json:"description,omitempty"`
	// Ephemeral marks the branch of a change (ADR 0140): it lives until the change commits, its state is its fork with
	// what it committed, never a checkpoint; only the lasting branches (main, the named ones) store states.
	Ephemeral bool `json:"ephemeral,omitempty"`
	// ForkSeq is, for a branch forked from the branch of a change (a sub-change, ADR 0103), the order of commit up to
	// which the versions committed on that branch, and on the branches it is forked from, are in its fork (ADR 0140): a
	// change branch holds no baseline, so its fork is ForkBaseline, the lasting baseline under the chain, with those
	// commits. 0 for a branch forked from a lasting one.
	ForkSeq int64 `json:"forkSeq,omitempty"`
}

// AttributeCheck is what the graph needs of an attribute of a node type to judge the value of a property: its
// type (empty: unchecked) and, for an enum, the allowed values.
type AttributeCheck struct {
	Name   string
	Type   string
	Enum   string
	Values []string
}

// Check checks a value against the type of its attribute (the attribute types of lib/def: number, boolean, date, enum,
// string, code). A missing or empty value is accepted (requiring one is the job of a validator), and so is any value of an
// attribute that names no type.
func (a AttributeCheck) Check(v any) error {
	if v == nil || v == "" {
		return nil
	}
	switch a.Type {
	case "number":
		switch x := v.(type) {
		case float64, float32, int, int32, int64, uint, uint32, uint64, json.Number:
			return nil
		case string:
			if _, err := strconv.ParseFloat(x, 64); err == nil {
				return nil
			}
		}
		return fmt.Errorf("a number is expected, got %v", v)
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("a boolean is expected, got %v", v)
		}
	case "date":
		s, ok := v.(string)
		if ok {
			for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02T15:04:05"} {
				if _, err := time.Parse(layout, s); err == nil {
					return nil
				}
			}
		}
		return fmt.Errorf("a date (YYYY-MM-DD or RFC 3339) is expected, got %v", v)
	case "enum":
		s, ok := v.(string)
		if ok {
			if slices.Contains(a.Values, s) {
				return nil
			}
		}
		return fmt.Errorf("%v is not a value of the enum %s", v, a.Enum)
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("a string is expected, got %v", v)
		}
	case "code":
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("a code text is expected, got %v", v)
		}
		if err := codetext.Check(s); err != nil {
			return fmt.Errorf("not a code text: %w", err)
		}
	}
	return nil
}
