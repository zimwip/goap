package graph

import (
	"context"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/domain"
)

func TestBlackboardFacets(t *testing.T) { forEachRepo(t, testBlackboardFacets) }

// The blackboard holds the built-in facets (options, active option, decision points) and the ones the providers of
// Graph.Facets compute from the change; WithFacet leaves the original untouched.
func testBlackboardFacets(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	g.Facets = map[string]BlackboardFacet{
		"title": func(c domain.Change, _ time.Time) any { return "facet of " + c.Title },
	}
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "c", BaselineID: f.base.ID}))
	bb := must[domain.Blackboard](t)(g.Blackboard(ctx, c.ID))
	if got := domain.Facet[string](bb, "title"); got != "facet of c" {
		t.Fatalf("provider facet = %q", got)
	}
	for _, name := range []string{domain.FacetOptions, domain.FacetActiveOption, domain.FacetDecisionPoints} {
		if _, ok := bb.Facets[name]; !ok {
			t.Fatalf("no built-in facet %s: %v", name, bb.Facets)
		}
	}
	if len(domain.OptionsOf(bb)) != 0 || domain.ActiveOptionOf(bb) != "" || len(domain.DecisionPointsOf(bb)) != 0 {
		t.Fatalf("a fresh change has no option or decision point: %v", bb.Facets)
	}
	other := bb.WithFacet(domain.FacetActiveOption, "x")
	if domain.ActiveOptionOf(other) != "x" || domain.ActiveOptionOf(bb) != "" {
		t.Fatalf("WithFacet must not change the original: %v %v", other.Facets, bb.Facets)
	}
	if domain.Facet[int](bb, "title") != 0 {
		t.Fatal("a facet of another type reads as the zero value")
	}
}
