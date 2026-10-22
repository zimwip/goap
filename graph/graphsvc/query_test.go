package graphsvc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/identity"
)

// QueryView answers a read query over the RPC (ADR 0160): the rows as JSON lists naming the nodes they hold, the nodes
// themselves; a query the subset does not read is INVALID_ARGUMENT.
func TestQueryViewRPC(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Projects: graphsvc.EveryProject, Authz: authz.AllowAll{}, Graph: g}
	path, handler := graphv1connect.NewGraphServiceHandler(h, connect.WithInterceptors(h.Identify()))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := graphv1connect.NewGraphServiceClient(srv.Client(), srv.URL)
	view := &graphv1.View{Namespace: access.NamespaceOrganisation, Branch: domain.MainBranch}
	query := func(q string) (*connect.Response[graphv1.QueryViewResponse], error) {
		req := connect.NewRequest(&graphv1.QueryViewRequest{View: view, Query: q})
		identity.SetHeaders(authz.Principal{Subject: "alice"}, req.Header())
		return cl.QueryView(ctx, req)
	}
	res, err := query("MATCH (u:OrgUnit {key: '" + access.DefaultOrg + "'}) RETURN u, u.key AS key")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Msg.Rows) != 1 || len(res.Msg.Nodes) != 1 || res.Msg.Columns[1] != "key" {
		t.Fatalf("answer: %+v", res.Msg)
	}
	row := res.Msg.Rows[0].AsSlice()
	if row[0].(map[string]any)["$node"] != res.Msg.Nodes[0].Id || row[1] != access.DefaultOrg {
		t.Fatalf("row: %v", row)
	}
	if _, err := query("MATCH (u:OrgUnit) DELETE u"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a write: %v", err)
	}
	if _, err := query("MATCH (u) RETURN u"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("no anchor: %v", err)
	}
}
