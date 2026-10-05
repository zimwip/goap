package access_test

import (
	"testing"

	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/mcp"
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
		node("5", "DEP-ONE", mcp.NodeTypeOrgUnit, nil), node("6", "ASG:DEP-ONE/PROJ-A", access.NodeTypeAssignment, roles("developer")),
		node("7", "DEP-TWO", mcp.NodeTypeOrgUnit, nil), node("8", "ASG:DEP-TWO/PLATFORM", access.NodeTypeAssignment, roles(access.RoleAdmin)),
	}
	links := []domain.Link{
		lnk(access.LinkProjectPartOf, "1", "1"), lnk(access.LinkProjectPartOf, "2", "1"), lnk(access.LinkProjectPartOf, "3", "2"), lnk(access.LinkProjectPartOf, "4", "1"),
		lnk(access.LinkAssignsOrg, "6", "5"), lnk(access.LinkAssignsProject, "6", "2"), lnk(access.LinkAssignsOrg, "8", "7"),
	}
	s := access.BuildSnapshot(domain.BuiltinStructureSet(), "b", nodes, links)
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
