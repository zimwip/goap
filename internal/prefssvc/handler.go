package prefssvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	preferencesv1 "github.com/zimwip/goap/gen/goap/preferences/v1"
	"github.com/zimwip/goap/gen/goap/preferences/v1/preferencesv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/pkg/authz"
)

// Handler implements preferencesv1connect.PreferencesServiceHandler. The subject of a request is the caller's: there
// is no way to name another user, so no one reads or writes the preferences of someone else.
type Handler struct {
	Service  *Service
	Identity identity.Extractor
}

var _ preferencesv1connect.PreferencesServiceHandler = (*Handler)(nil)

func rpcErr(err error) error {
	var ce *connect.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ce):
		return err
	case errors.Is(err, ErrAnonymous):
		return connect.NewError(connect.CodeUnauthenticated, err)
	case errors.Is(err, ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func toStruct(p Prefs) (*structpb.Struct, error) {
	if p == nil {
		p = Prefs{}
	}
	return structpb.NewStruct(p)
}

func (h *Handler) GetPreferences(ctx context.Context, r *connect.Request[preferencesv1.GetPreferencesRequest]) (*connect.Response[preferencesv1.GetPreferencesResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	p, err := h.Service.Get(ctx, authz.From(ctx).Subject)
	if err != nil {
		return nil, rpcErr(err)
	}
	v, err := toStruct(p)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&preferencesv1.GetPreferencesResponse{Values: v}), nil
}

func (h *Handler) SetPreferences(ctx context.Context, r *connect.Request[preferencesv1.SetPreferencesRequest]) (*connect.Response[preferencesv1.SetPreferencesResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	p, err := h.Service.Set(ctx, authz.From(ctx).Subject, r.Msg.GetValues().AsMap())
	if err != nil {
		return nil, rpcErr(err)
	}
	v, err := toStruct(p)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&preferencesv1.SetPreferencesResponse{Values: v}), nil
}

func (h *Handler) ResetPreferences(ctx context.Context, r *connect.Request[preferencesv1.ResetPreferencesRequest]) (*connect.Response[preferencesv1.ResetPreferencesResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	if err := h.Service.Reset(ctx, authz.From(ctx).Subject); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&preferencesv1.ResetPreferencesResponse{}), nil
}
