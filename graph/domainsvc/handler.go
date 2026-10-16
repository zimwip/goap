package domainsvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/algo"
	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	registryv1 "github.com/zimwip/goap/contracts/gen/goap/registry/v1"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/lib/domainpb"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/pbconv"
)

// Handler implements registryv1connect.DomainServiceHandler.
type Handler struct {
	Service *Service
	// Identity extracts the caller from request headers.
	Identity identity.Extractor
}

var _ registryv1connect.DomainServiceHandler = (*Handler)(nil)

func toConnect(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrImmutable):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, authz.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func (h *Handler) ListDomains(ctx context.Context, r *connect.Request[registryv1.ListDomainsRequest]) (*connect.Response[registryv1.ListDomainsResponse], error) {
	rs, err := h.Service.DomainVersions(ctx, r.Msg.AllVersions)
	if err != nil {
		return nil, toConnect(err)
	}
	out := &registryv1.ListDomainsResponse{}
	for _, rec := range rs {
		out.Domains = append(out.Domains, domainpb.DomainSummaryToPB(rec))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) GetDomain(ctx context.Context, r *connect.Request[registryv1.GetDomainRequest]) (*connect.Response[registryv1.GetDomainResponse], error) {
	rec, err := h.Service.GetDomain(ctx, r.Msg.Name, r.Msg.Version)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.GetDomainResponse{Domain: domainpb.DomainToPB(rec)}), nil
}

func (h *Handler) SaveDomain(ctx context.Context, r *connect.Request[registryv1.SaveDomainRequest]) (*connect.Response[registryv1.SaveDomainResponse], error) {
	rec, issues, err := h.Service.SaveDomain(h.Identity.Context(ctx, r.Header()), domainpb.DomainFromPB(r.Msg.Domain))
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.SaveDomainResponse{Domain: domainpb.DomainToPB(rec), Issues: domainpb.IssuesToPB(issues)}), nil
}

func (h *Handler) ValidateDomain(_ context.Context, r *connect.Request[registryv1.ValidateDomainRequest]) (*connect.Response[registryv1.ValidateDomainResponse], error) {
	d := domainpb.DomainFromPB(r.Msg.Domain)
	return connect.NewResponse(&registryv1.ValidateDomainResponse{Issues: domainpb.IssuesToPB(d.Validate())}), nil
}

func (h *Handler) PublishDomain(ctx context.Context, r *connect.Request[registryv1.PublishDomainRequest]) (*connect.Response[registryv1.PublishDomainResponse], error) {
	rec, err := h.Service.PublishDomain(h.Identity.Context(ctx, r.Header()), r.Msg.Name, r.Msg.Version)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.PublishDomainResponse{Domain: domainpb.DomainToPB(rec)}), nil
}

func (h *Handler) CreateDomainVersion(ctx context.Context, r *connect.Request[registryv1.CreateDomainVersionRequest]) (*connect.Response[registryv1.CreateDomainVersionResponse], error) {
	rec, err := h.Service.CreateDomainVersion(h.Identity.Context(ctx, r.Header()), r.Msg.Name, r.Msg.FromVersion, r.Msg.NewVersion)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.CreateDomainVersionResponse{Domain: domainpb.DomainToPB(rec)}), nil
}

func (h *Handler) DeleteDomain(ctx context.Context, r *connect.Request[registryv1.DeleteDomainRequest]) (*connect.Response[registryv1.DeleteDomainResponse], error) {
	if err := h.Service.DeleteDomain(h.Identity.Context(ctx, r.Header()), r.Msg.Name, r.Msg.Version); err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.DeleteDomainResponse{}), nil
}

func (h *Handler) ImportDomain(ctx context.Context, r *connect.Request[registryv1.ImportDomainRequest]) (*connect.Response[registryv1.ImportDomainResponse], error) {
	rec, issues, err := h.Service.ImportDomain(h.Identity.Context(ctx, r.Header()), []byte(r.Msg.Yaml), r.Msg.Publish)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.ImportDomainResponse{Domain: domainpb.DomainToPB(rec), Issues: domainpb.IssuesToPB(issues)}), nil
}

func (h *Handler) ExportDomain(ctx context.Context, r *connect.Request[registryv1.ExportDomainRequest]) (*connect.Response[registryv1.ExportDomainResponse], error) {
	out, name, err := h.Service.ExportDomain(ctx, r.Msg.Name, r.Msg.Version, r.Msg.Format)
	if err != nil {
		return nil, toConnect(err)
	}
	media := "application/yaml"
	if r.Msg.Format == "json" {
		media = "application/json"
	}
	return connect.NewResponse(&registryv1.ExportDomainResponse{Yaml: string(out), Filename: name, MediaType: media}), nil
}

func (h *Handler) RunAlgorithm(ctx context.Context, r *connect.Request[registryv1.RunAlgorithmRequest]) (*connect.Response[registryv1.RunAlgorithmResponse], error) {
	if r.Msg.Algorithm == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("algorithm required"))
	}
	algs := domainpb.AlgorithmsFromPB([]*registryv1.Algorithm{r.Msg.Algorithm})
	out, err := h.Service.RunAlgorithm(h.Identity.Context(ctx, r.Header()), algs[0], pbconv.Map(r.Msg.Values), pbconv.Map(r.Msg.Input))
	resp := &registryv1.RunAlgorithmResponse{Failures: out.Failures, Set: pbconv.Struct(out.Set), Unset: out.Unset}
	for _, l := range out.Logs {
		resp.Logs = append(resp.Logs, l.Level+": "+l.Message)
	}
	switch {
	case errors.Is(err, ErrInvalid) || errors.Is(err, authz.ErrForbidden):
		return nil, toConnect(err)
	case err != nil:
		resp.Error = err.Error()
	default:
		resp.Ok = out.OK()
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) ListTypes(ctx context.Context, _ *connect.Request[registryv1.ListTypesRequest]) (*connect.Response[registryv1.ListTypesResponse], error) {
	cat, err := h.Service.Types(ctx)
	if err != nil {
		return nil, toConnect(err)
	}
	out := &registryv1.ListTypesResponse{Domains: cat.Domains()}
	for _, t := range cat.Types() {
		ti := &registryv1.TypeInfo{Ref: t.Ref.String(), Description: t.Description, Attributes: domainpb.AttributeInfosToPB(t.Attributes), ChangeControlled: t.ChangeControlled, Editor: t.Editor, AdditionalProperties: t.AdditionalProperties, Form: pbconv.FormToPB(t.Form)}
		for _, a := range t.Ancestors {
			ti.Ancestors = append(ti.Ancestors, a.String())
		}
		if t.Lifecycle != nil {
			ti.Lifecycle = domainpb.LifecyclesToPB([]domain.Lifecycle{*t.Lifecycle})[0]
		}
		if t.Document != nil {
			ti.Contains = t.Document.Contains
		}
		for _, v := range t.Validators {
			if v.Type == algo.UsageNodeValidator {
				ti.NodeValidators = append(ti.NodeValidators, v.Instance)
			}
		}
		out.Types = append(out.Types, ti)
	}
	for _, l := range cat.LinkTypes() {
		li := &registryv1.LinkTypeInfo{Ref: l.Ref.String(), Compose: l.Compose, Attributes: domainpb.AttributeInfosToPB(l.Attributes),
			Label: l.Label, InverseLabel: l.InverseLabel}
		if !l.From.IsZero() {
			li.From = l.From.String()
		}
		if !l.To.IsZero() {
			li.To = l.To.String()
		}
		out.LinkTypes = append(out.LinkTypes, li)
	}
	return connect.NewResponse(out), nil
}
