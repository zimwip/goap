package access_test

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/typecat"
)

// A principal may take a project as its active one when it holds a platform role, or a role an Assignment grants
// on that project or one above it; a stranger, or an Assignment on a sibling project, gives no access.
func TestMayAccessProject(t *testing.T) {
	node := func(id, key, typ string, props map[string]any) domain.Node {
		return domain.Node{ID: domain.NodeID(id), Key: key, Type: typ, Namespace: "organisation", Properties: props}
	}
	lnk := func(typ, from, to string) domain.Link {
		return domain.Link{Type: typ, From: domain.NodeRef{ID: domain.NodeID(from)}, To: domain.NodeRef{ID: domain.NodeID(to)}}
	}
	roles := func(r ...string) map[string]any { return access.Assignment{Roles: r}.Props() }
	nodes := []domain.Node{
		node("1", "PROJ-ROOT", access.NodeTypeProjectUnit, nil), node("2", "PROJ-A", access.NodeTypeProjectUnit, nil),
		node("3", "PROJ-A1", access.NodeTypeProjectUnit, nil), node("4", "PROJ-B", access.NodeTypeProjectUnit, nil),
		node("5", "DEP-ONE", access.NodeTypeOrgUnit, nil), node("6", "ASG:DEP-ONE/PROJ-A", access.NodeTypeAssignment, roles("developer")),
		node("7", "DEP-TWO", access.NodeTypeOrgUnit, nil), node("8", "ASG:DEP-TWO/PLATFORM", access.NodeTypeAssignment, roles(access.RoleAdmin)),
	}
	links := []domain.Link{
		lnk(access.LinkProjectPartOf, "1", "1"), lnk(access.LinkProjectPartOf, "2", "1"), lnk(access.LinkProjectPartOf, "3", "2"), lnk(access.LinkProjectPartOf, "4", "1"),
		lnk(access.LinkAssignsOrg, "6", "5"), lnk(access.LinkAssignsProject, "6", "2"), lnk(access.LinkAssignsOrg, "8", "7"),
	}
	s := access.BuildSnapshot(typecat.Builtin().Structures(), "b", nodes, links)
	dev := authz.Principal{Subject: "dev", Org: "DEP-ONE"}
	admin := authz.Principal{Subject: "ada", Org: "DEP-TWO"}
	for _, c := range []struct {
		who     authz.Principal
		project string
		want    bool
	}{
		{dev, "PROJ-A", true}, {dev, "PROJ-A1", true}, {dev, "PROJ-B", false}, {dev, "PROJ-ROOT", false},
		{admin, "PROJ-B", true}, {authz.Principal{Subject: "stranger", Org: "OTHER"}, "PROJ-A", false}, {authz.Principal{}, "PROJ-A", false},
	} {
		if got := s.MayAccessProject(c.who, c.project); got != c.want {
			t.Errorf("%s on %s = %v, want %v", c.who.Subject, c.project, got, c.want)
		}
	}
}

// The methodologies of a project are its own and those of its ancestors, nearest first, without repeats; a project that
// is not in the organisation is not one.
func TestApplicableMethodologies(t *testing.T) {
	node := func(id, key string, methods ...any) domain.Node {
		return domain.Node{ID: domain.NodeID(id), Key: key, Type: access.NodeTypeProjectUnit, Namespace: "organisation", Properties: map[string]any{"methodologies": methods}}
	}
	lnk := func(from, to string) domain.Link {
		return domain.Link{Type: access.LinkProjectPartOf, From: domain.NodeRef{ID: domain.NodeID(from)}, To: domain.NodeRef{ID: domain.NodeID(to)}}
	}
	nodes := []domain.Node{node("1", "PROJ-ROOT", "base"), node("2", "PROJ-A", "sdlc", "base"), node("3", "PROJ-A1", "risk")}
	links := []domain.Link{lnk("1", "1"), lnk("2", "1"), lnk("3", "2")}
	s := access.BuildSnapshot(typecat.Builtin().Structures(), "b", nodes, links)
	if got := strings.Join(s.ApplicableMethodologies("PROJ-A1"), ","); got != "risk,sdlc,base" {
		t.Errorf("PROJ-A1: %s", got)
	}
	if got := strings.Join(s.ApplicableMethodologies("PROJ-ROOT"), ","); got != "base" {
		t.Errorf("root: %s", got)
	}
	if !s.HasProject("PROJ-A") || s.HasProject("PROJ-NOPE") {
		t.Error("HasProject")
	}
}

// The source mapping of a project is its own merged over its ancestors', the nearest naming a source winning (ADR 0154
// §8); a malformed mapping is a problem of the snapshot, not a mapping.
func TestProjectSources(t *testing.T) {
	node := func(id, key string, sources any) domain.Node {
		props := map[string]any{}
		if sources != nil {
			props[access.PropSources] = sources
		}
		return domain.Node{ID: domain.NodeID(id), Key: key, Type: access.NodeTypeProjectUnit, Namespace: "organisation", Properties: props}
	}
	lnk := func(from, to string) domain.Link {
		return domain.Link{Type: access.LinkProjectPartOf, From: domain.NodeRef{ID: domain.NodeID(from)}, To: domain.NodeRef{ID: domain.NodeID(to)}}
	}
	nodes := []domain.Node{
		node("1", "PROJ-ROOT", map[string]any{"alm": "content", "code": "content"}),
		node("2", "PROJ-EU", map[string]any{"alm": "content-eu"}), node("3", "PROJ-EU1", nil),
		node("4", "PROJ-BAD", map[string]any{"alm": 3}),
	}
	links := []domain.Link{lnk("1", "1"), lnk("2", "1"), lnk("3", "2"), lnk("4", "1")}
	s := access.BuildSnapshot(typecat.Builtin().Structures(), "b", nodes, links)
	if got := s.ProjectSources("PROJ-EU1"); got["alm"] != "content-eu" || got["code"] != "content" || len(got) != 2 {
		t.Errorf("PROJ-EU1: %v", got)
	}
	if got := s.ProjectSources("PROJ-ROOT"); got["alm"] != "content" {
		t.Errorf("root: %v", got)
	}
	if got := s.ProjectSources("PROJ-BAD"); got["alm"] != "content" || len(s.Problems) != 1 {
		t.Errorf("bad: %v, problems %v", got, s.Problems)
	}
	if _, ok := s.StructureNode(domain.StructureProject, "PROJ-EU1"); !ok {
		t.Error("StructureNode reads a project by its key")
	}
	if _, ok := s.StructureNode(domain.StructureOrganisation, "PROJ-EU1"); ok {
		t.Error("a project is no unit")
	}
	if got := s.StructureChain(domain.StructureProject, "PROJ-EU1"); len(got) != 3 || got[2] != "PROJ-ROOT" {
		t.Errorf("chain = %v", got)
	}
	p, err := access.ProjectFromProps(access.ProjectUnit{Name: "X", Sources: map[string]string{"alm": "content"}}.Props())
	if err != nil || p.Sources["alm"] != "content" {
		t.Errorf("round trip: %+v, %v", p, err)
	}
}
