package graphsvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/lib/typecat"
)

// KnownTypes runs again, once, a request refused for a node or link type the catalogue does not hold when reading the
// domain registry again (TypesMissed) changed the domains in force: a domain published since the last reload is known
// at its first use, its event lost or never sent (a graph with no bus). A refused request wrote nothing: its
// transaction was rolled back. Install it outside LockedWrites, so the read of the registry holds no change's lock.
func (h *Handler) KnownTypes() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			res, err := next(ctx, req)
			if err == nil || h.TypesMissed == nil || !errors.Is(err, typecat.ErrUnknown) || !h.TypesMissed(ctx) {
				return res, err
			}
			return next(ctx, req)
		}
	})
}
