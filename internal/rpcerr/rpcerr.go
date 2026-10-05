// Package rpcerr maps domain errors to Connect codes and back.
package rpcerr

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/review"
)

// ErrNotFound is a generic not-found sentinel for services without their own.
var ErrNotFound = errors.New("not found")

// ToConnect wraps err with the matching Connect code.
func ToConnect(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	switch {
	case errors.As(err, &ce):
		return err
	case errors.Is(err, authz.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, graph.ErrNotFound), errors.Is(err, ErrNotFound), errors.Is(err, review.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, graph.ErrConflict), errors.Is(err, review.ErrConflict):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, graph.ErrInvalid), errors.Is(err, review.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

// FromConnect maps Connect codes back to domain sentinels so that remote
// adapters behave like in-process implementations.
func FromConnect(err error) error {
	if err == nil {
		return nil
	}
	switch connect.CodeOf(err) {
	case connect.CodePermissionDenied:
		return errors.Join(authz.ErrForbidden, err)
	case connect.CodeNotFound:
		return errors.Join(graph.ErrNotFound, err)
	case connect.CodeFailedPrecondition:
		return errors.Join(graph.ErrConflict, err)
	case connect.CodeInvalidArgument:
		return errors.Join(graph.ErrInvalid, err)
	}
	return err
}
