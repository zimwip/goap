package graph

import (
	"context"

	"github.com/zimwip/goap/pkg/domain"
)

// OperationAuthorizer decides whether the caller (in ctx) may perform an operation of the catalogue (pkg/operations,
// ADR 0103) on a change: an error refuses it, typically wrapping authz.ErrForbidden.
type OperationAuthorizer func(ctx context.Context, c domain.Change, op string) error

// allow asks the OperationAuthorizer whether the caller may perform op on the change, before any transaction. Every
// method of the catalogue calls it first (goap-opgen checks it); a change that does not exist is left to the operation
// to report.
func (g *Graph) allow(ctx context.Context, id domain.ChangeID, op string) error {
	if g.OperationAuthorizer == nil {
		return nil
	}
	c, err := g.Change(ctx, id)
	if err != nil {
		return nil
	}
	return g.OperationAuthorizer(ctx, c, op)
}
