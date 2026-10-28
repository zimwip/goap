package indexersvc

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	indexv1 "github.com/zimwip/goap/contracts/gen/goap/index/v1"
	"github.com/zimwip/goap/contracts/gen/goap/index/v1/indexv1connect"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/graph/index"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/pbconv"
	"github.com/zimwip/goap/lib/rpcerr"
)

// Handler implements indexv1connect.IndexServiceHandler.
type Handler struct {
	Service  *Service
	Identity identity.Extractor
	// Authz guards Reindex (admin on platform); nil grants it.
	Authz authz.Authorizer
}

var _ indexv1connect.IndexServiceHandler = (*Handler)(nil)

func (h *Handler) Search(ctx context.Context, r *connect.Request[indexv1.SearchRequest]) (*connect.Response[indexv1.SearchResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	for _, k := range m.Kinds {
		if k != index.KindNode && k != index.KindChange {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown kind %q (node or change)", k))
		}
	}
	q := index.Query{Text: m.Text, Facets: m.Facets, Limit: int(m.Limit), Offset: int(m.Offset), Mode: m.Mode, MinSimilarity: m.MinSimilarity, Snippet: m.Snippet,
		Filter: index.Filter{Kind: m.Kinds, Namespace: m.Namespaces, Type: m.Types, State: m.States, Branch: m.Branches,
			Project: m.Projects, Responsible: m.OwnerUnits, Status: m.Statuses, Methodology: m.Methodologies, RootsOnly: m.RootsOnly}}
	if err := h.inViews(ctx, m, &q); err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	if m.IncludeSubprojects && len(m.Projects) > 0 && h.Service.Access != nil {
		var all []string
		for _, p := range m.Projects {
			subs, err := h.Service.Access.SubProjects(ctx, p)
			if err != nil {
				return nil, rpcerr.ToConnect(err)
			}
			for _, k := range subs {
				if !slices.Contains(all, k) {
					all = append(all, k)
				}
			}
		}
		q.Filter.Project = all
	}
	if m.SimilarTo != nil {
		if m.SimilarTo.Id == "" || (m.SimilarTo.Kind != "" && m.SimilarTo.Kind != index.KindNode && m.SimilarTo.Kind != index.KindChange) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("similar_to needs a kind (node or change) and an id"))
		}
		q.SimilarTo = &index.Ref{Kind: m.SimilarTo.Kind, ID: domain.NodeID(m.SimilarTo.Id)}
	}
	for _, f := range m.FacetFilters {
		if q.Filter.Facets == nil {
			q.Filter.Facets = map[string][]string{}
		}
		q.Filter.Facets[f.Name] = f.Values
	}
	res, err := h.Service.Searcher.Search(ctx, q)
	if err != nil {
		return nil, searchErr(err)
	}
	out := &indexv1.SearchResponse{Total: int32(res.Total), Semantic: res.Semantic, Truncated: res.Truncated}
	for _, x := range res.Hits {
		hit := &indexv1.Hit{Kind: x.Kind, Id: string(x.ID), Version: int32(x.Version), Namespace: x.Namespace, Type: x.Type, Key: x.Key,
			State: x.State, Branch: x.Branch, Main: x.Main, Deleted: x.Deleted, Project: x.Project, OwnerUnit: x.Responsible, Facets: x.Facets,
			Score: x.Score, Similarity: x.Similarity, Snippet: x.Snippet}
		if x.Kind == index.KindChange {
			hit.Change = &indexv1.ChangeInfo{ChangeId: string(x.ID), Title: x.Title, Status: x.Status, ProjectId: x.Project, Methodology: x.Methodology,
				OwnerOrg: x.Responsible, ParentId: x.Parent}
		}
		out.Hits = append(out.Hits, hit)
	}
	for _, name := range m.Facets {
		fr := &indexv1.FacetResult{Name: name}
		for _, c := range res.Facets[name] {
			fr.Counts = append(fr.Counts, &indexv1.FacetCount{Value: c.Value, Count: int32(c.Count)})
		}
		out.Facets = append(out.Facets, fr)
	}
	return connect.NewResponse(out), nil
}

// searchErr maps the errors of a search to Connect codes: a semantic search without embedding is a failed
// precondition, so that a client can fall back to another mode.
func searchErr(err error) error {
	switch {
	case errors.Is(err, index.ErrSemanticUnavailable):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, index.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, index.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	}
	return rpcerr.ToConnect(err)
}

func (h *Handler) Reindex(ctx context.Context, r *connect.Request[indexv1.ReindexRequest]) (*connect.Response[indexv1.ReindexResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	if err := authz.Check(ctx, h.Authz, authz.Request{Subject: authz.From(ctx), Action: "admin", Resource: authz.Resource{Type: "platform"}}); err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	n, err := h.Service.Reindex(ctx)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&indexv1.ReindexResponse{Versions: int32(n)}), nil
}

func (h *Handler) Status(context.Context, *connect.Request[indexv1.StatusRequest]) (*connect.Response[indexv1.StatusResponse], error) {
	semantic, store, nodes, commits, changes, errs := h.Service.Status()
	return connect.NewResponse(&indexv1.StatusResponse{Semantic: semantic, Store: store, NodesIndexed: nodes, CommitsFollowed: commits, ChangesIndexed: changes, Errors: errs}), nil
}

// inViews scopes a search of nodes to the views it names (ADR 0157), one per namespace: the namespaces are theirs, and
// a hit of a node is kept when its version is the one its view holds (the heads of main when every view is one, the
// index's own flag). A search of nodes names its views, or the history (every version, refined by main); a search of
// changes only names neither.
func (h *Handler) inViews(ctx context.Context, m *indexv1.SearchRequest, q *index.Query) error {
	if len(m.Kinds) > 0 && !slices.Contains(m.Kinds, index.KindNode) {
		return nil
	}
	switch {
	case m.History && len(m.Views) > 0:
		return fmt.Errorf("a search names views or the history, not both: %w", graphapi.ErrInvalid)
	case m.History:
		q.Filter.Main = m.Main
		return nil
	case m.Main != nil:
		return fmt.Errorf("main refines a search of the history: %w", graphapi.ErrInvalid)
	case len(m.Views) == 0:
		return graphapi.ErrNoView
	case h.Service.Views == nil:
		return fmt.Errorf("this index resolves no view: %w", graphapi.ErrInvalid)
	}
	views := map[string]graphapi.View{}
	heads := map[string]bool{}
	every := true
	var namespaces []string
	for _, pv := range m.Views {
		v := pbconv.GraphViewFromPB(pv)
		st, err := h.Service.Views.ViewVersions(ctx, v, nil)
		if err != nil {
			return err
		}
		if _, dup := views[st.Namespace]; dup {
			return fmt.Errorf("two views of namespace %s: %w", st.Namespace, graphapi.ErrInvalid)
		}
		views[st.Namespace], heads[st.Namespace] = v, st.MainHead
		every = every && st.MainHead
		if len(m.Namespaces) == 0 || slices.Contains(m.Namespaces, st.Namespace) {
			namespaces = append(namespaces, st.Namespace)
		}
	}
	if len(namespaces) == 0 {
		namespaces = []string{"\x00"} // the namespaces asked have no view: nothing
	}
	q.Filter.Namespace = namespaces
	// the drafts of the change views: the index has no document of them
	for _, v := range views {
		if !v.IsChange() || m.SimilarTo != nil {
			continue
		}
		drafts, err := h.Service.Views.ViewDrafts(ctx, v, m.Text, index.PoolSize)
		if err != nil {
			return err
		}
		for _, n := range drafts {
			q.Extra = append(q.Extra, index.Hit{Kind: index.KindNode, ID: n.ID, Namespace: n.Namespace, Type: n.Type, Key: n.Key, State: n.State,
				Branch: n.Branch, Project: n.Project.Key, Responsible: n.Responsible.Key, Visibility: domain.VisibilityOf(n.Visibility), Creator: n.Creator.Subject})
		}
	}
	if every {
		yes := true
		q.Filter.Main = &yes
		return nil
	}
	q.Select = func(ctx context.Context, hits []index.Hit) ([]index.Hit, error) {
		ids := map[string][]domain.NodeID{}
		for _, x := range hits {
			if x.Kind != index.KindChange && !heads[x.Namespace] {
				ids[x.Namespace] = append(ids[x.Namespace], x.ID)
			}
		}
		held := map[string]map[domain.NodeID]domain.Version{}
		for ns, nodes := range ids {
			slices.Sort(nodes)
			st, err := h.Service.Views.ViewVersions(ctx, views[ns], slices.Compact(nodes))
			if err != nil {
				return nil, err
			}
			held[ns] = st.Versions
		}
		return slices.DeleteFunc(hits, func(x index.Hit) bool {
			switch {
			case x.Kind == index.KindChange:
				return false
			case heads[x.Namespace]:
				return !x.Main
			}
			v, ok := held[x.Namespace][x.ID]
			return !ok || v != x.Version
		}), nil
	}
	return nil
}
