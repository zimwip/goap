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
	// Node returns an exact node version, or the latest one when ref.Version is 0.
	Node(ctx context.Context, ref domain.NodeRef) (domain.Node, error)
	NodeByKey(ctx context.Context, key string) (domain.Node, error)
	NodesIn(ctx context.Context, baseline domain.BaselineID, nodeType string) ([]domain.Node, error)
	// LatestNodes returns the latest version of every node.
	LatestNodes(ctx context.Context) ([]domain.Node, error)
	OutLinks(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error)
	InLinks(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error)
	Baseline(ctx context.Context, id domain.BaselineID) (domain.Baseline, error)
	Baselines(ctx context.Context) ([]domain.Baseline, error)
	Change(ctx context.Context, id domain.ChangeID) (domain.ChangeSet, error)
	Changes(ctx context.Context) ([]domain.ChangeSet, error)

	PutNode(ctx context.Context, n domain.Node) error
	PutLink(ctx context.Context, l domain.Link) error
	PutBaseline(ctx context.Context, b domain.Baseline) error
	// PutChange inserts or updates the change header (items are ignored).
	PutChange(ctx context.Context, c domain.ChangeSet) error
	PutItem(ctx context.Context, change domain.ChangeID, it domain.ChangeItem) error
}
