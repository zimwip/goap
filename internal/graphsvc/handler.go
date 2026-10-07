// Package graphsvc exposes pkg/graph over Connect.
package graphsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/zimwip/goap/pkg/events"
	"slices"
	"strings"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/internal/rpcerr"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/criticality"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/journal"
	"github.com/zimwip/goap/pkg/prov"
	"github.com/zimwip/goap/pkg/review"
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

func (h *Handler) GetNode(ctx context.Context, r *connect.Request[graphv1.GetNodeRequest]) (*connect.Response[graphv1.GetNodeResponse], error) {
	if r.Msg.ChangeId != "" {
		// as the change sees it (ADR 0079): the draft it holds of the node, else the stored version
		change := domain.ChangeID(r.Msg.ChangeId)
		if r.Msg.Key != "" {
			v, err := h.Graph.ChangeNodeViewByKey(ctx, change, r.Msg.Flow, r.Msg.Namespace, r.Msg.Key)
			return res(&graphv1.GetNodeResponse{View: pbconv.ViewToPB(v)}, err)
		}
		v, err := h.Graph.ChangeNodeView(ctx, change, r.Msg.Flow, pbconv.RefFromPB(r.Msg.Ref))
		return res(&graphv1.GetNodeResponse{View: pbconv.ViewToPB(v)}, err)
	}
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
	ctx = h.Identity.Context(ctx, r.Header())
	entries, counts, err := h.Graph.ChangeLog(ctx, f)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	// the prompts of the model calls (stream model) are read with their own permission, prompt:inspect on the project of
	// the change: asking for them without it is refused, a log read without types leaves them out
	hide := false
	if f.MatchType(journal.StreamModel + ".call") {
		if f.Change == "" { // a log read by processes only names its types
			return nil, rpcerr.ToConnect(fmt.Errorf("a log read without a change names its types: %w", graph.ErrInvalid))
		}
		c, err := h.Graph.Change(ctx, f.Change)
		if err != nil {
			return nil, rpcerr.ToConnect(err)
		}
		if err := h.checkPrompts(ctx, c); err != nil {
			if slices.ContainsFunc(f.Types, func(t string) bool { return strings.HasPrefix(t, journal.StreamModel+".") }) {
				return nil, rpcerr.ToConnect(err)
			}
			hide = true
		}
	}
	out := &graphv1.ListChangeLogResponse{Counts: map[string]int32{}}
	for t, n := range counts {
		if hide && strings.HasPrefix(t, journal.StreamModel+".") {
			continue
		}
		out.Counts[t] = int32(n)
	}
	for _, e := range entries {
		if hide && e.Stream() == journal.StreamModel {
			continue
		}
		out.Entries = append(out.Entries, pbconv.LogEntryToPB(e))
	}
	return connect.NewResponse(out), nil
}

// checkPrompts is nil when the caller may read the prompts of the model calls of the change.
func (h *Handler) checkPrompts(ctx context.Context, c domain.Change) error {
	return authz.Check(ctx, h.Authz, authz.Request{Subject: authz.From(ctx), Action: "inspect",
		Resource: authz.Resource{Type: "prompt", ID: string(c.ID), Name: c.Title, Org: c.OwnerOrg, ProjectID: c.ProjectID}})
}

// ExportChangeProvenance is the whole log of a change as PROV-O provenance (ADR 0057).
func (h *Handler) ExportChangeProvenance(ctx context.Context, r *connect.Request[graphv1.ExportChangeProvenanceRequest]) (*connect.Response[graphv1.ExportChangeProvenanceResponse], error) {
	id := domain.ChangeID(r.Msg.ChangeId)
	c, err := h.Graph.Change(ctx, id)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	entries, _, err := h.Graph.ChangeLog(ctx, domain.LogFilter{Change: id})
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	if h.checkPrompts(ctx, c) != nil { // the export carries the prompts only for whoever may read them
		entries = slices.DeleteFunc(entries, func(e domain.LogEntry) bool { return e.Stream() == journal.StreamModel })
	}
	doc, err := prov.Export(c, entries)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&graphv1.ExportChangeProvenanceResponse{Document: string(raw),
		Filename: "change-" + string(c.ID) + ".prov.jsonld", MediaType: prov.MediaType}), nil
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

// IsAdminOnlyType tells whether a node type is written by platform administrators only (ADR 0068).
func (h *Handler) IsAdminOnlyType(ctx context.Context, r *connect.Request[graphv1.IsAdminOnlyTypeRequest]) (*connect.Response[graphv1.IsAdminOnlyTypeResponse], error) {
	adminOnly, err := h.Graph.AdminOnlyType(ctx, r.Msg.Type)
	return res(&graphv1.IsAdminOnlyTypeResponse{AdminOnly: adminOnly}, err)
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
		if parent, perr := h.Graph.Change(ctx, domain.ChangeID(p)); perr == nil && (access.IsPersonal(parent) || access.IsPersonalUnit(owner)) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("a personal change has no sub-changes"))
		}
	}
	projectID := r.Msg.ProjectId
	if projectID == "" {
		projectID = authz.From(ctx).Project
	}
	if err := validCriticality(pbconv.Map(r.Msg.Data)); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	c, err := h.Graph.CreateChange(ctx, graph.NewChange{ParentID: domain.ChangeID(r.Msg.ParentId), OwnerOrg: owner, OwnBranch: r.Msg.OwnBranch, Namespace: r.Msg.Namespace, Title: r.Msg.Title, Intent: r.Msg.Intent, Methodology: r.Msg.Methodology,
		BaselineID: domain.BaselineID(r.Msg.BaselineId), Branch: r.Msg.Branch, Data: pbconv.Map(r.Msg.Data), ProjectID: projectID})
	if err == nil {
		h.publish(ctx, events.ChangeSubject(string(c.ID), events.ChangeCreated), domain.ChangeEvent{Type: events.ChangeCreated, Change: c})
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
	ctx = h.Identity.Context(ctx, r.Header())
	p := graph.ChangePatch{Title: r.Msg.Title, Intent: r.Msg.Intent, Goal: r.Msg.Goal, Data: pbconv.Map(r.Msg.Data)}
	if err := h.checkCriticality(ctx, domain.ChangeID(r.Msg.Id), p.Data); err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	if r.Msg.Status != nil {
		st := domain.ChangeStatus(*r.Msg.Status)
		p.Status = &st
	}
	c, err := h.Graph.UpdateChange(ctx, domain.ChangeID(r.Msg.Id), p)
	return res(&graphv1.UpdateChangeResponse{Change: pbconv.ChangeToPB(c)}, err)
}

func (h *Handler) AddItems(ctx context.Context, r *connect.Request[graphv1.AddItemsRequest]) (*connect.Response[graphv1.AddItemsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	proposed := pbconv.ItemsFromPB(r.Msg.Items)
	items, err := h.Graph.AddItems(ctx, domain.ChangeID(r.Msg.ChangeId), proposed)
	if err == nil {
		if c, cerr := h.Graph.Change(ctx, domain.ChangeID(r.Msg.ChangeId)); cerr == nil {
			c.Items = nil
			h.publish(ctx, events.ChangeSubject(r.Msg.ChangeId, events.ChangeItemAdded), domain.ChangeEvent{Type: events.ChangeItemAdded, Change: c, Items: items})
		}
	}
	return res(&graphv1.AddItemsResponse{Items: pbconv.ItemsToPB(items)}, err)
}

// gateAccess applies to a change impact the gate of the access nodes (`adminOnly` node types, ADR 0068): they need the access
// permission ("policy":"write"), checked against the floor (ADR 0020) — administrator-only, same as every
// other organisation-namespace write (authz.DefaultPolicies has no rule for "node"/"object" write on it).
func (h *Handler) gateAccess(ctx context.Context, typ string) error {
	who := authz.From(ctx)
	if adminOnly, err := h.Graph.AdminOnlyType(ctx, typ); err != nil {
		return rpcerr.ToConnect(err)
	} else if adminOnly {
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

func (h *Handler) ProposeImpact(ctx context.Context, r *connect.Request[graphv1.ProposeImpactRequest]) (*connect.Response[graphv1.ProposeImpactResponse], error) {
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
	out, err := h.Graph.ProposeImpact(ctx, domain.ChangeID(r.Msg.ChangeId), nodes)
	return res(&graphv1.ProposeImpactResponse{Nodes: pbconv.ChangeImpactsToPB(out)}, err)
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
			h.publish(ctx, events.ChangeSubject(string(out.Change), events.ChangeApplied), domain.ChangeEvent{Type: events.ChangeApplied, Change: c, Baseline: &out.Baseline})
		}
	}
	return res(&graphv1.CommitEditsResponse{ChangeId: string(out.Change), Baseline: pbconv.BaselineToPB(out.Baseline)}, err)
}

// ---- Node edits (ADR 0076) ---------------------------------------------------------------------------------

// impactType is the node type of a change impact of a change ("" when the change holds none under the id).
func (h *Handler) impactType(ctx context.Context, change, impact string) (string, error) {
	list, err := h.Graph.ListChangeImpacts(ctx, domain.ChangeID(change))
	if err != nil {
		return "", rpcerr.ToConnect(err)
	}
	for _, cn := range list {
		if string(cn.ID) == impact {
			return cn.Type, nil
		}
	}
	return "", nil
}

// gateImpact applies to an operation on a change impact the gate of the access nodes (gateAccess): the impact names
// the node by its id, or the call names the node.
func (h *Handler) gateImpact(ctx context.Context, change, impact, node string) error {
	typ, err := h.impactType(ctx, change, impact)
	if err != nil {
		return err
	}
	if typ == "" && node != "" {
		if n, err := h.Graph.Node(ctx, domain.NodeRef{ID: domain.NodeID(node)}); err == nil {
			typ = n.Type
		}
	}
	return h.gateAccess(ctx, typ)
}

// gateLink applies the gate of the access nodes to an operation on a link of a draft of the change: the type of its
// source.
func (h *Handler) gateLink(ctx context.Context, change, link string) error {
	typ, err := h.Graph.LinkSourceType(ctx, domain.ChangeID(change), domain.LinkID(link))
	if err != nil {
		return rpcerr.ToConnect(err)
	}
	return h.gateAccess(ctx, typ)
}

func linkWrites(ls []*graphv1.NodeLinkWrite) []graph.LinkWrite {
	var out []graph.LinkWrite
	for _, l := range ls {
		out = append(out, graph.LinkWrite{Type: l.Type, To: pbconv.RefFromPB(l.To), Properties: pbconv.Map(l.Props)})
	}
	return out
}

// ImpactNodeCreate creates a node in a change: ABAC object:create on the project of the change, and the gate of the access
// nodes for an adminOnly type.
func (h *Handler) ImpactNodeCreate(ctx context.Context, r *connect.Request[graphv1.ImpactNodeCreateRequest]) (*connect.Response[graphv1.ImpactNodeCreateResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	c, err := h.Graph.Change(ctx, domain.ChangeID(m.ChangeId))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	who := authz.From(ctx)
	if err := authz.Check(ctx, h.Authz, authz.Request{Subject: who, Action: "create",
		Resource: authz.Resource{Type: "object", Name: m.Type, Namespace: c.Namespace, Org: c.OwnerOrg, Owner: who.Subject, ProjectID: c.ProjectID}}); err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	if err := h.gateAccess(ctx, m.Type); err != nil {
		return nil, err
	}
	cn, err := h.Graph.ImpactNodeCreate(ctx, c.ID, graph.NodeCreate{Key: m.Key, Type: m.Type, Properties: pbconv.Map(m.Props), Owner: m.Owner, Rationale: m.Rationale,
		Links: linkWrites(m.Links), Flow: m.Flow, Execution: m.Execution, ProducedBy: who.Subject})
	return res(&graphv1.ImpactNodeCreateResponse{Node: pbconv.ChangeImpactToPB(cn)}, err)
}

// gateFunc is the gate of the access nodes as a function of a node type, for the operations that modify nodes they
// discover themselves (graph.MergeInput.Gate): it needs no transaction of the graph.
func (h *Handler) gateFunc(ctx context.Context) func(string) error {
	return func(typ string) error { return h.gateAccess(ctx, typ) }
}

func nameOf(n *graphv1.NodeName) graph.NodeName {
	return graph.NodeName{Impact: domain.ChangeImpactID(n.GetChangeImpactId()), Node: domain.NodeID(n.GetNodeId()), Key: n.GetKey()}
}

func createSpec(s *graphv1.NodeCreateSpec, flow, execution, by string) graph.NodeCreate {
	return graph.NodeCreate{Key: s.GetKey(), Type: s.GetType(), Properties: pbconv.Map(s.GetProps()), Owner: s.GetOwner(), Rationale: s.GetRationale(),
		Links: linkWrites(s.GetLinks()), Flow: flow, Execution: execution, ProducedBy: by}
}

// checkCreate is the ABAC check of the creation of a node of a type in a change.
func (h *Handler) checkCreate(ctx context.Context, c domain.Change, typ string) error {
	who := authz.From(ctx)
	if err := authz.Check(ctx, h.Authz, authz.Request{Subject: who, Action: "create",
		Resource: authz.Resource{Type: "object", Name: typ, Namespace: c.Namespace, Org: c.OwnerOrg, Owner: who.Subject, ProjectID: c.ProjectID}}); err != nil {
		return rpcerr.ToConnect(err)
	}
	return h.gateAccess(ctx, typ)
}

// ImpactNodeMerge merges nodes into a new one (ADR 0077): the creation of the new node is authorized like
// ImpactNodeCreate, and the gate of the access nodes stands in front of every node the merge modifies.
func (h *Handler) ImpactNodeMerge(ctx context.Context, r *connect.Request[graphv1.ImpactNodeMergeRequest]) (*connect.Response[graphv1.ImpactNodeRestructureResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	c, err := h.Graph.Change(ctx, domain.ChangeID(m.ChangeId))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	if err := h.checkCreate(ctx, c, m.GetInto().GetType()); err != nil {
		return nil, err
	}
	in := graph.MergeInput{Into: createSpec(m.Into, m.Flow, m.Execution, authz.From(ctx).Subject), Rationale: m.Rationale, Flow: m.Flow, Execution: m.Execution, Gate: h.gateFunc(ctx)}
	for _, s := range m.Sources {
		in.Sources = append(in.Sources, nameOf(s))
	}
	out, err := h.Graph.ImpactNodeMerge(ctx, c.ID, in)
	return res(pbconv.RestructuredToPB(out), err)
}

// ImpactNodeSplit splits a node into new ones (ADR 0077), authorized like ImpactNodeMerge.
func (h *Handler) ImpactNodeSplit(ctx context.Context, r *connect.Request[graphv1.ImpactNodeSplitRequest]) (*connect.Response[graphv1.ImpactNodeRestructureResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	c, err := h.Graph.Change(ctx, domain.ChangeID(m.ChangeId))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	in := graph.SplitInput{Source: nameOf(m.Source), Rationale: m.Rationale, Flow: m.Flow, Execution: m.Execution, Gate: h.gateFunc(ctx)}
	for _, s := range m.Into {
		if err := h.checkCreate(ctx, c, s.GetType()); err != nil {
			return nil, err
		}
		in.Into = append(in.Into, createSpec(s, m.Flow, m.Execution, authz.From(ctx).Subject))
	}
	out, err := h.Graph.ImpactNodeSplit(ctx, c.ID, in)
	return res(pbconv.RestructuredToPB(out), err)
}

// DerivedNodes answers which nodes derive from a node (ADR 0077).
func (h *Handler) DerivedNodes(ctx context.Context, r *connect.Request[graphv1.DerivedNodesRequest]) (*connect.Response[graphv1.DerivedNodesResponse], error) {
	ns, err := h.Graph.DerivedNodes(ctx, pbconv.RefFromPB(r.Msg.Ref))
	return res(&graphv1.DerivedNodesResponse{Nodes: pbconv.NodesToPB(ns)}, err)
}

func (h *Handler) ImpactNodeCheckout(ctx context.Context, r *connect.Request[graphv1.ImpactNodeCheckoutRequest]) (*connect.Response[graphv1.ImpactNodeCheckoutResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	if err := h.gateImpact(ctx, m.ChangeId, m.ChangeImpactId, m.NodeId); err != nil {
		return nil, err
	}
	cn, err := h.Graph.ImpactNodeCheckout(ctx, domain.ChangeID(m.ChangeId), graph.NodeCheckout{Impact: domain.ChangeImpactID(m.ChangeImpactId), Node: domain.NodeID(m.NodeId),
		Rationale: m.Rationale, Flow: m.Flow, Execution: m.Execution, ProducedBy: authz.From(ctx).Subject})
	return res(&graphv1.ImpactNodeCheckoutResponse{Node: pbconv.ChangeImpactToPB(cn)}, err)
}

func (h *Handler) ImpactNodeUpdate(ctx context.Context, r *connect.Request[graphv1.ImpactNodeUpdateRequest]) (*connect.Response[graphv1.ImpactNodeUpdateResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	if err := h.gateImpact(ctx, m.ChangeId, m.ChangeImpactId, ""); err != nil {
		return nil, err
	}
	cn, err := h.Graph.ImpactNodeUpdate(ctx, domain.ChangeID(m.ChangeId), domain.ChangeImpactID(m.ChangeImpactId),
		graph.NodeUpdate{Properties: pbconv.Map(m.Props), Owner: m.Owner, Flow: m.Flow, Execution: m.Execution})
	return res(&graphv1.ImpactNodeUpdateResponse{Node: pbconv.ChangeImpactToPB(cn)}, err)
}

func (h *Handler) ImpactLinkCreate(ctx context.Context, r *connect.Request[graphv1.ImpactLinkCreateRequest]) (*connect.Response[graphv1.ImpactLinkCreateResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	if err := h.gateImpact(ctx, m.ChangeId, m.ChangeImpactId, ""); err != nil {
		return nil, err
	}
	l, err := h.Graph.ImpactLinkCreate(ctx, domain.ChangeID(m.ChangeId), domain.ChangeImpactID(m.ChangeImpactId),
		graph.LinkWrite{Type: m.Type, To: pbconv.RefFromPB(m.To), Properties: pbconv.Map(m.Props)}, m.Flow, m.Execution)
	return res(&graphv1.ImpactLinkCreateResponse{Link: pbconv.LinkToPB(l)}, err)
}

func (h *Handler) ImpactLinkUpdate(ctx context.Context, r *connect.Request[graphv1.ImpactLinkUpdateRequest]) (*connect.Response[graphv1.ImpactLinkUpdateResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	if err := h.gateLink(ctx, m.ChangeId, m.LinkId); err != nil {
		return nil, err
	}
	l, err := h.Graph.ImpactLinkUpdate(ctx, domain.ChangeID(m.ChangeId), domain.LinkID(m.LinkId), pbconv.Map(m.Props), m.Flow, m.Execution)
	return res(&graphv1.ImpactLinkUpdateResponse{Link: pbconv.LinkToPB(l)}, err)
}

func (h *Handler) ImpactLinkDelete(ctx context.Context, r *connect.Request[graphv1.ImpactLinkDeleteRequest]) (*connect.Response[graphv1.ImpactLinkDeleteResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	if err := h.gateLink(ctx, m.ChangeId, m.LinkId); err != nil {
		return nil, err
	}
	return res(&graphv1.ImpactLinkDeleteResponse{}, h.Graph.ImpactLinkDelete(ctx, domain.ChangeID(m.ChangeId), domain.LinkID(m.LinkId), m.Flow, m.Execution))
}

func (h *Handler) ImpactNodeTransition(ctx context.Context, r *connect.Request[graphv1.ImpactNodeTransitionRequest]) (*connect.Response[graphv1.ImpactNodeTransitionResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header()) // the transition is authorized for the caller
	m := r.Msg
	if err := h.gateImpact(ctx, m.ChangeId, m.ChangeImpactId, m.NodeId); err != nil {
		return nil, err
	}
	cn, err := h.Graph.ImpactNodeTransition(ctx, domain.ChangeID(m.ChangeId), graph.NodeTransition{NodeCheckout: graph.NodeCheckout{Impact: domain.ChangeImpactID(m.ChangeImpactId),
		Node: domain.NodeID(m.NodeId), Rationale: m.Rationale, Flow: m.Flow, Execution: m.Execution, ProducedBy: authz.From(ctx).Subject}, To: m.State})
	return res(&graphv1.ImpactNodeTransitionResponse{Node: pbconv.ChangeImpactToPB(cn)}, err)
}

func (h *Handler) ImpactNodeCancel(ctx context.Context, r *connect.Request[graphv1.ImpactNodeCancelRequest]) (*connect.Response[graphv1.ImpactNodeCancelResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	if err := h.gateImpact(ctx, m.ChangeId, m.ChangeImpactId, ""); err != nil {
		return nil, err
	}
	cn, err := h.Graph.ImpactNodeCancel(ctx, domain.ChangeID(m.ChangeId), domain.ChangeImpactID(m.ChangeImpactId), m.Flow, m.Execution)
	return res(&graphv1.ImpactNodeCancelResponse{Node: pbconv.ChangeImpactToPB(cn)}, err)
}

func (h *Handler) WithdrawImpact(ctx context.Context, r *connect.Request[graphv1.WithdrawImpactRequest]) (*connect.Response[graphv1.WithdrawImpactResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	if err := h.gateImpact(ctx, m.ChangeId, m.ChangeImpactId, ""); err != nil {
		return nil, err
	}
	err := h.Graph.WithdrawImpact(ctx, domain.ChangeID(m.ChangeId), domain.ChangeImpactID(m.ChangeImpactId), m.Flow, m.Execution)
	return res(&graphv1.WithdrawImpactResponse{}, err)
}

// RebaseChange brings a sub-change up to date with its parent (ADR 0082).
func (h *Handler) RebaseChange(ctx context.Context, r *connect.Request[graphv1.RebaseChangeRequest]) (*connect.Response[graphv1.RebaseChangeResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	rb, err := h.Graph.RebaseChange(ctx, domain.ChangeID(r.Msg.ChangeId))
	out := &graphv1.RebaseChangeResponse{ParentId: string(rb.Parent)}
	for _, i := range rb.Impacts {
		out.Impacts = append(out.Impacts, &graphv1.RebasedImpact{ChangeImpactId: string(i.Impact), Key: i.Key, Changed: i.Changed, Conflicts: i.Conflicts})
	}
	return res(out, err)
}

// ImpactNodeResolve keeps the sub-change's values for the conflicts a rebase left on a change impact (ADR 0082 §2).
func (h *Handler) ImpactNodeResolve(ctx context.Context, r *connect.Request[graphv1.ImpactNodeResolveRequest]) (*connect.Response[graphv1.ImpactNodeResolveResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	if err := h.gateImpact(ctx, m.ChangeId, m.ChangeImpactId, ""); err != nil {
		return nil, err
	}
	cn, err := h.Graph.ImpactNodeResolve(ctx, domain.ChangeID(m.ChangeId), domain.ChangeImpactID(m.ChangeImpactId), m.Execution)
	return res(&graphv1.ImpactNodeResolveResponse{Node: pbconv.ChangeImpactToPB(cn)}, err)
}

// GetRebaseState tells where a sub-change stands against its parent (ADR 0082).
func (h *Handler) GetRebaseState(ctx context.Context, r *connect.Request[graphv1.GetRebaseStateRequest]) (*connect.Response[graphv1.GetRebaseStateResponse], error) {
	st, err := h.Graph.RebaseState(ctx, domain.ChangeID(r.Msg.ChangeId))
	out := &graphv1.GetRebaseStateResponse{ParentId: string(st.Parent)}
	for _, id := range st.Behind {
		out.BehindImpactIds = append(out.BehindImpactIds, string(id))
	}
	for _, c := range st.Conflicts {
		out.Conflicts = append(out.Conflicts, &graphv1.ImpactConflicts{ChangeImpactId: string(c.Impact), Key: c.Key, Conflicts: c.Conflicts})
	}
	return res(out, err)
}

func (h *Handler) ImpactNodeReview(ctx context.Context, r *connect.Request[graphv1.ImpactNodeReviewRequest]) (*connect.Response[graphv1.ImpactNodeReviewResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	status := domain.ReviewRejected
	if r.Msg.Accept {
		status = domain.ReviewAccepted
	}
	cn, err := h.Graph.ImpactNodeReviewOn(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Flow, r.Msg.Execution, domain.ChangeImpactID(r.Msg.ChangeImpactId), status, authz.From(ctx).Subject, r.Msg.Comment)
	return res(&graphv1.ImpactNodeReviewResponse{Node: pbconv.ChangeImpactToPB(cn)}, err)
}

// ImpactNodeReviewBatch applies reviews together (ADR 0080), the reviewer being the principal of the request.
func (h *Handler) ImpactNodeReviewBatch(ctx context.Context, r *connect.Request[graphv1.ImpactNodeReviewBatchRequest]) (*connect.Response[graphv1.ImpactNodeReviewBatchResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	b := domain.ReviewBatch{ID: m.ReviewId, Flow: m.Flow, Execution: m.Execution, By: authz.From(ctx).Subject}
	for _, v := range m.Verdicts {
		status := domain.ReviewRejected
		if v.Accept {
			status = domain.ReviewAccepted
		}
		b.Verdicts = append(b.Verdicts, domain.ImpactVerdict{Impact: domain.ChangeImpactID(v.ChangeImpactId), Status: status, Comment: v.Comment})
	}
	if m.Item != nil {
		it := pbconv.ItemFromPB(m.Item)
		b.Item = &it
	}
	out, err := h.Graph.ImpactNodeReviewBatch(ctx, domain.ChangeID(m.ChangeId), b)
	resp := &graphv1.ImpactNodeReviewBatchResponse{}
	for _, cn := range out {
		resp.Nodes = append(resp.Nodes, pbconv.ChangeImpactToPB(cn))
	}
	return res(resp, err)
}

// reviews is the use case of the review object (ADR 0080) over the graph.
func (h *Handler) reviews() review.Service { return review.Service{Port: h.Graph} }

// reviewer is the actor of a review call: the principal of the request, an administrator acting on the reviews of others.
func (h *Handler) reviewer(ctx context.Context) review.Actor {
	a := review.ActorOf(ctx)
	if !a.Admin {
		gate := h.Floor
		if gate == nil {
			gate = h.Authz
		}
		who := authz.From(ctx)
		a.Admin = authz.Check(ctx, gate, authz.Request{Subject: who, Action: "write", Resource: authz.Resource{Type: access.ResourcePolicy, Org: who.Org}}) == nil
	}
	return a
}

func (h *Handler) ReviewOpen(ctx context.Context, r *connect.Request[graphv1.ReviewOpenRequest]) (*connect.Response[graphv1.ReviewResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	rv, err := h.reviews().Open(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Flow, r.Msg.Comment, h.reviewer(ctx))
	return res(&graphv1.ReviewResponse{Review: pbconv.ReviewToPB(rv)}, err)
}

func (h *Handler) ReviewUpdate(ctx context.Context, r *connect.Request[graphv1.ReviewUpdateRequest]) (*connect.Response[graphv1.ReviewResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	rv, err := h.reviews().Update(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Key, pbconv.ReviewEditFromPB(r.Msg), h.reviewer(ctx))
	return res(&graphv1.ReviewResponse{Review: pbconv.ReviewToPB(rv)}, err)
}

func (h *Handler) ReviewSubmit(ctx context.Context, r *connect.Request[graphv1.ReviewSubmitRequest]) (*connect.Response[graphv1.ReviewResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	rv, err := h.reviews().Submit(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Key, h.reviewer(ctx))
	return res(&graphv1.ReviewResponse{Review: pbconv.ReviewToPB(rv)}, err)
}

func (h *Handler) ReviewDiscard(ctx context.Context, r *connect.Request[graphv1.ReviewDiscardRequest]) (*connect.Response[graphv1.ReviewResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	rv, err := h.reviews().Discard(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Key, h.reviewer(ctx))
	return res(&graphv1.ReviewResponse{Review: pbconv.ReviewToPB(rv)}, err)
}

func (h *Handler) ReopenChangeImpacts(ctx context.Context, r *connect.Request[graphv1.ReopenChangeImpactsRequest]) (*connect.Response[graphv1.ReopenChangeImpactsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	ids := make([]domain.ChangeImpactID, len(r.Msg.ChangeImpactIds))
	for i, id := range r.Msg.ChangeImpactIds {
		ids[i] = domain.ChangeImpactID(id)
	}
	done, err := h.Graph.ReopenImpacts(ctx, domain.ChangeID(r.Msg.ChangeId), ids, r.Msg.Comment)
	out := &graphv1.ReopenChangeImpactsResponse{}
	for _, id := range done {
		out.Reopened = append(out.Reopened, string(id))
	}
	return res(out, err)
}

func (h *Handler) GetBlackboard(ctx context.Context, r *connect.Request[graphv1.GetBlackboardRequest]) (*connect.Response[graphv1.GetBlackboardResponse], error) {
	bb, err := h.Graph.BlackboardIn(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Flow)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	out := &graphv1.GetBlackboardResponse{Change: pbconv.ChangeToPB(bb.Change), ActiveOption: domain.ActiveOptionOf(bb), At: pbconv.Time(bb.At)}
	for _, f := range domain.OptionsOf(bb) {
		out.Options = append(out.Options, pbconv.FlowToPB(f))
	}
	for _, d := range domain.DecisionPointsOf(bb) {
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
			h.publish(ctx, events.ChangeSubject(r.Msg.ChangeId, events.ChangeApplied), domain.ChangeEvent{Type: events.ChangeApplied, Change: c, Baseline: &b})
		}
	}
	return res(&graphv1.ApplyChangeResponse{Baseline: pbconv.BaselineToPB(b)}, err)
}

func (h *Handler) TransitionChange(ctx context.Context, r *connect.Request[graphv1.TransitionChangeRequest]) (*connect.Response[graphv1.TransitionChangeResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header()) // the transition is authorized for the caller
	c, err := h.Graph.TransitionChange(ctx, domain.ChangeID(r.Msg.ChangeId), graph.TransitionRequest{Transition: r.Msg.Transition, Decision: r.Msg.Decision, By: authz.From(ctx).Subject})
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	c.Items = nil
	return connect.NewResponse(&graphv1.TransitionChangeResponse{Change: pbconv.ChangeToPB(c)}), nil
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
		h.publish(ctx, events.ChangeSubject(string(c.ID), events.ChangeApplied), domain.ChangeEvent{Type: events.ChangeApplied, Change: c, Baseline: &out.Baseline})
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
		h.publish(ctx, events.ChangeSubject(string(c.ID), events.ChangeApplied), domain.ChangeEvent{Type: events.ChangeApplied, Change: c})
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
		h.publish(ctx, events.ChangeSubject(string(c.ID), events.ChangeCreated), domain.ChangeEvent{Type: events.ChangeCreated, Change: c})
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
		Seeds: itemIDs(m.Seeds), StaleRuns: m.StaleRuns, Origin: pbconv.Map(m.Origin), Items: pbconv.ItemsFromPB(m.Items)})
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

// AppendLog appends the entries of a use case (the execution journal, ADR 0011) to the logs of their changes; the
// graph refuses the streams it writes itself.
func (h *Handler) AppendLog(ctx context.Context, r *connect.Request[graphv1.AppendLogRequest]) (*connect.Response[graphv1.AppendLogResponse], error) {
	return res(&graphv1.AppendLogResponse{}, h.Graph.AppendLog(ctx, pbconv.LogEntriesFromPB(r.Msg.Entries)))
}

// ListExecutions reads the execution journal of a change and / or of processes as records.
func (h *Handler) ListExecutions(ctx context.Context, r *connect.Request[graphv1.ListExecutionsRequest]) (*connect.Response[graphv1.ListExecutionsResponse], error) {
	rs, err := journal.Read(ctx, h.Graph, journal.Filter{ChangeID: domain.ChangeID(r.Msg.ChangeId), ProcessIDs: r.Msg.ProcessIds})
	if errors.Is(err, journal.ErrInvalid) {
		err = errors.Join(graph.ErrInvalid, err)
	}
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	out := &graphv1.ListExecutionsResponse{}
	for _, rec := range rs {
		out.Records = append(out.Records, pbconv.ExecutionToPB(rec))
	}
	return connect.NewResponse(out), nil
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

func (h *Handler) DiffFlows(ctx context.Context, r *connect.Request[graphv1.DiffFlowsRequest]) (*connect.Response[graphv1.DiffFlowsResponse], error) {
	d, err := h.Graph.DiffFlows(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Left, r.Msg.Right, r.Msg.Level)
	return res(pbconv.FlowDiffToPB(d), err)
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
	in := graph.OpenDecisionRequest{Question: m.Question, Options: m.Options, Criteria: m.Criteria, Policy: pbconv.Map(m.Policy), By: authz.From(ctx).Subject}
	if m.AllOptions {
		in.Options = nil
	} else if in.Options == nil {
		in.Options = []string{}
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

// validCriticality refuses a criticality of the data of a change that is not C1, C2 or C3 (ADR 0075 §3).
func validCriticality(data map[string]any) error {
	v, ok := data[domain.DataCriticality]
	if !ok {
		return nil
	}
	if s, _ := v.(string); !criticality.Valid(criticality.Level(s)) {
		return fmt.Errorf("criticality %v: C1, C2 or C3", v)
	}
	return nil
}

// checkCriticality guards the criticality of the header of a change (ADR 0075 §3): the requester may raise it, lowering
// it (below the level it holds, the default of its methodology included) asks the permission change:lower-criticality,
// which administrators hold. A platform service acting by itself is not asked.
func (h *Handler) checkCriticality(ctx context.Context, id domain.ChangeID, data map[string]any) error {
	if err := validCriticality(data); err != nil {
		return fmt.Errorf("%w: %w", err, graph.ErrInvalid)
	}
	v, ok := data[domain.DataCriticality]
	if !ok {
		return nil
	}
	c, err := h.Graph.Change(ctx, id)
	if err != nil {
		return err
	}
	to := criticality.Level(v.(string))
	if !criticality.Lowers(criticality.Of(c.Data), to) || h.Authz == nil || authz.From(ctx).System() {
		return nil
	}
	return authz.Check(ctx, h.Authz, authz.Request{Subject: authz.From(ctx), Action: "lower-criticality",
		Resource: authz.Resource{Type: "change", ID: string(c.ID), Name: c.Title, Org: c.OwnerOrg, ProjectID: c.ProjectID}})
}
