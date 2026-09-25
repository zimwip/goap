// Package graphsvc exposes pkg/graph over Connect.
package graphsvc

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/internal/rpcerr"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/metamodel"
)

// Handler implements graphv1connect.GraphServiceHandler.
type Handler struct {
	Graph  *graph.Graph
	Events engine.Publisher
	// Authz gates writes to the metadata layer (NodeType nodes and extends
	// edges, ADR 0012), resource "nodetype", and object creation (CreateObject),
	// resource "object". Nil grants everything. Ordinary domain-node proposals
	// and instanceOf edges are never gated by it.
	Authz authz.Authorizer
	// Identity extracts the caller from request headers (set by the gateway).
	Identity identity.Extractor
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

// refuseDirectWrite rejects writes outside a change to nodes of a type that has
// a lifecycle (ADR 0014): such nodes are modified through changes only.
func (h *Handler) refuseDirectWrite(ctx context.Context, typ string) error {
	controlled, err := h.Graph.LifecycleControlled(ctx, typ)
	if err != nil {
		return rpcerr.ToConnect(err)
	}
	if controlled {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("node type %s has a lifecycle: modify its nodes through a change", typ))
	}
	return nil
}

func (h *Handler) CreateNode(ctx context.Context, r *connect.Request[graphv1.CreateNodeRequest]) (*connect.Response[graphv1.CreateNodeResponse], error) {
	if err := h.refuseDirectWrite(ctx, r.Msg.Type); err != nil {
		return nil, err
	}
	n, err := h.Graph.CreateNode(ctx, graph.NewNode{Namespace: r.Msg.Namespace, Key: r.Msg.Key, Type: r.Msg.Type, Properties: pbconv.Map(r.Msg.Props)})
	return res(&graphv1.CreateNodeResponse{Node: pbconv.NodeToPB(n)}, err)
}

func (h *Handler) CreateObject(ctx context.Context, r *connect.Request[graphv1.CreateObjectRequest]) (*connect.Response[graphv1.CreateObjectResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	who := authz.From(ctx)
	if err := authz.Check(ctx, h.Authz, authz.Request{Subject: who, Action: "create",
		Resource: authz.Resource{Type: "object", Name: r.Msg.NodeType, Namespace: domain.NamespaceOf(r.Msg.Namespace), Org: who.Org, Owner: who.Subject}}); err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	n, b, err := metamodel.CreateObject(ctx, h.Graph, r.Msg.Methodology, r.Msg.Namespace, r.Msg.NodeType, r.Msg.Key, pbconv.Map(r.Msg.Props))
	if err == nil {
		h.publish(ctx, "goap.graph.object.created", map[string]string{"methodology": r.Msg.Methodology, "key": n.Key})
	}
	return res(&graphv1.CreateObjectResponse{Node: pbconv.NodeToPB(n), Baseline: pbconv.BaselineToPB(b)}, err)
}

func (h *Handler) UpdateNode(ctx context.Context, r *connect.Request[graphv1.UpdateNodeRequest]) (*connect.Response[graphv1.UpdateNodeResponse], error) {
	if cur, err := h.Graph.Node(ctx, pbconv.RefFromPB(r.Msg.Base)); err == nil {
		if err := h.refuseDirectWrite(ctx, cur.Type); err != nil {
			return nil, err
		}
	}
	n, err := h.Graph.UpdateNode(ctx, pbconv.RefFromPB(r.Msg.Base), pbconv.Map(r.Msg.Props))
	return res(&graphv1.UpdateNodeResponse{Node: pbconv.NodeToPB(n)}, err)
}

func (h *Handler) GetNode(ctx context.Context, r *connect.Request[graphv1.GetNodeRequest]) (*connect.Response[graphv1.GetNodeResponse], error) {
	ref := pbconv.RefFromPB(r.Msg.Ref)
	if r.Msg.Key != "" {
		n, err := h.Graph.NodeByKey(ctx, r.Msg.Namespace, r.Msg.Key)
		if err != nil {
			return nil, rpcerr.ToConnect(err)
		}
		ref = n.Ref()
	}
	v, err := h.Graph.View(ctx, ref)
	return res(&graphv1.GetNodeResponse{View: pbconv.ViewToPB(v)}, err)
}

func (h *Handler) CreateLink(ctx context.Context, r *connect.Request[graphv1.CreateLinkRequest]) (*connect.Response[graphv1.CreateLinkResponse], error) {
	if src, err := h.Graph.Node(ctx, pbconv.RefFromPB(r.Msg.From)); err == nil {
		if err := h.refuseDirectWrite(ctx, src.Type); err != nil {
			return nil, err
		}
	}
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
	c, err := h.Graph.CreateChange(ctx, graph.NewChange{ParentID: domain.ChangeID(r.Msg.ParentId), OwnerOrg: r.Msg.OwnerOrg, OwnBranch: r.Msg.OwnBranch, Namespace: r.Msg.Namespace, Title: r.Msg.Title, Intent: r.Msg.Intent, Methodology: r.Msg.Methodology,
		BaselineID: domain.BaselineID(r.Msg.BaselineId), Branch: r.Msg.Branch, Data: pbconv.Map(r.Msg.Data)})
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

func (h *Handler) GetChangeNodes(ctx context.Context, r *connect.Request[graphv1.GetChangeNodesRequest]) (*connect.Response[graphv1.GetChangeNodesResponse], error) {
	refs, err := h.Graph.ChangeNodes(ctx, domain.ChangeID(r.Msg.ChangeId))
	out := &graphv1.GetChangeNodesResponse{}
	for _, ref := range refs {
		out.Nodes = append(out.Nodes, pbconv.RefToPB(ref))
	}
	return res(out, err)
}

func (h *Handler) ListNodeChanges(ctx context.Context, r *connect.Request[graphv1.ListNodeChangesRequest]) (*connect.Response[graphv1.ListNodeChangesResponse], error) {
	cs, err := h.Graph.NodeChanges(ctx, domain.NodeID(r.Msg.NodeId))
	out := &graphv1.ListNodeChangesResponse{}
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

// touchesMetadataLayer reports whether items author or delete NodeType nodes
// or extends edges (ADR 0012), the only graph writes gated by the "nodetype"
// resource. remove_link items never carry a Type (metamodel.Sync never
// proposes removing an extends edge), so they are not inspected here.
func (h *Handler) touchesMetadataLayer(ctx context.Context, items []domain.ChangeItem) bool {
	for _, it := range items {
		p := it.Proposal
		if p == nil {
			continue
		}
		switch p.Op {
		case domain.OpCreateNode:
			if p.Node != nil && p.Node.Type == metamodel.TypeNodeType {
				return true
			}
		case domain.OpUpdateNode, domain.OpDeleteNode:
			if p.Node != nil && p.Node.Base != nil {
				if n, err := h.Graph.Node(ctx, *p.Node.Base); err == nil && n.Type == metamodel.TypeNodeType {
					return true
				}
			}
		case domain.OpAddLink:
			if p.Link != nil && p.Link.Type == metamodel.LinkExtends {
				return true
			}
		}
	}
	return false
}

func (h *Handler) AddItems(ctx context.Context, r *connect.Request[graphv1.AddItemsRequest]) (*connect.Response[graphv1.AddItemsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	proposed := pbconv.ItemsFromPB(r.Msg.Items)
	if h.touchesMetadataLayer(ctx, proposed) {
		who := authz.From(ctx)
		if err := authz.Check(ctx, h.Authz, authz.Request{Subject: who, Action: "write", Resource: authz.Resource{Type: "nodetype", Org: who.Org}}); err != nil {
			return nil, rpcerr.ToConnect(err)
		}
	}
	items, err := h.Graph.AddItems(ctx, domain.ChangeID(r.Msg.ChangeId), proposed)
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
	ctx = h.Identity.Context(ctx, r.Header()) // lifecycle transitions are authorized for the caller
	b, err := h.Graph.Apply(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.BaselineName)
	if err == nil {
		if c, cerr := h.Graph.Change(ctx, domain.ChangeID(r.Msg.ChangeId)); cerr == nil {
			c.Items = nil
			h.publish(ctx, "goap.change."+r.Msg.ChangeId+".applied", domain.ChangeEvent{Type: "change.applied", Change: c, Baseline: &b})
		}
	}
	return res(&graphv1.ApplyChangeResponse{Baseline: pbconv.BaselineToPB(b)}, err)
}

func (h *Handler) CreateBranch(ctx context.Context, r *connect.Request[graphv1.CreateBranchRequest]) (*connect.Response[graphv1.CreateBranchResponse], error) {
	b, err := h.Graph.CreateBranch(ctx, graph.NewBranch{Name: r.Msg.Name, From: domain.BaselineID(r.Msg.FromBaseline), Origin: r.Msg.Origin})
	return res(&graphv1.CreateBranchResponse{Branch: pbconv.BranchToPB(b)}, err)
}

func (h *Handler) ListBranches(ctx context.Context, _ *connect.Request[graphv1.ListBranchesRequest]) (*connect.Response[graphv1.ListBranchesResponse], error) {
	bs, err := h.Graph.Branches(ctx)
	out := &graphv1.ListBranchesResponse{}
	for _, b := range bs {
		out.Branches = append(out.Branches, pbconv.BranchToPB(b))
	}
	return res(out, err)
}

func (h *Handler) GetBranch(ctx context.Context, r *connect.Request[graphv1.GetBranchRequest]) (*connect.Response[graphv1.GetBranchResponse], error) {
	b, err := h.Graph.Branch(ctx, r.Msg.Name)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	head, err := h.Graph.BranchHead(ctx, b.Name)
	return res(&graphv1.GetBranchResponse{Branch: pbconv.BranchToPB(b), Head: pbconv.BaselineToPB(head)}, err)
}

func (h *Handler) SetBranchStatus(ctx context.Context, r *connect.Request[graphv1.SetBranchStatusRequest]) (*connect.Response[graphv1.SetBranchStatusResponse], error) {
	return res(&graphv1.SetBranchStatusResponse{}, h.Graph.SetBranchStatus(ctx, r.Msg.Name, r.Msg.Status))
}

func (h *Handler) ListNodeVersions(ctx context.Context, r *connect.Request[graphv1.ListNodeVersionsRequest]) (*connect.Response[graphv1.ListNodeVersionsResponse], error) {
	vs, err := h.Graph.Versions(ctx, domain.NodeID(r.Msg.Id))
	return res(&graphv1.ListNodeVersionsResponse{Versions: pbconv.NodesToPB(vs)}, err)
}

func (h *Handler) PlanMerge(ctx context.Context, r *connect.Request[graphv1.PlanMergeRequest]) (*connect.Response[graphv1.PlanMergeResponse], error) {
	p, err := h.Graph.PlanMerge(ctx, r.Msg.From, r.Msg.Into)
	return res(&graphv1.PlanMergeResponse{Plan: pbconv.MergePlanToPB(p)}, err)
}

func (h *Handler) MergeBranch(ctx context.Context, r *connect.Request[graphv1.MergeBranchRequest]) (*connect.Response[graphv1.MergeBranchResponse], error) {
	req := graph.MergeRequest{From: r.Msg.From, Into: r.Msg.Into, Title: r.Msg.Title, Resolutions: map[domain.NodeID]graph.Resolution{}}
	for id, res := range r.Msg.Resolutions {
		req.Resolutions[domain.NodeID(id)] = graph.Resolution{Props: pbconv.Map(res.GetProps()), Skip: res.GetSkip()}
	}
	out, err := h.Graph.MergeBranch(ctx, req)
	if err == nil {
		c := out.Change
		c.Items = nil
		h.publish(ctx, "goap.change."+string(c.ID)+".applied", domain.ChangeEvent{Type: "change.applied", Change: c, Baseline: &out.Baseline})
	}
	return res(&graphv1.MergeBranchResponse{Change: pbconv.ChangeToPB(out.Change), Baseline: pbconv.BaselineToPB(out.Baseline), Plan: pbconv.MergePlanToPB(out.Plan)}, err)
}

func (h *Handler) MergeChange(ctx context.Context, r *connect.Request[graphv1.MergeChangeRequest]) (*connect.Response[graphv1.MergeChangeResponse], error) {
	resolutions := map[domain.NodeID]graph.Resolution{}
	for id, rs := range r.Msg.Resolutions {
		resolutions[domain.NodeID(id)] = graph.Resolution{Props: pbconv.Map(rs.GetProps()), Skip: rs.GetSkip()}
	}
	c, err := h.Graph.MergeChange(ctx, domain.ChangeID(r.Msg.ChangeId), resolutions)
	if err == nil {
		c.Items = nil
		h.publish(ctx, "goap.change."+string(c.ID)+".applied", domain.ChangeEvent{Type: "change.applied", Change: c})
	}
	return res(&graphv1.MergeChangeResponse{Change: pbconv.ChangeToPB(c)}, err)
}

func (h *Handler) GetSharedNodes(ctx context.Context, r *connect.Request[graphv1.GetSharedNodesRequest]) (*connect.Response[graphv1.GetSharedNodesResponse], error) {
	ns, err := h.Graph.SharedNodes(ctx, domain.ChangeID(r.Msg.ChangeId))
	out := &graphv1.GetSharedNodesResponse{}
	for _, n := range ns {
		sn := &graphv1.SharedNode{Node: pbconv.RefToPB(n.Node), Key: n.Key}
		for _, c := range n.Changes {
			sn.Changes = append(sn.Changes, string(c))
		}
		out.Nodes = append(out.Nodes, sn)
	}
	return res(out, err)
}

func (h *Handler) SplitChange(ctx context.Context, r *connect.Request[graphv1.SplitChangeRequest]) (*connect.Response[graphv1.SplitChangeResponse], error) {
	cs, err := h.Graph.SplitByOwner(ctx, domain.ChangeID(r.Msg.ChangeId))
	out := &graphv1.SplitChangeResponse{}
	for _, c := range cs {
		h.publish(ctx, "goap.change."+string(c.ID)+".created", domain.ChangeEvent{Type: "change.created", Change: c})
		out.Changes = append(out.Changes, pbconv.ChangeToPB(c))
	}
	return res(out, err)
}

func (h *Handler) ListSubChanges(ctx context.Context, r *connect.Request[graphv1.ListSubChangesRequest]) (*connect.Response[graphv1.ListSubChangesResponse], error) {
	cs, err := h.Graph.SubChanges(ctx, domain.ChangeID(r.Msg.ChangeId))
	out := &graphv1.ListSubChangesResponse{}
	for _, c := range cs {
		c.Items = nil
		out.Changes = append(out.Changes, pbconv.ChangeToPB(c))
	}
	return res(out, err)
}

func (h *Handler) GetImpacts(ctx context.Context, r *connect.Request[graphv1.GetImpactsRequest]) (*connect.Response[graphv1.GetImpactsResponse], error) {
	ims, err := h.Graph.Impacts(ctx, domain.ChangeID(r.Msg.ChangeId))
	out := &graphv1.GetImpactsResponse{}
	for _, im := range ims {
		out.Impacts = append(out.Impacts, &graphv1.Impact{Item: string(im.Item), Key: im.Key, Type: im.Type,
			Pre: pbconv.RefPtrToPB(im.Pre), PreState: im.PreState, Post: pbconv.RefPtrToPB(im.Post), PostState: im.PostState})
	}
	return res(out, err)
}

func (h *Handler) GetDivergences(ctx context.Context, r *connect.Request[graphv1.GetDivergencesRequest]) (*connect.Response[graphv1.GetDivergencesResponse], error) {
	ds, err := h.Graph.Divergences(ctx, domain.ChangeID(r.Msg.ChangeId))
	return res(&graphv1.GetDivergencesResponse{Divergences: pbconv.DivergencesToPB(ds)}, err)
}

func (h *Handler) RebaseChange(ctx context.Context, r *connect.Request[graphv1.RebaseChangeRequest]) (*connect.Response[graphv1.RebaseChangeResponse], error) {
	resolutions := map[domain.ItemID]map[string]any{}
	for id, s := range r.Msg.Resolutions {
		resolutions[domain.ItemID(id)] = pbconv.Map(s)
		if resolutions[domain.ItemID(id)] == nil {
			resolutions[domain.ItemID(id)] = map[string]any{}
		}
	}
	out, err := h.Graph.Rebase(ctx, domain.ChangeID(r.Msg.ChangeId), resolutions)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	superseded := map[string]string{}
	for k, v := range out.Superseded {
		superseded[string(k)] = string(v)
	}
	c := out.Change
	h.publish(ctx, "goap.change."+string(c.ID)+".rebased", domain.ChangeEvent{Type: "change.rebased", Change: c})
	return res(&graphv1.RebaseChangeResponse{Change: pbconv.ChangeToPB(c), Superseded: superseded, Divergences: pbconv.DivergencesToPB(out.Divergences)}, nil)
}

func (h *Handler) RecordExecutions(ctx context.Context, r *connect.Request[graphv1.RecordExecutionsRequest]) (*connect.Response[graphv1.RecordExecutionsResponse], error) {
	return res(&graphv1.RecordExecutionsResponse{}, h.Graph.Record(ctx, pbconv.ExecutionsFromPB(r.Msg.Records)))
}

func (h *Handler) ListExecutions(ctx context.Context, r *connect.Request[graphv1.ListExecutionsRequest]) (*connect.Response[graphv1.ListExecutionsResponse], error) {
	rs, err := h.Graph.Journal(ctx, domain.ExecutionFilter{ChangeID: domain.ChangeID(r.Msg.ChangeId), ProcessIDs: r.Msg.ProcessIds})
	return res(&graphv1.ListExecutionsResponse{Records: pbconv.ExecutionsToPB(rs)}, err)
}
