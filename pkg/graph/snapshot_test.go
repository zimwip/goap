package graph

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// A snapshot is the state of a namespace without its history (ADR 0104): exported from the head of main, imported as
// one change that creates what the graph lacks, brings what differs to the snapshot and leaves the rest alone.
func TestSnapshotExportImport(t *testing.T) { forEachRepo(t, testSnapshotExportImport) }

func testSnapshotExportImport(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	s := must[Snapshot](t)(g.ExportSnapshot(ctx, "", "", ""))
	if s.Format != SnapshotFormat || s.Namespace != domain.DefaultNamespace || s.Baseline != string(f.base.ID) || len(s.Nodes) != 3 {
		t.Fatalf("export of the head of main: %+v", s)
	}
	if keys := []string{s.Nodes[0].Key, s.Nodes[1].Key, s.Nodes[2].Key}; !slices.Equal(keys, []string{"NEED-1", "REQ-1", "TST-1"}) {
		t.Fatalf("nodes sorted by key: %v", keys)
	}
	if l := s.Nodes[1].Links; len(l) != 1 || l[0].Type != "satisfies" || l[0].To != "NEED-1" {
		t.Fatalf("links by key: %+v", l)
	}
	// JSON round trip
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	s = must[Snapshot](t)(ParseSnapshot(data))

	// the graph already holds it: nothing to import, no change
	res := must[SnapshotImportResult](t)(g.ImportSnapshot(ctx, SnapshotImport{Snapshot: s, ProjectID: "PROJ-ROOT"}))
	if res.Change != "" || res.Unchanged != 3 {
		t.Fatalf("nothing to import: %+v", res)
	}

	// a title changed, a node added, a link removed, a link to nowhere
	s.Nodes[1].Properties = map[string]any{"title": "Use PSP v2"}
	s.Nodes[2].Links = nil
	s.Nodes = append(s.Nodes, SnapshotNode{Key: "TST-2", Type: "TestCase", Links: []SnapshotLink{{Type: "verifies", To: "REQ-1"}, {Type: "verifies", To: "NOPE"}}})
	res = must[SnapshotImportResult](t)(g.ImportSnapshot(ctx, SnapshotImport{Snapshot: s, ProjectID: "PROJ-ROOT"}))
	if res.Change == "" || res.Created != 1 || res.Updated != 2 || res.Unchanged != 1 || len(res.Warnings) != 1 {
		t.Fatalf("import as a change: %+v", res)
	}
	c := must[domain.Change](t)(g.Change(ctx, res.Change))
	if c.Status != domain.ChangeActive || c.ProjectID != "PROJ-ROOT" {
		t.Fatalf("the change stays open for review: %+v", c)
	}
	if latest := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID})); latest.Version != f.req.Version {
		t.Fatal("nothing lands before the change is merged")
	}
	if err := g.acceptAll(ctx, res.Change); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(ctx, res.Change, ""); err != nil {
		t.Fatal(err)
	}
	after := must[Snapshot](t)(g.ExportSnapshot(ctx, "", "", ""))
	byKey := map[string]SnapshotNode{}
	for _, n := range after.Nodes {
		byKey[n.Key] = n
	}
	if len(after.Nodes) != 4 || byKey["REQ-1"].Properties["title"] != "Use PSP v2" || len(byKey["TST-1"].Links) != 0 ||
		len(byKey["TST-2"].Links) != 1 || byKey["TST-2"].Links[0].To != "REQ-1" {
		t.Fatalf("the graph is the snapshot once merged: %+v", after.Nodes)
	}
	// and importing the state it has is a no-op again
	res = must[SnapshotImportResult](t)(g.ImportSnapshot(ctx, SnapshotImport{Snapshot: after, ProjectID: "PROJ-ROOT"}))
	if res.Change != "" || res.Unchanged != 4 {
		t.Fatalf("nothing to import after the merge: %+v", res)
	}
}

// A snapshot imported into an empty graph creates every node, links between new nodes included.
func TestSnapshotImportIntoEmptyGraph(t *testing.T) { forEachRepo(t, testSnapshotImportIntoEmptyGraph) }

func testSnapshotImportIntoEmptyGraph(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	s := Snapshot{Format: SnapshotFormat, Namespace: domain.DefaultNamespace, Nodes: []SnapshotNode{
		{Key: "A", Type: "Need", Properties: map[string]any{"title": "a"}, Links: []SnapshotLink{{Type: "refines", To: "B", Properties: map[string]any{"why": "x"}}}},
		{Key: "B", Type: "Need", Links: []SnapshotLink{{Type: "refines", To: "A"}}},
	}}
	res := must[SnapshotImportResult](t)(g.ImportSnapshot(ctx, SnapshotImport{Snapshot: s, ProjectID: "PROJ-ROOT", Title: "seed"}))
	if res.Change == "" || res.Created != 2 {
		t.Fatalf("created: %+v", res)
	}
	if err := g.acceptAll(ctx, res.Change); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(ctx, res.Change, ""); err != nil {
		t.Fatal(err)
	}
	got := must[Snapshot](t)(g.ExportSnapshot(ctx, "", "", ""))
	if len(got.Nodes) != 2 || len(got.Nodes[0].Links) != 1 || got.Nodes[0].Links[0].Properties["why"] != "x" || len(got.Nodes[1].Links) != 1 {
		t.Fatalf("round trip: %+v", got.Nodes)
	}
	if _, err := ParseSnapshot([]byte(`{"format":"other","namespace":"x","nodes":[]}`)); err == nil {
		t.Fatal("an unknown format is refused")
	}
}
