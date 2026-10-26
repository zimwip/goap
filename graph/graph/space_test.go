package graph_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graph/graphtest"
)

// platformDirectory is the organisation as a content graph reads it from the platform graph: units and projects by key,
// each with its parent.
type platformDirectory map[string]map[string]string // kind -> key -> parent ("" for the root)

func (d platformDirectory) StructureNode(_ context.Context, kind, key string) (domain.Node, bool, error) {
	if _, ok := d[kind][key]; !ok {
		return domain.Node{}, false, nil
	}
	return domain.Node{Key: key, Namespace: "organisation", Type: map[string]string{domain.StructureOrganisation: "organisation@OrgUnit", domain.StructureProject: "organisation@ProjectUnit"}[kind]}, true, nil
}

func (d platformDirectory) StructureChain(_ context.Context, kind, key string) ([]string, error) {
	var out []string
	for cur := key; cur != "" && !slices.Contains(out, cur); cur = d[kind][cur] {
		out = append(out, cur)
	}
	return out, nil
}

// TestContentGraph: a graph whose structures live in the platform graph (ADR 0154 §9) creates no root, places its
// changes and versions in the units and projects of the platform by their keys, and refuses a key the platform does
// not know.
func TestContentGraph(t *testing.T) {
	bothRepos(t, func(t *testing.T, repo graph.Repo) {
		ctx := context.Background()
		g := graph.New(repo)
		g.Directory = platformDirectory{
			domain.StructureOrganisation: {"ORG-DEFAULT": "", "ORG-SALES": "ORG-DEFAULT"},
			domain.StructureProject:      {"PROJ-ROOT": "", "PROJ-CRM": "PROJ-ROOT"},
		}
		if err := g.Bootstrap(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := g.NodeByKey(ctx, "organisation", "ORG-DEFAULT"); !errors.Is(err, graph.ErrNotFound) {
			t.Fatalf("a content graph holds no root: %v", err)
		}
		open := func(owner, project string) (domain.Change, error) {
			head, err := g.BranchHead(ctx, "alm", domain.MainBranch)
			if err != nil {
				t.Fatal(err)
			}
			return g.CreateChange(ctx, graph.NewChange{Namespace: "alm", Title: "t", BaselineID: head.ID, OwnBranch: true, OwnerOrg: owner, ProjectID: project})
		}
		if _, err := open("ORG-SALES", "PROJ-NOWHERE"); !errors.Is(err, graph.ErrInvalid) {
			t.Fatalf("a project the platform does not know: %v", err)
		}
		if _, err := open("ORG-NOWHERE", "PROJ-CRM"); !errors.Is(err, graph.ErrInvalid) {
			t.Fatalf("a unit the platform does not know: %v", err)
		}
		c, err := open("ORG-SALES", "PROJ-CRM")
		if err != nil {
			t.Fatal(err)
		}
		cn, err := g.ImpactNodeCreate(ctx, c.ID, graph.NodeCreate{Key: "REQ-1", Type: "alm@Requirement", Rationale: "new"})
		if err != nil {
			t.Fatal(err)
		}
		if err := graphtest.Accept(ctx, g, c.ID, cn.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := graphtest.CommitMerge(ctx, g, c.ID, ""); err != nil {
			t.Fatal(err)
		}
		n, err := g.NodeByKey(ctx, "alm", "REQ-1")
		if err != nil {
			t.Fatal(err)
		}
		if n.Responsible.Key != "ORG-SALES" || n.Project.Key != "PROJ-CRM" {
			t.Fatalf("owner %q, project %q", n.Responsible, n.Project)
		}
	})
}
