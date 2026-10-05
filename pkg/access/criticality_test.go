package access_test

import (
	"context"
	"github.com/zimwip/goap/internal/graphsvc"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/criticality"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/typecat"
)

// The policy of a criticality level is resolved along the unit chain, the nearest unit holding one for the level
// winning, the compiled-in table where none does (ADR 0075 §3, design rule 3).
func TestCriticalityPolicyResolution(t *testing.T) {
	node := func(id, key, typ, owner string, props map[string]any) domain.Node {
		return domain.Node{ID: domain.NodeID(id), Key: key, Type: typ, Namespace: "organisation", Properties: props, Owner: domain.NodeID(owner)}
	}
	pol := func(id, owner string, l criticality.Level, p criticality.Policy) domain.Node {
		return node(id, "CP-"+id, access.NodeTypeCriticalityPolicy, owner, access.CriticalityProps(l, p))
	}
	nodes := []domain.Node{
		node("1", "ORG-DEFAULT", access.NodeTypeOrgUnit, "", nil), node("2", "DEP", access.NodeTypeOrgUnit, "", nil), node("3", "TEAM", access.NodeTypeOrgUnit, "", nil),
		node("4", "OTHER", access.NodeTypeOrgUnit, "", nil),
		pol("10", "1", criticality.C3, criticality.Policy{Oracles: []string{"human", "tool"}, SignatoryRole: "ciso", MaxDerogation: 48 * time.Hour}),
		pol("11", "2", criticality.C3, criticality.Policy{Oracles: []string{"human"}, SignatoryRole: "dep_head", MaxDerogation: 24 * time.Hour}),
		pol("12", "4", criticality.C1, criticality.Policy{Oracles: []string{"human"}}),
		// a policy whose owner is no unit of the organisation, and one that names no level, are problems
		pol("13", "99", criticality.C2, criticality.Policy{}),
		node("14", "CP-bad", access.NodeTypeCriticalityPolicy, "1", map[string]any{"level": "C9"}),
	}
	links := []domain.Link{
		{Type: access.LinkPartOf, From: domain.NodeRef{ID: "2"}, To: domain.NodeRef{ID: "1"}},
		{Type: access.LinkPartOf, From: domain.NodeRef{ID: "3"}, To: domain.NodeRef{ID: "2"}},
		{Type: access.LinkPartOf, From: domain.NodeRef{ID: "4"}, To: domain.NodeRef{ID: "1"}},
	}
	s := access.BuildSnapshot(typecat.Builtin().Structures(), "b", nodes, links)
	if len(s.Problems) != 2 {
		t.Fatalf("problems: %v", s.Problems)
	}
	if p := s.CriticalityPolicy("TEAM", criticality.C3); p.SignatoryRole != "dep_head" || p.MaxDerogation != 24*time.Hour {
		t.Errorf("the nearest unit wins: %+v", p)
	}
	if p := s.CriticalityPolicy("OTHER", criticality.C3); p.SignatoryRole != "ciso" || len(p.Oracles) != 2 {
		t.Errorf("a unit inherits its ancestor: %+v", p)
	}
	if p := s.CriticalityPolicy("TEAM", criticality.C1); !p.Accepts("model") || !p.Sampling {
		t.Errorf("the policy of a sibling does not apply, the table does: %+v", p)
	}
	if p := s.CriticalityPolicy("OTHER", criticality.C1); p.Accepts("tool") {
		t.Errorf("OTHER holds its own C1: %+v", p)
	}
	c := domain.Change{OwnerOrg: "TEAM"}
	if p := s.CriticalityOf(c, criticality.C3); p.SignatoryRole != "dep_head" {
		t.Errorf("a change is resolved from its unit: %+v", p)
	}
	var none *access.Snapshot
	if p := none.CriticalityPolicy("TEAM", criticality.C2); !p.Accepts("tool") || p.Accepts("model") {
		t.Errorf("no snapshot, the table: %+v", p)
	}
	// round trip of the properties
	l, p, err := access.CriticalityPolicyFromProps(access.CriticalityProps(criticality.C2, criticality.Policy{Oracles: []string{"tool"}, Sampling: true, SignatoryRole: "r", MaxDerogation: 36 * time.Hour}))
	if err != nil || l != criticality.C2 || !p.Sampling || p.SignatoryRole != "r" || p.MaxDerogation != 36*time.Hour || len(p.Oracles) != 1 {
		t.Errorf("props: %v %+v %v", l, p, err)
	}
	if _, _, err := access.CriticalityPolicyFromProps(map[string]any{"level": "C1", "maxDerogationHours": -1.0}); err == nil {
		t.Error("a negative lifetime is refused")
	}
}

// Through a real graph: a unit owns a CriticalityPolicy node of the organisation domain, its sub-unit resolves it, the
// facet reads the snapshot last built and the compiled-in table applies before any.
func TestCriticalityPolicyFromTheGraph(t *testing.T) {
	ctx := context.Background()
	g, _ := setup(t)
	dir := &access.Directory{Graph: g, TTL: 1}
	if p := dir.CriticalityFacet()(domain.Change{OwnerOrg: "TEAM"}, time.Time{}).(criticality.Policy); !p.Accepts("tool") {
		t.Fatalf("before any snapshot: %+v", p)
	}
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedUnit(ctx, g, "DEP", "Department", "department", ""); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedUnit(ctx, g, "TEAM", "Team", "team", "DEP"); err != nil {
		t.Fatal(err)
	}
	want := criticality.Policy{Oracles: []string{"human"}, SignatoryRole: "dep_head", MaxDerogation: 12 * time.Hour}
	if err := graphsvc.SeedCriticalityPolicy(ctx, g, "DEP", criticality.C2, want); err != nil {
		t.Fatal(err)
	}
	s, err := dir.Snapshot(ctx)
	if err != nil || len(s.Problems) != 0 {
		t.Fatalf("snapshot: %v %v", err, s.Problems)
	}
	c := domain.Change{OwnerOrg: "TEAM", Data: map[string]any{domain.DataCriticality: "C2"}}
	if p := s.CriticalityOf(c, criticality.C2); p.SignatoryRole != "dep_head" || p.MaxDerogation != 12*time.Hour || p.Accepts("tool") {
		t.Fatalf("resolved: %+v", p)
	}
	if p := dir.CriticalityResolver()(ctx, c, criticality.C2); p.SignatoryRole != "dep_head" {
		t.Fatalf("resolver: %+v", p)
	}
	if p := dir.CriticalityFacet()(c, time.Time{}).(criticality.Policy); p.SignatoryRole != "dep_head" {
		t.Fatalf("facet: %+v", p)
	}
}
