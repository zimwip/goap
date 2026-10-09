package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/zimwip/goap/pkg/changeapi"
	"github.com/zimwip/goap/pkg/domain"
)

// Guardian is changeapi.Guardian (ADR 0098).
type Guardian = changeapi.Guardian

// guardianOf resolves the guardian a change names: nil for a free change; an error (ErrConflict) for a name the graph
// has no guardian of: the change is held by rules nothing can check now, and the operation is refused.
func (g *Graph) guardianOf(c domain.Change) (Guardian, error) {
	if c.Guardian == "" {
		return nil, nil
	}
	if gd, ok := g.Guardians[c.Guardian]; ok && gd != nil {
		return gd, nil
	}
	return nil, fmt.Errorf("change %s is guarded by %s, which is not available: %w", c.ID, c.Guardian, ErrConflict)
}

// edits are the impacts the guardians have let a transaction edit: an operation on an impact the change already holds
// asks the guardian of the change first (Guardian.MayEdit), outside the transaction, which the guardian may read.
type edits struct {
	allowed map[domain.ChangeID]map[domain.ChangeImpactID]bool
}

type editsKey struct{}

// withEdits prepares ctx for editTx.
func withEdits(ctx context.Context) context.Context {
	if _, ok := ctx.Value(editsKey{}).(*edits); ok {
		return ctx
	}
	return context.WithValue(ctx, editsKey{}, &edits{allowed: map[domain.ChangeID]map[domain.ChangeImpactID]bool{}})
}

// guardNeeded stops a transaction that reached an impact its guardian has not been asked about.
type guardNeeded struct {
	change domain.Change
	impact domain.ChangeImpactID
}

func (e *guardNeeded) Error() string {
	return fmt.Sprintf("the guardian of change %s was not asked about impact %s", e.change.ID, e.impact)
}

func (e *guardNeeded) Unwrap() error { return ErrConflict }

// mayEdit tells whether the transaction may edit an impact of c: a free change, or an impact its guardian accepted.
func (g *Graph) mayEdit(ctx context.Context, c domain.Change, impact domain.ChangeImpactID) error {
	gd, err := g.guardianOf(c)
	if err != nil || gd == nil {
		return err
	}
	if e, ok := ctx.Value(editsKey{}).(*edits); ok && e.allowed[c.ID][impact] {
		return nil
	}
	return &guardNeeded{change: c, impact: impact}
}

// editTx runs fn in a transaction (ctx prepared by withEdits); when it reaches an impact its guardian was not asked
// about, the transaction is dropped, the guardian asked and fn run again: the guardian reads the change outside any
// transaction of it.
func (g *Graph) editTx(ctx context.Context, fn func(tx Tx) error) error {
	e, _ := ctx.Value(editsKey{}).(*edits)
	for asked := 0; ; asked++ {
		err := g.repo.InTx(ctx, fn)
		var need *guardNeeded
		if e == nil || !errors.As(err, &need) || asked >= 64 {
			return err
		}
		gd, gerr := g.guardianOf(need.change)
		if gerr != nil {
			return gerr
		}
		if err := gd.MayEdit(ctx, need.change, need.impact); err != nil {
			return err
		}
		if e.allowed[need.change.ID] == nil {
			e.allowed[need.change.ID] = map[domain.ChangeImpactID]bool{}
		}
		e.allowed[need.change.ID][need.impact] = true
	}
}
