package pbconv

import (
	"reflect"
	"testing"
	"time"

	"github.com/zimwip/goap/contracts/criticality"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/journal"
)

// A blackboard sent to the guardian of a change (ADR 0168) comes back with each facet as the type its readers expect.
func TestBlackboardRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	ref := domain.NodeRef{ID: "n1", Version: 2}
	bb := domain.Blackboard{
		Change:     domain.Change{ID: "c1", Methodology: "sdlc", State: "framing"},
		Nodes:      map[domain.NodeRef]domain.NodeView{ref: {Node: domain.Node{ID: "n1", Version: 2, Key: "REQ-1", Type: "alm@Requirement"}}},
		Neighbors:  map[domain.NodeRef]domain.Node{},
		Vars:       map[string]any{"item": "REQ-1"},
		Supertypes: map[string][]string{"alm@Requirement": {"alm@Item"}},
		At:         at,
		Facets: map[string]any{
			domain.FacetActiveOption:      "opt-1",
			domain.FacetOptions:           []domain.Flow{{ID: "opt-1"}},
			domain.FacetDependencies:      []domain.Dependency{{ID: "c0", Status: domain.ChangeApplied, Satisfied: true}},
			domain.FacetHistory:           journal.History{Steps: []journal.StepRun{{Path: "p/s", Outcome: "succeeded", Current: true}}},
			domain.FacetCriticalityPolicy: criticality.Policy{Oracles: []string{"human"}, Sampling: true},
		},
	}
	pb, err := BlackboardToPB(bb)
	if err != nil {
		t.Fatal(err)
	}
	got, err := BlackboardFromPB(pb)
	if err != nil {
		t.Fatal(err)
	}
	if domain.ActiveOptionOf(got) != "opt-1" || len(domain.OptionsOf(got)) != 1 {
		t.Fatalf("options: %+v", got.Facets)
	}
	if d := domain.Facet[[]domain.Dependency](got, domain.FacetDependencies); len(d) != 1 || !d[0].Satisfied {
		t.Fatalf("dependencies: %+v", d)
	}
	if h := domain.Facet[journal.History](got, domain.FacetHistory); !h.Last()["p/s"].Current {
		t.Fatalf("history: %+v", h)
	}
	if p := domain.Facet[criticality.Policy](got, domain.FacetCriticalityPolicy); !p.Sampling {
		t.Fatalf("criticality policy: %+v", p)
	}
	if !reflect.DeepEqual(got.Supertypes, bb.Supertypes) || got.Vars["item"] != "REQ-1" || !got.At.Equal(at) || got.Change.State != "framing" {
		t.Fatalf("blackboard: %+v", got)
	}
	if v, ok := got.Nodes[ref]; !ok || v.Key != "REQ-1" {
		t.Fatalf("nodes: %+v", got.Nodes)
	}
}
