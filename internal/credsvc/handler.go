package credsvc

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"

	credentialsv1 "github.com/zimwip/goap/gen/goap/credentials/v1"
	"github.com/zimwip/goap/gen/goap/credentials/v1/credentialsv1connect"
)

// TokenHeader carries the service credential of the gateway on every request to the credentials service.
const TokenHeader = "X-Goap-Credentials-Token"

// Handler implements credentialsv1connect.CredentialsServiceHandler. Only the gateway calls it: the subject
// is a request field, not derived from caller identity (ADR 0040 — there is nothing to authenticate before a
// subject has signed in at all), so the gateway authenticates itself with a shared service credential (Token):
// anyone able to reach the service could otherwise set the password of any subject, or end its sessions. With
// no Token configured every request is refused.
type Handler struct {
	Service *Service
	Token   string
}

// ClientToken is the client option of the gateway that presents the service credential.
func ClientToken(token string) connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if req.Spec().IsClient {
				req.Header().Set(TokenHeader, token)
			}
			return next(ctx, req)
		}
	}))
}

// authn refuses a request that does not carry the service credential.
func (h *Handler) authn(hdr http.Header) error {
	if h.Token == "" || subtle.ConstantTimeCompare([]byte(hdr.Get(TokenHeader)), []byte(h.Token)) != 1 {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("credentials: the service credential is missing or invalid"))
	}
	return nil
}

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
	if err := h.authn(r.Header()); err != nil {
		return nil, err
	}
	if err := h.Service.Register(ctx, r.Msg.GetSubject(), r.Msg.GetPassword()); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&credentialsv1.RegisterResponse{}), nil
}

func (h *Handler) Verify(ctx context.Context, r *connect.Request[credentialsv1.VerifyRequest]) (*connect.Response[credentialsv1.VerifyResponse], error) {
	if err := h.authn(r.Header()); err != nil {
		return nil, err
	}
	ok, err := h.Service.Verify(ctx, r.Msg.GetSubject(), r.Msg.GetPassword())
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&credentialsv1.VerifyResponse{Ok: ok}), nil
}

func (h *Handler) SetPassword(ctx context.Context, r *connect.Request[credentialsv1.SetPasswordRequest]) (*connect.Response[credentialsv1.SetPasswordResponse], error) {
	if err := h.authn(r.Header()); err != nil {
		return nil, err
	}
	if err := h.Service.SetPassword(ctx, r.Msg.GetSubject(), r.Msg.GetPassword()); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&credentialsv1.SetPasswordResponse{}), nil
}

func (h *Handler) Exists(ctx context.Context, r *connect.Request[credentialsv1.ExistsRequest]) (*connect.Response[credentialsv1.ExistsResponse], error) {
	if err := h.authn(r.Header()); err != nil {
		return nil, err
	}
	ok, err := h.Service.Exists(ctx, r.Msg.GetSubject())
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&credentialsv1.ExistsResponse{Exists: ok}), nil
}

func (h *Handler) StartSession(ctx context.Context, r *connect.Request[credentialsv1.StartSessionRequest]) (*connect.Response[credentialsv1.StartSessionResponse], error) {
	if err := h.authn(r.Header()); err != nil {
		return nil, err
	}
	id, err := h.Service.StartSession(ctx, r.Msg.GetSubject(), time.Duration(r.Msg.GetMaxAgeSeconds())*time.Second)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&credentialsv1.StartSessionResponse{Id: id}), nil
}

func (h *Handler) CheckSession(ctx context.Context, r *connect.Request[credentialsv1.CheckSessionRequest]) (*connect.Response[credentialsv1.CheckSessionResponse], error) {
	if err := h.authn(r.Header()); err != nil {
		return nil, err
	}
	ok, err := h.Service.SessionActive(ctx, r.Msg.GetId(), r.Msg.GetSubject())
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&credentialsv1.CheckSessionResponse{Active: ok}), nil
}

func (h *Handler) EndSession(ctx context.Context, r *connect.Request[credentialsv1.EndSessionRequest]) (*connect.Response[credentialsv1.EndSessionResponse], error) {
	if err := h.authn(r.Header()); err != nil {
		return nil, err
	}
	if err := h.Service.EndSession(ctx, r.Msg.GetId()); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&credentialsv1.EndSessionResponse{}), nil
}

func (h *Handler) EndSessions(ctx context.Context, r *connect.Request[credentialsv1.EndSessionsRequest]) (*connect.Response[credentialsv1.EndSessionsResponse], error) {
	if err := h.authn(r.Header()); err != nil {
		return nil, err
	}
	if err := h.Service.EndSessions(ctx, r.Msg.GetSubject()); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&credentialsv1.EndSessionsResponse{}), nil
}
