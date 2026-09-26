package registrysvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	registryv1 "github.com/zimwip/goap/gen/goap/registry/v1"
	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/authz"
)

// Handler implements registryv1connect.RegistryServiceHandler.
type Handler struct {
	Service *Service
	// Identity extracts the caller from request headers.
	Identity identity.Extractor
}

var _ registryv1connect.RegistryServiceHandler = (*Handler)(nil)

func toConnect(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrImmutable):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, ErrNoDomainStore):
		return connect.NewError(connect.CodeUnimplemented, err)
	case errors.Is(err, ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, authz.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func (h *Handler) ListMethodologies(ctx context.Context, r *connect.Request[registryv1.ListMethodologiesRequest]) (*connect.Response[registryv1.ListMethodologiesResponse], error) {
	rs, err := h.Service.Versions(ctx, r.Msg.AllVersions)
	if err != nil {
		return nil, toConnect(err)
	}
	out := &registryv1.ListMethodologiesResponse{}
	for _, rec := range rs {
		out.Methodologies = append(out.Methodologies, SummaryToPB(rec))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) GetMethodology(ctx context.Context, r *connect.Request[registryv1.GetMethodologyRequest]) (*connect.Response[registryv1.GetMethodologyResponse], error) {
	get := h.Service.Get
	if r.Msg.ResolveDomain {
		get = h.Service.GetResolved
	}
	rec, err := get(ctx, r.Msg.Name, r.Msg.Version)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.GetMethodologyResponse{Methodology: ToPB(rec)}), nil
}

func (h *Handler) SaveMethodology(ctx context.Context, r *connect.Request[registryv1.SaveMethodologyRequest]) (*connect.Response[registryv1.SaveMethodologyResponse], error) {
	rec, issues, err := h.Service.Save(h.Identity.Context(ctx, r.Header()), FromPB(r.Msg.Methodology))
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.SaveMethodologyResponse{Methodology: ToPB(rec), Issues: IssuesToPB(issues)}), nil
}

func (h *Handler) ValidateMethodology(ctx context.Context, r *connect.Request[registryv1.ValidateMethodologyRequest]) (*connect.Response[registryv1.ValidateMethodologyResponse], error) {
	m := FromPB(r.Msg.Methodology)
	return connect.NewResponse(&registryv1.ValidateMethodologyResponse{Issues: IssuesToPB(h.Service.validate(ctx, &m))}), nil
}

func (h *Handler) PublishMethodology(ctx context.Context, r *connect.Request[registryv1.PublishMethodologyRequest]) (*connect.Response[registryv1.PublishMethodologyResponse], error) {
	rec, err := h.Service.Publish(h.Identity.Context(ctx, r.Header()), r.Msg.Name, r.Msg.Version)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.PublishMethodologyResponse{Methodology: ToPB(rec)}), nil
}

func (h *Handler) CreateVersion(ctx context.Context, r *connect.Request[registryv1.CreateVersionRequest]) (*connect.Response[registryv1.CreateVersionResponse], error) {
	rec, err := h.Service.CreateVersion(h.Identity.Context(ctx, r.Header()), r.Msg.Name, r.Msg.FromVersion, r.Msg.NewVersion)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.CreateVersionResponse{Methodology: ToPB(rec)}), nil
}

func (h *Handler) DeleteMethodology(ctx context.Context, r *connect.Request[registryv1.DeleteMethodologyRequest]) (*connect.Response[registryv1.DeleteMethodologyResponse], error) {
	if err := h.Service.Delete(h.Identity.Context(ctx, r.Header()), r.Msg.Name, r.Msg.Version); err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.DeleteMethodologyResponse{}), nil
}

func (h *Handler) ImportMethodology(ctx context.Context, r *connect.Request[registryv1.ImportMethodologyRequest]) (*connect.Response[registryv1.ImportMethodologyResponse], error) {
	rec, issues, err := h.Service.Import(h.Identity.Context(ctx, r.Header()), []byte(r.Msg.Yaml), r.Msg.Publish)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.ImportMethodologyResponse{Methodology: ToPB(rec), Issues: IssuesToPB(issues)}), nil
}

func (h *Handler) ExportMethodology(ctx context.Context, r *connect.Request[registryv1.ExportMethodologyRequest]) (*connect.Response[registryv1.ExportMethodologyResponse], error) {
	out, name, err := h.Service.Export(ctx, r.Msg.Name, r.Msg.Version)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.ExportMethodologyResponse{Yaml: string(out), Filename: name}), nil
}

func (h *Handler) ListDomains(ctx context.Context, r *connect.Request[registryv1.ListDomainsRequest]) (*connect.Response[registryv1.ListDomainsResponse], error) {
	rs, err := h.Service.DomainVersions(ctx, r.Msg.AllVersions)
	if err != nil {
		return nil, toConnect(err)
	}
	out := &registryv1.ListDomainsResponse{}
	for _, rec := range rs {
		out.Domains = append(out.Domains, DomainSummaryToPB(rec))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) GetDomain(ctx context.Context, r *connect.Request[registryv1.GetDomainRequest]) (*connect.Response[registryv1.GetDomainResponse], error) {
	rec, err := h.Service.GetDomain(ctx, r.Msg.Name, r.Msg.Version)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.GetDomainResponse{Domain: DomainToPB(rec)}), nil
}

func (h *Handler) SaveDomain(ctx context.Context, r *connect.Request[registryv1.SaveDomainRequest]) (*connect.Response[registryv1.SaveDomainResponse], error) {
	rec, issues, err := h.Service.SaveDomain(h.Identity.Context(ctx, r.Header()), DomainFromPB(r.Msg.Domain))
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.SaveDomainResponse{Domain: DomainToPB(rec), Issues: IssuesToPB(issues)}), nil
}

func (h *Handler) ValidateDomain(_ context.Context, r *connect.Request[registryv1.ValidateDomainRequest]) (*connect.Response[registryv1.ValidateDomainResponse], error) {
	d := DomainFromPB(r.Msg.Domain)
	return connect.NewResponse(&registryv1.ValidateDomainResponse{Issues: IssuesToPB(d.Validate())}), nil
}

func (h *Handler) PublishDomain(ctx context.Context, r *connect.Request[registryv1.PublishDomainRequest]) (*connect.Response[registryv1.PublishDomainResponse], error) {
	rec, err := h.Service.PublishDomain(h.Identity.Context(ctx, r.Header()), r.Msg.Name, r.Msg.Version)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.PublishDomainResponse{Domain: DomainToPB(rec)}), nil
}

func (h *Handler) CreateDomainVersion(ctx context.Context, r *connect.Request[registryv1.CreateDomainVersionRequest]) (*connect.Response[registryv1.CreateDomainVersionResponse], error) {
	rec, err := h.Service.CreateDomainVersion(h.Identity.Context(ctx, r.Header()), r.Msg.Name, r.Msg.FromVersion, r.Msg.NewVersion)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.CreateDomainVersionResponse{Domain: DomainToPB(rec)}), nil
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
	return connect.NewResponse(&registryv1.ImportDomainResponse{Domain: DomainToPB(rec), Issues: IssuesToPB(issues)}), nil
}

func (h *Handler) ExportDomain(ctx context.Context, r *connect.Request[registryv1.ExportDomainRequest]) (*connect.Response[registryv1.ExportDomainResponse], error) {
	out, name, err := h.Service.ExportDomain(ctx, r.Msg.Name, r.Msg.Version)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.ExportDomainResponse{Yaml: string(out), Filename: name}), nil
}

func (h *Handler) GetDomainUsage(ctx context.Context, r *connect.Request[registryv1.GetDomainUsageRequest]) (*connect.Response[registryv1.GetDomainUsageResponse], error) {
	rs, err := h.Service.DomainUsage(ctx, r.Msg.Name, r.Msg.Version)
	if err != nil {
		return nil, toConnect(err)
	}
	out := &registryv1.GetDomainUsageResponse{}
	for _, rec := range rs {
		out.Methodologies = append(out.Methodologies, &registryv1.DomainUser{Name: rec.Methodology.Name, Version: rec.Methodology.Version, Status: string(rec.Status)})
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) RunAlgorithm(ctx context.Context, r *connect.Request[registryv1.RunAlgorithmRequest]) (*connect.Response[registryv1.RunAlgorithmResponse], error) {
	if r.Msg.Algorithm == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("algorithm required"))
	}
	algs := algorithmsFromPB([]*registryv1.Algorithm{r.Msg.Algorithm})
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
