package graph

import (
	"context"

	"github.com/zimwip/goap/contracts/domain"
)

// ambientRepo is the outer layer of the repository of a graph: a transaction run under one already open for the same
// graph (inOne) joins it rather than opening its own, so that an operation built of the graph's own operations, such as
// Commit, writes everything in one transaction or nothing. A joined transaction ends with the one it joins: the checks
// of the guard and the events run once, at its end.
type ambientRepo struct {
	Repo
	g *Graph
}

type ambientKey struct{ g *Graph }

func (r *ambientRepo) InTx(ctx context.Context, fn func(tx Tx) error) error {
	if tx, ok := ctx.Value(ambientKey{r.g}).(Tx); ok {
		return fn(tx)
	}
	return r.Repo.InTx(ctx, fn)
}

// inOne runs fn in one transaction: every operation of the graph fn calls with the context it is given joins it.
func (g *Graph) inOne(ctx context.Context, fn func(ctx context.Context) error) error {
	return g.repo.InTx(ctx, func(tx Tx) error {
		return fn(context.WithValue(ctx, ambientKey{g}, tx))
	})
}

// inChange runs fn in one transaction holding the lock of the tree of change id from its start (ADR 0127): what fn
// reads, the guardians included, still holds when it writes.
func (g *Graph) inChange(ctx context.Context, id domain.ChangeID, fn func(ctx context.Context) error) error {
	return g.inOne(ctx, func(ctx context.Context) error {
		if err := g.changeTx(ctx, id, func(Tx) error { return nil }); err != nil {
			return err
		}
		return fn(ctx)
	})
}
