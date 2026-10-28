package graphsvc

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/contracts/lnk"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/lib/graphclient"
	"github.com/zimwip/goap/lib/rpcerr"
)

// Resolvers are the resolvers of the kinds of source the graph service reads beyond its own namespaces (ADR 0154): a
// namespace of another graph service (lnk.KindGraph), a directory of files on the disk of the service (lnk.KindLocalFS)
// and the web (lnk.KindWeb, also the reader of the http and https addresses, ADR 0171). A source is declared by a
// platform@Source node, written by administrators only (adminOnly).
func Resolvers() map[string]lnk.Resolver {
	return map[string]lnk.Resolver{
		lnk.KindGraph:   &graphclient.Federation{},
		lnk.KindLocalFS: lnk.LocalFS{},
		lnk.KindWeb:     lnk.Web{},
	}
}

// ResolveAddress reads and opens an address for a project (ADR 0171 §4-5): the request's, else the caller's active
// project. A node of this graph the caller may not read is not found; a node of another graph is that graph's to judge.
func (h *Handler) ResolveAddress(ctx context.Context, r *connect.Request[graphv1.ResolveAddressRequest]) (*connect.Response[graphv1.ResolveAddressResponse], error) {
	project := r.Msg.ProjectId
	if project == "" {
		project = authz.From(ctx).Project
	}
	t, err := h.Graph.ResolveAddress(ctx, project, r.Msg.Address)
	if err == nil && t.Node != nil {
		n, nerr := h.Graph.Node(ctx, domain.NodeRef{ID: domain.NodeID(t.Node.ID), Version: domain.Version(t.Node.Version)})
		switch {
		case nerr == nil && !h.mayReadNode(ctx, n):
			err = fmt.Errorf("%s: %w", r.Msg.Address, graph.ErrNotFound)
		case nerr != nil && !errors.Is(nerr, graph.ErrNotFound):
			err = nerr
		}
	}
	if err != nil {
		return nil, rpcerr.ToConnect(lnkError(err))
	}
	return connect.NewResponse(&graphv1.ResolveAddressResponse{Target: addressTargetToPB(t)}), nil
}

// lnkError gives the errors of the logical URLs the sentinels of the graph's contract.
func lnkError(err error) error {
	switch {
	case lnk.IsNotFound(err):
		return errors.Join(graphapi.ErrNotFound, err)
	case errors.Is(err, lnk.ErrInvalid):
		return errors.Join(graphapi.ErrInvalid, err)
	case errors.Is(err, lnk.ErrNoResolver):
		return connect.NewError(connect.CodeUnimplemented, err)
	}
	return err
}

func addressTargetToPB(t lnk.Target) *graphv1.AddressTarget {
	out := &graphv1.AddressTarget{
		Url: t.URL.String(), Kind: t.Kind, Type: t.Type, Key: t.Key, Title: t.Title, Location: t.Location, Content: t.Content,
	}
	if t.Node != nil {
		out.Node = &graphv1.NodeRef{Id: t.Node.ID, Version: int32(t.Node.Version)}
	}
	if o := t.Open; o != nil {
		out.Open = &graphv1.AddressOpen{Mode: o.Mode, Href: o.Href, Media: o.Media, Language: o.Language, Truncated: o.Truncated}
	}
	return out
}
