package modelgw

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	modelv1 "github.com/zimwip/goap/gen/goap/model/v1"
	"github.com/zimwip/goap/gen/goap/model/v1/modelv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/llm"
)

// Handler implements modelv1connect.ModelServiceHandler.
type Handler struct {
	Service  *Service
	Identity identity.Extractor
	// Authz guards the administration (AdminAction on AdminResource).
	Authz authz.Authorizer
}

var _ modelv1connect.ModelServiceHandler = (*Handler)(nil)

// AdminAction and AdminResource are the permission required to administer the gateway.
const (
	AdminAction   = "admin"
	AdminResource = "platform"
)

func rpcErr(err error) error {
	var ce *connect.Error
	switch {
	case errors.As(err, &ce):
		return err
	case errors.Is(err, ErrForbidden), errors.Is(err, authz.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, ErrQuotaExceeded):
		return connect.NewError(connect.CodeResourceExhausted, err)
	case errors.Is(err, ErrModelDisabled):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func (h *Handler) guard(ctx context.Context, hdr http.Header) (context.Context, error) {
	ctx = h.Identity.Context(ctx, hdr)
	if h.Authz == nil {
		return ctx, nil
	}
	err := authz.Check(ctx, h.Authz, authz.Request{Subject: authz.From(ctx), Action: AdminAction, Resource: authz.Resource{Type: AdminResource}})
	if err != nil {
		return ctx, rpcErr(err)
	}
	return ctx, nil
}

func (h *Handler) Complete(ctx context.Context, r *connect.Request[modelv1.CompleteRequest]) (*connect.Response[modelv1.CompleteResponse], error) {
	// callers without identity headers (and no dev default) are trusted internal services
	ctx = h.Identity.Context(ctx, r.Header())
	req := llm.Request{Model: r.Msg.Model, System: r.Msg.System, MaxTokens: int(r.Msg.MaxTokens), JSON: r.Msg.Json}
	for _, m := range r.Msg.Messages {
		req.Messages = append(req.Messages, llm.Message{Role: m.Role, Content: m.Content})
	}
	resp, err := h.Service.Complete(ctx, req)
	if err != nil {
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrQuotaExceeded) || errors.Is(err, ErrModelDisabled) || errors.Is(err, ErrInvalid) {
			return nil, rpcErr(err)
		}
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&modelv1.CompleteResponse{Text: resp.Text, Provider: resp.Provider, Model: resp.Model,
		Usage: &modelv1.Usage{InputTokens: int32(resp.Usage.InputTokens), OutputTokens: int32(resp.Usage.OutputTokens)}}), nil
}

func (h *Handler) ListModels(ctx context.Context, r *connect.Request[modelv1.ListModelsRequest]) (*connect.Response[modelv1.ListModelsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	models, aliases, err := h.Service.Available(ctx)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &modelv1.ListModelsResponse{}
	seen := map[string]bool{}
	for _, m := range models {
		out.Models = append(out.Models, &modelv1.AvailableModel{Provider: m.Provider, Model: m.Model, DisplayName: m.DisplayName})
		if !seen[m.Provider] {
			seen[m.Provider] = true
			out.Providers = append(out.Providers, m.Provider)
		}
	}
	for _, a := range aliases {
		if t, ok := parseTarget(a.Target); ok {
			out.Aliases = append(out.Aliases, &modelv1.ModelAlias{Alias: a.Alias, Provider: t.Provider, Model: t.Model})
		}
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) ListProviderKinds(ctx context.Context, r *connect.Request[modelv1.ListProviderKindsRequest]) (*connect.Response[modelv1.ListProviderKindsResponse], error) {
	if _, err := h.guard(ctx, r.Header()); err != nil {
		return nil, err
	}
	out := &modelv1.ListProviderKindsResponse{}
	for _, k := range Kinds() {
		out.Kinds = append(out.Kinds, &modelv1.ProviderKind{Id: k.ID, Label: k.Label, Protocol: k.Protocol, DefaultBaseUrl: k.DefaultBaseURL, KeyRequired: k.KeyRequired, Description: k.Description})
	}
	for _, p := range Protocols() {
		out.Protocols = append(out.Protocols, &modelv1.ProviderProtocol{Id: p.ID, Label: p.Label})
	}
	return connect.NewResponse(out), nil
}

func providerToPB(v ProviderView) *modelv1.Provider {
	return &modelv1.Provider{Name: v.Name, Kind: v.Kind, Protocol: v.Protocol, BaseUrl: v.BaseURL, Enabled: v.Enabled, HasKey: v.HasKey, KeyHint: v.KeyHint, Active: v.Active}
}

func providerFromPB(p *modelv1.Provider) ProviderRecord {
	if p == nil {
		return ProviderRecord{}
	}
	return ProviderRecord{Name: p.Name, Kind: p.Kind, Protocol: p.Protocol, BaseURL: p.BaseUrl, Enabled: p.Enabled}
}

func (h *Handler) ListProviders(ctx context.Context, r *connect.Request[modelv1.ListProvidersRequest]) (*connect.Response[modelv1.ListProvidersResponse], error) {
	ctx, err := h.guard(ctx, r.Header())
	if err != nil {
		return nil, err
	}
	views, err := h.Service.Providers(ctx)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &modelv1.ListProvidersResponse{}
	for _, v := range views {
		out.Providers = append(out.Providers, providerToPB(v))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) SaveProvider(ctx context.Context, r *connect.Request[modelv1.SaveProviderRequest]) (*connect.Response[modelv1.SaveProviderResponse], error) {
	ctx, err := h.guard(ctx, r.Header())
	if err != nil {
		return nil, err
	}
	v, err := h.Service.SaveProvider(ctx, providerFromPB(r.Msg.Provider), r.Msg.ApiKey, r.Msg.ClearKey)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&modelv1.SaveProviderResponse{Provider: providerToPB(v)}), nil
}

func (h *Handler) DeleteProvider(ctx context.Context, r *connect.Request[modelv1.DeleteProviderRequest]) (*connect.Response[modelv1.DeleteProviderResponse], error) {
	ctx, err := h.guard(ctx, r.Header())
	if err != nil {
		return nil, err
	}
	if err := h.Service.DeleteProvider(ctx, r.Msg.Name); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&modelv1.DeleteProviderResponse{}), nil
}

func (h *Handler) DiscoverModels(ctx context.Context, r *connect.Request[modelv1.DiscoverModelsRequest]) (*connect.Response[modelv1.DiscoverModelsResponse], error) {
	ctx, err := h.guard(ctx, r.Header())
	if err != nil {
		return nil, err
	}
	found, err := h.Service.Discover(ctx, providerFromPB(r.Msg.Provider), r.Msg.ApiKey)
	if err != nil {
		if errors.Is(err, ErrInvalid) {
			return nil, rpcErr(err)
		}
		// the provider refused or is unreachable: not an internal error
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	out := &modelv1.DiscoverModelsResponse{}
	for _, m := range found {
		out.Models = append(out.Models, &modelv1.DiscoveredModel{Id: m.ID, DisplayName: m.DisplayName, Registered: m.Registered})
	}
	return connect.NewResponse(out), nil
}

func catalogToPB(e CatalogEntry) *modelv1.CatalogModel {
	return &modelv1.CatalogModel{Provider: e.Provider, Model: e.Model, DisplayName: e.DisplayName, Enabled: e.Enabled,
		QuotaTokens: e.QuotaTokens, QuotaPeriod: e.QuotaPeriod, Roles: e.Roles, UsedTokens: e.Used}
}

func (h *Handler) ListCatalog(ctx context.Context, r *connect.Request[modelv1.ListCatalogRequest]) (*connect.Response[modelv1.ListCatalogResponse], error) {
	ctx, err := h.guard(ctx, r.Header())
	if err != nil {
		return nil, err
	}
	models, aliases, err := h.Service.Catalog(ctx)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &modelv1.ListCatalogResponse{}
	for _, m := range models {
		out.Models = append(out.Models, catalogToPB(m))
	}
	for _, a := range aliases {
		if t, ok := parseTarget(a.Target); ok {
			out.Aliases = append(out.Aliases, &modelv1.ModelAlias{Alias: a.Alias, Provider: t.Provider, Model: t.Model})
		}
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) SaveModel(ctx context.Context, r *connect.Request[modelv1.SaveModelRequest]) (*connect.Response[modelv1.SaveModelResponse], error) {
	ctx, err := h.guard(ctx, r.Header())
	if err != nil {
		return nil, err
	}
	m := r.Msg.Model
	if m == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("model required"))
	}
	e, err := h.Service.SaveModel(ctx, ModelEntry{Provider: m.Provider, Model: m.Model, DisplayName: m.DisplayName, Enabled: m.Enabled,
		QuotaTokens: m.QuotaTokens, QuotaPeriod: m.QuotaPeriod, Roles: m.Roles})
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&modelv1.SaveModelResponse{Model: catalogToPB(e)}), nil
}

func (h *Handler) DeleteModel(ctx context.Context, r *connect.Request[modelv1.DeleteModelRequest]) (*connect.Response[modelv1.DeleteModelResponse], error) {
	ctx, err := h.guard(ctx, r.Header())
	if err != nil {
		return nil, err
	}
	if err := h.Service.DeleteModel(ctx, r.Msg.Provider, r.Msg.Model); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&modelv1.DeleteModelResponse{}), nil
}

func (h *Handler) SaveAlias(ctx context.Context, r *connect.Request[modelv1.SaveAliasRequest]) (*connect.Response[modelv1.SaveAliasResponse], error) {
	ctx, err := h.guard(ctx, r.Header())
	if err != nil {
		return nil, err
	}
	a := r.Msg.Alias
	if a == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("alias required"))
	}
	if err := h.Service.SaveAlias(ctx, AliasEntry{Alias: a.Alias, Target: a.Provider + "/" + a.Model}); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&modelv1.SaveAliasResponse{}), nil
}

func (h *Handler) DeleteAlias(ctx context.Context, r *connect.Request[modelv1.DeleteAliasRequest]) (*connect.Response[modelv1.DeleteAliasResponse], error) {
	ctx, err := h.guard(ctx, r.Header())
	if err != nil {
		return nil, err
	}
	if err := h.Service.DeleteAlias(ctx, r.Msg.Alias); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&modelv1.DeleteAliasResponse{}), nil
}

// Client adapts the model gateway Connect client to llm.Client.
type Client struct {
	rpc modelv1connect.ModelServiceClient
}

var _ llm.Client = (*Client)(nil)

// NewClient returns a model gateway client.
func NewClient(hc *http.Client, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{rpc: modelv1connect.NewModelServiceClient(hc, baseURL, opts...)}
}

// Complete implements llm.Client.
func (c *Client) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	in := &modelv1.CompleteRequest{Model: req.Model, System: req.System, MaxTokens: int32(req.MaxTokens), Json: req.JSON}
	for _, m := range req.Messages {
		in.Messages = append(in.Messages, &modelv1.Message{Role: m.Role, Content: m.Content})
	}
	r, err := c.rpc.Complete(ctx, connect.NewRequest(in))
	if err != nil {
		return llm.Response{}, err
	}
	out := llm.Response{Text: r.Msg.Text, Provider: r.Msg.Provider, Model: r.Msg.Model}
	if u := r.Msg.Usage; u != nil {
		out.Usage = llm.Usage{InputTokens: int(u.InputTokens), OutputTokens: int(u.OutputTokens)}
	}
	return out, nil
}
