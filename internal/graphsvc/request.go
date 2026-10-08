package graphsvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/internal/rpcerr"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// ResourceRequest is the ABAC resource of a request (ADR 0098): Owner its requester, ProjectID its project (empty
// until triaged). Its actions are create, view, update, link, close, reject, withdraw: "view", not "read", so that the
// generic read rule of the organisation does not open the untriaged requests (authz.DefaultPolicies: the requester and
// the triagers see them, the members of the project see a triaged one).
const ResourceRequest = "request"

// requestActions maps a final status to the action that sets it.
var requestActions = map[domain.RequestStatus]string{domain.RequestClosed: "close", domain.RequestRejected: "reject", domain.RequestWithdrawn: "withdraw"}

// checkRequest is the ABAC check of an action on a request.
func (h *Handler) checkRequest(ctx context.Context, r domain.Request, action string) error {
	who := authz.From(ctx)
	if err := authz.Check(ctx, h.Authz, authz.Request{Subject: who, Action: action,
		Resource: authz.Resource{Type: ResourceRequest, ID: string(r.ID), Name: r.Title, Owner: r.Requester, ProjectID: r.ProjectID}}); err != nil {
		return rpcerr.ToConnect(err)
	}
	return nil
}

// request reads a request the caller may view, and checks the action they ask (empty: viewing only): a request they may
// not view is not found.
func (h *Handler) request(ctx context.Context, id, action string) (domain.Request, error) {
	r, err := h.Graph.Request(ctx, domain.RequestID(id))
	if err != nil {
		return r, rpcerr.ToConnect(err)
	}
	if err := h.checkRequest(ctx, r, "view"); err != nil {
		return r, connect.NewError(connect.CodeNotFound, errors.New("request "+id+" not found"))
	}
	if action != "" {
		return r, h.checkRequest(ctx, r, action)
	}
	return r, nil
}

func requestResponse(r domain.Request, err error) (*connect.Response[graphv1.RequestResponse], error) {
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&graphv1.RequestResponse{Request: pbconv.RequestToPB(r)}), nil
}

// CreateRequest records a request of the caller.
func (h *Handler) CreateRequest(ctx context.Context, r *connect.Request[graphv1.CreateRequestRequest]) (*connect.Response[graphv1.RequestResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	who := authz.From(ctx)
	if err := h.checkRequest(ctx, domain.Request{Requester: who.Subject, ProjectID: m.ProjectId}, "create"); err != nil {
		return nil, err
	}
	return requestResponse(h.Graph.CreateRequest(ctx, graph.NewRequest{Title: m.Title, Text: m.Text, Requester: who.Subject, ProjectID: m.ProjectId,
		Origin: domain.RequestOrigin{Kind: m.OriginKind, Ref: m.OriginRef}}))
}

// GetRequest reads a request the caller may view.
func (h *Handler) GetRequest(ctx context.Context, r *connect.Request[graphv1.GetRequestRequest]) (*connect.Response[graphv1.RequestResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	return requestResponse(h.request(ctx, r.Msg.RequestId, ""))
}

// ListRequests lists the requests matching the request that the caller may view.
func (h *Handler) ListRequests(ctx context.Context, r *connect.Request[graphv1.ListRequestsRequest]) (*connect.Response[graphv1.ListRequestsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	f := domain.RequestFilter{Requester: m.Requester, Change: domain.ChangeID(m.ChangeId)}
	if f.Requester == "@me" {
		f.Requester = authz.From(ctx).Subject
	}
	if m.FilterProjects {
		f.Projects = append([]string{}, m.ProjectIds...)
	}
	for _, s := range m.Statuses {
		f.Statuses = append(f.Statuses, domain.RequestStatus(s))
	}
	rs, err := h.Graph.Requests(ctx, f)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	out := &graphv1.ListRequestsResponse{}
	for _, x := range rs {
		if h.checkRequest(ctx, x, "view") != nil {
			continue
		}
		out.Requests = append(out.Requests, pbconv.RequestToPB(x))
		if m.Limit > 0 && len(out.Requests) == int(m.Limit) {
			break
		}
	}
	return connect.NewResponse(out), nil
}

// UpdateRequest renames a request.
func (h *Handler) UpdateRequest(ctx context.Context, r *connect.Request[graphv1.UpdateRequestRequest]) (*connect.Response[graphv1.RequestResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	if _, err := h.request(ctx, r.Msg.RequestId, "update"); err != nil {
		return nil, err
	}
	return requestResponse(h.Graph.UpdateRequest(ctx, domain.RequestID(r.Msg.RequestId), r.Msg.Title))
}

// SetRequestStatus closes (the requester or a triager), rejects or withdraws (the requester) a request.
func (h *Handler) SetRequestStatus(ctx context.Context, r *connect.Request[graphv1.SetRequestStatusRequest]) (*connect.Response[graphv1.RequestResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	status := domain.RequestStatus(r.Msg.Status)
	action, ok := requestActions[status]
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a request is closed, rejected or withdrawn"))
	}
	if _, err := h.request(ctx, r.Msg.RequestId, action); err != nil {
		return nil, err
	}
	return requestResponse(h.Graph.SetRequestStatus(ctx, domain.RequestID(r.Msg.RequestId), status, r.Msg.Comment))
}

// LinkRequest links a request to a change (the change is screened by the PersonalScope interceptor).
func (h *Handler) LinkRequest(ctx context.Context, r *connect.Request[graphv1.LinkRequestRequest]) (*connect.Response[graphv1.RequestResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	if _, err := h.request(ctx, r.Msg.RequestId, "link"); err != nil {
		return nil, err
	}
	return requestResponse(h.Graph.LinkRequest(ctx, domain.RequestID(r.Msg.RequestId), domain.ChangeID(r.Msg.ChangeId), domain.LinkRole(r.Msg.Role)))
}

// UnlinkRequest removes the link of a request to a change.
func (h *Handler) UnlinkRequest(ctx context.Context, r *connect.Request[graphv1.UnlinkRequestRequest]) (*connect.Response[graphv1.RequestResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	if _, err := h.request(ctx, r.Msg.RequestId, "link"); err != nil {
		return nil, err
	}
	return requestResponse(h.Graph.UnlinkRequest(ctx, domain.RequestID(r.Msg.RequestId), domain.ChangeID(r.Msg.ChangeId)))
}

// ListRequestLog reads the log of a request the caller may view.
func (h *Handler) ListRequestLog(ctx context.Context, r *connect.Request[graphv1.ListRequestLogRequest]) (*connect.Response[graphv1.ListRequestLogResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	if _, err := h.request(ctx, r.Msg.RequestId, ""); err != nil {
		return nil, err
	}
	es, err := h.Graph.RequestLog(ctx, domain.RequestID(r.Msg.RequestId))
	return res(&graphv1.ListRequestLogResponse{Entries: pbconv.RequestEntriesToPB(es)}, err)
}
