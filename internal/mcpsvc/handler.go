package mcpsvc

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"connectrpc.com/connect"

	mcpv1 "github.com/zimwip/goap/gen/goap/mcp/v1"
	"github.com/zimwip/goap/gen/goap/mcp/v1/mcpv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/adapter"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/mcpbuiltin"
)

// Handler implements mcpv1connect.McpServiceHandler.
type Handler struct {
	Service  *Service
	Authz    authz.Authorizer
	Identity identity.Extractor
	// ConnectorToken must accompany RegisterConnector (header ConnectorTokenHeader); with none configured no
	// connector can register: an unauthenticated service could otherwise announce itself as the implementation
	// of any MCP and receive the arguments and secrets of its calls.
	ConnectorToken string
}

var _ mcpv1connect.McpServiceHandler = (*Handler)(nil)

func rpcErr(err error) error {
	var ce *connect.Error
	var te *ToolError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ce):
		return err
	case errors.Is(err, authz.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrNotBound):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, mcp.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, ErrUnavailable):
		return connect.NewError(connect.CodeUnavailable, err)
	case errors.As(err, &te):
		return connect.NewError(connect.CodeUnknown, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

// check authorizes the caller (identity of the request) for action on res.
func (h *Handler) check(ctx context.Context, hdr http.Header, action string, res authz.Resource) (context.Context, error) {
	ctx = h.Identity.Context(ctx, hdr)
	return ctx, rpcErr(authz.Check(ctx, h.Authz, authz.Request{Subject: authz.From(ctx), Action: action, Resource: res}))
}

func (h *Handler) RegisterConnector(ctx context.Context, r *connect.Request[mcpv1.RegisterConnectorRequest]) (*connect.Response[mcpv1.RegisterConnectorResponse], error) {
	if h.ConnectorToken == "" {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("connector registration is closed: no connector token is configured (GOAP_CONNECTOR_TOKEN)"))
	}
	if subtle.ConstantTimeCompare([]byte(r.Header().Get(ConnectorTokenHeader)), []byte(h.ConnectorToken)) != 1 {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("invalid connector token"))
	}
	lease, err := h.Service.RegisterConnector(ctx, r.Msg.Info, r.Msg.Endpoint)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&mcpv1.RegisterConnectorResponse{LeaseSeconds: int32(lease.Seconds())}), nil
}

func (h *Handler) ListConnectors(ctx context.Context, r *connect.Request[mcpv1.ListConnectorsRequest]) (*connect.Response[mcpv1.ListConnectorsResponse], error) {
	if _, err := h.check(ctx, r.Header(), "read", authz.Resource{Type: "connector"}); err != nil {
		return nil, err
	}
	cs, err := h.Service.Connectors(ctx)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &mcpv1.ListConnectorsResponse{}
	for _, c := range cs {
		out.Connectors = append(out.Connectors, &mcpv1.Connector{Info: c.Info, Endpoint: c.Endpoint, LastSeen: pbconv.Time(c.LastSeen), Live: c.Live})
	}
	return connect.NewResponse(out), nil
}

func defToPB(d mcp.Def) *mcpv1.Mcp {
	out := &mcpv1.Mcp{Name: d.Name, Description: d.Description, Scope: mcp.ScopeOf(d.Scope)}
	for _, t := range d.Tools {
		out.Tools = append(out.Tools, &mcpv1.McpTool{Name: t.Name, Description: t.Description, InputSchema: pbconv.Struct(t.InputSchema), ReadOnly: t.ReadOnly})
	}
	return out
}

func adapterToPB(a adapter.Instance) *mcpv1.Adapter {
	return &mcpv1.Adapter{Unit: a.Unit, Mcp: a.MCP, Adapter: a.Adapter, Params: pbconv.Struct(a.Params),
		Disabled: a.Disabled, Tools: a.Tools, Deny: a.Deny, ReadOnly: a.ReadOnly}
}

func adapterFromPB(a *mcpv1.Adapter) adapter.Instance {
	if a == nil {
		return adapter.Instance{}
	}
	return adapter.Instance{Unit: a.Unit, MCP: a.Mcp, Adapter: a.Adapter, Params: pbconv.Map(a.Params),
		Disabled: a.Disabled, Tools: a.Tools, Deny: a.Deny, ReadOnly: a.ReadOnly}
}

// callerResource is the resource of a request made on behalf of the caller's own tenant.
func (h *Handler) callerResource(ctx context.Context, hdr http.Header, typ, name string) authz.Resource {
	who := authz.From(h.Identity.Context(ctx, hdr))
	return authz.Resource{Type: typ, Name: name, Org: who.Org, ProjectID: who.Project}
}

// unitOf resolves the unit a request acts for against its caller: the caller's own unit when the request names
// none, else the requested one must be the caller's unit or below it (the caller's unit is in its chain), so that
// a member of one unit cannot use the adapters, and the secrets they hold, of another. Administrators and
// platform services (system: subjects) may name any unit.
func (h *Handler) unitOf(ctx context.Context, hdr http.Header, requested string) (string, error) {
	who := authz.From(h.Identity.Context(ctx, hdr))
	if who.System() || slices.Contains(who.Roles, "admin") {
		return requested, nil
	}
	if requested == "" {
		return who.Org, nil
	}
	if who.Org != "" {
		snap, err := h.Service.Directory.Snapshot(ctx)
		if err != nil {
			return "", rpcErr(err)
		}
		if slices.Contains(snap.Chain(requested), who.Org) {
			return requested, nil
		}
	}
	return "", connect.NewError(connect.CodePermissionDenied, fmt.Errorf("unit %s is not within the unit of the caller: %w", requested, authz.ErrForbidden))
}

func (h *Handler) ListMcps(ctx context.Context, r *connect.Request[mcpv1.ListMcpsRequest]) (*connect.Response[mcpv1.ListMcpsResponse], error) {
	if _, err := h.check(ctx, r.Header(), "read", h.callerResource(ctx, r.Header(), "mcp", "")); err != nil {
		return nil, err
	}
	ds, err := h.Service.MCPs(ctx)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &mcpv1.ListMcpsResponse{}
	for _, d := range ds {
		out.Mcps = append(out.Mcps, defToPB(d))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) ListEffective(ctx context.Context, r *connect.Request[mcpv1.ListEffectiveRequest]) (*connect.Response[mcpv1.ListEffectiveResponse], error) {
	if _, err := h.check(ctx, r.Header(), "read", h.callerResource(ctx, r.Header(), "adapter", r.Msg.Unit)); err != nil {
		return nil, err
	}
	org, err := h.unitOf(ctx, r.Header(), r.Msg.Unit)
	if err != nil {
		return nil, err
	}
	chain, eff, err := h.Service.Effective(ctx, org)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &mcpv1.ListEffectiveResponse{Chain: chain}
	for _, e := range eff {
		em := &mcpv1.EffectiveMcp{Mcp: defToPB(e.MCP), Adapter: adapterToPB(e.Adapter), Inherited: e.Inherited,
			Connector: h.Service.ConnectorOf(ctx, e.Adapter), RestrictedBy: e.Restriction.By, Disabled: e.Restriction.Disabled, Builtin: mcpbuiltin.Is(e.MCP.Name)}
		for _, t := range e.Allowed().Tools {
			em.AllowedTools = append(em.AllowedTools, t.Name)
		}
		out.Mcps = append(out.Mcps, em)
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) CheckAdapter(ctx context.Context, r *connect.Request[mcpv1.CheckAdapterRequest]) (*connect.Response[mcpv1.CheckAdapterResponse], error) {
	a := adapterFromPB(r.Msg.Adapter)
	if _, err := h.check(ctx, r.Header(), "read", h.callerResource(ctx, r.Header(), "adapter", a.MCP)); err != nil {
		return nil, err
	}
	warnings, err := h.Service.CheckAdapter(ctx, a)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&mcpv1.CheckAdapterResponse{Warnings: warnings}), nil
}

func (h *Handler) AdapterTemplate(ctx context.Context, r *connect.Request[mcpv1.AdapterTemplateRequest]) (*connect.Response[mcpv1.AdapterTemplateResponse], error) {
	if _, err := h.check(ctx, r.Header(), "read", h.callerResource(ctx, r.Header(), "adapter", r.Msg.Mcp)); err != nil {
		return nil, err
	}
	code, params, err := h.Service.Template(ctx, r.Msg.Mcp, r.Msg.Connector)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &mcpv1.AdapterTemplateResponse{Code: code}
	for _, p := range params {
		out.Params = append(out.Params, &mcpv1.TemplateParam{Name: p.Name, Type: p.Type, Description: p.Description, Required: p.Required})
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) ListTools(ctx context.Context, r *connect.Request[mcpv1.ListToolsRequest]) (*connect.Response[mcpv1.ListToolsResponse], error) {
	if _, err := h.check(ctx, r.Header(), "read", h.callerResource(ctx, r.Header(), "tool", "")); err != nil {
		return nil, err
	}
	org, err := h.unitOf(ctx, r.Header(), r.Msg.Unit)
	if err != nil {
		return nil, err
	}
	tools, mcps, err := h.Service.Tools(ctx, org)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &mcpv1.ListToolsResponse{Mcps: mcps}
	for _, t := range tools {
		out.Tools = append(out.Tools, &mcpv1.Tool{Name: t.Name, Description: t.Description, InputSchema: pbconv.Struct(t.InputSchema), ReadOnly: t.ReadOnly, Scope: t.Scope})
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) CallTool(ctx context.Context, r *connect.Request[mcpv1.CallToolRequest]) (*connect.Response[mcpv1.CallToolResponse], error) {
	ctx, err := h.check(ctx, r.Header(), "call", h.callerResource(ctx, r.Header(), "tool", r.Msg.Name))
	if err != nil {
		return nil, err
	}
	org, err := h.unitOf(ctx, r.Header(), r.Msg.Unit)
	if err != nil {
		return nil, err
	}
	ctx = mcp.WithCall(ctx, mcp.CallContext{Change: r.Msg.ChangeId, Process: r.Msg.ProcessId})
	res, err := h.Service.Call(ctx, org, r.Msg.Name, pbconv.Map(r.Msg.Arguments))
	var te *ToolError
	if errors.As(err, &te) {
		return connect.NewResponse(&mcpv1.CallToolResponse{IsError: true, Error: te.Msg}), nil
	}
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&mcpv1.CallToolResponse{Result: pbconv.Struct(res)}), nil
}
