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
	case errors.Is(err, ErrConflict), errors.Is(err, ErrNotBound):
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

// orgOf returns the requested organization, defaulting to the caller's.
func orgOf(ctx context.Context, org string) string {
	if org != "" {
		return org
	}
	return authz.From(ctx).Org
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

func defFromPB(m *mcpv1.Mcp) mcp.Def {
	if m == nil {
		return mcp.Def{}
	}
	d := mcp.Def{Name: m.Name, Description: m.Description, Tools: []mcp.Tool{}}
	for _, t := range m.Tools {
		d.Tools = append(d.Tools, mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: pbconv.Map(t.InputSchema)})
	}
	return d
}

func adapterToPB(a mcp.Adapter) *mcpv1.Adapter {
	out := &mcpv1.Adapter{Mcp: a.MCP, Connector: a.Connector}
	for _, m := range a.Tools {
		out.Tools = append(out.Tools, &mcpv1.ToolMapping{Tool: m.Tool, Operation: m.Operation, Arguments: pbconv.Struct(m.Arguments), ResultPath: m.ResultPath})
	}
	return out
}

func adapterFromPB(a *mcpv1.Adapter) mcp.Adapter {
	if a == nil {
		return mcp.Adapter{}
	}
	out := mcp.Adapter{MCP: a.Mcp, Connector: a.Connector, Tools: []mcp.ToolMapping{}}
	for _, m := range a.Tools {
		out.Tools = append(out.Tools, mcp.ToolMapping{Tool: m.Tool, Operation: m.Operation, Arguments: pbconv.Map(m.Arguments), ResultPath: m.ResultPath})
	}
	return out
}

func bindingToPB(b mcp.Binding) *mcpv1.Binding {
	return &mcpv1.Binding{OrgId: b.OrgID, Mcp: b.MCP, Connector: b.Connector, Config: pbconv.Struct(b.Config), Secrets: b.Secrets}
}

func (h *Handler) SaveMcp(ctx context.Context, r *connect.Request[mcpv1.SaveMcpRequest]) (*connect.Response[mcpv1.SaveMcpResponse], error) {
	d := defFromPB(r.Msg.Mcp)
	if _, err := h.check(ctx, r.Header(), "write", authz.Resource{Type: "mcp", Name: d.Name}); err != nil {
		return nil, err
	}
	if err := h.Service.SaveMcp(ctx, d); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&mcpv1.SaveMcpResponse{Mcp: defToPB(d)}), nil
}

func (h *Handler) ListMcps(ctx context.Context, r *connect.Request[mcpv1.ListMcpsRequest]) (*connect.Response[mcpv1.ListMcpsResponse], error) {
	if _, err := h.check(ctx, r.Header(), "read", authz.Resource{Type: "mcp"}); err != nil {
		return nil, err
	}
	ds, err := h.Service.Store.Mcps(ctx)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &mcpv1.ListMcpsResponse{}
	for _, d := range ds {
		out.Mcps = append(out.Mcps, defToPB(d))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) DeleteMcp(ctx context.Context, r *connect.Request[mcpv1.DeleteMcpRequest]) (*connect.Response[mcpv1.DeleteMcpResponse], error) {
	if _, err := h.check(ctx, r.Header(), "write", authz.Resource{Type: "mcp", Name: r.Msg.Name}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&mcpv1.DeleteMcpResponse{}), rpcErr(h.Service.Store.DeleteMcp(ctx, r.Msg.Name))
}

func (h *Handler) SaveAdapter(ctx context.Context, r *connect.Request[mcpv1.SaveAdapterRequest]) (*connect.Response[mcpv1.SaveAdapterResponse], error) {
	a := adapterFromPB(r.Msg.Adapter)
	if _, err := h.check(ctx, r.Header(), "write", authz.Resource{Type: "mcp", Name: a.MCP}); err != nil {
		return nil, err
	}
	warnings, err := h.Service.SaveAdapter(ctx, a)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&mcpv1.SaveAdapterResponse{Adapter: adapterToPB(a), Warnings: warnings}), nil
}

func (h *Handler) ListAdapters(ctx context.Context, r *connect.Request[mcpv1.ListAdaptersRequest]) (*connect.Response[mcpv1.ListAdaptersResponse], error) {
	if _, err := h.check(ctx, r.Header(), "read", authz.Resource{Type: "mcp", Name: r.Msg.Mcp}); err != nil {
		return nil, err
	}
	as, err := h.Service.Store.Adapters(ctx, r.Msg.Mcp)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &mcpv1.ListAdaptersResponse{}
	for _, a := range as {
		out.Adapters = append(out.Adapters, adapterToPB(a))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) DeleteAdapter(ctx context.Context, r *connect.Request[mcpv1.DeleteAdapterRequest]) (*connect.Response[mcpv1.DeleteAdapterResponse], error) {
	if _, err := h.check(ctx, r.Header(), "write", authz.Resource{Type: "mcp", Name: r.Msg.Mcp}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&mcpv1.DeleteAdapterResponse{}), rpcErr(h.Service.Store.DeleteAdapter(ctx, r.Msg.Mcp, r.Msg.Connector))
}

func (h *Handler) BindMcp(ctx context.Context, r *connect.Request[mcpv1.BindMcpRequest]) (*connect.Response[mcpv1.BindMcpResponse], error) {
	pb := r.Msg.Binding
	if pb == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("binding is required"))
	}
	b := mcp.Binding{OrgID: pb.OrgId, MCP: pb.Mcp, Connector: pb.Connector, Config: pbconv.Map(pb.Config), Secrets: pb.Secrets}
	if _, err := h.check(ctx, r.Header(), "write", authz.Resource{Type: "binding", Org: b.OrgID, Name: b.MCP}); err != nil {
		return nil, err
	}
	if err := h.Service.Bind(ctx, b); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&mcpv1.BindMcpResponse{Binding: bindingToPB(b)}), nil
}

func (h *Handler) ListBindings(ctx context.Context, r *connect.Request[mcpv1.ListBindingsRequest]) (*connect.Response[mcpv1.ListBindingsResponse], error) {
	ctx, err := h.check(ctx, r.Header(), "read", authz.Resource{Type: "binding", Org: orgOf(h.Identity.Context(ctx, r.Header()), r.Msg.OrgId)})
	if err != nil {
		return nil, err
	}
	org := orgOf(ctx, r.Msg.OrgId)
	bs, err := h.Service.Store.Bindings(ctx, org)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &mcpv1.ListBindingsResponse{}
	for _, b := range bs {
		out.Bindings = append(out.Bindings, bindingToPB(b))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) UnbindMcp(ctx context.Context, r *connect.Request[mcpv1.UnbindMcpRequest]) (*connect.Response[mcpv1.UnbindMcpResponse], error) {
	if _, err := h.check(ctx, r.Header(), "write", authz.Resource{Type: "binding", Org: r.Msg.OrgId, Name: r.Msg.Mcp}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&mcpv1.UnbindMcpResponse{}), rpcErr(h.Service.Store.DeleteBinding(ctx, r.Msg.OrgId, r.Msg.Mcp))
}

func (h *Handler) ListTools(ctx context.Context, r *connect.Request[mcpv1.ListToolsRequest]) (*connect.Response[mcpv1.ListToolsResponse], error) {
	ctx, err := h.check(ctx, r.Header(), "read", authz.Resource{Type: "tool", Org: orgOf(h.Identity.Context(ctx, r.Header()), r.Msg.OrgId)})
	if err != nil {
		return nil, err
	}
	tools, mcps, err := h.Service.Tools(ctx, orgOf(ctx, r.Msg.OrgId))
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
	org := orgOf(h.Identity.Context(ctx, r.Header()), r.Msg.OrgId)
	if _, err := h.check(ctx, r.Header(), "call", authz.Resource{Type: "tool", Org: org, Name: r.Msg.Name}); err != nil {
		return nil, err
	}
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
