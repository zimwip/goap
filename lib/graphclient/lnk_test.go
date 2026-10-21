package graphclient

import (
	"context"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/contracts/lnk"
)

// remote is a graph serving the versions of one node, keyed REQ-1 in namespace alm.
type remote struct {
	graphv1connect.UnimplementedGraphServiceHandler
}

func (remote) GetNode(_ context.Context, req *connect.Request[graphv1.GetNodeRequest]) (*connect.Response[graphv1.GetNodeResponse], error) {
	m := req.Msg
	v := int32(2)
	switch {
	case m.GetRef() != nil && m.GetRef().GetId() == "n1":
		v = m.GetRef().GetVersion()
	case m.GetKey() != "REQ-1" || m.GetNamespace() != "alm":
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	n := &graphv1.Node{Id: "n1", Version: v, Namespace: "alm", Key: "REQ-1", Type: "alm@Requirement"}
	return connect.NewResponse(&graphv1.GetNodeResponse{View: &graphv1.NodeView{Node: n}}), nil
}

// TestFederation: a source of kind graph reads its nodes from another graph service by their key (ADR 0154).
func TestFederation(t *testing.T) {
	ctx := context.Background()
	_, h := graphv1connect.NewGraphServiceHandler(remote{})
	srv := httptest.NewServer(h)
	defer srv.Close()

	r := lnk.NewRegistry(lnk.ResolverFuncs{ResolveFunc: func(context.Context, lnk.Source, lnk.URL) (lnk.Target, error) {
		return lnk.Target{}, lnk.ErrNotFound
	}})
	r.Register(lnk.KindGraph, &Federation{HTTP: srv.Client()})
	if err := r.Declare(lnk.Source{Name: "acme", Kind: lnk.KindGraph, Namespace: "alm", Location: srv.URL + "/"}); err != nil {
		t.Fatal(err)
	}
	got, err := r.ResolveString(ctx, "lnk:acme/Requirement/REQ-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Node == nil || got.Node.Version != 2 || got.Type != "alm@Requirement" || got.URL.String() != "lnk:acme/Requirement/REQ-1" {
		t.Fatalf("Resolve = %+v", got)
	}
	if got, err := r.ResolveString(ctx, "lnk:acme/Requirement/REQ-1?v=1"); err != nil || got.Node.Version != 1 {
		t.Fatalf("Resolve v1 = %+v, %v", got, err)
	}
	for _, s := range []string{"lnk:acme/Requirement/REQ-2", "lnk:acme/Function/REQ-1"} {
		if _, err := r.ResolveString(ctx, s); !lnk.IsNotFound(err) {
			t.Fatalf("%s: %v, want not found", s, err)
		}
	}
	if u, err := r.URLOf(ctx, "acme", lnk.Target{Type: got.Type, Key: got.Key}); err != nil || u.String() != "lnk:acme/Requirement/REQ-1" {
		t.Fatalf("URLOf = %s, %v", u, err)
	}
}
