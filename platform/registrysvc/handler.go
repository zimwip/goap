package registrysvc

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	registryv1 "github.com/zimwip/goap/contracts/gen/goap/registry/v1"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/domainpb"
	"github.com/zimwip/goap/lib/identity"
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
		sum := SummaryToPB(rec)
		sum.Suspended = h.Service.Suspended(ctx, rec)
		out.Methodologies = append(out.Methodologies, sum)
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) GetMethodology(ctx context.Context, r *connect.Request[registryv1.GetMethodologyRequest]) (*connect.Response[registryv1.GetMethodologyResponse], error) {
	rec, err := h.Service.Get(ctx, r.Msg.Name, r.Msg.Version)
	if err != nil {
		return nil, toConnect(err)
	}
	m := ToPB(rec)
	m.Suspended = h.Service.Suspended(ctx, rec)
	return connect.NewResponse(&registryv1.GetMethodologyResponse{Methodology: m}), nil
}

func (h *Handler) SaveMethodology(ctx context.Context, r *connect.Request[registryv1.SaveMethodologyRequest]) (*connect.Response[registryv1.SaveMethodologyResponse], error) {
	rec, issues, err := h.Service.Save(h.Identity.Context(ctx, r.Header()), FromPB(r.Msg.Methodology))
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.SaveMethodologyResponse{Methodology: ToPB(rec), Issues: domainpb.IssuesToPB(issues)}), nil
}

func (h *Handler) ValidateMethodology(ctx context.Context, r *connect.Request[registryv1.ValidateMethodologyRequest]) (*connect.Response[registryv1.ValidateMethodologyResponse], error) {
	m := FromPB(r.Msg.Methodology)
	return connect.NewResponse(&registryv1.ValidateMethodologyResponse{Issues: domainpb.IssuesToPB(h.Service.validate(ctx, &m))}), nil
}

// GetProcessGraph builds the graph of a process of a methodology as edited (ADR 0036 §4), with the issues of the
// methodology: a draft that does not compile still has its graph drawn as far as it stands.
func (h *Handler) GetProcessGraph(ctx context.Context, r *connect.Request[registryv1.GetProcessGraphRequest]) (*connect.Response[registryv1.GetProcessGraphResponse], error) {
	c, issues, err := h.compiledFor(ctx, r.Msg.Methodology, r.Msg.Name, r.Msg.Version)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return connect.NewResponse(&registryv1.GetProcessGraphResponse{Issues: domainpb.IssuesToPB(issues)}), nil
	}
	g, ok := c.ProcessGraph(r.Msg.Process)
	if !ok {
		if issues.HasErrors() {
			return connect.NewResponse(&registryv1.GetProcessGraphResponse{Issues: domainpb.IssuesToPB(issues)}), nil
		}
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no process %q in %s", r.Msg.Process, c.Name()))
	}
	return connect.NewResponse(&registryv1.GetProcessGraphResponse{Graph: ProcessGraphToPB(g), Issues: domainpb.IssuesToPB(issues)}), nil
}

// compiledFor is the methodology a read of its shape is about: the one given as edited, compiled leniently with its
// issues (a draft is drawn as far as it stands), else the stored version name / version names, compiled against its
// domains (ADR 0113; the version a change follows).
func (h *Handler) compiledFor(ctx context.Context, edited *registryv1.Methodology, name, version string) (*methodology.Compiled, def.Issues, error) {
	if edited != nil || name == "" {
		m := FromPB(edited)
		c, issues := m.CompileLenient()
		return c, issues, nil
	}
	c, err := h.Service.Methodology(ctx, domain.MethodologyRef(name, version))
	switch {
	case errors.Is(err, ErrSuspended):
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition, err)
	case err != nil:
		return nil, nil, connect.NewError(connect.CodeNotFound, err)
	}
	return c, nil, nil
}

// GetMethodologyView is the compiled view of the activities of a methodology (ADR 0126 step 3): as edited, with its
// issues, drawn as far as it stands; else the stored version named.
func (h *Handler) GetMethodologyView(ctx context.Context, r *connect.Request[registryv1.GetMethodologyViewRequest]) (*connect.Response[registryv1.GetMethodologyViewResponse], error) {
	c, issues, err := h.compiledFor(ctx, r.Msg.Methodology, r.Msg.Name, r.Msg.Version)
	if err != nil {
		return nil, err
	}
	out := &registryv1.GetMethodologyViewResponse{Issues: domainpb.IssuesToPB(issues)}
	if c != nil {
		out.View = MethodologyViewToPB(c.View())
	}
	return connect.NewResponse(out), nil
}

// CheckLevels checks the coherence of a process or method, level by level, as edited, with the issues of the
// methodology.
func (h *Handler) CheckLevels(ctx context.Context, r *connect.Request[registryv1.CheckLevelsRequest]) (*connect.Response[registryv1.CheckLevelsResponse], error) {
	c, issues, err := h.compiledFor(ctx, r.Msg.Methodology, r.Msg.Name, r.Msg.Version)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return connect.NewResponse(&registryv1.CheckLevelsResponse{Issues: domainpb.IssuesToPB(issues)}), nil
	}
	ls, ok := c.CheckLevels(r.Msg.Root)
	if !ok {
		if issues.HasErrors() {
			return connect.NewResponse(&registryv1.CheckLevelsResponse{Issues: domainpb.IssuesToPB(issues)}), nil
		}
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no process or method with steps %q in %s", r.Msg.Root, c.Name()))
	}
	return connect.NewResponse(&registryv1.CheckLevelsResponse{Levels: LevelsToPB(ls), Conditions: c.Conditions.Exprs(), Issues: domainpb.IssuesToPB(issues)}), nil
}

// PreviewPlan plans toward a goal with the planner its agent is actually configured with, from an empty
// blackboard whose evaluated conditions the request's overrides patch on top (ADR 0034; no live Change needed). Like
// GetProcessGraph it plans over what stands of a draft and returns the issues of the methodology alongside the plan.
func (h *Handler) PreviewPlan(ctx context.Context, r *connect.Request[registryv1.PreviewPlanRequest]) (*connect.Response[registryv1.PreviewPlanResponse], error) {
	m := FromPB(r.Msg.Methodology)
	c, issues := m.CompileLenient()
	if c == nil {
		return connect.NewResponse(&registryv1.PreviewPlanResponse{Issues: domainpb.IssuesToPB(issues)}), nil
	}
	p, err := engine.PreviewPlan(c, domain.Blackboard{}, r.Msg.Agent, r.Msg.Goal, r.Msg.Overrides)
	if err != nil {
		if issues.HasErrors() { // the agent or goal may be one the draft's issues dropped: tell the issues
			return connect.NewResponse(&registryv1.PreviewPlanResponse{Issues: domainpb.IssuesToPB(issues)}), nil
		}
		if errors.Is(err, engine.ErrLivePlanner) {
			return connect.NewResponse(&registryv1.PreviewPlanResponse{Issues: domainpb.IssuesToPB(def.Issues{{Message: err.Error()}})}), nil
		}
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&registryv1.PreviewPlanResponse{Preview: PlanPreviewToPB(p), Issues: domainpb.IssuesToPB(issues)}), nil
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
	return connect.NewResponse(&registryv1.ImportMethodologyResponse{Methodology: ToPB(rec), Issues: domainpb.IssuesToPB(issues)}), nil
}

func (h *Handler) ExportMethodology(ctx context.Context, r *connect.Request[registryv1.ExportMethodologyRequest]) (*connect.Response[registryv1.ExportMethodologyResponse], error) {
	out, name, err := h.Service.Export(ctx, r.Msg.Name, r.Msg.Version)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&registryv1.ExportMethodologyResponse{Yaml: string(out), Filename: name}), nil
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
