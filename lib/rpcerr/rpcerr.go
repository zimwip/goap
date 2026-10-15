// Package rpcerr maps domain errors to Connect codes and back.
package rpcerr

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/contracts/review"
)

// ErrNotFound is a generic not-found sentinel for services without their own.
var ErrNotFound = errors.New("not found")

// reasonHeader carries, on a Connect error, the domain sentinel finer than its code (reasons), so that a remote caller
// tells them apart as an in-process one does.
const reasonHeader = "Goap-Error-Reason"

// reasons are the sentinels a Connect code does not tell apart, by the name they travel under.
var reasons = map[string]error{"nothing-to-commit": graphapi.ErrNothingToCommit}

func withReason(err error, ce *connect.Error) error {
	for name, sentinel := range reasons {
		if errors.Is(err, sentinel) {
			ce.Meta().Set(reasonHeader, name)
		}
	}
	return ce
}

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
	case errors.Is(err, graphapi.ErrNotFound), errors.Is(err, ErrNotFound), errors.Is(err, review.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, graphapi.ErrConflict), errors.Is(err, review.ErrConflict):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, graphapi.ErrInvalid), errors.Is(err, review.ErrInvalid):
		return withReason(err, connect.NewError(connect.CodeInvalidArgument, err))
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
	var ce *connect.Error
	if errors.As(err, &ce) {
		if sentinel, ok := reasons[ce.Meta().Get(reasonHeader)]; ok {
			err = errors.Join(sentinel, err)
		}
	}
	switch connect.CodeOf(err) {
	case connect.CodePermissionDenied:
		return errors.Join(authz.ErrForbidden, err)
	case connect.CodeNotFound:
		return errors.Join(graphapi.ErrNotFound, err)
	case connect.CodeFailedPrecondition:
		return errors.Join(graphapi.ErrConflict, err)
	case connect.CodeInvalidArgument:
		return errors.Join(graphapi.ErrInvalid, err)
	}
	return err
}
