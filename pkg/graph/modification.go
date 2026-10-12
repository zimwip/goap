package graph

import (
	"context"

	"github.com/zimwip/goap/pkg/domain"
)

// What a change modifies (ADR 0102): a change impact is a modification when its draft differs from the version it was
// checked out from (or creates a node). A candidate (an impact with no draft) or a draft left as it was checked out is
// not: it cannot be accepted, and a commit with no modification is refused. The bulk producer path (Graph.Commit: seeds,
// EnsureUser, the settings dialog) keeps confirming impacts as it always did: it is not a user edit.

type bulkKey struct{}

// withBulk marks the context of Graph.Commit: its reviews and its landing keep accepting confirmations.
func withBulk(ctx context.Context) context.Context { return context.WithValue(ctx, bulkKey{}, true) }

func isBulk(ctx context.Context) bool { b, _ := ctx.Value(bulkKey{}).(bool); return b }

// draftModifies reports whether a draft is a modification of the version it was checked out from.
func (g *Graph) draftModifies(ctx context.Context, tx Tx, d *domain.Draft) (bool, error) {
	if d == nil {
		return false, nil
	}
	if d.Base == nil {
		return true, nil
	}
	base, err := g.storedDraft(ctx, tx, *d.Base)
	if err != nil {
		return false, err
	}
	return domain.DraftChanged(&base, *d), nil
}
