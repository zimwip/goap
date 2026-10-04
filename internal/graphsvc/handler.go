// Package graphsvc exposes pkg/graph over Connect.
package graphsvc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/internal/rpcerr"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
	"google.golang.org/protobuf/types/known/structpb"
)

// Handler implements graphv1connect.GraphServiceHandler.
type Handler struct {
	Graph  *graph.Graph
	Events engine.Publisher
	// Authz gates object creation (CreateObject, resource "object") and direct-write baseline creation
	// (CreateBaseline, resource "baseline", always refused for the organisation namespace). Nil grants
	// everything. Ordinary change impacts are not gated by it.
	Authz authz.Authorizer
	// Floor gates changes to User and Policy nodes (resource "policy", action "write"). It must not depend
	// on the policies themselves, so that no policy can lock the administrators out; nil falls back to Authz.
	Floor authz.Authorizer
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
	if isAccessType(typ) {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("node type %s is access control: modify its nodes through a change", typ))
	}
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
		Resource: authz.Resource{Type: "object", Name: r.Msg.NodeType, Namespace: domain.NamespaceOf(r.Msg.Namespace), Org: who.Org, Owner: who.Subject, ProjectID: who.Project}}); err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	n, b, err := h.Graph.CreateObject(ctx, r.Msg.Methodology, r.Msg.Namespace, r.Msg.NodeType, r.Msg.Key, pbconv.Map(r.Msg.Props))
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
	l, err := h.Graph.Link(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Type, pbconv.RefFromPB(r.Msg.From), pbconv.RefFromPB(r.Msg.To), pbconv.Map(r.Msg.Props))
	return res(&graphv1.CreateLinkResponse{Link: pbconv.LinkToPB(l)}, err)
}

func (h *Handler) TagChange(ctx context.Context, r *connect.Request[graphv1.TagChangeRequest]) (*connect.Response[graphv1.TagChangeResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	c, err := h.Graph.Change(ctx, domain.ChangeID(r.Msg.ChangeId))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	who := authz.From(ctx)
	if err := authz.Check(ctx, h.Authz, authz.Request{Subject: who, Action: "create",
		Resource: authz.Resource{Type: "tag", Namespace: c.Namespace, Org: who.Org, Owner: who.Subject, ProjectID: c.ProjectID}}); err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	t, err := h.Graph.TagChange(ctx, c.ID, r.Msg.Name, who.Subject)
	return res(&graphv1.TagChangeResponse{Tag: pbconv.TagToPB(t)}, err)
}

func (h *Handler) ListTags(ctx context.Context, r *connect.Request[graphv1.ListTagsRequest]) (*connect.Response[graphv1.ListTagsResponse], error) {
	ts, err := h.Graph.Tags(ctx, domain.TagFilter{Namespace: r.Msg.Namespace, Name: r.Msg.Name, Change: domain.ChangeID(r.Msg.ChangeId)})
	out := &graphv1.ListTagsResponse{}
	for _, t := range ts {
		out.Tags = append(out.Tags, pbconv.TagToPB(t))
	}
	return res(out, err)
}

func (h *Handler) DeleteTag(ctx context.Context, r *connect.Request[graphv1.DeleteTagRequest]) (*connect.Response[graphv1.DeleteTagResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	who := authz.From(ctx)
	if err := authz.Check(ctx, h.Authz, authz.Request{Subject: who, Action: "delete",
		Resource: authz.Resource{Type: "tag", Org: who.Org, Owner: who.Subject, ProjectID: who.Project}}); err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return res(&graphv1.DeleteTagResponse{}, h.Graph.DeleteTag(ctx, domain.TagID(r.Msg.Id)))
}

func (h *Handler) ListBaselines(ctx context.Context, r *connect.Request[graphv1.ListBaselinesRequest]) (*connect.Response[graphv1.ListBaselinesResponse], error) {
	bs, err := h.Graph.Baselines(ctx, r.Msg.Namespace)
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

// Caller names the principal of a request, recorded on the events of the change impacts (graph.Graph.Caller,
// ADR 0029).
func Caller(ctx context.Context) string { return authz.From(ctx).Subject }

func (h *Handler) ListChangeLog(ctx context.Context, r *connect.Request[graphv1.ListChangeLogRequest]) (*connect.Response[graphv1.ListChangeLogResponse], error) {
	f := domain.LogFilter{Change: domain.ChangeID(r.Msg.ChangeId), Types: r.Msg.Types, Processes: r.Msg.ProcessIds, Execution: r.Msg.Execution,
		AfterSeq: r.Msg.AfterSeq, Limit: int(r.Msg.Limit)}
	for _, fl := range r.Msg.Flows {
		if fl == "main" {
			fl = ""
		}
		f.Flows = append(f.Flows, fl)
	}
	entries, counts, err := h.Graph.ChangeLog(ctx, f)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	out := &graphv1.ListChangeLogResponse{Counts: map[string]int32{}}
	for t, n := range counts {
		out.Counts[t] = int32(n)
	}
	for _, e := range entries {
		out.Entries = append(out.Entries, &graphv1.LogEntry{Seq: e.Seq, Id: e.ID, ChangeId: string(e.Change), Type: e.Type, Flow: e.Flow, ProcessId: e.Process,
			Execution: e.Execution, Subject: e.Subject, By: e.By, At: pbconv.Time(e.At), Payload: string(e.Payload)})
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) ListChangeEvents(ctx context.Context, r *connect.Request[graphv1.ListChangeEventsRequest]) (*connect.Response[graphv1.ListChangeEventsResponse], error) {
	evs, err := h.Graph.ChangeEvents(ctx, domain.ChangeID(r.Msg.ChangeId))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	out := &graphv1.ListChangeEventsResponse{}
	for _, e := range evs {
		out.Events = append(out.Events, pbconv.ImpactEventToPB(e))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) ListBaselineNodes(ctx context.Context, r *connect.Request[graphv1.ListBaselineNodesRequest]) (*connect.Response[graphv1.ListBaselineNodesResponse], error) {
	id := domain.BaselineID(r.Msg.BaselineId)
	b, err := h.Graph.Baseline(ctx, id)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	page, err := h.Graph.BaselineNodes(ctx, id, graph.NodeQuery{Type: r.Msg.Type, Text: r.Msg.Query,
		IncludeDeleted: r.Msg.IncludeDeleted, Offset: int(r.Msg.Offset), Limit: int(r.Msg.Limit)})
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	out := &graphv1.ListBaselineNodesResponse{Baseline: pbconv.BaselineToPB(b), Nodes: pbconv.NodesToPB(page.Nodes), Total: int32(page.Total)}
	for _, t := range page.Types {
		out.Types = append(out.Types, &graphv1.TypeCount{Type: t.Type, Count: int32(t.Count)})
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) ListBaselineLinks(ctx context.Context, r *connect.Request[graphv1.ListBaselineLinksRequest]) (*connect.Response[graphv1.ListBaselineLinksResponse], error) {
	id := domain.BaselineID(r.Msg.BaselineId)
	b, err := h.Graph.Baseline(ctx, id)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	page, err := h.Graph.BaselineLinks(ctx, id, graph.LinkQuery{Type: r.Msg.Type, Text: r.Msg.Query,
		Offset: int(r.Msg.Offset), Limit: int(r.Msg.Limit)})
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	out := &graphv1.ListBaselineLinksResponse{Baseline: pbconv.BaselineToPB(b), Links: pbconv.LinksToPB(page.Links), Total: int32(page.Total)}
	for _, t := range page.Types {
		out.Types = append(out.Types, &graphv1.TypeCount{Type: t.Type, Count: int32(t.Count)})
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) GetNodeNeighbourhood(ctx context.Context, r *connect.Request[graphv1.GetNodeNeighbourhoodRequest]) (*connect.Response[graphv1.GetNodeNeighbourhoodResponse], error) {
	nb, err := h.Graph.NodeNeighbourhood(ctx, domain.BaselineID(r.Msg.BaselineId), domain.NodeID(r.Msg.NodeId))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	out := &graphv1.GetNodeNeighbourhoodResponse{Node: pbconv.NodeToPB(nb.Node), Nodes: pbconv.NodesToPB(nb.Nodes), Links: pbconv.LinksToPB(nb.Links)}
	for _, l := range nb.Suspect {
		out.SuspectLinkIds = append(out.SuspectLinkIds, string(l))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) GetStructures(ctx context.Context, _ *connect.Request[graphv1.GetStructuresRequest]) (*connect.Response[graphv1.GetStructuresResponse], error) {
	st, err := h.Graph.Structures(ctx)
	return res(pbconv.StructuresToPB(st), err)
}

func (h *Handler) ListNamespaces(ctx context.Context, _ *connect.Request[graphv1.ListNamespacesRequest]) (*connect.Response[graphv1.ListNamespacesResponse], error) {
	ns, err := h.Graph.Namespaces(ctx)
	return res(&graphv1.ListNamespacesResponse{Namespaces: ns}, err)
}

func (h *Handler) CreateChange(ctx context.Context, r *connect.Request[graphv1.CreateChangeRequest]) (*connect.Response[graphv1.CreateChangeResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	owner, err := h.resolveOwner(ctx, r.Msg.OwnerOrg)
	if err != nil {
		return nil, err
	}
	if p := r.Msg.ParentId; p != "" {
		// a personal change is never split, and nothing is split off a personal change
		if parent, perr := h.Graph.Change(ctx, domain.ChangeID(p)); perr == nil && (parent.Personal() || domain.IsPersonalUnit(owner)) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("a personal change has no sub-changes"))
		}
	}
	projectID := r.Msg.ProjectId
	if projectID == "" {
		projectID = authz.From(ctx).Project
	}
	c, err := h.Graph.CreateChange(ctx, graph.NewChange{ParentID: domain.ChangeID(r.Msg.ParentId), OwnerOrg: owner, OwnBranch: r.Msg.OwnBranch, Namespace: r.Msg.Namespace, Title: r.Msg.Title, Intent: r.Msg.Intent, Methodology: r.Msg.Methodology,
		BaselineID: domain.BaselineID(r.Msg.BaselineId), Branch: r.Msg.Branch, Data: pbconv.Map(r.Msg.Data), ProjectID: projectID, Administrative: r.Msg.Administrative})
	if err == nil {
		h.publish(ctx, "goap.change."+string(c.ID)+".created", domain.ChangeEvent{Type: "change.created", Change: c})
	}
	return res(&graphv1.CreateChangeResponse{Change: pbconv.ChangeToPB(c)}, err)
}

func (h *Handler) GetChange(ctx context.Context, r *connect.Request[graphv1.GetChangeRequest]) (*connect.Response[graphv1.GetChangeResponse], error) {
	c, err := h.Graph.Change(ctx, domain.ChangeID(r.Msg.Id))
	return res(&graphv1.GetChangeResponse{Change: pbconv.ChangeToPB(c)}, err)
}

func (h *Handler) ListChanges(ctx context.Context, r *connect.Request[graphv1.ListChangesRequest]) (*connect.Response[graphv1.ListChangesResponse], error) {
	f := graph.ChangesFilter{Namespace: r.Msg.Namespace, OwnerOrg: r.Msg.OwnerOrg}
	for _, s := range r.Msg.Status {
		f.Status = append(f.Status, domain.ChangeStatus(s))
	}
	ctx = h.Identity.Context(ctx, r.Header())
	cs, err := h.Graph.ListChanges(ctx, f)
	out := &graphv1.ListChangesResponse{}
	for _, c := range cs {
		if !visibleTo(ctx, c) {
			continue
		}
		out.Changes = append(out.Changes, pbconv.ChangeToPB(c))
	}
	return res(out, err)
}

func (h *Handler) GetChangeImpacts(ctx context.Context, r *connect.Request[graphv1.GetChangeImpactsRequest]) (*connect.Response[graphv1.GetChangeImpactsResponse], error) {
	refs, err := h.Graph.ChangeImpacts(ctx, domain.ChangeID(r.Msg.ChangeId))
	out := &graphv1.GetChangeImpactsResponse{}
	for _, ref := range refs {
		out.Nodes = append(out.Nodes, pbconv.RefToPB(ref))
	}
	return res(out, err)
}

func (h *Handler) ListNodeChanges(ctx context.Context, r *connect.Request[graphv1.ListNodeChangesRequest]) (*connect.Response[graphv1.ListNodeChangesResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	cs, err := h.Graph.NodeChanges(ctx, domain.NodeID(r.Msg.NodeId))
	out := &graphv1.ListNodeChangesResponse{}
	for _, c := range cs {
		if !visibleTo(ctx, c) {
			continue
		}
		out.Changes = append(out.Changes, pbconv.ChangeToPB(c))
	}
	return res(out, err)
}

func (h *Handler) UpdateChange(ctx context.Context, r *connect.Request[graphv1.UpdateChangeRequest]) (*connect.Response[graphv1.UpdateChangeResponse], error) {
	p := graph.ChangePatch{Title: r.Msg.Title, Intent: r.Msg.Intent, Goal: r.Msg.Goal, Data: pbconv.Map(r.Msg.Data)}
	if r.Msg.Status != nil {
		st := domain.ChangeStatus(*r.Msg.Status)
		p.Status = &st
	}
	c, err := h.Graph.UpdateChange(ctx, domain.ChangeID(r.Msg.Id), p)
	return res(&graphv1.UpdateChangeResponse{Change: pbconv.ChangeToPB(c)}, err)
}

// isAccessType reports whether a node type governs access itself — who may do what (User, Policy), the
// organisation and project structure (OrgUnit, ProjectUnit), who holds what role or platform role where
// (Assignment, ADR 0043/0046/0047), and what an organisational unit may reach (Adapter, ADR 0028). None of
// these are domain data a project's own members write through the ordinary change/object/node rules
// (authz.DefaultPolicies): administered by administrators only, gated here (security fix: CommitEdits and
// AddChangeImpacts/WriteChangeImpact otherwise let any authenticated subject write them unchecked, which,
// since Assignment can grant the "admin" platform role (ADR 0047), amounted to unauthenticated privilege
// escalation to full administrator).
func isAccessType(typ string) bool {
	switch typ {
	case access.NodeTypeUser, access.NodeTypePolicy, access.NodeTypeProjectUnit, access.NodeTypeAssignment, mcp.NodeTypeOrgUnit, mcp.NodeTypeAdapter:
		return true
	default:
		return false
	}
}

func (h *Handler) AddItems(ctx context.Context, r *connect.Request[graphv1.AddItemsRequest]) (*connect.Response[graphv1.AddItemsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	proposed := pbconv.ItemsFromPB(r.Msg.Items)
	items, err := h.Graph.AddItems(ctx, domain.ChangeID(r.Msg.ChangeId), proposed)
	if err == nil {
		if c, cerr := h.Graph.Change(ctx, domain.ChangeID(r.Msg.ChangeId)); cerr == nil {
			c.Items = nil
			h.publish(ctx, "goap.change."+r.Msg.ChangeId+".item_added", domain.ChangeEvent{Type: "change.item_added", Change: c, Items: items})
		}
	}
	return res(&graphv1.AddItemsResponse{Items: pbconv.ItemsToPB(items)}, err)
}

// gateAccess applies to a change impact the gate of the access nodes (isAccessType): they need the access
// permission ("policy":"write"), checked against the floor (ADR 0020) — administrator-only, same as every
// other organisation-namespace write (authz.DefaultPolicies has no rule for "node"/"object" write on it).
func (h *Handler) gateAccess(ctx context.Context, typ string) error {
	who := authz.From(ctx)
	if isAccessType(typ) {
		gate := h.Floor
		if gate == nil {
			gate = h.Authz
		}
		if err := authz.Check(ctx, gate, authz.Request{Subject: who, Action: "write", Resource: authz.Resource{Type: access.ResourcePolicy, Org: who.Org}}); err != nil {
			return rpcerr.ToConnect(err)
		}
	}
	return nil
}

func (h *Handler) AddChangeImpacts(ctx context.Context, r *connect.Request[graphv1.AddChangeImpactsRequest]) (*connect.Response[graphv1.AddChangeImpactsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	nodes := pbconv.ChangeImpactsFromPB(r.Msg.Nodes)
	for _, cn := range nodes {
		typ := cn.Type
		if cn.Pre != nil {
			if n, err := h.Graph.Node(ctx, *cn.Pre); err == nil {
				typ = n.Type
			}
		}
		if err := h.gateAccess(ctx, typ); err != nil {
			return nil, err
		}
	}
	out, err := h.Graph.AddNodes(ctx, domain.ChangeID(r.Msg.ChangeId), nodes)
	return res(&graphv1.AddChangeImpactsResponse{Nodes: pbconv.ChangeImpactsToPB(out)}, err)
}

func (h *Handler) CommitEdits(ctx context.Context, r *connect.Request[graphv1.CommitEditsRequest]) (*connect.Response[graphv1.CommitEditsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	edits := pbconv.EditsFromPB(r.Msg.Edits)
	for _, e := range edits {
		typ := e.Type
		if e.Pre != nil {
			if n, err := h.Graph.Node(ctx, *e.Pre); err == nil {
				typ = n.Type
			}
		}
		if err := h.gateAccess(ctx, typ); err != nil {
			return nil, err
		}
	}
	by := authz.From(ctx).Subject
	if by == "" {
		by = "graphsvc"
	}
	owner, err := h.resolveOwner(ctx, r.Msg.OwnerOrg)
	if err != nil {
		return nil, err
	}
	projectID := r.Msg.ProjectId
	if projectID == "" {
		projectID = authz.From(ctx).Project
	}
	out, err := h.Graph.Commit(ctx, graph.Commit{Namespace: r.Msg.Namespace, Title: r.Msg.Title, Intent: r.Msg.Intent, Methodology: r.Msg.Methodology,
		Data: pbconv.Map(r.Msg.Data), Baseline: domain.BaselineID(r.Msg.BaselineId), By: by, BaselineName: r.Msg.BaselineName, Edits: edits,
		OwnerOrg: owner, ProjectID: projectID})
	if err == nil {
		if c, cerr := h.Graph.Change(ctx, out.Change); cerr == nil {
			c.Items, c.Nodes = nil, nil
			h.publish(ctx, "goap.change."+string(out.Change)+".applied", domain.ChangeEvent{Type: "change.applied", Change: c, Baseline: &out.Baseline})
		}
	}
	return res(&graphv1.CommitEditsResponse{ChangeId: string(out.Change), Baseline: pbconv.BaselineToPB(out.Baseline)}, err)
}

func (h *Handler) WriteChangeImpact(ctx context.Context, r *connect.Request[graphv1.WriteChangeImpactRequest]) (*connect.Response[graphv1.WriteChangeImpactResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	list, err := h.Graph.ListChangeImpacts(ctx, domain.ChangeID(r.Msg.ChangeId))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	var typ string
	for _, cn := range list {
		if string(cn.ID) == r.Msg.ChangeImpactId {
			typ = cn.Type
		}
	}
	if err := h.gateAccess(ctx, typ); err != nil {
		return nil, err
	}
	w := graph.NodeWrite{Properties: pbconv.Map(r.Msg.Props), State: r.Msg.State, Retire: r.Msg.Retire, Flow: r.Msg.Flow, Execution: r.Msg.Execution, Owner: r.Msg.Owner}
	for _, l := range r.Msg.AddLinks {
		w.AddLinks = append(w.AddLinks, graph.LinkWrite{Type: l.Type, To: pbconv.RefFromPB(l.To), Properties: pbconv.Map(l.Props)})
	}
	for _, id := range r.Msg.RemoveLinks {
		w.RemoveLinks = append(w.RemoveLinks, domain.LinkID(id))
	}
	cn, err := h.Graph.WriteNode(ctx, domain.ChangeID(r.Msg.ChangeId), domain.ChangeImpactID(r.Msg.ChangeImpactId), w)
	return res(&graphv1.WriteChangeImpactResponse{Node: pbconv.ChangeImpactToPB(cn)}, err)
}

func (h *Handler) ReviewChangeImpact(ctx context.Context, r *connect.Request[graphv1.ReviewChangeImpactRequest]) (*connect.Response[graphv1.ReviewChangeImpactResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	status := domain.ReviewRejected
	if r.Msg.Accept {
		status = domain.ReviewAccepted
	}
	cn, err := h.Graph.ReviewNodeOn(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Flow, r.Msg.Execution, domain.ChangeImpactID(r.Msg.ChangeImpactId), status, authz.From(ctx).Subject, r.Msg.Comment)
	return res(&graphv1.ReviewChangeImpactResponse{Node: pbconv.ChangeImpactToPB(cn)}, err)
}

func (h *Handler) GetBlackboard(ctx context.Context, r *connect.Request[graphv1.GetBlackboardRequest]) (*connect.Response[graphv1.GetBlackboardResponse], error) {
	bb, err := h.Graph.BlackboardIn(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Flow)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	out := &graphv1.GetBlackboardResponse{Change: pbconv.ChangeToPB(bb.Change), ActiveOption: bb.ActiveOption, At: pbconv.Time(bb.At)}
	for _, f := range bb.Options {
		out.Options = append(out.Options, pbconv.FlowToPB(f))
	}
	for _, d := range bb.DecisionPoints {
		out.DecisionPoints = append(out.DecisionPoints, pbconv.DecisionPointToPB(d))
	}
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
	b, err := h.Graph.CreateBranch(ctx, graph.NewBranch{Name: r.Msg.Name, Namespace: r.Msg.Namespace, From: domain.BaselineID(r.Msg.FromBaseline), Origin: r.Msg.Origin, Description: r.Msg.Description})
	return res(&graphv1.CreateBranchResponse{Branch: pbconv.BranchToPB(b)}, err)
}

func (h *Handler) ListBranches(ctx context.Context, r *connect.Request[graphv1.ListBranchesRequest]) (*connect.Response[graphv1.ListBranchesResponse], error) {
	bs, err := h.Graph.Branches(ctx, r.Msg.Namespace)
	out := &graphv1.ListBranchesResponse{}
	for _, b := range bs {
		out.Branches = append(out.Branches, pbconv.BranchToPB(b))
	}
	return res(out, err)
}

func (h *Handler) GetBranch(ctx context.Context, r *connect.Request[graphv1.GetBranchRequest]) (*connect.Response[graphv1.GetBranchResponse], error) {
	b, err := h.Graph.Branch(ctx, r.Msg.Namespace, r.Msg.Name)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	head, err := h.Graph.BranchHead(ctx, r.Msg.Namespace, b.Name)
	return res(&graphv1.GetBranchResponse{Branch: pbconv.BranchToPB(b), Head: pbconv.BaselineToPB(head)}, err)
}

func (h *Handler) SetBranchStatus(ctx context.Context, r *connect.Request[graphv1.SetBranchStatusRequest]) (*connect.Response[graphv1.SetBranchStatusResponse], error) {
	return res(&graphv1.SetBranchStatusResponse{}, h.Graph.SetBranchStatus(ctx, r.Msg.Namespace, r.Msg.Name, r.Msg.Status))
}

func (h *Handler) SetBranchDescription(ctx context.Context, r *connect.Request[graphv1.SetBranchDescriptionRequest]) (*connect.Response[graphv1.SetBranchDescriptionResponse], error) {
	return res(&graphv1.SetBranchDescriptionResponse{}, h.Graph.SetBranchDescription(ctx, r.Msg.Namespace, r.Msg.Name, r.Msg.Description))
}

func (h *Handler) ListNodeVersions(ctx context.Context, r *connect.Request[graphv1.ListNodeVersionsRequest]) (*connect.Response[graphv1.ListNodeVersionsResponse], error) {
	vs, err := h.Graph.Versions(ctx, domain.NodeID(r.Msg.Id))
	return res(&graphv1.ListNodeVersionsResponse{Versions: pbconv.NodesToPB(vs)}, err)
}

func (h *Handler) PlanMerge(ctx context.Context, r *connect.Request[graphv1.PlanMergeRequest]) (*connect.Response[graphv1.PlanMergeResponse], error) {
	p, err := h.Graph.PlanMerge(ctx, r.Msg.Namespace, r.Msg.From, r.Msg.Into)
	return res(&graphv1.PlanMergeResponse{Plan: pbconv.MergePlanToPB(p)}, err)
}

func (h *Handler) MergeBranch(ctx context.Context, r *connect.Request[graphv1.MergeBranchRequest]) (*connect.Response[graphv1.MergeBranchResponse], error) {
	req := graph.MergeRequest{From: r.Msg.From, Into: r.Msg.Into, Title: r.Msg.Title, Namespace: r.Msg.Namespace, Resolutions: map[domain.NodeID]graph.Resolution{}}
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

func (h *Handler) DiffBaselines(ctx context.Context, r *connect.Request[graphv1.DiffBaselinesRequest]) (*connect.Response[graphv1.DiffBaselinesResponse], error) {
	ds, err := h.Graph.DiffBaselines(ctx, domain.BaselineID(r.Msg.From), domain.BaselineID(r.Msg.To))
	out := &graphv1.DiffBaselinesResponse{}
	for _, d := range ds {
		pb := &graphv1.BaselineDiff{Node: string(d.Node), Key: d.Key, Type: d.Type, Kind: d.Kind}
		if d.From != nil {
			pb.From = pbconv.NodeToPB(*d.From)
		}
		if d.To != nil {
			pb.To = pbconv.NodeToPB(*d.To)
		}
		out.Nodes = append(out.Nodes, pb)
	}
	return res(out, err)
}

func (h *Handler) MergeChange(ctx context.Context, r *connect.Request[graphv1.MergeChangeRequest]) (*connect.Response[graphv1.MergeChangeResponse], error) {
	resolutions := map[domain.NodeID]graph.Resolution{}
	for id, rs := range r.Msg.Resolutions {
		resolutions[domain.NodeID(id)] = graph.Resolution{Props: pbconv.Map(rs.GetProps()), Skip: rs.GetSkip()}
	}
	c, err := h.Graph.IntegrateChange(ctx, domain.ChangeID(r.Msg.ChangeId), resolutions)
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

func (h *Handler) OpenFlow(ctx context.Context, r *connect.Request[graphv1.OpenFlowRequest]) (*connect.Response[graphv1.OpenFlowResponse], error) {
	m := r.Msg
	f, err := h.Graph.OpenFlow(ctx, domain.ChangeID(m.ChangeId), graph.OpenFlowRequest{Parent: m.Parent, ForkAfter: domain.ItemID(m.ForkAfter),
		Seeds: itemIDs(m.Seeds), FromStep: int(m.FromStep), Execution: m.Execution, Process: m.Process, Reason: m.Reason, Guidance: m.Guidance, By: m.By, StaleExecutions: m.StaleExecutions})
	if err == nil {
		h.publish(ctx, "goap.change."+m.ChangeId+".flow_opened", f)
	}
	return res(&graphv1.OpenFlowResponse{Flow: pbconv.FlowToPB(f)}, err)
}

func itemIDs(ss []string) []domain.ItemID {
	var out []domain.ItemID
	for _, s := range ss {
		out = append(out, domain.ItemID(s))
	}
	return out
}

func (h *Handler) AdoptFlow(ctx context.Context, r *connect.Request[graphv1.AdoptFlowRequest]) (*connect.Response[graphv1.AdoptFlowResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	f, err := h.Graph.AdoptFlow(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Flow, authz.From(ctx).Subject)
	if err == nil {
		h.publish(ctx, "goap.change."+r.Msg.ChangeId+".flow_adopted", f)
	}
	return res(&graphv1.AdoptFlowResponse{Flow: pbconv.FlowToPB(f)}, err)
}

func (h *Handler) DiscardFlow(ctx context.Context, r *connect.Request[graphv1.DiscardFlowRequest]) (*connect.Response[graphv1.DiscardFlowResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	f, err := h.Graph.DiscardFlow(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Flow, authz.From(ctx).Subject)
	if err == nil {
		h.publish(ctx, "goap.change."+r.Msg.ChangeId+".flow_discarded", f)
	}
	return res(&graphv1.DiscardFlowResponse{Flow: pbconv.FlowToPB(f)}, err)
}

func (h *Handler) ListFlows(ctx context.Context, r *connect.Request[graphv1.ListFlowsRequest]) (*connect.Response[graphv1.ListFlowsResponse], error) {
	fs, err := h.Graph.Flows(ctx, domain.ChangeID(r.Msg.ChangeId))
	out := &graphv1.ListFlowsResponse{}
	for _, f := range fs {
		out.Flows = append(out.Flows, pbconv.FlowToPB(f))
	}
	return res(out, err)
}

func (h *Handler) ValidateBoard(ctx context.Context, r *connect.Request[graphv1.ValidateBoardRequest]) (*connect.Response[graphv1.ValidateBoardResponse], error) {
	is, err := h.Graph.ValidateBoard(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Flow)
	out := &graphv1.ValidateBoardResponse{}
	for _, i := range is {
		out.Issues = append(out.Issues, pbconv.BoardIssueToPB(i))
	}
	return res(out, err)
}

func (h *Handler) RecordExecutions(ctx context.Context, r *connect.Request[graphv1.RecordExecutionsRequest]) (*connect.Response[graphv1.RecordExecutionsResponse], error) {
	return res(&graphv1.RecordExecutionsResponse{}, h.Graph.Record(ctx, pbconv.ExecutionsFromPB(r.Msg.Records)))
}

func (h *Handler) ListExecutions(ctx context.Context, r *connect.Request[graphv1.ListExecutionsRequest]) (*connect.Response[graphv1.ListExecutionsResponse], error) {
	rs, err := h.Graph.Journal(ctx, domain.ExecutionFilter{ChangeID: domain.ChangeID(r.Msg.ChangeId), ProcessIDs: r.Msg.ProcessIds})
	return res(&graphv1.ListExecutionsResponse{Records: pbconv.ExecutionsToPB(rs)}, err)
}

// RepublishIndex publishes again the node and baseline events so that an index can be rebuilt (ADR 0026).
func (h *Handler) RepublishIndex(ctx context.Context, r *connect.Request[graphv1.RepublishIndexRequest]) (*connect.Response[graphv1.RepublishIndexResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	if err := authz.Check(ctx, h.Authz, authz.Request{Subject: authz.From(ctx), Action: "admin", Resource: authz.Resource{Type: "platform"}}); err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	if h.Events == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no event bus"))
	}
	n, err := h.Graph.Republish(ctx, h.Events)
	return res(&graphv1.RepublishIndexResponse{Versions: int32(n)}, err)
}

// ---- Options (ADR 0009 §3, ADR 0032 §6) ------------------------------------------------------------------

func (h *Handler) OpenOption(ctx context.Context, r *connect.Request[graphv1.OpenOptionRequest]) (*connect.Response[graphv1.OpenOptionResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	f, err := h.Graph.OpenOption(ctx, domain.ChangeID(m.ChangeId), graph.OpenOptionRequest{Name: m.Name, Hypothesis: m.Hypothesis, Activate: m.Activate,
		By: authz.From(ctx).Subject})
	if err == nil {
		h.publish(ctx, "goap.change."+m.ChangeId+".option_opened", f)
	}
	return res(&graphv1.OpenOptionResponse{Option: pbconv.FlowToPB(f)}, err)
}

func (h *Handler) ActivateOption(ctx context.Context, r *connect.Request[graphv1.ActivateOptionRequest]) (*connect.Response[graphv1.ActivateOptionResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	active, err := h.Graph.ActivateOption(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Option, authz.From(ctx).Subject)
	if err == nil {
		h.publish(ctx, "goap.change."+r.Msg.ChangeId+".option_activated", map[string]any{"active": active})
	}
	return res(&graphv1.ActivateOptionResponse{Active: active}, err)
}

func (h *Handler) EvaluateOption(ctx context.Context, r *connect.Request[graphv1.EvaluateOptionRequest]) (*connect.Response[graphv1.EvaluateOptionResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	f, err := h.Graph.EvaluateOption(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Option, authz.From(ctx).Subject, r.Msg.Comment)
	return res(&graphv1.EvaluateOptionResponse{Option: pbconv.FlowToPB(f)}, err)
}

func (h *Handler) SelectOption(ctx context.Context, r *connect.Request[graphv1.SelectOptionRequest]) (*connect.Response[graphv1.SelectOptionResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	f, err := h.Graph.SelectOption(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Option, authz.From(ctx).Subject)
	if err == nil {
		h.publish(ctx, "goap.change."+r.Msg.ChangeId+".option_selected", f)
	}
	return res(&graphv1.SelectOptionResponse{Option: pbconv.FlowToPB(f)}, err)
}

func (h *Handler) RejectOption(ctx context.Context, r *connect.Request[graphv1.RejectOptionRequest]) (*connect.Response[graphv1.RejectOptionResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	f, err := h.Graph.RejectOption(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Option, authz.From(ctx).Subject)
	if err == nil {
		h.publish(ctx, "goap.change."+r.Msg.ChangeId+".option_rejected", f)
	}
	return res(&graphv1.RejectOptionResponse{Option: pbconv.FlowToPB(f)}, err)
}

func (h *Handler) ListOptions(ctx context.Context, r *connect.Request[graphv1.ListOptionsRequest]) (*connect.Response[graphv1.ListOptionsResponse], error) {
	os, err := h.Graph.Options(ctx, domain.ChangeID(r.Msg.ChangeId))
	out := &graphv1.ListOptionsResponse{}
	for _, f := range os {
		out.Options = append(out.Options, pbconv.FlowToPB(f))
		if f.Active {
			out.Active = f.ID
		}
	}
	return res(out, err)
}

func (h *Handler) CompareOptions(ctx context.Context, r *connect.Request[graphv1.CompareOptionsRequest]) (*connect.Response[graphv1.CompareOptionsResponse], error) {
	cmp, err := h.Graph.CompareOptions(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Level, r.Msg.All)
	out := &graphv1.CompareOptionsResponse{Level: cmp.Level}
	for _, f := range cmp.Options {
		out.Options = append(out.Options, pbconv.FlowToPB(f))
	}
	for _, n := range cmp.Nodes {
		on := &graphv1.OptionNode{Node: string(n.Node), Key: n.Key, Type: n.Type, Main: pbconv.RefPtrToPB(n.Main),
			Options: map[string]*graphv1.NodeRef{}, Props: map[string]*structpb.Struct{}}
		for id, ref := range n.Options {
			if ref != nil {
				on.Options[id] = pbconv.RefToPB(*ref)
			}
		}
		for side, p := range n.Props {
			on.Props[side] = pbconv.Struct(p)
		}
		out.Nodes = append(out.Nodes, on)
	}
	return res(out, err)
}

func (h *Handler) GetChangeView(ctx context.Context, r *connect.Request[graphv1.GetChangeViewRequest]) (*connect.Response[graphv1.GetChangeViewResponse], error) {
	b, err := h.Graph.ChangeView(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Flow, r.Msg.Level)
	return res(&graphv1.GetChangeViewResponse{Baseline: pbconv.BaselineToPB(b)}, err)
}

func (h *Handler) GetChangeGraph(ctx context.Context, r *connect.Request[graphv1.GetChangeGraphRequest]) (*connect.Response[graphv1.GetChangeGraphResponse], error) {
	nodes, links, err := h.Graph.ChangeGraph(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Flow)
	return res(&graphv1.GetChangeGraphResponse{Nodes: pbconv.NodesToPB(nodes), Links: pbconv.LinksToPB(links)}, err)
}

// ---- Decision points (ADR 0009 §4) ----------------------------------------------------------------------------

func (h *Handler) OpenDecision(ctx context.Context, r *connect.Request[graphv1.OpenDecisionRequest]) (*connect.Response[graphv1.OpenDecisionResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	in := graph.OpenDecisionRequest{Question: m.Question, Options: m.Options, Criteria: m.Criteria, Decider: m.Decider, Threshold: m.Threshold,
		MaxRounds: int(m.MaxRounds), By: authz.From(ctx).Subject}
	if m.AllOptions {
		in.Options = nil
	} else if in.Options == nil {
		in.Options = []string{}
	}
	if m.MaxDuration != "" {
		d, err := time.ParseDuration(m.MaxDuration)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("max duration: %w", err))
		}
		in.MaxDuration = d
	}
	d, err := h.Graph.OpenDecision(ctx, domain.ChangeID(m.ChangeId), in)
	if err == nil {
		h.publish(ctx, "goap.change."+m.ChangeId+".decision_opened", d)
	}
	return res(&graphv1.OpenDecisionResponse{Point: pbconv.DecisionPointToPB(d)}, err)
}

func (h *Handler) RuleDecision(ctx context.Context, r *connect.Request[graphv1.RuleDecisionRequest]) (*connect.Response[graphv1.RuleDecisionResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	d, err := h.Graph.RuleDecision(ctx, domain.ChangeID(m.ChangeId), graph.RuleRequest{Point: m.Point, Outcome: m.Outcome, Option: m.Option,
		Confidence: m.Confidence, Justification: m.Justification, Questions: m.Questions, Human: !m.Agent, By: authz.From(ctx).Subject})
	if err == nil {
		h.publish(ctx, "goap.change."+m.ChangeId+".decision_ruled", d)
	}
	return res(&graphv1.RuleDecisionResponse{Point: pbconv.DecisionPointToPB(d)}, err)
}

func (h *Handler) AnswerQuestion(ctx context.Context, r *connect.Request[graphv1.AnswerQuestionRequest]) (*connect.Response[graphv1.AnswerQuestionResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	d, err := h.Graph.AnswerQuestion(ctx, domain.ChangeID(m.ChangeId), m.Question, m.Answer, m.Process, authz.From(ctx).Subject)
	if err == nil {
		h.publish(ctx, "goap.change."+m.ChangeId+".question_answered", d)
	}
	return res(&graphv1.AnswerQuestionResponse{Point: pbconv.DecisionPointToPB(d)}, err)
}

func (h *Handler) RatifyDecision(ctx context.Context, r *connect.Request[graphv1.RatifyDecisionRequest]) (*connect.Response[graphv1.RatifyDecisionResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	d, err := h.Graph.RatifyDecision(ctx, domain.ChangeID(m.ChangeId), m.Point, m.Accept, authz.From(ctx).Subject, m.Comment)
	if err == nil {
		h.publish(ctx, "goap.change."+m.ChangeId+".decision_ratified", d)
	}
	return res(&graphv1.RatifyDecisionResponse{Point: pbconv.DecisionPointToPB(d)}, err)
}

func (h *Handler) ListDecisionPoints(ctx context.Context, r *connect.Request[graphv1.ListDecisionPointsRequest]) (*connect.Response[graphv1.ListDecisionPointsResponse], error) {
	ds, err := h.Graph.DecisionPoints(ctx, domain.ChangeID(r.Msg.ChangeId))
	out := &graphv1.ListDecisionPointsResponse{}
	for _, d := range ds {
		out.Points = append(out.Points, pbconv.DecisionPointToPB(d))
	}
	return res(out, err)
}
