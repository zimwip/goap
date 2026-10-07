package modelgw

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

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
	ctx = llm.WithMeta(h.Identity.Context(ctx, r.Header()), metaFromPB(r.Msg.Meta))
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
			out.Aliases = append(out.Aliases, &modelv1.ModelAlias{Alias: a.Alias, Provider: t.Provider, Model: t.Model, Protected: a.Protected})
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
	return &modelv1.Provider{Name: v.Name, Kind: v.Kind, Protocol: v.Protocol, BaseUrl: v.BaseURL, Enabled: v.Enabled, HasKey: v.HasKey, ApiKeyRef: v.APIKeyRef, Active: v.Active}
}

func providerFromPB(p *modelv1.Provider) ProviderRecord {
	if p == nil {
		return ProviderRecord{}
	}
	return ProviderRecord{Name: p.Name, Kind: p.Kind, Protocol: p.Protocol, BaseURL: p.BaseUrl, Enabled: p.Enabled, APIKeyRef: p.ApiKeyRef}
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
			out.Aliases = append(out.Aliases, &modelv1.ModelAlias{Alias: a.Alias, Provider: t.Provider, Model: t.Model, Protected: a.Protected})
		} else if a.Protected { // listed for the administrators to retarget it, resolving to nothing
			out.Aliases = append(out.Aliases, &modelv1.ModelAlias{Alias: a.Alias, Protected: true})
		}
	}
	return connect.NewResponse(out), nil
}

// metaToPB is the declaration of the calls made with ctx, for the ledger of the gateway (ADR 0089).
func metaToPB(ctx context.Context) *modelv1.CallMeta {
	m := llm.MetaFrom(ctx)
	return &modelv1.CallMeta{Source: m.Source, ConversationId: m.ConversationID, ProcessId: m.ProcessID, ChangeId: m.ChangeID,
		Step: int32(m.Step), Action: m.Action, Agent: m.Agent, Call: int32(m.Call)}
}

// metaFromPB is the declaration of a request, cleaned (the caller is not trusted with it beyond accounting).
func metaFromPB(m *modelv1.CallMeta) llm.CallMeta {
	if m == nil {
		return llm.CallMeta{}.Clean()
	}
	return llm.CallMeta{Source: m.Source, ConversationID: m.ConversationId, ProcessID: m.ProcessId, ChangeID: m.ChangeId,
		Step: int(m.Step), Action: m.Action, Agent: m.Agent, Call: int(m.Call)}.Clean()
}

// Client adapts the model gateway Connect client to llm.Client.
type Client struct {
	rpc modelv1connect.ModelServiceClient
}

var (
	_ llm.Client   = (*Client)(nil)
	_ llm.Embedder = (*Client)(nil)
	_ SuggestModel = (*Client)(nil)
)

// NewClient returns a model gateway client.
func NewClient(hc *http.Client, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{rpc: modelv1connect.NewModelServiceClient(hc, baseURL, opts...)}
}

// Complete implements llm.Client.
func (c *Client) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	in := &modelv1.CompleteRequest{Model: req.Model, System: req.System, MaxTokens: int32(req.MaxTokens), Json: req.JSON, Meta: metaToPB(ctx)}
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

func (h *Handler) Embed(ctx context.Context, r *connect.Request[modelv1.EmbedRequest]) (*connect.Response[modelv1.EmbedResponse], error) {
	ctx = llm.WithMeta(h.Identity.Context(ctx, r.Header()), metaFromPB(r.Msg.Meta))
	resp, err := h.Service.Embed(ctx, llm.EmbedRequest{Model: r.Msg.Model, Texts: r.Msg.Texts})
	if err != nil {
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrQuotaExceeded) || errors.Is(err, ErrModelDisabled) || errors.Is(err, ErrInvalid) {
			return nil, rpcErr(err)
		}
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	out := &modelv1.EmbedResponse{Provider: resp.Provider, Model: resp.Model, Tokens: int32(resp.Tokens)}
	for _, v := range resp.Vectors {
		out.Vectors = append(out.Vectors, &modelv1.Vector{Values: v})
	}
	return connect.NewResponse(out), nil
}

// Available lists what the caller may use: the models and the aliases pointing to them (the contextual helper and the
// assistant check their alias with it).
func (c *Client) Available(ctx context.Context) ([]ModelEntry, []AliasEntry, error) {
	r, err := c.rpc.ListModels(ctx, connect.NewRequest(&modelv1.ListModelsRequest{}))
	if err != nil {
		return nil, nil, err
	}
	var models []ModelEntry
	for _, m := range r.Msg.Models {
		models = append(models, ModelEntry{Provider: m.Provider, Model: m.Model, DisplayName: m.DisplayName, Enabled: true})
	}
	var aliases []AliasEntry
	for _, a := range r.Msg.Aliases {
		aliases = append(aliases, AliasEntry{Alias: a.Alias, Target: a.Provider + "/" + a.Model, Protected: a.Protected})
	}
	return models, aliases, nil
}

// Embed implements llm.Embedder.
func (c *Client) Embed(ctx context.Context, req llm.EmbedRequest) (llm.EmbedResponse, error) {
	r, err := c.rpc.Embed(ctx, connect.NewRequest(&modelv1.EmbedRequest{Model: req.Model, Texts: req.Texts, Meta: metaToPB(ctx)}))
	if err != nil {
		return llm.EmbedResponse{}, err
	}
	out := llm.EmbedResponse{Provider: r.Msg.Provider, Model: r.Msg.Model, Tokens: int(r.Msg.Tokens)}
	for _, v := range r.Msg.Vectors {
		out.Vectors = append(out.Vectors, v.Values)
	}
	return out, nil
}

// Suggest implements the contextual helper (ADR 0086): stateless, as the calling principal.
func (h *Handler) Suggest(ctx context.Context, r *connect.Request[modelv1.SuggestRequest]) (*connect.Response[modelv1.SuggestResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	res, err := h.Service.Suggest(ctx, suggestFromPB(r.Msg))
	if err != nil {
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrQuotaExceeded) || errors.Is(err, ErrModelDisabled) || errors.Is(err, ErrInvalid) {
			return nil, rpcErr(err)
		}
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	out := &modelv1.SuggestResponse{Message: res.Message, Usage: &modelv1.Usage{InputTokens: int32(res.Usage.InputTokens), OutputTokens: int32(res.Usage.OutputTokens)}}
	for _, p := range res.Proposals {
		out.Proposals = append(out.Proposals, &modelv1.SuggestProposal{FieldId: p.FieldID, Value: string(p.Value), Rationale: p.Rationale})
	}
	return connect.NewResponse(out), nil
}

func suggestFromPB(m *modelv1.SuggestRequest) SuggestInput {
	in := SuggestInput{Instruction: m.Instruction}
	if c := m.Context; c != nil {
		in.Context = SuggestContext{Subject: c.Subject, Selection: c.Selection}
		if c.Tab != nil {
			in.Context.TabKind, in.Context.TabParams = c.Tab.Kind, c.Tab.Params
		}
		for _, f := range c.Fields {
			in.Context.Fields = append(in.Context.Fields, SuggestField{ID: f.Id, Label: f.Label, Type: f.Type, EnumValues: f.EnumValues, Description: f.Description, Current: f.CurrentValue, ReadOnly: f.ReadOnly})
		}
	}
	for _, x := range m.Messages {
		in.Messages = append(in.Messages, SuggestMessage{Role: x.Role, Text: x.Text})
	}
	return in
}

// isAdmin reports whether the caller administers the platform and so may read the calls of any subject. Without an
// authorizer nobody is: the ledger is then readable by its callers for their own calls only.
func (h *Handler) isAdmin(ctx context.Context) bool {
	if h.Authz == nil {
		return false
	}
	return authz.Check(ctx, h.Authz, authz.Request{Subject: authz.From(ctx), Action: AdminAction, Resource: authz.Resource{Type: AdminResource}}) == nil
}

func filterFromPB(f *modelv1.UsageFilter) UsageFilter {
	if f == nil {
		return UsageFilter{}
	}
	out := UsageFilter{Subject: f.Subject, Project: f.Project, Model: f.Model, Alias: f.Alias, Source: f.Source, ProcessID: f.ProcessId,
		ChangeID: f.ChangeId, ConversationID: f.ConversationId, AfterSeq: f.AfterSeq, Limit: int(f.Limit)}
	if f.From != nil {
		out.From = f.From.AsTime()
	}
	if f.To != nil {
		out.To = f.To.AsTime()
	}
	return out
}

func callToPB(c Call) *modelv1.LLMCall {
	return &modelv1.LLMCall{Seq: c.Seq, At: timestamppb.New(c.At), DurationMs: c.DurationMs, Subject: c.Subject, Project: c.Project, Org: c.Org,
		Alias: c.Alias, Provider: c.Provider, Model: c.Model, Kind: c.Kind, InputTokens: c.InputTokens, OutputTokens: c.OutputTokens, Error: c.Error,
		Source: c.Source, ConversationId: c.ConversationID, ProcessId: c.ProcessID, ChangeId: c.ChangeID, Step: int32(c.Step), Action: c.Action,
		Agent: c.Agent, Call: int32(c.CallIndex)}
}

// ListUsage reads the ledger: the caller's own calls, any subject's for an administrator.
func (h *Handler) ListUsage(ctx context.Context, r *connect.Request[modelv1.ListUsageRequest]) (*connect.Response[modelv1.ListUsageResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	calls, next, more, err := h.Service.ListCalls(ctx, filterFromPB(r.Msg.Filter), h.isAdmin(ctx))
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &modelv1.ListUsageResponse{NextSeq: next, HasMore: more}
	for _, c := range calls {
		out.Calls = append(out.Calls, callToPB(c))
	}
	return connect.NewResponse(out), nil
}

// UsageSummary groups the ledger, with the same visibility as ListUsage.
func (h *Handler) UsageSummary(ctx context.Context, r *connect.Request[modelv1.UsageSummaryRequest]) (*connect.Response[modelv1.UsageSummaryResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	rows, err := h.Service.Summary(ctx, filterFromPB(r.Msg.Filter), r.Msg.GroupBy, h.isAdmin(ctx))
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &modelv1.UsageSummaryResponse{}
	for _, x := range rows {
		out.Rows = append(out.Rows, &modelv1.UsageSummaryRow{Key: x.Key, Calls: x.Calls, InputTokens: x.Input, OutputTokens: x.Output, Errors: x.Errors, DurationMs: x.DurationMs})
	}
	return connect.NewResponse(out), nil
}
