package graphsvc_test

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/typecat"
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
	h := &graphsvc.Handler{Graph: g, Authz: authorizer}
	ctx := context.Background()

	exp, err := h.ExportGraph(ctx, connect.NewRequest(&graphv1.ExportGraphRequest{Namespace: "alm"}))
	if err != nil || exp.Msg.Nodes != 0 || exp.Msg.MediaType != "application/json" {
		t.Fatalf("export of an empty namespace: %+v %v", exp, err)
	}
	okfExp, err := h.ExportGraph(ctx, connect.NewRequest(&graphv1.ExportGraphRequest{Namespace: "alm", Format: "okf"}))
	if err != nil || okfExp.Msg.MediaType != "application/zip" || len(okfExp.Msg.Archive) == 0 || okfExp.Msg.Document != "" {
		t.Fatalf("OKF export of an empty namespace, its index only (ADR 0111): %+v %v", okfExp, err)
	}
	if _, err := h.ExportGraph(ctx, connect.NewRequest(&graphv1.ExportGraphRequest{Namespace: "alm", Format: "csv"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("an unknown export format: %v", err)
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
