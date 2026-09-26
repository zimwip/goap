package mcpsvc

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	mcpv1 "github.com/zimwip/goap/gen/goap/mcp/v1"
	"github.com/zimwip/goap/gen/goap/mcp/v1/mcpv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/mcp"
)

// Handler implements mcpv1connect.McpServiceHandler.
type Handler struct {
	Service  *Service
	Authz    authz.Authorizer
	Identity identity.Extractor
	// ConnectorToken, when set, must accompany RegisterConnector (header ConnectorTokenHeader).
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
	if h.ConnectorToken != "" && subtle.ConstantTimeCompare([]byte(r.Header().Get(ConnectorTokenHeader)), []byte(h.ConnectorToken)) != 1 {
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
	out := &mcpv1.Mcp{Name: d.Name, Description: d.Description}
	for _, t := range d.Tools {
		out.Tools = append(out.Tools, &mcpv1.McpTool{Name: t.Name, Description: t.Description, InputSchema: pbconv.Struct(t.InputSchema)})
	}
	return out
}

func adapterToPB(a mcp.Adapter) *mcpv1.Adapter {
	return &mcpv1.Adapter{Unit: a.Unit, Mcp: a.MCP, Domain: a.Domain, Version: a.Version, Algorithm: a.Algorithm, Params: pbconv.Struct(a.Params)}
}

func adapterFromPB(a *mcpv1.Adapter) mcp.Adapter {
	if a == nil {
		return mcp.Adapter{}
	}
	return mcp.Adapter{Unit: a.Unit, MCP: a.Mcp, Domain: a.Domain, Version: a.Version, Algorithm: a.Algorithm, Params: pbconv.Map(a.Params)}
}

// unit is the requested unit, else the default organisation.
func unit(u string) string { return domain.OrgOf(u) }

// callerResource is the resource of a request made on behalf of the caller's own tenant.
func (h *Handler) callerResource(ctx context.Context, hdr http.Header, typ, name string) authz.Resource {
	return authz.Resource{Type: typ, Name: name, Org: authz.From(h.Identity.Context(ctx, hdr)).Org}
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
	if _, err := h.check(ctx, r.Header(), "read", h.callerResource(ctx, r.Header(), "adapter", unit(r.Msg.Unit))); err != nil {
		return nil, err
	}
	chain, eff, err := h.Service.Effective(ctx, r.Msg.Unit)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &mcpv1.ListEffectiveResponse{Chain: chain}
	for _, e := range eff {
		out.Mcps = append(out.Mcps, &mcpv1.EffectiveMcp{Mcp: defToPB(e.MCP), Adapter: adapterToPB(e.Adapter), Inherited: e.Inherited,
			Connector: h.Service.ConnectorOf(ctx, e.Adapter)})
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
	tools, mcps, err := h.Service.Tools(ctx, r.Msg.Unit)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &mcpv1.ListToolsResponse{Mcps: mcps}
	for _, t := range tools {
		out.Tools = append(out.Tools, &mcpv1.Tool{Name: t.Name, Description: t.Description, InputSchema: pbconv.Struct(t.InputSchema)})
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) CallTool(ctx context.Context, r *connect.Request[mcpv1.CallToolRequest]) (*connect.Response[mcpv1.CallToolResponse], error) {
	ctx, err := h.check(ctx, r.Header(), "call", h.callerResource(ctx, r.Header(), "tool", r.Msg.Name))
	if err != nil {
		return nil, err
	}
	res, err := h.Service.Call(ctx, r.Msg.Unit, r.Msg.Name, pbconv.Map(r.Msg.Arguments))
	var te *ToolError
	if errors.As(err, &te) {
		return connect.NewResponse(&mcpv1.CallToolResponse{IsError: true, Error: te.Msg}), nil
	}
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&mcpv1.CallToolResponse{Result: pbconv.Struct(res)}), nil
}
