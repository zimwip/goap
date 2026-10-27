package graph

import (
	"context"
	"errors"

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

// InChangeLock runs fn in one transaction holding the lock of the tree of change id from its start (ADR 0127): every
// operation of the graph fn calls with the context it is given joins it, so that the header of the change, read once
// the lock is held, serves the checks before the operation and the operation itself. Nothing is read of the change
// before fn: one not stored is fn's to refuse. A refusal fn returns that keeps what was written (a rebase left with
// conflicts, keep) commits the transaction, then is returned.
func (g *Graph) InChangeLock(ctx context.Context, id domain.ChangeID, fn func(ctx context.Context) error) error {
	return g.inOneKeeping(ctx, func(ctx context.Context) error {
		if err := g.repo.InTx(ctx, func(tx Tx) error { return tx.LockChange(ctx, id) }); err != nil {
			return err
		}
		return fn(ctx)
	})
}

// keptError is a refusal that keeps what the transaction wrote before it (a rebase left with conflicts, ADR 0165): the
// transaction it ends commits, then the refusal is returned. Under a transaction of an outer operation (inOne), the
// outer one decides.
type keptError struct{ err error }

func (e *keptError) Error() string { return e.err.Error() }
func (e *keptError) Unwrap() error { return e.err }

// keep marks a refusal that keeps what was written.
func keep(err error) error { return &keptError{err} }

// inOneKeeping is inOne committing the transaction on a refusal marked by keep, then returning it. Joined to an open
// transaction, fn runs in it and its refusal goes to the operation that opened it.
func (g *Graph) inOneKeeping(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(ambientKey{g}).(Tx); ok {
		return fn(ctx)
	}
	var kept error
	err := g.inOne(ctx, func(ctx context.Context) error {
		err := fn(ctx)
		if k := new(keptError); errors.As(err, &k) {
			kept = err
			return nil
		}
		return err
	})
	if err == nil {
		err = kept
	}
	return err
}

// inChangeKeeping is inChange committing on a refusal marked by keep (inOneKeeping).
func (g *Graph) inChangeKeeping(ctx context.Context, id domain.ChangeID, fn func(ctx context.Context) error) error {
	return g.inOneKeeping(ctx, func(ctx context.Context) error {
		if err := g.changeTx(ctx, id, func(Tx) error { return nil }); err != nil {
			return err
		}
		return fn(ctx)
	})
}
