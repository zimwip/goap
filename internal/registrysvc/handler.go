package registrysvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	registryv1 "github.com/zimwip/goap/gen/goap/registry/v1"
	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/internal/identity"
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

func (h *Handler) ValidateMethodology(_ context.Context, r *connect.Request[registryv1.ValidateMethodologyRequest]) (*connect.Response[registryv1.ValidateMethodologyResponse], error) {
	m := FromPB(r.Msg.Methodology)
	return connect.NewResponse(&registryv1.ValidateMethodologyResponse{Issues: IssuesToPB(m.Validate())}), nil
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
