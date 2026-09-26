package iamsvc

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	iamv1 "github.com/zimwip/goap/gen/goap/iam/v1"
	"github.com/zimwip/goap/gen/goap/iam/v1/iamv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/engine"
)

// Handler implements iamv1connect.IamServiceHandler. User management is not
// implemented yet (milestone M2).
type Handler struct {
	iamv1connect.UnimplementedIamServiceHandler
	Enforcer *authz.Casbin
	// Orgs stores the organizations (nil: an in-memory store holding the default one).
	Orgs     OrgStore
	Identity identity.Extractor
	Events   engine.Publisher
}

var _ iamv1connect.IamServiceHandler = (*Handler)(nil)

// PolicyChangedSubject is published after a policy change so that replicas reload.
const PolicyChangedSubject = "goap.iam.policy.changed"

func principalFromPB(p *iamv1.Principal) authz.Principal {
	if p == nil {
		return authz.Principal{}
	}
	return authz.Principal{Subject: p.Subject, Org: p.Org, Roles: p.Roles}
}

func (h *Handler) WhoAmI(ctx context.Context, r *connect.Request[iamv1.WhoAmIRequest]) (*connect.Response[iamv1.WhoAmIResponse], error) {
	p := authz.From(h.Identity.Context(ctx, r.Header()))
	return connect.NewResponse(&iamv1.WhoAmIResponse{Principal: &iamv1.Principal{Subject: p.Subject, Org: p.Org, Roles: p.Roles}}), nil
}

func (h *Handler) CheckPermission(ctx context.Context, r *connect.Request[iamv1.CheckPermissionRequest]) (*connect.Response[iamv1.CheckPermissionResponse], error) {
	res := r.Msg.Resource
	if res == nil {
		res = &iamv1.Resource{}
	}
	ok, err := h.Enforcer.Authorize(ctx, authz.Request{Subject: principalFromPB(r.Msg.Subject), Action: r.Msg.Action,
		Resource: authz.Resource{Type: res.Type, ID: res.Id, Org: res.Org, Owner: res.Owner, Name: res.Name}})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&iamv1.CheckPermissionResponse{Allowed: ok}), nil
}

// guard authorizes policy administration for the caller.
func (h *Handler) guard(ctx context.Context, hdr http.Header, action string) error {
	return h.guardResource(ctx, hdr, action, authz.Resource{Type: "policy"})
}

func (h *Handler) guardResource(ctx context.Context, hdr http.Header, action string, res authz.Resource) error {
	ctx = h.Identity.Context(ctx, hdr)
	err := authz.Check(ctx, h.Enforcer, authz.Request{Subject: authz.From(ctx), Action: action, Resource: res})
	if errors.Is(err, authz.ErrForbidden) {
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	return err
}

func toPB(p authz.Policy) *iamv1.Policy {
	return &iamv1.Policy{Rule: p.Rule, Resource: p.Resource, Action: p.Action, Effect: p.Effect}
}

func fromPB(p *iamv1.Policy) authz.Policy {
	if p == nil {
		return authz.Policy{}
	}
	return authz.Policy{Rule: p.Rule, Resource: p.Resource, Action: p.Action, Effect: p.Effect}
}

func (h *Handler) ListPolicies(ctx context.Context, r *connect.Request[iamv1.ListPoliciesRequest]) (*connect.Response[iamv1.ListPoliciesResponse], error) {
	if err := h.guard(ctx, r.Header(), "read"); err != nil {
		return nil, err
	}
	pols, err := h.Enforcer.Policies()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := &iamv1.ListPoliciesResponse{}
	for _, p := range pols {
		out.Policies = append(out.Policies, toPB(p))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) AddPolicy(ctx context.Context, r *connect.Request[iamv1.AddPolicyRequest]) (*connect.Response[iamv1.AddPolicyResponse], error) {
	if err := h.guard(ctx, r.Header(), "write"); err != nil {
		return nil, err
	}
	p := fromPB(r.Msg.Policy)
	if err := authz.Validate(p); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := h.Enforcer.AddPolicy(p); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	h.changed(ctx)
	return connect.NewResponse(&iamv1.AddPolicyResponse{Policy: toPB(p)}), nil
}

func (h *Handler) RemovePolicy(ctx context.Context, r *connect.Request[iamv1.RemovePolicyRequest]) (*connect.Response[iamv1.RemovePolicyResponse], error) {
	if err := h.guard(ctx, r.Header(), "write"); err != nil {
		return nil, err
	}
	if err := h.Enforcer.RemovePolicy(fromPB(r.Msg.Policy)); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	h.changed(ctx)
	return connect.NewResponse(&iamv1.RemovePolicyResponse{}), nil
}

func (h *Handler) changed(ctx context.Context) {
	if h.Events != nil {
		_ = h.Events.Publish(ctx, PolicyChangedSubject, map[string]string{"by": authz.From(ctx).Subject})
	}
}

// Client adapts IamService.CheckPermission to authz.Authorizer.
type Client struct {
	rpc iamv1connect.IamServiceClient
}

var _ authz.Authorizer = (*Client)(nil)

// NewClient returns an IAM client.
func NewClient(hc *http.Client, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{rpc: iamv1connect.NewIamServiceClient(hc, baseURL, opts...)}
}

// Authorize implements authz.Authorizer.
func (c *Client) Authorize(ctx context.Context, req authz.Request) (bool, error) {
	r, err := c.rpc.CheckPermission(ctx, connect.NewRequest(&iamv1.CheckPermissionRequest{
		Subject:  &iamv1.Principal{Subject: req.Subject.Subject, Org: req.Subject.Org, Roles: req.Subject.Roles},
		Action:   req.Action,
		Resource: &iamv1.Resource{Type: req.Resource.Type, Id: req.Resource.ID, Org: req.Resource.Org, Owner: req.Resource.Owner, Name: req.Resource.Name},
	}))
	if err != nil {
		return false, err
	}
	return r.Msg.Allowed, nil
}

func (h *Handler) orgs() OrgStore {
	if h.Orgs == nil {
		h.Orgs = NewMemoryOrgStore()
	}
	return h.Orgs
}

func orgToPB(o Organization) *iamv1.Organization {
	return &iamv1.Organization{Id: o.ID, Name: o.Name, CreatedAt: timestamppb.New(o.CreatedAt)}
}

func orgErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrOrgNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrOrgExists):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, ErrOrgInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func (h *Handler) CreateOrganization(ctx context.Context, r *connect.Request[iamv1.CreateOrganizationRequest]) (*connect.Response[iamv1.CreateOrganizationResponse], error) {
	if err := h.guardResource(ctx, r.Header(), "write", authz.Resource{Type: "organization", ID: r.Msg.Id}); err != nil {
		return nil, err
	}
	o, err := NewOrganization(r.Msg.Id, r.Msg.Name)
	if err != nil {
		return nil, orgErr(err)
	}
	o, err = h.orgs().Create(ctx, o)
	if err != nil {
		return nil, orgErr(err)
	}
	return connect.NewResponse(&iamv1.CreateOrganizationResponse{Organization: orgToPB(o)}), nil
}

func (h *Handler) GetOrganization(ctx context.Context, r *connect.Request[iamv1.GetOrganizationRequest]) (*connect.Response[iamv1.GetOrganizationResponse], error) {
	if err := h.guardResource(ctx, r.Header(), "read", authz.Resource{Type: "organization", ID: r.Msg.Id, Org: r.Msg.Id}); err != nil {
		return nil, err
	}
	o, err := h.orgs().Get(ctx, r.Msg.Id)
	if err != nil {
		return nil, orgErr(err)
	}
	return connect.NewResponse(&iamv1.GetOrganizationResponse{Organization: orgToPB(o)}), nil
}

// ListOrganizations lists the organizations; a caller who is not an admin sees only its own.
func (h *Handler) ListOrganizations(ctx context.Context, r *connect.Request[iamv1.ListOrganizationsRequest]) (*connect.Response[iamv1.ListOrganizationsResponse], error) {
	who := authz.From(h.Identity.Context(ctx, r.Header()))
	all, err := h.orgs().List(ctx)
	if err != nil {
		return nil, orgErr(err)
	}
	admin := false
	for _, role := range who.Roles {
		admin = admin || role == "admin"
	}
	out := &iamv1.ListOrganizationsResponse{}
	for _, o := range all {
		if admin || o.ID == who.Org {
			out.Organizations = append(out.Organizations, orgToPB(o))
		}
	}
	return connect.NewResponse(out), nil
}
