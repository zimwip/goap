package graphsvc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/graphclient"
)

// The positions of the log through the service (ADR 0153): a write answers where it left the log of the change, the
// client puts it in the caller's watermark, a read requiring it is answered, one requiring more is refused as behind,
// and the change is read as it stood at an earlier position.
func TestPositionsThroughTheService(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	h := &graphsvc.Handler{Projects: graphsvc.EveryProject, Authz: authz.AllowAll{}, Graph: g}
	path, handler := graphv1connect.NewGraphServiceHandler(h, connect.WithInterceptors(h.Positions()))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := graphclient.NewClient(srv.Client(), srv.URL)

	base, err := g.BranchHead(ctx, "", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: "t", ForkCommit: base.ID})
	if err != nil {
		t.Fatal(err)
	}
	wctx, w := graphapi.WithWatermark(ctx)
	note := domain.ChangeItem{Type: domain.TypeArtifact, Target: "note", Data: map[string]any{"text": "one"}}
	if _, err := cl.AddItems(wctx, c.ID, []domain.ChangeItem{note}); err != nil {
		t.Fatal(err)
	}
	first := w.Of(c.ID)
	if first <= 0 {
		t.Fatalf("the answer carried no position: %v", w.All())
	}
	if _, err := cl.AddItems(wctx, c.ID, []domain.ChangeItem{note}); err != nil {
		t.Fatal(err)
	}
	second := w.Of(c.ID)
	if second <= first {
		t.Fatalf("the second write left the log at %d, after %d", second, first)
	}
	got, err := cl.Change(graphapi.AtLeast(ctx, c.ID, second), c.ID)
	if err != nil || got.Seq != second || len(got.Items) != 2 {
		t.Fatalf("read at least %d: seq %d, %d items, %v", second, got.Seq, len(got.Items), err)
	}
	if _, err := cl.Change(graphapi.AtLeast(ctx, c.ID, second+100), c.ID); !errors.Is(err, graphapi.ErrBehind) {
		t.Fatalf("a read past the head: %v, want ErrBehind", err)
	}
	at, err := cl.ChangeAt(ctx, c.ID, first)
	if err != nil || at.Seq != first || len(at.Items) != 1 {
		t.Fatalf("at %d: seq %d, %d items, %v", first, at.Seq, len(at.Items), err)
	}
}
