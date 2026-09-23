// Package graphsvc exposes pkg/graph over Connect.
package graphsvc

import (
	"context"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/internal/rpcerr"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
)

// Handler implements graphv1connect.GraphServiceHandler.
type Handler struct {
	Graph  *graph.Graph
	Events engine.Publisher
}

var _ graphv1connect.GraphServiceHandler = (*Handler)(nil)

func res[T any](m *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(m), nil
}

func (h *Handler) publish(ctx context.Context, subject string, v any) {
	if h.Events != nil {
		_ = h.Events.Publish(ctx, subject, v)
	}
}

func (h *Handler) CreateNode(ctx context.Context, r *connect.Request[graphv1.CreateNodeRequest]) (*connect.Response[graphv1.CreateNodeResponse], error) {
	n, err := h.Graph.CreateNode(ctx, graph.NewNode{Key: r.Msg.Key, Type: r.Msg.Type, Properties: pbconv.Map(r.Msg.Props)})
	return res(&graphv1.CreateNodeResponse{Node: pbconv.NodeToPB(n)}, err)
}

func (h *Handler) UpdateNode(ctx context.Context, r *connect.Request[graphv1.UpdateNodeRequest]) (*connect.Response[graphv1.UpdateNodeResponse], error) {
	n, err := h.Graph.UpdateNode(ctx, pbconv.RefFromPB(r.Msg.Base), pbconv.Map(r.Msg.Props))
	return res(&graphv1.UpdateNodeResponse{Node: pbconv.NodeToPB(n)}, err)
}

func (h *Handler) GetNode(ctx context.Context, r *connect.Request[graphv1.GetNodeRequest]) (*connect.Response[graphv1.GetNodeResponse], error) {
	ref := pbconv.RefFromPB(r.Msg.Ref)
	if r.Msg.Key != "" {
		n, err := h.Graph.NodeByKey(ctx, r.Msg.Key)
		if err != nil {
			return nil, rpcerr.ToConnect(err)
		}
		ref = n.Ref()
	}
	v, err := h.Graph.View(ctx, ref)
	return res(&graphv1.GetNodeResponse{View: pbconv.ViewToPB(v)}, err)
}

func (h *Handler) CreateLink(ctx context.Context, r *connect.Request[graphv1.CreateLinkRequest]) (*connect.Response[graphv1.CreateLinkResponse], error) {
	l, err := h.Graph.Link(ctx, r.Msg.Type, pbconv.RefFromPB(r.Msg.From), pbconv.RefFromPB(r.Msg.To), pbconv.Map(r.Msg.Props))
	return res(&graphv1.CreateLinkResponse{Link: pbconv.LinkToPB(l)}, err)
}

func (h *Handler) CreateBaseline(ctx context.Context, r *connect.Request[graphv1.CreateBaselineRequest]) (*connect.Response[graphv1.CreateBaselineResponse], error) {
	var b domain.Baseline
	var err error
	if r.Msg.AllLatest {
		b, err = h.Graph.CreateBaselineFromLatest(ctx, r.Msg.Name)
	} else {
		refs := make([]domain.NodeRef, len(r.Msg.Nodes))
		for i, n := range r.Msg.Nodes {
			refs[i] = pbconv.RefFromPB(n)
		}
		b, err = h.Graph.CreateBaseline(ctx, r.Msg.Name, refs)
	}
	return res(&graphv1.CreateBaselineResponse{Baseline: pbconv.BaselineToPB(b)}, err)
}

func (h *Handler) ListBaselines(ctx context.Context, _ *connect.Request[graphv1.ListBaselinesRequest]) (*connect.Response[graphv1.ListBaselinesResponse], error) {
	bs, err := h.Graph.Baselines(ctx)
	out := &graphv1.ListBaselinesResponse{}
	for _, b := range bs {
		out.Baselines = append(out.Baselines, pbconv.BaselineToPB(b))
	}
	return res(out, err)
}

func (h *Handler) GetBaselineGraph(ctx context.Context, r *connect.Request[graphv1.GetBaselineGraphRequest]) (*connect.Response[graphv1.GetBaselineGraphResponse], error) {
	id := domain.BaselineID(r.Msg.Id)
	b, err := h.Graph.Baseline(ctx, id)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	nodes, links, err := h.Graph.BaselineGraph(ctx, id)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	suspects, err := h.Graph.SuspectLinks(ctx, id)
	return res(&graphv1.GetBaselineGraphResponse{Baseline: pbconv.BaselineToPB(b), Nodes: pbconv.NodesToPB(nodes),
		Links: pbconv.LinksToPB(links), SuspectLinks: pbconv.LinksToPB(suspects)}, err)
}

func (h *Handler) CreateChange(ctx context.Context, r *connect.Request[graphv1.CreateChangeRequest]) (*connect.Response[graphv1.CreateChangeResponse], error) {
	c, err := h.Graph.CreateChange(ctx, graph.NewChange{Title: r.Msg.Title, Intent: r.Msg.Intent, Methodology: r.Msg.Methodology,
		BaselineID: domain.BaselineID(r.Msg.BaselineId), Data: pbconv.Map(r.Msg.Data)})
	if err == nil {
		h.publish(ctx, "goap.change."+string(c.ID)+".created", domain.ChangeEvent{Type: "change.created", Change: c})
	}
	return res(&graphv1.CreateChangeResponse{Change: pbconv.ChangeToPB(c)}, err)
}

func (h *Handler) GetChange(ctx context.Context, r *connect.Request[graphv1.GetChangeRequest]) (*connect.Response[graphv1.GetChangeResponse], error) {
	c, err := h.Graph.Change(ctx, domain.ChangeID(r.Msg.Id))
	return res(&graphv1.GetChangeResponse{Change: pbconv.ChangeToPB(c)}, err)
}

func (h *Handler) ListChanges(ctx context.Context, _ *connect.Request[graphv1.ListChangesRequest]) (*connect.Response[graphv1.ListChangesResponse], error) {
	cs, err := h.Graph.Changes(ctx)
	out := &graphv1.ListChangesResponse{}
	for _, c := range cs {
		out.Changes = append(out.Changes, pbconv.ChangeToPB(c))
	}
	return res(out, err)
}

func (h *Handler) UpdateChange(ctx context.Context, r *connect.Request[graphv1.UpdateChangeRequest]) (*connect.Response[graphv1.UpdateChangeResponse], error) {
	p := graph.ChangePatch{Goal: r.Msg.Goal, Data: pbconv.Map(r.Msg.Data)}
	if r.Msg.Status != nil {
		st := domain.ChangeStatus(*r.Msg.Status)
		p.Status = &st
	}
	c, err := h.Graph.UpdateChange(ctx, domain.ChangeID(r.Msg.Id), p)
	return res(&graphv1.UpdateChangeResponse{Change: pbconv.ChangeToPB(c)}, err)
}

func (h *Handler) AddItems(ctx context.Context, r *connect.Request[graphv1.AddItemsRequest]) (*connect.Response[graphv1.AddItemsResponse], error) {
	items, err := h.Graph.AddItems(ctx, domain.ChangeID(r.Msg.ChangeId), pbconv.ItemsFromPB(r.Msg.Items))
	if err == nil {
		if c, cerr := h.Graph.Change(ctx, domain.ChangeID(r.Msg.ChangeId)); cerr == nil {
			c.Items = nil
			h.publish(ctx, "goap.change."+r.Msg.ChangeId+".item_added", domain.ChangeEvent{Type: "change.item_added", Change: c, Items: items})
		}
	}
	return res(&graphv1.AddItemsResponse{Items: pbconv.ItemsToPB(items)}, err)
}

func (h *Handler) GetBlackboard(ctx context.Context, r *connect.Request[graphv1.GetBlackboardRequest]) (*connect.Response[graphv1.GetBlackboardResponse], error) {
	bb, err := h.Graph.Blackboard(ctx, domain.ChangeID(r.Msg.ChangeId))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	out := &graphv1.GetBlackboardResponse{Change: pbconv.ChangeToPB(bb.Change)}
	for _, v := range bb.Nodes {
		out.Nodes = append(out.Nodes, pbconv.ViewToPB(v))
	}
	for _, n := range bb.Neighbors {
		out.Neighbors = append(out.Neighbors, pbconv.NodeToPB(n))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) ApplyChange(ctx context.Context, r *connect.Request[graphv1.ApplyChangeRequest]) (*connect.Response[graphv1.ApplyChangeResponse], error) {
	b, err := h.Graph.Apply(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.BaselineName)
	if err == nil {
		if c, cerr := h.Graph.Change(ctx, domain.ChangeID(r.Msg.ChangeId)); cerr == nil {
			c.Items = nil
			h.publish(ctx, "goap.change."+r.Msg.ChangeId+".applied", domain.ChangeEvent{Type: "change.applied", Change: c, Baseline: &b})
		}
	}
	return res(&graphv1.ApplyChangeResponse{Baseline: pbconv.BaselineToPB(b)}, err)
}
