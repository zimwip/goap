package okf_test

import (
	"archive/zip"
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/okf"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/typecat"
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
			{Key: "REQ-1", Type: "alm@Requirement", State: "approved", Properties: map[string]any{"title": "Card payment", "state": "x"}},
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
	var fm map[string]any
	if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"key": "NEED:1", "type": "alm@Need", "title": "Pay online", "state": "draft", "owner": "ORG-DEFAULT", "namespace": "alm", "baseline": "B1",
		"aliases": []any{"NEED:1"}, "tags": []any{"alm/Need", "state/draft"},
		"refines":        []any{"[[Requirement/REQ-1|Card payment]]"},
		"linkProperties": []any{map[string]any{"type": "alm@refines", "to": "REQ-1", "properties": map[string]any{"why": "x"}}},
	}
	if !reflect.DeepEqual(fm, want) {
		t.Fatalf("front matter:\n%#v\nwant\n%#v", fm, want)
	}
	if !strings.Contains(parts[1], `- '[[Requirement/REQ-1|Card payment]]'`) || !strings.HasPrefix(parts[1], "key: ") {
		t.Fatalf("ordered front matter, quoted wikilinks:\n%s", parts[1])
	}
	for _, want := range []string{"# Pay online\n", "## Description\n\nThe customer\npays by card.\n", "- refines: [Card payment](../Requirement/REQ-1.md)"} {
		if !strings.Contains(parts[2], want) {
			t.Fatalf("body misses %q:\n%s", want, parts[2])
		}
	}
	if req := got["alm/Requirement/REQ-1.md"]; !strings.Contains(req, "## Referenced by\n\n- [Pay online](../Need/NEED-1.md) refines") {
		t.Fatalf("back link:\n%s", req)
	}
	if req := got["alm/Requirement/REQ-1.md"]; !strings.Contains(req, "\nprop_state: x\n") {
		t.Fatalf("a property named like a key of the front matter takes the prefix:\n%s", req)
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
