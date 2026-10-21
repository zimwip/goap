package graph

import (
	"context"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

func TestGraphStats(t *testing.T) { forEachRepo(t, testGraphStats) }

func testGraphStats(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	// NEED-1 <-satisfies- REQ-1 <-verifies- TST-1, then an isolated node and a new version of NEED-1: the link satisfying
	// it is left on its first version (suspect)
	need := f.need.Ref()
	if _, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Title: "More", Baseline: f.base.ID, By: "test", Edits: []NodeEdit{
		{Key: "ISO-1", Type: "Need", Props: map[string]any{"title": "Alone"}},
		{Pre: &need, Props: map[string]any{"title": "Pay online, safely"}},
	}}); err != nil {
		t.Fatal(err)
	}
	s, err := g.GraphStats(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Namespace != domain.DefaultNamespace || s.Branch != domain.MainBranch || s.Baseline.ID == "" {
		t.Fatalf("read at %s/%s %q", s.Namespace, s.Branch, s.Baseline.ID)
	}
	st := s.State
	if st.Nodes != 4 || st.Links != 2 || st.Suspect != 1 || st.External != 0 || st.Isolated != 1 {
		t.Fatalf("state: nodes %d links %d suspect %d external %d isolated %d", st.Nodes, st.Links, st.Suspect, st.External, st.Isolated)
	}
	// TST-1 -> REQ-1 -> NEED-1 (a suspect link is a link still), ISO-1 alone
	if st.ComponentCount != 2 || st.Components[0] != 3 || st.Components[1] != 1 {
		t.Fatalf("components %d %v", st.ComponentCount, st.Components)
	}
	if st.Degree.Max != 2 || st.Degree.Min != 0 || st.Degree.Sum != 4 || st.Out.Sum != 2 || st.In.Sum != 2 {
		t.Fatalf("degrees %+v out %+v in %+v", st.Degree, st.Out, st.In)
	}
	if st.Versions.Max != 2 || st.Versions.Min != 1 {
		t.Fatalf("versions %+v", st.Versions)
	}
	if len(st.Types) != 3 || st.Types[0].Type != "Need" || st.Types[0].Nodes != 2 || st.Types[0].Isolated != 1 {
		t.Fatalf("types %+v", st.Types)
	}
	byLink := map[string]LinkTypeStats{}
	for _, lt := range st.LinkTypes {
		byLink[lt.Type] = lt
	}
	if v := byLink["verifies"]; v.Links != 1 || v.Sources != 1 || v.Targets != 1 || len(v.Pairs) != 1 || v.Pairs[0] != (TypePair{From: "TestCase", To: "Requirement", Count: 1}) {
		t.Fatalf("verifies %+v", v)
	}
	if v := byLink["satisfies"]; v.Links != 1 || v.Suspect != 1 {
		t.Fatalf("satisfies %+v", v)
	}
	if len(st.Hubs) != 3 || st.Hubs[0].Key != "REQ-1" || st.Histogram[0].Count != 1 || st.Histogram[1].Count != 2 || st.Histogram[2].Count != 1 {
		t.Fatalf("hubs %+v histogram %+v", st.Hubs, st.Histogram)
	}
	// the store: five versions of four nodes, the links written, the changes
	if n := sum(s.Store[StatNodesByType]); n != 4 {
		t.Fatalf("node rows %d (%v)", n, s.Store[StatNodesByType])
	}
	if n := sum(s.Store[StatVersionsByType]); n != 5 || s.Store[StatVersionsPerNode]["2"] != 1 || s.Store[StatVersionsPerNode]["1"] != 3 {
		t.Fatalf("versions %v per node %v", s.Store[StatVersionsByType], s.Store[StatVersionsPerNode])
	}
	if s.Store[StatLinkRowsByType]["verifies"] != 1 || s.Store[StatLinkRowsByType]["satisfies"] < 1 {
		t.Fatalf("link rows %v", s.Store[StatLinkRowsByType])
	}
	if sum(s.Store[StatChangesByStatus]) == 0 || sum(s.Store[StatBaselinesByKind]) == 0 || sum(s.Store[StatLogByType]) == 0 {
		t.Fatalf("store %v", s.Store)
	}
	if sum(s.Store[StatLinkSetsByMode]) != 5 {
		t.Fatalf("link sets %v", s.Store[StatLinkSetsByMode])
	}
	if s.Branches.Lasting == 0 || s.Branches.Names[0] != domain.MainBranch {
		t.Fatalf("branches %+v", s.Branches)
	}
	// the overview names the namespace with the same counts
	ov, err := g.GraphOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found, domainListed := false, false
	for _, n := range ov {
		if n.Namespace == domain.DefaultNamespace {
			found = n.Nodes == 4 && n.Versions == 5 && n.Types == 3
		}
		// a domain in force holding no node is listed too, empty
		if n.Namespace == "methodology" {
			domainListed = n.Nodes == 0
		}
	}
	if !found || !domainListed {
		t.Fatalf("overview %+v", ov)
	}
	// a namespace with nothing committed: an empty state, no error
	if e, err := g.GraphStats(ctx, "nothing", ""); err != nil || e.State.Nodes != 0 || e.Baseline.ID != "" {
		t.Fatalf("empty namespace: %+v, %v", e, err)
	}
}
