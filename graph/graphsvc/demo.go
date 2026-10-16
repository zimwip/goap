package graphsvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/rpcerr"
)

// LoadDemoData loads the demo data on demand (the "Load demo data" setting): administrators only, checked against the
// floor like the access nodes; the seed then acts as the graph service, for the administrator who asked.
func (h *Handler) LoadDemoData(ctx context.Context, r *connect.Request[graphv1.LoadDemoDataRequest]) (*connect.Response[graphv1.LoadDemoDataResponse], error) {
	if h.Demo == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this deployment ships no demo data"))
	}
	ctx = h.Identity.Context(ctx, r.Header())
	who := authz.From(ctx)
	gate := h.Floor
	if gate == nil {
		gate = h.Authz
	}
	if err := authz.Check(ctx, gate, authz.Request{Subject: who, Action: "write", Resource: authz.Resource{Type: access.ResourcePolicy, Org: who.Org}}); err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	loaded, project, err := h.Demo(authz.With(ctx, SystemPrincipal.For(who.EndUser())), h.Graph)
	return res(&graphv1.LoadDemoDataResponse{Loaded: loaded, Project: project}, err)
}
