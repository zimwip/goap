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
	// SetNodeProps replaces the properties of a node version written earlier in
	// the same transaction (transition actions, ADR 0018).
	SetNodeProps(ctx context.Context, ref domain.NodeRef, props map[string]any) error
	PutLink(ctx context.Context, l domain.Link) error
	PutBaseline(ctx context.Context, b domain.Baseline) error
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
	// ChangeImpacts lists the change impacts of a change, in creation order.
	ChangeImpacts(ctx context.Context, change domain.ChangeID) ([]domain.ChangeImpact, error)
	// NodeChangeImpacts lists the changes holding a change impact on a node, oldest first.
	NodeChangeImpacts(ctx context.Context, node domain.NodeID) ([]domain.ChangeID, error)
	// SetNodeOrigin records on a node version the change and change impact that produced it and its comment.
	SetNodeOrigin(ctx context.Context, ref domain.NodeRef, change domain.ChangeID, cn domain.ChangeImpactID, comment string) error

	// JoinBranch makes a node version part of a branch it was not written on (a merge that lands it as is,
	// ADR 0032): the version is not copied and keeps the branch it was written on.
	JoinBranch(ctx context.Context, ref domain.NodeRef, branch string) error

	// DeleteChange removes a change that landed nothing (ADR 0037): its log, impacts, node versions and links, the
	// nodes it alone created, and its branch when it has one of its own (empty: none). It refuses (ErrConflict) when
	// what the change wrote is used: a baseline holds one of its versions, a later version or a link of another
	// change builds on one, or it produced a baseline.
	DeleteChange(ctx context.Context, id domain.ChangeID, namespace, branch string) error
}
