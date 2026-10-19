// Package graphsvc exposes pkg/graph over Connect.
package graphsvc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/criticality"
	"github.com/zimwip/goap/contracts/domain"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/contracts/journal"
	"github.com/zimwip/goap/contracts/review"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/okf"
	"github.com/zimwip/goap/graph/prov"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/pbconv"
	"github.com/zimwip/goap/lib/rpcerr"
	"github.com/zimwip/goap/lib/typecat"
	"google.golang.org/protobuf/types/known/structpb"
)

// Publisher receives the documents RepublishIndex sends again to rebuild an index (ADR 0026); the engine's Publisher
// has the same method. The events of the graph are not the handler's: the graph emits them through its outbox
// (ADR 0136), whoever calls it.
type Publisher interface {
	Publish(ctx context.Context, subject string, v any) error
}

// Handler implements graphv1connect.GraphServiceHandler.
type Handler struct {
	Graph *graph.Graph
	// Events receives what RepublishIndex sends (nil: the RPC is refused).
	Events Publisher
	// Authz gates the creation of a node in a change (resource "object": ImpactNodeCreate, the successors of a
	// merge or split, a graph import), the tags and the reading of prompts. Nil refuses (ADR 0138). The other
	// impact operations are not gated by it.
	Authz authz.Authorizer
	// Projects tells whether a person works on a project (access.Directory.MayAccessProject): a change, and a node
	// read outside a change, are read by those who work on its project (ADR 0138), the PersonalScope interceptor and
	// the lists of changes asking it. Nil: no person reads them (a platform service, acting by itself, still does).
	Projects func(ctx context.Context, p authz.Principal, project string) (bool, error)
	// Stopped tells whether a project takes no new change (access.Directory.ProjectStopped, ADR 0138); nil: none is.
	Stopped func(ctx context.Context, project string) (bool, error)
	// Floor gates changes to User and Policy nodes (resource "policy", action "write"). It must not depend
	// on the policies themselves, so that no policy can lock the administrators out; nil falls back to Authz.
	Floor authz.Authorizer
	// Identity extracts the caller from request headers (set by the gateway).
	Identity identity.Extractor
	// Demo loads the demo data of the composition (platform/devseed), reporting whether it wrote anything and the key
	// of the demo project; nil: the deployment ships no demo (LoadDemoData is unimplemented).
	Demo func(ctx context.Context, g *graph.Graph) (loaded bool, project string, err error)
}

var _ graphv1connect.GraphServiceHandler = (*Handler)(nil)

// EveryProject is the explicit opening of Handler.Projects (ADR 0138): every person works on every project. For the
// tests and the gateway-less dev mode, never a default.
func EveryProject(context.Context, authz.Principal, string) (bool, error) { return true, nil }

func res[T any](m *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(m), nil
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
	if err == nil && !h.mayReadNode(ctx, v.Node) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("node %s: not found", ref.ID))
	}
	return res(&graphv1.GetNodeResponse{View: pbconv.ViewToPB(v)}, err)
}

// mayReadNode tells whether the caller reads a node outside a change (ADR 0138): those who work on its project; the
// nodes of the built-in namespaces (the organisation, the platform, the methodologies) describe the platform and are
// every person's to read. Read through a change, a node is the change's (PersonalScope).
func (h *Handler) mayReadNode(ctx context.Context, n domain.Node) bool {
	switch n.Namespace {
	case access.NamespaceOrganisation, domain.NamespacePlatform, domain.NamespaceMethodology:
		return !authz.From(ctx).Anonymous()
	}
	if n.Project == "" {
		return h.worksOn(ctx, "")
	}
	project, err := h.Graph.Node(ctx, domain.NodeRef{ID: n.Project})
	return err == nil && h.worksOn(ctx, project.Key)
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
	bs, err := h.Graph.BaselineHeaders(ctx, r.Msg.Namespace) // their headers: the states are read one at a time
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

// EndUser is the graph's EndUser (ADR 0114): the person the authenticated principal acts for, named by the events of
// the changes.
func EndUser(ctx context.Context) string { return authz.From(ctx).EndUser() }

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

// ExportGraph serves the state of a namespace at a baseline as a JSON snapshot, without history (ADR 0104).
func (h *Handler) ExportGraph(ctx context.Context, r *connect.Request[graphv1.ExportGraphRequest]) (*connect.Response[graphv1.ExportGraphResponse], error) {
	s, err := h.Graph.ExportSnapshot(ctx, r.Msg.Namespace, r.Msg.Branch, domain.BaselineID(r.Msg.BaselineId))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	doc, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	name := fmt.Sprintf("graph-%s-%s.json", s.Namespace, s.ExportedAt.Format("20060102-150405"))
	return connect.NewResponse(&graphv1.ExportGraphResponse{Document: string(doc), Filename: name, MediaType: "application/json", Nodes: int32(len(s.Nodes))}), nil
}

// okfChunk is the size of the chunks of an OKF archive sent on the stream.
const okfChunk = 64 << 10

// ExportGraphOkf streams the state of a namespace at a baseline as an OKF tree, one Markdown file per node, in a zip
// archive written while it is rendered (ADR 0111): a first message with the name of the archive, then its bytes in
// chunks. A streaming call passes none of the unary interceptors: the caller is identified here.
func (h *Handler) ExportGraphOkf(ctx context.Context, r *connect.Request[graphv1.ExportGraphOkfRequest], stream *connect.ServerStream[graphv1.ExportGraphOkfResponse]) error {
	ctx = h.Identity.Context(ctx, r.Header())
	if authz.From(ctx).Anonymous() {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("the caller is not identified"))
	}
	s, err := h.Graph.ExportSnapshot(ctx, r.Msg.Namespace, r.Msg.Branch, domain.BaselineID(r.Msg.BaselineId))
	if err != nil {
		return rpcerr.ToConnect(err)
	}
	var o okf.Options
	if h.Graph.Types != nil {
		if c, ok := h.Graph.Types().(interface {
			Type(string) (*typecat.Type, bool)
		}); ok {
			o = okf.FromTypes(c.Type)
		}
	}
	name := fmt.Sprintf("graph-%s-%s.okf.zip", s.Namespace, s.ExportedAt.Format("20060102-150405"))
	if err := stream.Send(&graphv1.ExportGraphOkfResponse{Filename: name, MediaType: "application/zip", Nodes: int32(len(s.Nodes))}); err != nil {
		return err
	}
	w := bufio.NewWriterSize(chunkSender(func(p []byte) error {
		return stream.Send(&graphv1.ExportGraphOkfResponse{Chunk: p})
	}), okfChunk)
	if err := okf.Write(w, s, o, s.ExportedAt); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	return w.Flush()
}

// chunkSender is an io.Writer sending every write as one message (a bufio.Writer in front sizes the chunks).
type chunkSender func([]byte) error

func (f chunkSender) Write(p []byte) (int, error) {
	if err := f(slices.Clone(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// ImportGraph imports a JSON snapshot as one new change of the caller (ADR 0104): every node type it writes is
// authorized like a creation in that change (ABAC object:create on its project, the gate of the access nodes).
func (h *Handler) ImportGraph(ctx context.Context, r *connect.Request[graphv1.ImportGraphRequest]) (*connect.Response[graphv1.ImportGraphResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	s, err := graph.ParseSnapshot([]byte(r.Msg.Document))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	owner, err := h.resolveOwner(ctx, r.Msg.OwnerOrg)
	if err != nil {
		return nil, err
	}
	out, err := h.Graph.ImportSnapshot(ctx, graph.SnapshotImport{Snapshot: s, Title: r.Msg.Title, Intent: r.Msg.Intent, Methodology: r.Msg.Methodology,
		OwnerOrg: owner, ProjectID: h.projectOf(ctx, r.Msg.ProjectId), By: authz.From(ctx).Subject,
		Gate: func(c domain.Change, typ string) error { return h.checkCreate(ctx, c, typ) }})
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	resp := &graphv1.ImportGraphResponse{Created: int32(out.Created), Updated: int32(out.Updated), Unchanged: int32(out.Unchanged), Warnings: out.Warnings}
	if out.Change != "" {
		c, err := h.Graph.Change(ctx, out.Change)
		if err != nil {
			return nil, rpcerr.ToConnect(err)
		}
		resp.Change = pbconv.ChangeToPB(c)
	}
	return connect.NewResponse(resp), nil
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
	b, err := h.Graph.BaselineHeader(ctx, id) // the header: the page is what is sent
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	page, err := h.Graph.BaselineNodes(ctx, id, graph.NodeQuery{Type: r.Msg.Type, Text: r.Msg.Query,
		IncludeDeleted: r.Msg.IncludeDeleted, Change: domain.ChangeID(r.Msg.ChangeId), Offset: int(r.Msg.Offset), Limit: int(r.Msg.Limit)})
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
	b, err := h.Graph.BaselineHeader(ctx, id) // the header: the page is what is sent
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

// ListItemTypes lists the item types a change may hold: the ones the domains in force declare (ADR 0128), open to any
// identified caller like the types of the catalogue.
func (h *Handler) ListItemTypes(context.Context, *connect.Request[graphv1.ListItemTypesRequest]) (*connect.Response[graphv1.ListItemTypesResponse], error) {
	return connect.NewResponse(pbconv.ItemTypesToPB(h.Graph.ItemTypes())), nil
}

func (h *Handler) GetStructures(ctx context.Context, _ *connect.Request[graphv1.GetStructuresRequest]) (*connect.Response[graphv1.GetStructuresResponse], error) {
	st, err := h.Graph.Structures(ctx)
	return res(pbconv.StructuresToPB(st), err)
}

func (h *Handler) ListNamespaces(ctx context.Context, _ *connect.Request[graphv1.ListNamespacesRequest]) (*connect.Response[graphv1.ListNamespacesResponse], error) {
	ns, err := h.Graph.Namespaces(ctx)
	return res(&graphv1.ListNamespacesResponse{Namespaces: ns}, err)
}

// projectOf resolves the project a change is created in (ADR 0091): the one the request names, else the caller's active
// project, else the root project (an empty claim means the root). The graph refuses a change with none, so the edge
// resolves it.
func (h *Handler) projectOf(ctx context.Context, requested string) string {
	if requested != "" {
		return requested
	}
	if p := authz.From(ctx).Project; p != "" {
		return p
	}
	return h.Graph.Structure(domain.StructureProject).Root
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
	projectID := h.projectOf(ctx, r.Msg.ProjectId)
	if h.Stopped != nil {
		if stopped, err := h.Stopped(ctx, projectID); err != nil {
			return nil, rpcerr.ToConnect(err)
		} else if stopped {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("project %s is stopped: it takes no new change", projectID))
		}
	}
	if err := validCriticality(pbconv.Map(r.Msg.Data)); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	c, err := h.Graph.CreateChange(ctx, graph.NewChange{ParentID: domain.ChangeID(r.Msg.ParentId), OwnerOrg: owner, OwnBranch: r.Msg.OwnBranch, Namespace: r.Msg.Namespace, Title: r.Msg.Title, Intent: r.Msg.Intent, Methodology: r.Msg.Methodology, MethodologyVersion: r.Msg.MethodologyVersion,
		BaselineID: domain.BaselineID(r.Msg.BaselineId), Branch: r.Msg.Branch, Data: pbconv.Map(r.Msg.Data), ProjectID: projectID})
	return res(&graphv1.CreateChangeResponse{Change: pbconv.ChangeToPB(c)}, err)
}

// mayRead tells whether the caller may read a change (ADR 0037, 0138): a personal change is its subject's alone; any
// other, those who work on its project, platform services acting by themselves.
func (h *Handler) mayRead(ctx context.Context, c domain.Change) bool {
	if access.IsPersonal(c) {
		return access.IsPersonalTo(c, subjectOf(ctx))
	}
	return h.worksOn(ctx, c.ProjectID)
}

// worksOn tells whether the caller works on a project: a platform service acting by itself does; a person when
// Projects says so (a platform administrator or reader, a role or a flag of an Assignment on it or above it).
func (h *Handler) worksOn(ctx context.Context, project string) bool {
	who := authz.From(ctx)
	if who.System() {
		return true
	}
	if h.Projects == nil || who.Anonymous() {
		return false
	}
	if project == "" {
		project = h.Graph.Structure(domain.StructureProject).Root
	}
	ok, err := h.Projects(ctx, who, project)
	return err == nil && ok
}

func (h *Handler) GetChange(ctx context.Context, r *connect.Request[graphv1.GetChangeRequest]) (*connect.Response[graphv1.GetChangeResponse], error) {
	c, err := h.Graph.ChangeChecked(ctx, domain.ChangeID(r.Msg.Id))
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
		if !h.mayRead(ctx, c) {
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
	cs, next, err := h.Graph.NodeChanges(ctx, domain.NodeID(r.Msg.NodeId), r.Msg.Before, int(r.Msg.Limit))
	out := &graphv1.ListNodeChangesResponse{Next: next}
	for _, c := range cs {
		if !h.mayRead(ctx, c) {
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
	// the graph emits change.edited, what a reactive activity of the methodology may want to look at again (ADR 0110)
	c, err := h.Graph.UpdateChange(ctx, domain.ChangeID(r.Msg.Id), p)
	return res(&graphv1.UpdateChangeResponse{Change: pbconv.ChangeToPB(c)}, err)
}

// MoveChange moves a root change, with its open sub-changes, to another project (ADR 0091). The graph's ProjectMoveGate
// authorizes it on both projects and checks the methodology; a personal change of someone else is refused by the
// PersonalScope interceptor (change_id).
func (h *Handler) MoveChange(ctx context.Context, r *connect.Request[graphv1.MoveChangeRequest]) (*connect.Response[graphv1.MoveChangeResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	c, err := h.Graph.MoveChange(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.ProjectId)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	c.Items, c.Nodes = nil, nil
	return connect.NewResponse(&graphv1.MoveChangeResponse{Change: pbconv.ChangeToPB(c)}), nil
}

// AttachChange makes a root change a sub-change of a change integrating it (ADR 0106) (the graph emits change.attached).
func (h *Handler) AttachChange(ctx context.Context, r *connect.Request[graphv1.AttachChangeRequest]) (*connect.Response[graphv1.AttachChangeResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	c, err := h.Graph.AttachChange(ctx, domain.ChangeID(r.Msg.ChangeId), domain.ChangeID(r.Msg.ParentId))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	c.Items, c.Nodes = nil, nil
	return connect.NewResponse(&graphv1.AttachChangeResponse{Change: pbconv.ChangeToPB(c)}), nil
}

// DetachChange gives a sub-change back as a root change (ADR 0106) (the graph emits change.detached).
func (h *Handler) DetachChange(ctx context.Context, r *connect.Request[graphv1.DetachChangeRequest]) (*connect.Response[graphv1.DetachChangeResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	c, err := h.Graph.DetachChange(ctx, domain.ChangeID(r.Msg.ChangeId))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	c.Items, c.Nodes = nil, nil
	return connect.NewResponse(&graphv1.DetachChangeResponse{Change: pbconv.ChangeToPB(c)}), nil
}

// SetChangeDependencies replaces the changes a change depends on (ADR 0108); the graph publishes change.updated.
func (h *Handler) SetChangeDependencies(ctx context.Context, r *connect.Request[graphv1.SetChangeDependenciesRequest]) (*connect.Response[graphv1.SetChangeDependenciesResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	c, err := h.Graph.SetChangeDependencies(ctx, domain.ChangeID(r.Msg.ChangeId), pbconv.ChangeIDsFromPB(r.Msg.DependsOn))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	c.Items, c.Nodes = nil, nil
	return connect.NewResponse(&graphv1.SetChangeDependenciesResponse{Change: pbconv.ChangeToPB(c)}), nil
}

// UpgradeChange moves an open change to another version of its methodology (ADR 0113).
func (h *Handler) UpgradeChange(ctx context.Context, r *connect.Request[graphv1.UpgradeChangeRequest]) (*connect.Response[graphv1.UpgradeChangeResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	c, err := h.Graph.UpgradeChange(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Version)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	c.Items, c.Nodes = nil, nil
	return connect.NewResponse(&graphv1.UpgradeChangeResponse{Change: pbconv.ChangeToPB(c)}), nil
}

// SetChangeMethodology gives an open change that follows no methodology one; the graph's MethodologyGate checks that it
// applies to the project of the change.
func (h *Handler) SetChangeMethodology(ctx context.Context, r *connect.Request[graphv1.SetChangeMethodologyRequest]) (*connect.Response[graphv1.SetChangeMethodologyResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	c, err := h.Graph.SetChangeMethodology(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Methodology)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	c.Items, c.Nodes = nil, nil
	return connect.NewResponse(&graphv1.SetChangeMethodologyResponse{Change: pbconv.ChangeToPB(c)}), nil
}

// GetChangeDependencies reads the dependencies of a change, its dependents and the merge order of its sub-changes
// (ADR 0108).
func (h *Handler) GetChangeDependencies(ctx context.Context, r *connect.Request[graphv1.GetChangeDependenciesRequest]) (*connect.Response[graphv1.GetChangeDependenciesResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	d, err := h.Graph.ChangeDependencies(ctx, domain.ChangeID(r.Msg.ChangeId))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&graphv1.GetChangeDependenciesResponse{DependsOn: pbconv.DependenciesToPB(d.DependsOn),
		Dependents: pbconv.DependenciesToPB(d.Dependents), Order: pbconv.DependenciesToPB(d.Order)}), nil
}

func (h *Handler) AddItems(ctx context.Context, r *connect.Request[graphv1.AddItemsRequest]) (*connect.Response[graphv1.AddItemsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	proposed := pbconv.ItemsFromPB(r.Msg.Items)
	items, err := h.Graph.AddItems(ctx, domain.ChangeID(r.Msg.ChangeId), proposed)
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
		err := authz.Check(ctx, gate, authz.Request{Subject: who, Action: "write", Resource: authz.Resource{Type: access.ResourcePolicy, Org: who.Org}})
		if err != nil && (typ == access.NodeTypeAssignment || typ == access.NodeTypeProjectUnit) {
			// the Assignments and the projects are also their owners' and administrators' (ADR 0138): one who
			// administers their active project may try, access.ProjectScopeValidator judges each write at commit
			err = h.mayAdministerProjects(ctx, who, typ)
		}
		if err != nil {
			return rpcerr.ToConnect(err)
		}
	}
	return nil
}

// mayAdministerProjects is the early gate of the Assignments and projects for a person who is no platform
// administrator (ADR 0138): they administer their active project (the root one when they name none).
func (h *Handler) mayAdministerProjects(ctx context.Context, who authz.Principal, typ string) error {
	project := who.Project
	if project == "" {
		project = h.Graph.Structure(domain.StructureProject).Root
	}
	res := "assignment"
	if typ == access.NodeTypeProjectUnit {
		res = "project"
	}
	return authz.Check(ctx, h.Authz, authz.Request{Subject: who, Action: "write", Resource: authz.Resource{Type: res, Org: who.Org, ProjectID: project}})
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
	projectID := h.projectOf(ctx, r.Msg.ProjectId)
	// a platform service's own events ride on its commit (ADR 0136); a person carries none
	var carried []graph.CarriedEvent
	if len(r.Msg.Events) > 0 && !authz.From(ctx).System() {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only a platform service carries events on a commit"))
	}
	for _, e := range r.Msg.Events {
		carried = append(carried, graph.CarriedEvent{Subject: e.Subject, Payload: e.Payload})
	}
	out, err := h.Graph.Commit(ctx, graph.Commit{Namespace: r.Msg.Namespace, Title: r.Msg.Title, Intent: r.Msg.Intent, Methodology: r.Msg.Methodology,
		Data: pbconv.Map(r.Msg.Data), Baseline: domain.BaselineID(r.Msg.BaselineId), By: by, BaselineName: r.Msg.BaselineName, Edits: edits,
		OwnerOrg: owner, ProjectID: projectID, Events: carried})
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

// WithdrawImpacts takes several impacts out of the change in one transaction; each is gated like WithdrawImpact.
func (h *Handler) WithdrawImpacts(ctx context.Context, r *connect.Request[graphv1.WithdrawImpactsRequest]) (*connect.Response[graphv1.WithdrawImpactsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	impacts := make([]domain.ChangeImpactID, 0, len(m.ChangeImpactIds))
	for _, id := range m.ChangeImpactIds {
		if err := h.gateImpact(ctx, m.ChangeId, id, ""); err != nil {
			return nil, err
		}
		impacts = append(impacts, domain.ChangeImpactID(id))
	}
	err := h.Graph.WithdrawImpacts(ctx, domain.ChangeID(m.ChangeId), impacts, m.Flow, m.Execution)
	return res(&graphv1.WithdrawImpactsResponse{}, err)
}

// RebaseChange brings a sub-change up to date with its parent (ADR 0082), a root change with the head of its target branch (ADR 0101).
func (h *Handler) RebaseChange(ctx context.Context, r *connect.Request[graphv1.RebaseChangeRequest]) (*connect.Response[graphv1.RebaseChangeResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	rb, err := h.Graph.RebaseChange(ctx, domain.ChangeID(r.Msg.ChangeId))
	out := &graphv1.RebaseChangeResponse{ParentId: string(rb.Parent), Branch: rb.Branch, From: string(rb.From), Onto: string(rb.Onto)}
	for _, i := range rb.Impacts {
		out.Impacts = append(out.Impacts, &graphv1.RebasedImpact{ChangeImpactId: string(i.Impact), Key: i.Key, Changed: i.Changed, Merged: i.Merged, Conflicts: i.Conflicts})
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

// GetRebaseState tells where a change stands against its parent or its target branch (ADR 0082, 0101).
func (h *Handler) GetRebaseState(ctx context.Context, r *connect.Request[graphv1.GetRebaseStateRequest]) (*connect.Response[graphv1.GetRebaseStateResponse], error) {
	st, err := h.Graph.RebaseState(ctx, domain.ChangeID(r.Msg.ChangeId))
	out := &graphv1.GetRebaseStateResponse{ParentId: string(st.Parent), Branch: st.Branch, Onto: string(st.Onto)}
	for _, id := range st.Behind {
		out.BehindImpactIds = append(out.BehindImpactIds, string(id))
	}
	for _, c := range st.Conflicts {
		out.Conflicts = append(out.Conflicts, &graphv1.ImpactConflicts{ChangeImpactId: string(c.Impact), Key: c.Key, Conflicts: c.Conflicts})
	}
	return res(out, err)
}

func (h *Handler) GetCommitState(ctx context.Context, r *connect.Request[graphv1.GetCommitStateRequest]) (*connect.Response[graphv1.GetCommitStateResponse], error) {
	st, err := h.Graph.CommitState(ctx, domain.ChangeID(r.Msg.ChangeId))
	return res(&graphv1.GetCommitStateResponse{ToCommit: st.ToCommit, Reverts: int32(st.Reverts), Unsettled: st.Unsettled, Commits: st.Commits,
		CanCommit: st.CanCommit, CanMerge: st.CanMerge}, err)
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
	return res(&graphv1.ApplyChangeResponse{Baseline: pbconv.BaselineToPB(b)}, err)
}

// CommitChange is the first step of ApplyChange: the versions are written on the change's own branch, its integration
// (MergeChange) is a later, separate act. The change header write publishes change.updated by itself.
func (h *Handler) CommitChange(ctx context.Context, r *connect.Request[graphv1.CommitChangeRequest]) (*connect.Response[graphv1.CommitChangeResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header()) // lifecycle transitions are authorized for the caller
	b, err := h.Graph.CommitChangeHeader(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.BaselineName)
	return res(&graphv1.CommitChangeResponse{Baseline: pbconv.BaselineToPB(b)}, err)
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
	if r.Msg.HeaderOnly {
		head, err := h.Graph.BranchHeadHeader(ctx, r.Msg.Namespace, b.Name)
		return res(&graphv1.GetBranchResponse{Branch: pbconv.BranchToPB(b), Head: pbconv.BaselineToPB(head)}, err)
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
	vs, next, err := h.Graph.VersionPage(ctx, domain.NodeID(r.Msg.Id), domain.Version(r.Msg.Before), int(r.Msg.Limit))
	return res(&graphv1.ListNodeVersionsResponse{Versions: pbconv.NodesToPB(vs), Next: int32(next)}, err)
}

func (h *Handler) PlanMerge(ctx context.Context, r *connect.Request[graphv1.PlanMergeRequest]) (*connect.Response[graphv1.PlanMergeResponse], error) {
	p, err := h.Graph.PlanMerge(ctx, r.Msg.Namespace, r.Msg.From, r.Msg.Into)
	return res(&graphv1.PlanMergeResponse{Plan: pbconv.MergePlanToPB(p)}, err)
}

func (h *Handler) MergeBranch(ctx context.Context, r *connect.Request[graphv1.MergeBranchRequest]) (*connect.Response[graphv1.MergeBranchResponse], error) {
	req := graph.MergeRequest{From: r.Msg.From, Into: r.Msg.Into, Title: r.Msg.Title, Namespace: r.Msg.Namespace, ProjectID: h.projectOf(ctx, ""), Resolutions: map[domain.NodeID]graph.Resolution{}}
	for id, res := range r.Msg.Resolutions {
		req.Resolutions[domain.NodeID(id)] = graph.Resolution{Props: pbconv.Map(res.GetProps()), Skip: res.GetSkip()}
	}
	out, err := h.Graph.MergeBranch(ctx, req)
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
	if err != nil {
		return res(out, err)
	}
	ctx = h.Identity.Context(ctx, r.Header())
	added, removed, err := h.Graph.DiffStacks(ctx, domain.BaselineID(r.Msg.From), domain.BaselineID(r.Msg.To))
	out.ChangesAdded, out.ChangesRemoved = pbconv.StackChangesToPB(readableStack(ctx, added)), pbconv.StackChangesToPB(readableStack(ctx, removed))
	return res(out, err)
}

// GetChangeStack serves the change-stack of a node version or a baseline (ADR 0133).
func (h *Handler) GetChangeStack(ctx context.Context, r *connect.Request[graphv1.GetChangeStackRequest]) (*connect.Response[graphv1.GetChangeStackResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	s, err := h.Graph.ChangeStack(ctx, graph.ChangeStackQuery{Node: domain.NodeRef{ID: domain.NodeID(r.Msg.NodeId), Version: domain.Version(r.Msg.Version)},
		Baseline: domain.BaselineID(r.Msg.BaselineId), Limit: int(r.Msg.Limit)})
	s.Changes = readableStack(ctx, s.Changes)
	return res(&graphv1.GetChangeStackResponse{Stack: pbconv.ChangeStackToPB(s)}, err)
}

// readableStack leaves the title of the personal changes of others out of a stack: their id stays, what they did is
// in the graph for everyone, what they were called is their subject's (ADR 0037).
func readableStack(ctx context.Context, cs []domain.StackChange) []domain.StackChange {
	for i, c := range cs {
		if !visibleTo(ctx, domain.Change{OwnerOrg: c.OwnerOrg}) {
			cs[i].Title = ""
		}
	}
	return cs
}

func (h *Handler) MergeChange(ctx context.Context, r *connect.Request[graphv1.MergeChangeRequest]) (*connect.Response[graphv1.MergeChangeResponse], error) {
	resolutions := map[domain.NodeID]graph.Resolution{}
	for id, rs := range r.Msg.Resolutions {
		resolutions[domain.NodeID(id)] = graph.Resolution{Props: pbconv.Map(rs.GetProps()), Skip: rs.GetSkip()}
	}
	ctx = h.Identity.Context(ctx, r.Header()) // the merge asks the change's methodology for the caller (ADR 0102)
	c, err := h.Graph.IntegrateChange(ctx, domain.ChangeID(r.Msg.ChangeId), resolutions)
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
	return res(&graphv1.AdoptFlowResponse{Flow: pbconv.FlowToPB(f)}, err)
}

func (h *Handler) DiscardFlow(ctx context.Context, r *connect.Request[graphv1.DiscardFlowRequest]) (*connect.Response[graphv1.DiscardFlowResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	f, err := h.Graph.DiscardFlow(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Flow, authz.From(ctx).Subject)
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
	return res(&graphv1.OpenOptionResponse{Option: pbconv.FlowToPB(f)}, err)
}

func (h *Handler) ActivateOption(ctx context.Context, r *connect.Request[graphv1.ActivateOptionRequest]) (*connect.Response[graphv1.ActivateOptionResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	active, err := h.Graph.ActivateOption(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Option, authz.From(ctx).Subject)
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
	return res(&graphv1.SelectOptionResponse{Option: pbconv.FlowToPB(f)}, err)
}

func (h *Handler) RejectOption(ctx context.Context, r *connect.Request[graphv1.RejectOptionRequest]) (*connect.Response[graphv1.RejectOptionResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	f, err := h.Graph.RejectOption(ctx, domain.ChangeID(r.Msg.ChangeId), r.Msg.Option, authz.From(ctx).Subject)
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
	in := graph.OpenDecisionRequest{Question: m.Question, Options: m.Options, Criteria: m.Criteria, Policy: pbconv.Map(m.Policy), Choices: m.Choices, By: authz.From(ctx).Subject}
	if m.AllOptions {
		in.Options = nil
	} else if in.Options == nil {
		in.Options = []string{}
	}
	d, err := h.Graph.OpenDecision(ctx, domain.ChangeID(m.ChangeId), in)
	return res(&graphv1.OpenDecisionResponse{Point: pbconv.DecisionPointToPB(d)}, err)
}

func (h *Handler) RuleDecision(ctx context.Context, r *connect.Request[graphv1.RuleDecisionRequest]) (*connect.Response[graphv1.RuleDecisionResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	d, err := h.Graph.RuleDecision(ctx, domain.ChangeID(m.ChangeId), graph.RuleRequest{Point: m.Point, Outcome: m.Outcome, Option: m.Option,
		Confidence: m.Confidence, Justification: m.Justification, Questions: m.Questions, Human: !m.Agent, By: authz.From(ctx).Subject, Role: m.Role})
	return res(&graphv1.RuleDecisionResponse{Point: pbconv.DecisionPointToPB(d)}, err)
}

func (h *Handler) AnswerQuestion(ctx context.Context, r *connect.Request[graphv1.AnswerQuestionRequest]) (*connect.Response[graphv1.AnswerQuestionResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	d, err := h.Graph.AnswerQuestion(ctx, domain.ChangeID(m.ChangeId), m.Question, m.Answer, m.Process, authz.From(ctx).Subject)
	return res(&graphv1.AnswerQuestionResponse{Point: pbconv.DecisionPointToPB(d)}, err)
}

func (h *Handler) RatifyDecision(ctx context.Context, r *connect.Request[graphv1.RatifyDecisionRequest]) (*connect.Response[graphv1.RatifyDecisionResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	d, err := h.Graph.RatifyDecision(ctx, domain.ChangeID(m.ChangeId), m.Point, m.Accept, authz.From(ctx).Subject, m.Comment)
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
