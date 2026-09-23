package modelgw

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	modelv1 "github.com/zimwip/goap/gen/goap/model/v1"
	"github.com/zimwip/goap/gen/goap/model/v1/modelv1connect"
	"github.com/zimwip/goap/pkg/llm"
)

// Handler implements modelv1connect.ModelServiceHandler.
type Handler struct {
	Router *Router
}

var _ modelv1connect.ModelServiceHandler = (*Handler)(nil)

func (h *Handler) Complete(ctx context.Context, r *connect.Request[modelv1.CompleteRequest]) (*connect.Response[modelv1.CompleteResponse], error) {
	req := llm.Request{Model: r.Msg.Model, System: r.Msg.System, MaxTokens: int(r.Msg.MaxTokens), JSON: r.Msg.Json}
	for _, m := range r.Msg.Messages {
		req.Messages = append(req.Messages, llm.Message{Role: m.Role, Content: m.Content})
	}
	if _, _, err := h.Router.Resolve(req.Model); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	resp, err := h.Router.Complete(ctx, req)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&modelv1.CompleteResponse{Text: resp.Text, Provider: resp.Provider, Model: resp.Model,
		Usage: &modelv1.Usage{InputTokens: int32(resp.Usage.InputTokens), OutputTokens: int32(resp.Usage.OutputTokens)}}), nil
}

func (h *Handler) ListModels(context.Context, *connect.Request[modelv1.ListModelsRequest]) (*connect.Response[modelv1.ListModelsResponse], error) {
	names, targets, providers := h.Router.Aliases()
	out := &modelv1.ListModelsResponse{Providers: providers}
	for _, n := range names {
		out.Aliases = append(out.Aliases, &modelv1.ModelAlias{Alias: n, Provider: targets[n].Provider, Model: targets[n].Model})
	}
	return connect.NewResponse(out), nil
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
