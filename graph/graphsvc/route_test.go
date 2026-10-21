package graphsvc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/domain"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/graph/graphsvc"
)

// fakeGraph answers as a graph named by its title: the changes it holds, and a change created in it.
type fakeGraph struct {
	graphv1connect.UnimplementedGraphServiceHandler
	name    string
	changes map[string]bool
	asked   []string // the user header of each call
}

func (f *fakeGraph) GetChange(_ context.Context, r *connect.Request[graphv1.GetChangeRequest]) (*connect.Response[graphv1.GetChangeResponse], error) {
	f.asked = append(f.asked, r.Header().Get("X-Goap-User"))
	if !f.changes[r.Msg.Id] {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	return connect.NewResponse(&graphv1.GetChangeResponse{Change: &graphv1.Change{Id: r.Msg.Id, Title: f.name}}), nil
}

func (f *fakeGraph) CreateChange(_ context.Context, r *connect.Request[graphv1.CreateChangeRequest]) (*connect.Response[graphv1.CreateChangeResponse], error) {
	f.asked = append(f.asked, r.Header().Get("X-Goap-User"))
	return connect.NewResponse(&graphv1.CreateChangeResponse{Change: &graphv1.Change{Id: "new", Title: f.name}}), nil
}

func (f *fakeGraph) GetNode(_ context.Context, r *connect.Request[graphv1.GetNodeRequest]) (*connect.Response[graphv1.GetNodeResponse], error) {
	if r.Msg.Ref.GetId() != f.name+"-node" {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	return connect.NewResponse(&graphv1.GetNodeResponse{View: &graphv1.NodeView{Node: &graphv1.Node{Id: r.Msg.Ref.GetId(), Key: f.name}}}), nil
}

func serve(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// TestRouter: the platform graph serves its namespaces and forwards the calls naming a namespace or a change of the
// content graph to it, headers included (ADR 0154 §9).
func TestRouter(t *testing.T) {
	local := &fakeGraph{name: "platform", changes: map[string]bool{"C-local": true}}
	content := &fakeGraph{name: "content", changes: map[string]bool{"C-content": true}}
	_, contentHandler := graphv1connect.NewGraphServiceHandler(content)
	contentSrv := serve(t, contentHandler)
	routes, err := graphsvc.ParseRoutes("alm, code=" + contentSrv.URL)
	if err != nil || routes["alm"] != contentSrv.URL || routes["code"] != contentSrv.URL {
		t.Fatalf("routes = %v, %v", routes, err)
	}
	path, localHandler := graphv1connect.NewGraphServiceHandler(local)
	mux := http.NewServeMux()
	mux.Handle(path, &graphsvc.Router{Local: localHandler, Routes: routes, Client: http.DefaultClient,
		HasChange: func(_ context.Context, id domain.ChangeID) (bool, error) { return local.changes[string(id)], nil }})
	srv := serve(t, mux)

	for _, opts := range [][]connect.ClientOption{nil, {connect.WithProtoJSON()}} {
		c := graphv1connect.NewGraphServiceClient(http.DefaultClient, srv.URL, opts...)
		create := func(ns string) string {
			t.Helper()
			r := connect.NewRequest(&graphv1.CreateChangeRequest{Namespace: ns, Title: "t"})
			r.Header().Set("X-Goap-User", "ada")
			res, err := c.CreateChange(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			return res.Msg.Change.Title
		}
		if got := create("alm"); got != "content" {
			t.Fatalf("a change of alm is created by %s", got)
		}
		if got := create("organisation"); got != "platform" {
			t.Fatalf("a change of organisation is created by %s", got)
		}
		get := func(id string) (string, error) {
			r := connect.NewRequest(&graphv1.GetChangeRequest{Id: id})
			r.Header().Set("X-Goap-User", "ada")
			res, err := c.GetChange(context.Background(), r)
			if err != nil {
				return "", err
			}
			return res.Msg.Change.Title, nil
		}
		if got, err := get("C-content"); err != nil || got != "content" {
			t.Fatalf("a change of the content graph: %s, %v", got, err)
		}
		if got, err := get("C-local"); err != nil || got != "platform" {
			t.Fatalf("a change of this graph: %s, %v", got, err)
		}
		if _, err := get("C-none"); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("a change no graph holds: %v", err)
		}
		// a node read by its id: this graph, else the graph that holds it
		for _, id := range []string{"platform-node", "content-node"} {
			res, err := c.GetNode(context.Background(), connect.NewRequest(&graphv1.GetNodeRequest{Ref: &graphv1.NodeRef{Id: id}}))
			if err != nil || res.Msg.View.Node.Id != id {
				t.Fatalf("node %s: %v, %v", id, res, err)
			}
		}
		if _, err := c.GetNode(context.Background(), connect.NewRequest(&graphv1.GetNodeRequest{Ref: &graphv1.NodeRef{Id: "none"}})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("a node no graph holds: %v", err)
		}
	}
	for _, who := range content.asked {
		if who != "ada" {
			t.Fatalf("the content graph is called as the caller: %v", content.asked)
		}
	}
}
