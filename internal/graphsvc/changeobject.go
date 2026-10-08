package graphsvc

import (
	"context"
	"slices"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/internal/rpcerr"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// ResourceChangeObject is the ABAC resource of a change object (ADR 0098): change-object:write on the project of the
// change, Name the change object type.
const ResourceChangeObject = "change-object"

// checkObjects is the ABAC check of writing change objects of these types on a change: change-object:write, once per
// type.
func (h *Handler) checkObjects(ctx context.Context, c domain.Change, writes []domain.ObjectWrite) error {
	who := authz.From(ctx)
	var seen []string
	for _, w := range writes {
		if slices.Contains(seen, w.Type) {
			continue
		}
		seen = append(seen, w.Type)
		if err := authz.Check(ctx, h.Authz, authz.Request{Subject: who, Action: "write",
			Resource: authz.Resource{Type: ResourceChangeObject, Name: w.Type, Namespace: c.Namespace, Org: c.OwnerOrg, Owner: who.Subject, ProjectID: c.ProjectID}}); err != nil {
			return rpcerr.ToConnect(err)
		}
	}
	return nil
}

// PutChangeObjects writes change objects on a change (ADR 0098), authorized per type.
func (h *Handler) PutChangeObjects(ctx context.Context, r *connect.Request[graphv1.PutChangeObjectsRequest]) (*connect.Response[graphv1.PutChangeObjectsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	c, err := h.Graph.Change(ctx, domain.ChangeID(r.Msg.ChangeId))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	writes := pbconv.ObjectWritesFromPB(r.Msg.Objects)
	if err := h.checkObjects(ctx, c, writes); err != nil {
		return nil, err
	}
	out, err := h.Graph.PutObjects(ctx, c.ID, writes)
	return res(&graphv1.PutChangeObjectsResponse{Objects: pbconv.ChangeObjectsToPB(out)}, err)
}

// ListChangeObjects reads the change objects of a change.
func (h *Handler) ListChangeObjects(ctx context.Context, r *connect.Request[graphv1.ListChangeObjectsRequest]) (*connect.Response[graphv1.ListChangeObjectsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	f := domain.ObjectFilter{Types: m.Types, KeyPrefix: m.KeyPrefix, Labels: m.Labels, AtSeq: m.AtSeq}
	if m.FilterWorkspaces {
		f.Workspaces = []string{}
		for _, w := range m.Workspaces {
			if w == domain.MainFlow {
				w = ""
			}
			f.Workspaces = append(f.Workspaces, w)
		}
	}
	out, err := h.Graph.Objects(ctx, domain.ChangeID(m.ChangeId), f)
	return res(&graphv1.ListChangeObjectsResponse{Objects: pbconv.ChangeObjectsToPB(out)}, err)
}

// SubmitBatch writes impact operations, items and change objects on a change in one transaction (ADR 0098); each part
// is authorized as its own RPC is.
func (h *Handler) SubmitBatch(ctx context.Context, r *connect.Request[graphv1.SubmitBatchRequest]) (*connect.Response[graphv1.SubmitBatchResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	c, err := h.Graph.Change(ctx, domain.ChangeID(m.ChangeId))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	who := authz.From(ctx)
	var b graph.Batch
	for _, in := range m.Creates {
		if err := h.checkCreate(ctx, c, in.Type); err != nil {
			return nil, err
		}
		b.Creates = append(b.Creates, graph.NodeCreate{Key: in.Key, Type: in.Type, Properties: pbconv.Map(in.Props), Owner: in.Owner, Rationale: in.Rationale,
			Links: linkWrites(in.Links), Flow: in.Flow, Execution: in.Execution, ProducedBy: who.Subject})
	}
	for _, in := range m.Checkouts {
		if err := h.gateImpact(ctx, m.ChangeId, in.ChangeImpactId, in.NodeId); err != nil {
			return nil, err
		}
		b.Checkouts = append(b.Checkouts, graph.NodeCheckout{Impact: domain.ChangeImpactID(in.ChangeImpactId), Node: domain.NodeID(in.NodeId),
			Rationale: in.Rationale, Flow: in.Flow, Execution: in.Execution, ProducedBy: who.Subject})
	}
	for _, in := range m.Updates {
		if err := h.gateImpact(ctx, m.ChangeId, in.ChangeImpactId, ""); err != nil {
			return nil, err
		}
		b.Updates = append(b.Updates, graph.ImpactUpdate{Impact: domain.ChangeImpactID(in.ChangeImpactId),
			NodeUpdate: graph.NodeUpdate{Properties: pbconv.Map(in.Props), Owner: in.Owner, Flow: in.Flow, Execution: in.Execution}})
	}
	for _, in := range m.Links {
		if err := h.gateImpact(ctx, m.ChangeId, in.ChangeImpactId, ""); err != nil {
			return nil, err
		}
		b.Links = append(b.Links, graph.ImpactLink{Impact: domain.ChangeImpactID(in.ChangeImpactId),
			Link: graph.LinkWrite{Type: in.Type, To: pbconv.RefFromPB(in.To), Properties: pbconv.Map(in.Props)}, Flow: in.Flow, Execution: in.Execution})
	}
	b.Items = pbconv.ItemsFromPB(m.Items)
	b.Objects = pbconv.ObjectWritesFromPB(m.Objects)
	if err := h.checkObjects(ctx, c, b.Objects); err != nil {
		return nil, err
	}
	out, err := h.Graph.Submit(ctx, c.ID, b)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	resp := &graphv1.SubmitBatchResponse{Links: pbconv.LinksToPB(out.Links), Items: pbconv.ItemsToPB(out.Items), Objects: pbconv.ChangeObjectsToPB(out.Objects)}
	for _, cn := range out.Impacts {
		resp.Impacts = append(resp.Impacts, pbconv.ChangeImpactToPB(cn))
	}
	return connect.NewResponse(resp), nil
}
