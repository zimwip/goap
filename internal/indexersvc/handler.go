package indexersvc

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"connectrpc.com/connect"

	indexv1 "github.com/zimwip/goap/gen/goap/index/v1"
	"github.com/zimwip/goap/gen/goap/index/v1/indexv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/rpcerr"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/index"
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
		Filter: index.Filter{Kind: m.Kinds, Namespace: m.Namespaces, Type: m.Types, State: m.States, Branch: m.Branches, Main: m.Main,
			Project: m.Projects, Owner: m.OwnerUnits, Status: m.Statuses, Methodology: m.Methodologies, RootsOnly: m.RootsOnly}}
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
			State: x.State, Branch: x.Branch, Main: x.Main, Deleted: x.Deleted, Project: x.Project, OwnerUnit: x.Owner, Facets: x.Facets,
			Score: x.Score, Similarity: x.Similarity, Snippet: x.Snippet}
		if x.Kind == index.KindChange {
			hit.Change = &indexv1.ChangeInfo{ChangeId: string(x.ID), Title: x.Title, Status: x.Status, ProjectId: x.Project, Methodology: x.Methodology,
				OwnerOrg: x.Owner, ParentId: x.Parent}
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
	semantic, store, nodes, baselines, changes, errs := h.Service.Status()
	return connect.NewResponse(&indexv1.StatusResponse{Semantic: semantic, Store: store, NodesIndexed: nodes, BaselinesFollowed: baselines, ChangesIndexed: changes, Errors: errs}), nil
}
