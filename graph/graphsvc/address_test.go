package graphsvc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/contracts/lnk"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/identity"
)

// ResolveAddress opens an address over the RPC (ADR 0171 §4-5): a node of this graph, a web address for the browser;
// no file scheme, a malformed address and an unknown target answer their codes.
func TestResolveAddressRPC(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	g.Resolvers = graphsvc.Resolvers()
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
	open := func(address string) (*graphv1.AddressTarget, error) {
		req := connect.NewRequest(&graphv1.ResolveAddressRequest{Address: address})
		identity.SetHeaders(authz.Principal{Subject: "alice"}, req.Header())
		res, err := cl.ResolveAddress(ctx, req)
		if err != nil {
			return nil, err
		}
		return res.Msg.Target, nil
	}
	root := lnk.New(access.NamespaceOrganisation, "OrgUnit", access.DefaultOrg).String()
	if t0, err := open(root); err != nil || t0.Open.GetMode() != lnk.OpenNode || t0.Node.GetId() == "" || t0.Url != root {
		t.Fatalf("node = %+v, %v", t0, err)
	}
	if t0, err := open("https://example.com/a/logo.svg"); err != nil || t0.Open.GetMode() != lnk.OpenExternal || t0.Open.GetMedia() != "image/svg+xml" {
		t.Fatalf("web = %+v, %v", t0, err)
	}
	for address, code := range map[string]connect.Code{
		"file:///etc/passwd":               connect.CodeUnimplemented,
		"lnk:organisation/OrgUnit":         connect.CodeInvalidArgument,
		"lnk:organisation/OrgUnit/NO-UNIT": connect.CodeNotFound,
	} {
		if _, err := open(address); connect.CodeOf(err) != code {
			t.Fatalf("%s: %v, want %v", address, err, code)
		}
	}
}
