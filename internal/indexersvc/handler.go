package indexersvc

import (
	"context"

	"connectrpc.com/connect"

	indexv1 "github.com/zimwip/goap/gen/goap/index/v1"
	"github.com/zimwip/goap/gen/goap/index/v1/indexv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/rpcerr"
	"github.com/zimwip/goap/pkg/authz"
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
	q := index.Query{Text: m.Text, Facets: m.Facets, Limit: int(m.Limit), Offset: int(m.Offset),
		Filter: index.Filter{Namespace: m.Namespaces, Type: m.Types, State: m.States, Branch: m.Branches, Main: m.Main}}
	for _, f := range m.FacetFilters {
		if q.Filter.Facets == nil {
			q.Filter.Facets = map[string][]string{}
		}
		q.Filter.Facets[f.Name] = f.Values
	}
	res, err := h.Service.Searcher.Search(ctx, q)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	out := &indexv1.SearchResponse{Total: int32(res.Total), Semantic: res.Semantic, Truncated: res.Truncated}
	for _, x := range res.Hits {
		out.Hits = append(out.Hits, &indexv1.Hit{Id: string(x.ID), Version: int32(x.Version), Namespace: x.Namespace, Type: x.Type, Key: x.Key,
			State: x.State, Branch: x.Branch, Main: x.Main, Facets: x.Facets, Score: x.Score})
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
	semantic, store, nodes, baselines, errs := h.Service.Status()
	return connect.NewResponse(&indexv1.StatusResponse{Semantic: semantic, Store: store, NodesIndexed: nodes, BaselinesFollowed: baselines, Errors: errs}), nil
}
