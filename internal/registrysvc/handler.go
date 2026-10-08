package registrysvc

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	registryv1 "github.com/zimwip/goap/gen/goap/registry/v1"
	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/engine"
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
	rec, err := h.Service.Get(ctx, r.Msg.Name, r.Msg.Version)
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

// GetProcessGraph builds the graph of a process of a methodology as edited (ADR 0036 §4), with the issues of the
// methodology: a draft that does not compile still has its graph drawn as far as it stands.
func (h *Handler) GetProcessGraph(ctx context.Context, r *connect.Request[registryv1.GetProcessGraphRequest]) (*connect.Response[registryv1.GetProcessGraphResponse], error) {
	m := FromPB(r.Msg.Methodology)
	c, issues := m.CompileLenient()
	if c == nil {
		return connect.NewResponse(&registryv1.GetProcessGraphResponse{Issues: IssuesToPB(issues)}), nil
	}
	g, ok := c.ProcessGraph(r.Msg.Process)
	if !ok {
		if issues.HasErrors() {
			return connect.NewResponse(&registryv1.GetProcessGraphResponse{Issues: IssuesToPB(issues)}), nil
		}
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no process %q in %s", r.Msg.Process, m.Name))
	}
	return connect.NewResponse(&registryv1.GetProcessGraphResponse{Graph: ProcessGraphToPB(g), Issues: IssuesToPB(issues)}), nil
}

// CheckLevels checks the coherence of a process or method, level by level, as edited, with the issues of the
// methodology.
func (h *Handler) CheckLevels(ctx context.Context, r *connect.Request[registryv1.CheckLevelsRequest]) (*connect.Response[registryv1.CheckLevelsResponse], error) {
	m := FromPB(r.Msg.Methodology)
	c, issues := m.CompileLenient()
	if c == nil {
		return connect.NewResponse(&registryv1.CheckLevelsResponse{Issues: IssuesToPB(issues)}), nil
	}
	ls, ok := c.CheckLevels(r.Msg.Root)
	if !ok {
		if issues.HasErrors() {
			return connect.NewResponse(&registryv1.CheckLevelsResponse{Issues: IssuesToPB(issues)}), nil
		}
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no process or method with steps %q in %s", r.Msg.Root, m.Name))
	}
	return connect.NewResponse(&registryv1.CheckLevelsResponse{Levels: LevelsToPB(ls), Conditions: c.Conditions.Exprs(), Issues: IssuesToPB(issues)}), nil
}

// PreviewPlan plans toward a goal with the planner its agent is actually configured with, from an empty
// blackboard whose evaluated conditions the request's overrides patch on top (ADR 0034; no live Change needed). Like
// GetProcessGraph it plans over what stands of a draft and returns the issues of the methodology alongside the plan.
func (h *Handler) PreviewPlan(ctx context.Context, r *connect.Request[registryv1.PreviewPlanRequest]) (*connect.Response[registryv1.PreviewPlanResponse], error) {
	m := FromPB(r.Msg.Methodology)
	c, issues := m.CompileLenient()
	if c == nil {
		return connect.NewResponse(&registryv1.PreviewPlanResponse{Issues: IssuesToPB(issues)}), nil
	}
	p, err := engine.PreviewPlan(c, domain.Blackboard{}, r.Msg.Agent, r.Msg.Goal, r.Msg.Overrides)
	if err != nil {
		if issues.HasErrors() { // the agent or goal may be one the draft's issues dropped: tell the issues
			return connect.NewResponse(&registryv1.PreviewPlanResponse{Issues: IssuesToPB(issues)}), nil
		}
		if errors.Is(err, engine.ErrLivePlanner) {
			return connect.NewResponse(&registryv1.PreviewPlanResponse{Issues: IssuesToPB(def.Issues{{Message: err.Error()}})}), nil
		}
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&registryv1.PreviewPlanResponse{Preview: PlanPreviewToPB(p), Issues: IssuesToPB(issues)}), nil
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

func (h *Handler) ListTypes(ctx context.Context, _ *connect.Request[registryv1.ListTypesRequest]) (*connect.Response[registryv1.ListTypesResponse], error) {
	cat, err := h.Service.Types(ctx)
	if err != nil {
		return nil, toConnect(err)
	}
	out := &registryv1.ListTypesResponse{Domains: cat.Domains()}
	for _, t := range cat.Types() {
		ti := &registryv1.TypeInfo{Ref: t.Ref.String(), Description: t.Description, Attributes: attributeInfosToPB(t.Attributes), ChangeControlled: t.ChangeControlled, Editor: t.Editor, AdditionalProperties: t.AdditionalProperties}
		for _, a := range t.Ancestors {
			ti.Ancestors = append(ti.Ancestors, a.String())
		}
		if t.Lifecycle != nil {
			ti.Lifecycle = lifecyclesToPB([]domain.Lifecycle{*t.Lifecycle})[0]
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
		li := &registryv1.LinkTypeInfo{Ref: l.Ref.String(), Compose: l.Compose, Attributes: attributeInfosToPB(l.Attributes)}
		if !l.From.IsZero() {
			li.From = l.From.String()
		}
		if !l.To.IsZero() {
			li.To = l.To.String()
		}
		out.LinkTypes = append(out.LinkTypes, li)
	}
	for _, t := range cat.ObjectTypes() {
		oi := &registryv1.ChangeObjectTypeInfo{Ref: t.Ref.String(), Description: t.Description, Key: keyTypeToPB(t.Key), Scope: t.Scope,
			Attributes: attributeInfosToPB(t.Attributes), Editor: t.Editor, AdditionalProperties: t.AdditionalProperties}
		if t.Lifecycle != nil {
			oi.Lifecycle = lifecyclesToPB([]domain.Lifecycle{*t.Lifecycle})[0]
		}
		out.ChangeObjectTypes = append(out.ChangeObjectTypes, oi)
	}
	return connect.NewResponse(out), nil
}
