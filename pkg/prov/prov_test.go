package prov

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/domain"
)

func entries(t *testing.T, evs ...domain.ImpactEvent) []domain.LogEntry {
	t.Helper()
	var out []domain.LogEntry
	for i, ev := range evs {
		e, err := domain.ImpactEntry(ev)
		if err != nil {
			t.Fatal(err)
		}
		e.Seq = int64(i + 1)
		out = append(out, e)
	}
	return out
}

func find(t *testing.T, doc Document, id string) map[string]any {
	t.Helper()
	for _, n := range doc.Graph {
		if n["@id"] == id {
			return n
		}
	}
	t.Fatalf("no %s", id)
	return nil
}

func refs(v any) map[string]bool {
	out := map[string]bool{}
	l, ok := v.([]any)
	if !ok {
		l = []any{v}
	}
	for _, x := range l {
		if m, ok := x.(map[string]any); ok {
			out[m["@id"].(string)] = true
		}
	}
	return out
}

// A version merged on landing derives from the one written; an adopted flow replaces the runs it makes stale; a
// rebased impact names the newer head it is checked against.
func TestImpactEvents(t *testing.T) {
	c := domain.Change{ID: "c1", Title: "t", CreatedAt: time.Unix(0, 0)}
	pre := domain.NodeRef{ID: "n1", Version: 1}
	head := domain.NodeRef{ID: "n1", Version: 2}
	post := domain.NodeRef{ID: "n1", Version: 3}
	merged := domain.NodeRef{ID: "n1", Version: 4}
	doc, err := Export(c, entries(t,
		domain.ImpactEvent{ID: "v1", Change: "c1", Impact: "i1", Op: domain.ImpactDeclared, By: "bob",
			State: &domain.ChangeImpact{ID: "i1", Key: "REQ-1", Type: "alm@Requirement", Intent: domain.IntentModified, Pre: &pre}},
		domain.ImpactEvent{ID: "v2", Change: "c1", Impact: "i1", Op: domain.ImpactRebased, Pre: &head},
		domain.ImpactEvent{ID: "v3", Change: "c1", Impact: "i1", Op: domain.ImpactWritten, Post: &post, Execution: "e1"},
		domain.ImpactEvent{ID: "v4", Change: "c1", Op: domain.ImpactAdopted, Flow: "f1", Stale: []string{"e0"}, By: "bob"},
		domain.ImpactEvent{ID: "v5", Change: "c1", Impact: "i1", Op: domain.ImpactLanded, Landed: &merged, Baseline: "b9"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if !refs(find(t, doc, "urn:goap:node:n1@v3")["prov:wasRevisionOf"])["urn:goap:node:n1@v2"] {
		t.Fatal("the written version revises the head it was rebased on")
	}
	if !refs(find(t, doc, "urn:goap:node:n1@v4")["prov:wasDerivedFrom"])["urn:goap:node:n1@v3"] {
		t.Fatal("the merge version derives from the version written")
	}
	b := find(t, doc, "urn:goap:baseline:b9")
	if !refs(b["prov:hadMember"])["urn:goap:node:n1@v4"] || !refs(b["prov:wasGeneratedBy"])["urn:goap:change:c1"] {
		t.Fatalf("baseline: %v", b)
	}
	if !refs(find(t, doc, "urn:goap:adoption:v4")["goap:replaces"])["urn:goap:execution:e0"] {
		t.Fatal("the adoption replaces the stale runs")
	}
	if n := find(t, doc, "urn:goap:node:n1"); n["goap:type"] != "alm@Requirement" || n["goap:key"] != "REQ-1" {
		t.Fatalf("node: %v", n)
	}
	if _, err := json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
}

func TestBadPayload(t *testing.T) {
	_, err := Export(domain.Change{ID: "c1"}, []domain.LogEntry{{Seq: 1, Type: "fact.artifact", Payload: json.RawMessage(`[`)}})
	if err == nil {
		t.Fatal("a broken entry is reported")
	}
}
