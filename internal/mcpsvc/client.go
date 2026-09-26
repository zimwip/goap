package mcpsvc

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	mcpv1 "github.com/zimwip/goap/gen/goap/mcp/v1"
	"github.com/zimwip/goap/gen/goap/mcp/v1/mcpv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/authz"
)

// Client is the engine side of the hub: it lists the tools of an organization and
// calls them, forwarding the principal of the context (the initiator of the process).
type Client struct {
	rpc mcpv1connect.McpServiceClient
}

// NewClient returns a hub client.
func NewClient(hc *http.Client, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{rpc: mcpv1connect.NewMcpServiceClient(hc, baseURL, opts...)}
}

func forward[T any](ctx context.Context, msg *T) *connect.Request[T] {
	r := connect.NewRequest(msg)
	if p := authz.From(ctx); !p.Anonymous() {
		r.Header().Set(identity.HeaderSubject, p.Subject)
		r.Header().Set(identity.HeaderOrg, p.Org)
		for i, role := range p.Roles {
			if i == 0 {
				r.Header().Set(identity.HeaderRoles, role)
			} else {
				r.Header().Set(identity.HeaderRoles, r.Header().Get(identity.HeaderRoles)+","+role)
			}
		}
	}
	return r
}

// CallTool calls "<mcp>/<tool>" for an organization. A failure reported by the
// connector is returned as an error.
func (c *Client) CallTool(ctx context.Context, org, name string, args map[string]any) (any, error) {
	r, err := c.rpc.CallTool(ctx, forward(ctx, &mcpv1.CallToolRequest{OrgId: org, Name: name, Arguments: pbconv.Struct(args)}))
	if err != nil {
		return nil, err
	}
	if r.Msg.IsError {
		return nil, errors.New(r.Msg.Error)
	}
	return pbconv.Map(r.Msg.Result), nil
}

// Tools returns the tools available to an organization and the MCPs it binds.
func (c *Client) Tools(ctx context.Context, org string) ([]Tool, []string, error) {
	r, err := c.rpc.ListTools(ctx, forward(ctx, &mcpv1.ListToolsRequest{OrgId: org}))
	if err != nil {
		return nil, nil, err
	}
	out := make([]Tool, len(r.Msg.Tools))
	for i, t := range r.Msg.Tools {
		out[i] = Tool{Name: t.Name, Description: t.Description, InputSchema: pbconv.Map(t.InputSchema)}
	}
	return out, r.Msg.Mcps, nil
}
