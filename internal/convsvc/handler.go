package convsvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	conversationsv1 "github.com/zimwip/goap/gen/goap/conversations/v1"
	"github.com/zimwip/goap/gen/goap/conversations/v1/conversationsv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/pkg/authz"
)

// Handler implements conversationsv1connect.ConversationServiceHandler. The subject of a request is the caller's:
// there is no way to name another user, so no one reads or writes the conversations of someone else.
type Handler struct {
	Service  *Service
	Identity identity.Extractor
}

var _ conversationsv1connect.ConversationServiceHandler = (*Handler)(nil)

func rpcErr(err error) error {
	var ce *connect.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ce):
		return err
	case errors.Is(err, ErrAnonymous):
		return connect.NewError(connect.CodeUnauthenticated, err)
	case errors.Is(err, ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func toConversation(c Conversation) *conversationsv1.Conversation {
	return &conversationsv1.Conversation{Id: c.ID, Subject: c.Subject, Title: c.Title,
		CreatedAt: timestamppb.New(c.CreatedAt), UpdatedAt: timestamppb.New(c.UpdatedAt)}
}

func toStructs(actions []Action) ([]*structpb.Struct, error) {
	out := make([]*structpb.Struct, 0, len(actions))
	for _, a := range actions {
		s, err := structpb.NewStruct(a)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func fromStructs(in []*structpb.Struct) []Action {
	if in == nil {
		return nil
	}
	out := make([]Action, 0, len(in))
	for _, s := range in {
		out = append(out, s.AsMap())
	}
	return out
}

// MessageToPB converts a message to its wire form (the assistant's Send returns the messages it appended).
func MessageToPB(m Message) (*conversationsv1.Message, error) { return toMessage(m) }

func toMessage(m Message) (*conversationsv1.Message, error) {
	actions, err := toStructs(m.Actions)
	if err != nil {
		return nil, err
	}
	return &conversationsv1.Message{Id: m.ID, ConversationId: m.ConversationID, Seq: int32(m.Seq), Role: m.Role, Text: m.Text,
		Actions: actions, ProcessId: m.ProcessID, Status: m.Status, Error: m.Error, Context: m.Context, CreatedAt: timestamppb.New(m.CreatedAt)}, nil
}

func (h *Handler) CreateConversation(ctx context.Context, r *connect.Request[conversationsv1.CreateConversationRequest]) (*connect.Response[conversationsv1.CreateConversationResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	c, err := h.Service.Create(ctx, authz.From(ctx), r.Msg.GetTitle())
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&conversationsv1.CreateConversationResponse{Conversation: toConversation(c)}), nil
}

func (h *Handler) ListConversations(ctx context.Context, r *connect.Request[conversationsv1.ListConversationsRequest]) (*connect.Response[conversationsv1.ListConversationsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	cs, next, err := h.Service.List(ctx, authz.From(ctx), int(r.Msg.GetPageSize()), r.Msg.GetPageToken())
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &conversationsv1.ListConversationsResponse{NextPageToken: next}
	for _, c := range cs {
		out.Conversations = append(out.Conversations, toConversation(c))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) GetConversation(ctx context.Context, r *connect.Request[conversationsv1.GetConversationRequest]) (*connect.Response[conversationsv1.GetConversationResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	c, ms, err := h.Service.Get(ctx, authz.From(ctx), r.Msg.GetId())
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &conversationsv1.GetConversationResponse{Conversation: toConversation(c)}
	for _, m := range ms {
		pm, err := toMessage(m)
		if err != nil {
			return nil, rpcErr(err)
		}
		out.Messages = append(out.Messages, pm)
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) RenameConversation(ctx context.Context, r *connect.Request[conversationsv1.RenameConversationRequest]) (*connect.Response[conversationsv1.RenameConversationResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	c, err := h.Service.Rename(ctx, authz.From(ctx), r.Msg.GetId(), r.Msg.GetTitle())
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&conversationsv1.RenameConversationResponse{Conversation: toConversation(c)}), nil
}

func (h *Handler) DeleteConversation(ctx context.Context, r *connect.Request[conversationsv1.DeleteConversationRequest]) (*connect.Response[conversationsv1.DeleteConversationResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	if err := h.Service.Delete(ctx, authz.From(ctx), r.Msg.GetId()); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&conversationsv1.DeleteConversationResponse{}), nil
}

func (h *Handler) AppendMessage(ctx context.Context, r *connect.Request[conversationsv1.AppendMessageRequest]) (*connect.Response[conversationsv1.AppendMessageResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	msg, err := h.Service.Append(ctx, authz.From(ctx), m.GetConversationId(), m.GetRole(),
		Content{Text: m.GetText(), Actions: fromStructs(m.GetActions()), ProcessID: m.GetProcessId(), Status: m.GetStatus(), Context: m.GetContext()})
	if err != nil {
		return nil, rpcErr(err)
	}
	pm, err := toMessage(msg)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&conversationsv1.AppendMessageResponse{Message: pm}), nil
}

func (h *Handler) UpdateMessage(ctx context.Context, r *connect.Request[conversationsv1.UpdateMessageRequest]) (*connect.Response[conversationsv1.UpdateMessageResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	msg, err := h.Service.UpdateMessage(ctx, authz.From(ctx), m.GetId(),
		Content{Text: m.GetText(), Actions: fromStructs(m.GetActions()), ProcessID: m.GetProcessId(), Status: m.GetStatus(), Error: m.GetError()})
	if err != nil {
		return nil, rpcErr(err)
	}
	pm, err := toMessage(msg)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&conversationsv1.UpdateMessageResponse{Message: pm}), nil
}
