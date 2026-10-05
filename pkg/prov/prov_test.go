package prov

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/review"
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
		domain.ImpactEvent{ID: "v1", Change: "c1", Impact: "i1", Op: domain.ImpactProposed, By: "bob",
			State: &domain.ChangeImpact{ID: "i1", Key: "REQ-1", Type: "alm@Requirement", Intent: domain.IntentModified, Pre: &pre}},
		domain.ImpactEvent{ID: "v2", Change: "c1", Impact: "i1", Op: domain.ImpactRebased, Pre: &head},
		domain.ImpactEvent{ID: "v3", Change: "c1", Impact: "i1", Op: domain.ImpactTransitioned, Post: &post, Execution: "e1"},
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

// A derogation version is an entity attributed to its signatory (ADR 0075 §2).
func TestDerogationMapping(t *testing.T) {
	b := &builder{nodes: map[string]map[string]any{}}
	n := map[string]any{}
	b.derogation(n, domain.ChangeItem{Kind: "derogation", Data: map[string]any{"key": "DRG-1", "rule": "tests pass", "target": "REQ-1",
		"expires": "2030-01-01T00:00:00Z", "status": "open", "signatory": "alice"}})
	if n["goap:rule"] != "tests pass" || n["goap:target"] != "REQ-1" || n["goap:expires"] != "2030-01-01T00:00:00Z" || n["label"] != "derogation DRG-1" {
		t.Fatalf("derogation: %v", n)
	}
	if got := refs(n["prov:wasAttributedTo"]); !got[principalIRI("alice")] {
		t.Fatalf("attributed to the signatory: %v", n)
	}
}

// A review object is an activity grouping the reviewed events of its entries, with the record versions as entities
// (ADR 0080).
func TestReviewObjectMapping(t *testing.T) {
	c := domain.Change{ID: "c1", Title: "t", CreatedAt: time.Unix(0, 0)}
	sub := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	it := domain.ChangeItem{ID: "it1", Kind: review.KindReview, Status: domain.ItemProposed, ProducedBy: "alice", CreatedAt: sub, Data: map[string]any{
		"key": "REV-1", "status": review.Submitted, "comment": "all good", "by": "alice", "submittedAt": sub.Format(time.RFC3339Nano),
		"entries": []any{map[string]any{"impact": "i1", "outcome": "accept"}}}}
	fe, err := domain.FactEntry("c1", it)
	if err != nil {
		t.Fatal(err)
	}
	fe.Seq = 1
	ev := entries(t, domain.ImpactEvent{ID: "e1", Change: "c1", Impact: "i1", Op: domain.ImpactReviewed, By: "alice",
		Review: &domain.Review{Status: domain.ReviewAccepted, By: "alice", Comment: "fine", ReviewID: "REV-1"}})
	ev[0].Seq = 2
	doc, err := Export(c, append([]domain.LogEntry{fe}, ev...))
	if err != nil {
		t.Fatal(err)
	}
	act := find(t, doc, reviewObjectIRI("REV-1"))
	if act["goap:status"] != review.Submitted || act["rdfs:comment"] != "all good" || act["prov:endedAtTime"] == nil || !refs(act["goap:about"])[impactIRI("i1")] ||
		!refs(act["prov:wasAssociatedWith"])[principalIRI("alice")] {
		t.Fatalf("activity: %v", act)
	}
	rv := find(t, doc, iri("review", "e1"))
	if !refs(rv["goap:partOf"])[reviewObjectIRI("REV-1")] {
		t.Fatalf("the reviewed event is part of the review object: %v", rv)
	}
	if rec := find(t, doc, itemIRI("it1")); !refs(rec["goap:reviewOf"])[reviewObjectIRI("REV-1")] {
		t.Fatalf("record: %v", rec)
	}
}
