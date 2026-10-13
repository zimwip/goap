package okf_test

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/okf"
	"github.com/zimwip/goap/pkg/typecat"
)

// A snapshot is written as a zip of one Markdown file per node: the identity, properties and links in the YAML front matter, the
// name as heading, the text area attributes as sections, links and back links as relative Markdown links.
func TestRender(t *testing.T) {
	ds, err := def.LoadDomains("../../domains")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(ds...)
	if err != nil {
		t.Fatal(err)
	}
	s := graph.Snapshot{Format: graph.SnapshotFormat, Namespace: "alm", Branch: "main", Baseline: "B1", ExportedAt: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC),
		Nodes: []graph.SnapshotNode{
			{Key: "NEED:1", Type: "alm@Need", State: "draft", Owner: "ORG-DEFAULT", Properties: map[string]any{"title": "Pay online", "description": "The customer\npays by card."},
				Links: []graph.SnapshotLink{{Type: "alm@refines", To: "REQ-1", Properties: map[string]any{"why": "x"}}}},
			{Key: "NEED-1", Type: "alm@Need", Properties: map[string]any{"title": "Same file name"}},
			{Key: "REQ-1", Type: "alm@Requirement", Properties: map[string]any{"title": "Card payment"}},
		}}
	var buf bytes.Buffer
	if err := okf.Write(&buf, s, okf.FromTypes(cat.Type), s.ExportedAt); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r)
		got[f.Name] = string(b)
	}
	if z.File[0].Name != "alm/index.md" {
		t.Fatalf("the index comes first: %s", z.File[0].Name)
	}
	need, ok := got["alm/Need/NEED-1.md"]
	if !ok || got["alm/Need/NEED-1-2.md"] == "" || got["alm/Requirement/REQ-1.md"] == "" || got["alm/index.md"] == "" {
		t.Fatalf("files: %v", keys(got))
	}
	parts := strings.SplitN(need, "---\n", 3)
	if len(parts) != 3 || parts[0] != "" {
		t.Fatalf("front matter: %q", need)
	}
	var fm okf.FrontMatter
	if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
		t.Fatal(err)
	}
	if fm.Key != "NEED:1" || fm.Type != "alm@Need" || fm.Title != "Pay online" || fm.State != "draft" || fm.Baseline != "B1" ||
		fm.Properties["title"] != "Pay online" || fm.Properties["description"] != nil ||
		len(fm.Links) != 1 || fm.Links[0].Path != "../Requirement/REQ-1.md" || fm.Links[0].Properties["why"] != "x" {
		t.Fatalf("front matter: %+v", fm)
	}
	for _, want := range []string{"# Pay online\n", "## Description\n\nThe customer\npays by card.\n", "- refines: [Card payment](../Requirement/REQ-1.md)"} {
		if !strings.Contains(parts[2], want) {
			t.Fatalf("body misses %q:\n%s", want, parts[2])
		}
	}
	if req := got["alm/Requirement/REQ-1.md"]; !strings.Contains(req, "## Referenced by\n\n- [Pay online](../Need/NEED-1.md) refines") {
		t.Fatalf("back link:\n%s", req)
	}
	if idx := got["alm/index.md"]; !strings.Contains(idx, "format: goap.okf/v1") || !strings.Contains(idx, "- [Card payment](Requirement/REQ-1.md) `REQ-1`") {
		t.Fatalf("index:\n%s", idx)
	}

}

func keys(m map[string]string) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}
