package assistantsvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	assistantv1 "github.com/zimwip/goap/gen/goap/assistant/v1"
	"github.com/zimwip/goap/gen/goap/assistant/v1/assistantv1connect"
	"github.com/zimwip/goap/internal/convsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/modelgw"
)

// Handler implements assistantv1connect.AssistantServiceHandler. It is a service of its own rather than an RPC of the
// conversation service: that one only stores, and must stay free of the graph and the model gateway.
type Handler struct {
	Service  *Service
	Identity identity.Extractor
}

var _ assistantv1connect.AssistantServiceHandler = (*Handler)(nil)

func rpcErr(err error) error {
	var ce *connect.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ce):
		return err
	case errors.Is(err, convsvc.ErrAnonymous):
		return connect.NewError(connect.CodeUnauthenticated, err)
	case errors.Is(err, ErrForbidden), errors.Is(err, convsvc.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, convsvc.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrInvalid), errors.Is(err, convsvc.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, ErrDecided):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, ErrStale), errors.Is(err, ErrUnavailable), errors.Is(err, ErrBusy), errors.Is(err, modelgw.ErrModelDisabled):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewError(connect.CodeUnavailable, err)
}

func (h *Handler) Send(ctx context.Context, r *connect.Request[assistantv1.SendRequest]) (*connect.Response[assistantv1.SendResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	in := SendInput{ConversationID: r.Msg.GetConversationId(), Text: r.Msg.GetText()}
	if c := r.Msg.GetContext(); c != nil {
		in.Context = Context{Subject: c.GetSubject(), Selection: c.GetSelection(), Project: c.GetProject()}
		if t := c.GetTab(); t != nil {
			in.Context.TabKind, in.Context.TabParams = t.GetKind(), t.GetParams()
		}
	}
	user, pending, err := h.Service.Send(ctx, in)
	if err != nil {
		return nil, rpcErr(err)
	}
	u, err := convsvc.MessageToPB(user)
	if err != nil {
		return nil, rpcErr(err)
	}
	a, err := convsvc.MessageToPB(pending)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&assistantv1.SendResponse{UserMessage: u, AssistantMessage: a}), nil
}

func (h *Handler) ConfirmAction(ctx context.Context, r *connect.Request[assistantv1.ConfirmActionRequest]) (*connect.Response[assistantv1.ConfirmActionResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m, err := h.Service.Confirm(ctx, ConfirmInput{ConversationID: r.Msg.GetConversationId(), MessageID: r.Msg.GetMessageId(),
		ActionIndex: int(r.Msg.GetActionIndex()), Decision: r.Msg.GetDecision(), Project: r.Msg.GetProject()})
	if err != nil {
		return nil, rpcErr(err)
	}
	pb, err := convsvc.MessageToPB(m)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&assistantv1.ConfirmActionResponse{Message: pb}), nil
}
