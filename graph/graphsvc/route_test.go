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
	"github.com/zimwip/goap/contracts/lnk"
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

func (f *fakeGraph) GetGraphOverview(context.Context, *connect.Request[graphv1.GetGraphOverviewRequest]) (*connect.Response[graphv1.GetGraphOverviewResponse], error) {
	// each graph lists the namespaces it holds, and the domains in force it holds no node of with zero counts
	held := map[string][]string{"platform": {"organisation", "platform"}, "content": {"alm", "code"}}[f.name]
	empty := map[string][]string{"platform": {"alm", "code"}, "content": {"organisation", "platform"}}[f.name]
	out := &graphv1.GetGraphOverviewResponse{}
	for _, n := range held {
		out.Namespaces = append(out.Namespaces, &graphv1.NamespaceSummary{Namespace: n, Nodes: 1})
	}
	for _, n := range empty {
		out.Namespaces = append(out.Namespaces, &graphv1.NamespaceSummary{Namespace: n})
	}
	return connect.NewResponse(out), nil
}

func (f *fakeGraph) ListNamespaces(context.Context, *connect.Request[graphv1.ListNamespacesRequest]) (*connect.Response[graphv1.ListNamespacesResponse], error) {
	return connect.NewResponse(&graphv1.ListNamespacesResponse{Namespaces: map[string][]string{"platform": {"organisation", "platform"}, "content": {"alm", "code"}}[f.name]}), nil
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
	// the content graph is a declared source holding its namespaces; the platform source, this graph, routes nothing
	sources := []lnk.Source{{Name: "content", Kind: lnk.KindGraph, Location: contentSrv.URL, Namespaces: []string{"alm", "code"}},
		{Name: "platform", Kind: lnk.KindGraph}}
	path, localHandler := graphv1connect.NewGraphServiceHandler(local)
	mux := http.NewServeMux()
	mux.Handle(path, &graphsvc.Router{Local: localHandler, Sources: func(context.Context) ([]lnk.Source, error) { return sources, nil },
		LocalSource: "platform", Client: http.DefaultClient,
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
		// the statistics list every graph, each namespace naming its source
		ov, err := c.GetGraphOverview(context.Background(), connect.NewRequest(&graphv1.GetGraphOverviewRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		// each namespace counted by the graph holding it, never by the zero summary of the other
		got := map[string]string{}
		for _, n := range ov.Msg.Namespaces {
			got[n.Namespace] = n.Source
			if n.Nodes != 1 {
				t.Fatalf("%s counted by %s: %d nodes", n.Namespace, n.Source, n.Nodes)
			}
		}
		if len(ov.Msg.Namespaces) != 4 || got["alm"] != "content" || got["code"] != "content" || got["organisation"] != "platform" {
			t.Fatalf("overview = %v", got)
		}
		names, err := c.ListNamespaces(context.Background(), connect.NewRequest(&graphv1.ListNamespacesRequest{}))
		if err != nil || len(names.Msg.Namespaces) != 4 {
			t.Fatalf("namespaces = %v, %v", names, err)
		}
	}
	for _, who := range content.asked {
		if who != "ada" {
			t.Fatalf("the content graph is called as the caller: %v", content.asked)
		}
	}
}

// TestSeedSourcesSpec: the graph sources a bootstrap declares (GOAP_GRAPH_SOURCES).
func TestSeedSourcesSpec(t *testing.T) {
	got, err := graphsvc.ParseSources(" content = alm, code @ http://graph-content:8080 ; ")
	if err != nil || len(got) != 1 || got[0].Name != "content" || got[0].Location != "http://graph-content:8080" ||
		len(got[0].Namespaces) != 2 || !got[0].Holds("code") {
		t.Fatalf("sources = %+v, %v", got, err)
	}
	for _, bad := range []string{"content", "content=@http://x", "content=alm", "Content=alm@http://x"} {
		if _, err := graphsvc.ParseSources(bad); err == nil {
			t.Fatalf("%q is no source", bad)
		}
	}
}
