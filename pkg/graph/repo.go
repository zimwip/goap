// Package graph implements the domain/change graph on top of a transactional
// repository (in-memory for tests and dev, PostgreSQL for deployments).
package graph

import (
	"context"
	"errors"

	"github.com/zimwip/goap/pkg/domain"
)

var (
	// ErrNotFound is returned when an entity does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned on optimistic concurrency violations.
	ErrConflict = errors.New("conflict")
	// ErrInvalid is returned on invalid input.
	ErrInvalid = errors.New("invalid")
)

// Repo is the persistence primitive implemented by storage backends.
type Repo interface {
	// InTx runs fn in a transaction. The transaction is rolled back when fn
	// returns an error.
	InTx(ctx context.Context, fn func(tx Tx) error) error
}

// Tx gives access to storage primitives inside a transaction.
type Tx interface {
	// Node returns an exact node version, or the latest one on main when ref.Version is 0.
	Node(ctx context.Context, ref domain.NodeRef) (domain.Node, error)
	// LatestOn returns the latest version of a node on a branch: written there or joined (ErrNotFound if none).
	LatestOn(ctx context.Context, id domain.NodeID, branch string) (domain.Node, error)
	// Versions returns every version of a node, all branches, by version, with the branches each one joined.
	Versions(ctx context.Context, id domain.NodeID) ([]domain.Node, error)
	// DerivedNodes returns the versions that name ref among their origins (ADR 0077): the first versions of the
	// successors of a merge or split; Version 0 matches any version of the node.
	DerivedNodes(ctx context.Context, ref domain.NodeRef) ([]domain.Node, error)
	Branch(ctx context.Context, namespace, name string) (domain.Branch, error)
	Branches(ctx context.Context, namespace string) ([]domain.Branch, error)
	PutBranch(ctx context.Context, b domain.Branch) error
	NodeByKey(ctx context.Context, namespace, key string) (domain.Node, error)
	// NodeIDByKey resolves a key whatever the branch the node lives on.
	NodeIDByKey(ctx context.Context, namespace, key string) (domain.NodeID, error)
	NodesIn(ctx context.Context, baseline domain.BaselineID, nodeType string) ([]domain.Node, error)
	// LatestNodes returns the latest version of every node of a namespace on a branch.
	LatestNodes(ctx context.Context, namespace, branch string) ([]domain.Node, error)
	// Namespaces returns the distinct namespaces holding at least one node (admin/reindex use).
	Namespaces(ctx context.Context) ([]string, error)
	OutLinks(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error)
	InLinks(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error)
	Baseline(ctx context.Context, id domain.BaselineID) (domain.Baseline, error)
	Baselines(ctx context.Context, namespace string) ([]domain.Baseline, error)
	Change(ctx context.Context, id domain.ChangeID) (domain.Change, error)
	Changes(ctx context.Context) ([]domain.Change, error)

	PutNode(ctx context.Context, n domain.Node) error
	// SetNodeProps replaces the properties of a node version written earlier in the same transaction (transition
	// actions, ADR 0018) or of a working version (ADR 0076).
	SetNodeProps(ctx context.Context, ref domain.NodeRef, props map[string]any) error
	// SetNodeOwner moves a version to another owner unit (an in-place edit of a working version, ADR 0076).
	SetNodeOwner(ctx context.Context, ref domain.NodeRef, owner domain.NodeID) error
	// SetNodeState moves a working version to a lifecycle state in place (ADR 0077: a transition of a node checked out
	// in the change writes no version).
	SetNodeState(ctx context.Context, ref domain.NodeRef, state string) error
	// DropWorkingVersion removes a working version, the latest of its node, with its outgoing links (ADR 0076: a
	// checkout cancelled); a node left without version is removed (a creation cancelled before it is accepted).
	DropWorkingVersion(ctx context.Context, ref domain.NodeRef) error
	// FreezeVersion freezes a checked-out version (ADR 0077: an accepted review freezes it): ErrNotFound when it is not
	// checked out.
	FreezeVersion(ctx context.Context, ref domain.NodeRef) error
	PutLink(ctx context.Context, l domain.Link) error
	// Link reads one link; DeleteLink and SetLinkProps edit the links of a working version in place (ADR 0076).
	Link(ctx context.Context, id domain.LinkID) (domain.Link, error)
	DeleteLink(ctx context.Context, id domain.LinkID) error
	SetLinkProps(ctx context.Context, id domain.LinkID, props map[string]any) error
	// PutBaseline stores a baseline: its entries when b.Gap is 0, else only its header (ADR 0056); a Baseline read
	// back with a Gap holds no nodes, the graph computes them.
	PutBaseline(ctx context.Context, b domain.Baseline) error
	// BranchJoins lists the node versions a change joined to a branch (ADR 0032: a fast-forward), of the nodes of a
	// namespace.
	BranchJoins(ctx context.Context, namespace, branch string, change domain.ChangeID) ([]domain.NodeRef, error)
	// MaterializeBaseline stores the state of a baseline stored with a Gap: whole, its Gap becomes 0.
	MaterializeBaseline(ctx context.Context, id domain.BaselineID, nodes map[domain.NodeID]domain.Version) error
	// PutTag stores a tag (ADR 0056); DeleteTag removes it (ErrNotFound when absent); Tags lists the tags matching
	// f, oldest first.
	PutTag(ctx context.Context, t domain.Tag) error
	DeleteTag(ctx context.Context, id domain.TagID) error
	Tags(ctx context.Context, f domain.TagFilter) ([]domain.Tag, error)
	// PutChange inserts or updates the change header (items are ignored).
	PutChange(ctx context.Context, c domain.Change) error
	// AppendLog appends an entry to the log of its change (ADR 0030: facts, journal records and impact events in
	// one order) and returns it with its Seq. The log is insert-only: nothing updates or deletes an entry.
	AppendLog(ctx context.Context, e domain.LogEntry) (domain.LogEntry, error)
	// Log returns the entries matching f, in the order of the log.
	Log(ctx context.Context, f domain.LogFilter) ([]domain.LogEntry, error)
	// LogCounts counts the entries matching f by type (the limit and the position aside).
	LogCounts(ctx context.Context, f domain.LogFilter) (map[string]int, error)

	// OpenChangeIDs lists the changes that are neither applied nor abandoned.
	OpenChangeIDs(ctx context.Context) ([]domain.ChangeID, error)
	// PutChangeImpact inserts or updates a change impact of a change (ADR 0024). The table is the projection of the
	// impact log (ADR 0029): only Graph.emit writes it.
	PutChangeImpact(ctx context.Context, change domain.ChangeID, cn domain.ChangeImpact) error
	// DeleteChangeImpact removes a change impact from the projection (an event removed it: ADR 0076).
	DeleteChangeImpact(ctx context.Context, change domain.ChangeID, id domain.ChangeImpactID) error
	// ChangeImpacts lists the change impacts of a change, in creation order.
	ChangeImpacts(ctx context.Context, change domain.ChangeID) ([]domain.ChangeImpact, error)
	// NodeChangeImpacts lists the changes holding a change impact on a node, oldest first.
	NodeChangeImpacts(ctx context.Context, node domain.NodeID) ([]domain.ChangeID, error)
	// SetNodeOrigin records on a node version the change and change impact that produced it and its comment.
	SetNodeOrigin(ctx context.Context, ref domain.NodeRef, change domain.ChangeID, cn domain.ChangeImpactID, comment string) error

	// JoinBranch makes a node version part of a branch it was not written on (a merge that lands it as is,
	// ADR 0032): the version is not copied and keeps the branch it was written on. change is the change whose merge
	// makes it join (ADR 0054: no membership without a change).
	JoinBranch(ctx context.Context, ref domain.NodeRef, branch string, change domain.ChangeID) error

	// DeleteChange removes a change that landed nothing (ADR 0037): its log, impacts, node versions and links, the
	// nodes it alone created, and its branch when it has one of its own (empty: none). It refuses (ErrConflict) when
	// what the change wrote is used: a baseline holds one of its versions, a later version or a link of another
	// change builds on one, or it produced a baseline.
	DeleteChange(ctx context.Context, id domain.ChangeID, namespace, branch string) error
}
