package smarttagsvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	smarttagv1 "github.com/zimwip/goap/contracts/gen/goap/smarttag/v1"
	"github.com/zimwip/goap/contracts/gen/goap/smarttag/v1/smarttagv1connect"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/pbconv"
)

// Handler implements smarttagv1connect.SmartTagServiceHandler: every request acts for its caller.
type Handler struct {
	Service  *Service
	Identity identity.Extractor
}

var _ smarttagv1connect.SmartTagServiceHandler = (*Handler)(nil)

func rpcErr(err error) error {
	var ce *connect.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ce):
		return err
	case errors.Is(err, ErrAnonymous):
		return connect.NewError(connect.CodeUnauthenticated, err)
	case errors.Is(err, ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func refsFromPB(vs []*smarttagv1.TaggedVersion) []domain.NodeRef {
	out := make([]domain.NodeRef, 0, len(vs))
	for _, v := range vs {
		out = append(out, domain.NodeRef{ID: domain.NodeID(v.GetNodeId()), Version: domain.Version(v.GetVersion())})
	}
	return out
}

// toPB is t as subject sees it.
func toPB(t SmartTag, subject string) *smarttagv1.SmartTag {
	access := t.Access
	if access == "" {
		access = t.AccessOf(subject)
	}
	out := &smarttagv1.SmartTag{Id: t.ID, Name: t.Name, Description: t.Description, Owner: t.Owner, Access: access,
		Parent: t.Parent, Project: t.Project, By: t.By, CreatedAt: pbconv.Time(t.CreatedAt), UpdatedAt: pbconv.Time(t.UpdatedAt)}
	for _, r := range t.Versions {
		out.Versions = append(out.Versions, &smarttagv1.TaggedVersion{NodeId: string(r.ID), Version: int32(r.Version)})
	}
	for _, s := range t.Shares {
		out.Shares = append(out.Shares, &smarttagv1.Share{Subject: s.Subject, Access: s.Access})
	}
	return out
}

func (h *Handler) one(ctx context.Context, t SmartTag, err error) (*smarttagv1.SmartTag, error) {
	if err != nil {
		return nil, rpcErr(err)
	}
	return toPB(t, authz.From(ctx).Subject), nil
}

func (h *Handler) ListSmartTags(ctx context.Context, r *connect.Request[smarttagv1.ListSmartTagsRequest]) (*connect.Response[smarttagv1.ListSmartTagsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	ts, err := h.Service.List(ctx, domain.NodeID(r.Msg.NodeId), r.Msg.Project)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &smarttagv1.ListSmartTagsResponse{}
	for _, t := range ts {
		out.SmartTags = append(out.SmartTags, toPB(t, authz.From(ctx).Subject))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) GetSmartTag(ctx context.Context, r *connect.Request[smarttagv1.GetSmartTagRequest]) (*connect.Response[smarttagv1.GetSmartTagResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	t, err := h.Service.Get(ctx, r.Msg.Id)
	pb, err := h.one(ctx, t, err)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&smarttagv1.GetSmartTagResponse{SmartTag: pb}), nil
}

func (h *Handler) CreateSmartTag(ctx context.Context, r *connect.Request[smarttagv1.CreateSmartTagRequest]) (*connect.Response[smarttagv1.CreateSmartTagResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	t, err := h.Service.Create(ctx, New{Name: r.Msg.Name, Description: r.Msg.Description, Versions: refsFromPB(r.Msg.Versions),
		Parent: r.Msg.Parent, Project: r.Msg.Project})
	pb, err := h.one(ctx, t, err)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&smarttagv1.CreateSmartTagResponse{SmartTag: pb}), nil
}

func (h *Handler) UpdateSmartTag(ctx context.Context, r *connect.Request[smarttagv1.UpdateSmartTagRequest]) (*connect.Response[smarttagv1.UpdateSmartTagResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	t, err := h.Service.Update(ctx, r.Msg.Id, Patch{Name: r.Msg.Name, Description: r.Msg.Description, SetDescription: r.Msg.SetDescription,
		Add: refsFromPB(r.Msg.Add), Remove: refsFromPB(r.Msg.Remove), Parent: r.Msg.Parent, SetParent: r.Msg.SetParent})
	pb, err := h.one(ctx, t, err)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&smarttagv1.UpdateSmartTagResponse{SmartTag: pb}), nil
}

func (h *Handler) ShareSmartTag(ctx context.Context, r *connect.Request[smarttagv1.ShareSmartTagRequest]) (*connect.Response[smarttagv1.ShareSmartTagResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	t, err := h.Service.Share(ctx, r.Msg.Id, r.Msg.Subject, r.Msg.Access)
	pb, err := h.one(ctx, t, err)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&smarttagv1.ShareSmartTagResponse{SmartTag: pb}), nil
}

func (h *Handler) DeleteSmartTag(ctx context.Context, r *connect.Request[smarttagv1.DeleteSmartTagRequest]) (*connect.Response[smarttagv1.DeleteSmartTagResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	if err := h.Service.Delete(ctx, r.Msg.Id); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&smarttagv1.DeleteSmartTagResponse{}), nil
}
