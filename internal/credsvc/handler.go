package credsvc

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	credentialsv1 "github.com/zimwip/goap/gen/goap/credentials/v1"
	"github.com/zimwip/goap/gen/goap/credentials/v1/credentialsv1connect"
)

// Handler implements credentialsv1connect.CredentialsServiceHandler. Only the gateway calls it: the subject
// is a request field, not derived from caller identity (ADR 0040 — there is nothing to authenticate before a
// subject has signed in at all).
type Handler struct{ Service *Service }

var _ credentialsv1connect.CredentialsServiceHandler = (*Handler)(nil)

func rpcErr(err error) error {
	var ce *connect.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ce):
		return err
	case errors.Is(err, ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, ErrExists):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func (h *Handler) Register(ctx context.Context, r *connect.Request[credentialsv1.RegisterRequest]) (*connect.Response[credentialsv1.RegisterResponse], error) {
	if err := h.Service.Register(ctx, r.Msg.GetSubject(), r.Msg.GetPassword()); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&credentialsv1.RegisterResponse{}), nil
}

func (h *Handler) Verify(ctx context.Context, r *connect.Request[credentialsv1.VerifyRequest]) (*connect.Response[credentialsv1.VerifyResponse], error) {
	ok, err := h.Service.Verify(ctx, r.Msg.GetSubject(), r.Msg.GetPassword())
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&credentialsv1.VerifyResponse{Ok: ok}), nil
}

func (h *Handler) SetPassword(ctx context.Context, r *connect.Request[credentialsv1.SetPasswordRequest]) (*connect.Response[credentialsv1.SetPasswordResponse], error) {
	if err := h.Service.SetPassword(ctx, r.Msg.GetSubject(), r.Msg.GetPassword()); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&credentialsv1.SetPasswordResponse{}), nil
}

func (h *Handler) Exists(ctx context.Context, r *connect.Request[credentialsv1.ExistsRequest]) (*connect.Response[credentialsv1.ExistsResponse], error) {
	ok, err := h.Service.Exists(ctx, r.Msg.GetSubject())
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&credentialsv1.ExistsResponse{Exists: ok}), nil
}

func (h *Handler) StartSession(ctx context.Context, r *connect.Request[credentialsv1.StartSessionRequest]) (*connect.Response[credentialsv1.StartSessionResponse], error) {
	id, err := h.Service.StartSession(ctx, r.Msg.GetSubject(), time.Duration(r.Msg.GetMaxAgeSeconds())*time.Second)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&credentialsv1.StartSessionResponse{Id: id}), nil
}

func (h *Handler) CheckSession(ctx context.Context, r *connect.Request[credentialsv1.CheckSessionRequest]) (*connect.Response[credentialsv1.CheckSessionResponse], error) {
	ok, err := h.Service.SessionActive(ctx, r.Msg.GetId(), r.Msg.GetSubject())
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&credentialsv1.CheckSessionResponse{Active: ok}), nil
}

func (h *Handler) EndSession(ctx context.Context, r *connect.Request[credentialsv1.EndSessionRequest]) (*connect.Response[credentialsv1.EndSessionResponse], error) {
	if err := h.Service.EndSession(ctx, r.Msg.GetId()); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&credentialsv1.EndSessionResponse{}), nil
}

func (h *Handler) EndSessions(ctx context.Context, r *connect.Request[credentialsv1.EndSessionsRequest]) (*connect.Response[credentialsv1.EndSessionsResponse], error) {
	if err := h.Service.EndSessions(ctx, r.Msg.GetSubject()); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&credentialsv1.EndSessionsResponse{}), nil
}
