package access_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
)

// The session (ADR 0070) is what the web reads once: the structures and names it builds nodes with, the platform
// roles it lists, and what the caller may attempt, derived by the authorizer.
func TestSessionOfAdminMemberAndReader(t *testing.T) {
	ctx := context.Background()
	g, a := setup(t)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedUnit(ctx, g, "team-a", "Team A", "team", ""); err != nil {
		t.Fatal(err)
	}
	team, err := g.NodeByKey(ctx, access.NamespaceOrganisation, "team-a")
	if err != nil {
		t.Fatal(err)
	}
	head, err := g.BranchHead(ctx, access.NamespaceOrganisation, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	ref := team.Ref()
	if _, err := g.Commit(ctx, graph.Commit{Namespace: access.NamespaceOrganisation, Title: "reader", Baseline: head.ID, By: "test", BaselineName: "reader", Edits: []graph.NodeEdit{
		{Key: access.PlatformAssignmentKey("team-a"), Type: access.NodeTypeAssignment, Props: access.Assignment{Roles: []string{access.RoleReader}}.Props(), Rationale: "t",
			Links: []graph.LinkEdit{{Type: access.LinkAssignsOrg, To: &ref}}},
	}}); err != nil {
		t.Fatal(err)
	}

	admin, err := a.Session(ctx, authz.Principal{Subject: "root", Roles: []string{access.RoleAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	if !admin.Can.Administer || !admin.Can.Approve {
		t.Errorf("admin: %+v", admin.Can)
	}
	reader, err := a.Session(ctx, authz.Principal{Subject: "dev", Org: "team-a"})
	if err != nil {
		t.Fatal(err)
	}
	if reader.Can.Administer || !reader.Can.Approve {
		t.Errorf("a platform reader works on the root project but administers nothing: %+v", reader.Can)
	}
	stranger, err := a.Session(ctx, authz.Principal{Subject: "stranger", Org: "other-org"})
	if err != nil {
		t.Fatal(err)
	}
	if stranger.Can.Administer || stranger.Can.Approve {
		t.Errorf("a caller with no role may attempt nothing: %+v", stranger.Can)
	}

	// what every session carries
	st := admin.Structures
	if st.Organisation().Root != access.DefaultOrg || st.Project().Root != access.DefaultProject {
		t.Errorf("roots %q, %q", st.Organisation().Root, st.Project().Root)
	}
	if admin.Names.Types.OrgUnit != st.Organisation().Type || admin.Names.Links.ProjectPartOf != st.Project().Parent {
		t.Errorf("names do not follow the structures: %+v", admin.Names)
	}
	if admin.Names.Keys.User != access.UserPrefix || admin.Names.Keys.Assignment != access.AssignmentPrefix || admin.Names.Keys.PlatformScope != access.PlatformScope {
		t.Errorf("keys %+v", admin.Names.Keys)
	}
	var names []string
	for _, r := range admin.PlatformRoles {
		names = append(names, r.Name)
	}
	if !slices.Equal(names, access.PlatformRoles) {
		t.Errorf("platform roles %v, want %v", names, access.PlatformRoles)
	}

	// the payload keeps the principal's own fields at the top level
	raw, err := json.Marshal(reader)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["subject"] != "dev" || m["org"] != "team-a" || m["can"] == nil || m["structures"] == nil || m["platformRoles"] == nil {
		t.Errorf("payload %s", raw)
	}
}

// pkg/mcp may not import pkg/domain (pkg/layering), so the type of an MCP is written in both: they must agree.
func TestMCPTypeAgrees(t *testing.T) {
	if domain.TypeMCP != mcp.NodeTypeMCP {
		t.Errorf("%q != %q", domain.TypeMCP, mcp.NodeTypeMCP)
	}
}

func TestPlatformAssignmentKeyIsAnAssignmentKey(t *testing.T) {
	if got, want := access.PlatformAssignmentKey("u"), access.AssignmentKey("u", access.PlatformScope); got != want {
		t.Errorf("%q != %q", got, want)
	}
}
