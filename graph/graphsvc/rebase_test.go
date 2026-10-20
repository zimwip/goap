package graphsvc_test

import (
	"context"
	"errors"
	"github.com/zimwip/goap/contracts/authz"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/graphclient"
)

// A sub-change behind its parent through the service (ADR 0103): the parent committed the node the sub-change committed
// on its own branch; the state tells what is behind, the rebase names the conflict, the resolution keeps the
// sub-change's value, and the sub-change then applies into its parent.
func TestRebaseThroughTheService(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	path, handler := graphv1connect.NewGraphServiceHandler(&graphsvc.Handler{Projects: graphsvc.EveryProject, Authz: authz.AllowAll{}, Graph: g})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := graphclient.NewClient(srv.Client(), srv.URL)
	ok := func(_ any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	base, err := g.BranchHead(ctx, "", domain.MainBranch)
	ok(nil, err)
	ok(g.Commit(ctx, graph.Commit{ProjectID: "PROJ-ROOT", Title: "seed", Baseline: base.ID, By: "test", Edits: []graph.NodeEdit{{Key: "N-1", Type: "Design", Props: map[string]any{"title": "t"}}}}))
	head, err := g.BranchHead(ctx, "", domain.MainBranch)
	ok(nil, err)
	parent, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: "parent", BaselineID: head.ID, OwnBranch: true})
	ok(nil, err)
	pcn, err := g.ImpactNodeCheckout(ctx, parent.ID, graph.NodeCheckout{Key: "N-1", Rationale: "p"})
	ok(nil, err)
	sub, err := g.CreateChange(ctx, graph.NewChange{Title: "sub", ParentID: parent.ID})
	ok(nil, err)
	scn, err := g.ImpactNodeCheckout(ctx, sub.ID, graph.NodeCheckout{Key: "N-1", Rationale: "s"})
	ok(nil, err)
	ok(g.ImpactNodeUpdate(ctx, sub.ID, scn.ID, graph.NodeUpdate{Properties: map[string]any{"title": "sub"}}))
	ok(g.ImpactNodeReview(ctx, sub.ID, scn.ID, domain.ReviewAccepted, "r", "ok"))
	ok(g.CommitChange(ctx, sub.ID, ""))
	ok(g.ImpactNodeUpdate(ctx, parent.ID, pcn.ID, graph.NodeUpdate{Properties: map[string]any{"title": "parent"}}))
	ok(g.ImpactNodeReview(ctx, parent.ID, pcn.ID, domain.ReviewAccepted, "r", "ok"))
	ok(g.CommitChange(ctx, parent.ID, ""))

	st, err := cl.RebaseState(ctx, sub.ID)
	if err != nil || st.Parent != parent.ID || len(st.Behind) != 1 || st.Behind[0] != scn.ID || len(st.Conflicts) != 0 {
		t.Fatalf("state = %+v, %v", st, err)
	}
	rb, err := cl.RebaseChange(ctx, sub.ID)
	if err != nil || len(rb.Impacts) != 1 || len(rb.Impacts[0].Conflicts) != 1 || rb.Impacts[0].Conflicts[0] != domain.ConflictProp("title") {
		t.Fatalf("rebase = %+v, %v", rb, err)
	}
	if st, _ = cl.RebaseState(ctx, sub.ID); len(st.Behind) != 0 || len(st.Conflicts) != 1 {
		t.Fatalf("after the rebase: %+v", st)
	}
	if _, err := cl.ImpactNodeResolve(ctx, sub.ID, scn.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.ImpactNodeResolve(ctx, sub.ID, scn.ID, ""); !errors.Is(err, graph.ErrConflict) {
		t.Fatalf("nothing left to settle: %v", err)
	}
	ok(g.ImpactNodeReview(ctx, sub.ID, scn.ID, domain.ReviewAccepted, "r", "ours"))
	ok(cl.CommitChange(ctx, sub.ID, ""))
	ok(cl.IntegrateChange(ctx, sub.ID, nil))
	if v, err := g.ChangeNodeViewByKey(ctx, parent.ID, "", "", "N-1"); err != nil || v.Properties["title"] != "sub" {
		t.Fatalf("parent = %v, %v", v.Properties, err)
	}
}
