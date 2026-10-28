package graphsvc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/typecat"
)

// A domain published since the catalogue was loaded is known at the first use of one of its types, with no event:
// the request refused for a type the catalogue misses reads the registry again and runs once more.
func TestKnownTypesReadTheRegistryOnAMiss(t *testing.T) {
	ctx := context.Background()
	shop, err := def.ParseDomain([]byte("name: shop\nversion: 1.0.0\nnodeTypes:\n  - name: Order\n"))
	if err != nil {
		t.Fatal(err)
	}
	var published atomic.Bool
	types := typecat.NewLive(func(context.Context) ([]*def.Domain, error) {
		ds := typecat.Builtins()
		if published.Load() {
			ds = append(ds, shop)
		}
		return ds, nil
	})
	if err := types.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return types.Get() }
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	var misses atomic.Int32
	h := &graphsvc.Handler{Projects: graphsvc.EveryProject, Authz: authz.AllowAll{}, Graph: g,
		TypesMissed: func(ctx context.Context) bool { misses.Add(1); return types.Refresh(ctx, time.Hour) }}
	path, handler := graphv1connect.NewGraphServiceHandler(h, connect.WithInterceptors(h.KnownTypes(), h.LockedWrites()))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := graphv1connect.NewGraphServiceClient(srv.Client(), srv.URL)
	as := func(r connect.AnyRequest) { identity.SetHeaders(authz.Principal{Subject: "alice"}, r.Header()) }

	published.Store(true) // the registry has it, the catalogue not yet
	cr := connect.NewRequest(&graphv1.CreateChangeRequest{Title: "orders", Namespace: "shop", ProjectId: access.DefaultProject})
	as(cr)
	ch, err := cl.CreateChange(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	req := connect.NewRequest(&graphv1.ImpactNodeCreateRequest{ChangeId: ch.Msg.Change.GetId(), Key: "O-1", Type: "shop@Order", Rationale: "first order"})
	as(req)
	if _, err := cl.ImpactNodeCreate(ctx, req); err != nil {
		t.Fatalf("the type is read from the registry at its first use: %v", err)
	}
	if n := misses.Load(); n != 1 {
		t.Fatalf("one read of the registry: %d", n)
	}
	// a type no domain declares is still refused, the registry read at most once per interval
	bad := connect.NewRequest(&graphv1.ImpactNodeCreateRequest{ChangeId: ch.Msg.Change.GetId(), Key: "X-1", Type: "shop@Nothing", Rationale: "unknown"})
	as(bad)
	if _, err := cl.ImpactNodeCreate(ctx, bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("an undeclared type is refused: %v", err)
	}
}
