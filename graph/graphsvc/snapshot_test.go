package graphsvc_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/typecat"
)

// A graph snapshot is imported as one change of the caller (ADR 0104), each type it writes authorized like a creation;
// a refused import leaves no change behind. The export of an empty namespace is an empty snapshot.
func TestImportGraphCreatesAChange(t *testing.T) {
	ds, err := def.LoadDomains("../../domains")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(ds...)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return cat }
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Projects: graphsvc.EveryProject, Graph: g, Authz: authorizer}
	ctx := context.Background()

	exp, err := h.ExportGraph(ctx, connect.NewRequest(&graphv1.ExportGraphRequest{Namespace: "alm"}))
	if err != nil || exp.Msg.Nodes != 0 || exp.Msg.MediaType != "application/json" {
		t.Fatalf("export of an empty namespace: %+v %v", exp, err)
	}
	s := graph.Snapshot{Format: graph.SnapshotFormat, Namespace: "alm", Nodes: []graph.SnapshotNode{
		{Key: "NEED-1", Type: "alm@Need", Properties: map[string]any{"title": "Pay online"}},
	}}
	doc, _ := json.Marshal(s)
	call := func(roles string) (*connect.Response[graphv1.ImportGraphResponse], error) {
		req := connect.NewRequest(&graphv1.ImportGraphRequest{Document: string(doc), ProjectId: "PROJ-ROOT"})
		req.Header().Set(identity.HeaderSubject, "u")
		req.Header().Set(identity.HeaderOrg, "acme")
		req.Header().Set(identity.HeaderRoles, roles)
		return h.ImportGraph(ctx, req)
	}
	if _, err := call(""); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a caller who may not create the nodes cannot import them: %v", err)
	}
	if cs, err := g.ListChanges(ctx, graph.ChangesFilter{Namespace: "alm"}); err != nil || len(cs) != 0 {
		t.Fatalf("a refused import leaves no change: %+v %v", cs, err)
	}
	out, err := call("admin")
	if err != nil {
		t.Fatal(err)
	}
	if out.Msg.Change == nil || out.Msg.Created != 1 || out.Msg.Change.Status != string(domain.ChangeActive) {
		t.Fatalf("import as a change: %+v", out.Msg)
	}
	if _, err := h.ImportGraph(ctx, connect.NewRequest(&graphv1.ImportGraphRequest{Document: "{"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a malformed snapshot: %v", err)
	}
}

// The OKF export streams a zip archive (ADR 0111): a first message naming it, then its bytes; an anonymous caller is
// refused, a streaming call passing none of the unary interceptors.
func TestExportGraphOkfStreamsAZip(t *testing.T) {
	g := graph.New(graph.NewMemory())
	_, h := graphv1connect.NewGraphServiceHandler(&graphsvc.Handler{Projects: graphsvc.EveryProject, Authz: authz.AllowAll{}, Graph: g})
	srv := httptest.NewServer(h)
	defer srv.Close()
	client := graphv1connect.NewGraphServiceClient(srv.Client(), srv.URL)
	ctx := context.Background()

	read := func(subject string) (*graphv1.ExportGraphOkfResponse, []byte, error) {
		req := connect.NewRequest(&graphv1.ExportGraphOkfRequest{Namespace: "alm"})
		if subject != "" {
			req.Header().Set(identity.HeaderSubject, subject)
		}
		st, err := client.ExportGraphOkf(ctx, req)
		if err != nil {
			return nil, nil, err
		}
		var head *graphv1.ExportGraphOkfResponse
		var data []byte
		for st.Receive() {
			if head == nil {
				head = st.Msg()
				continue
			}
			data = append(data, st.Msg().Chunk...)
		}
		return head, data, st.Err()
	}
	if _, _, err := read(""); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("an anonymous caller: %v", err)
	}
	head, data, err := read("u")
	if err != nil {
		t.Fatal(err)
	}
	if head == nil || head.MediaType != "application/zip" || !strings.HasSuffix(head.Filename, ".okf.zip") {
		t.Fatalf("head: %+v", head)
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(z.File) != 1 || z.File[0].Name != "alm/index.md" {
		t.Fatalf("an empty namespace is its index: %v %+v", err, z)
	}
}
