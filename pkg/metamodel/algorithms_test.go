package metamodel

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
)

func almFromFile(t *testing.T) *methodology.Domain {
	t.Helper()
	src, err := os.ReadFile("../../domains/alm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d, err := methodology.ParseDomain(src)
	if err != nil {
		t.Fatal(err)
	}
	if issues := d.Validate(); len(issues) > 0 {
		t.Fatal(issues)
	}
	return d
}

func createNode(key, typ, state string, props map[string]any) domain.ChangeItem {
	return domain.ChangeItem{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpCreateNode, Node: &domain.NodeDraft{Key: key, Type: typ, State: state, Properties: props}}}
}

// The algorithms plugged in a shared domain are embedded in its NodeType nodes
// and enforced by the graph; republishing the domain updates them in place.
func TestDomainAlgorithmsEnforcedByTheGraph(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	d := almFromFile(t)
	if _, err := SyncDomain(ctx, g, d); err != nil {
		t.Fatal(err)
	}
	head, err := g.BranchHead(ctx, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	newChange := func() domain.ChangeSet {
		c, err := g.CreateChange(ctx, graph.NewChange{Title: "c", BaselineID: head.ID, Namespace: "sdlc"})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := newChange()
	// regex-match instance dotted-version on Release.version
	_, err = g.AddItems(ctx, c.ID, []domain.ChangeItem{createNode("REL-1", "Release", "", map[string]any{"version": "one"})})
	if !errors.Is(err, graph.ErrInvalid) || !strings.Contains(err.Error(), "version must look like 1.2 or 1.2.3") {
		t.Fatalf("bad version accepted: %v", err)
	}
	// max-length instance on Requirement.title (a subtype inherits it)
	_, err = g.AddItems(ctx, c.ID, []domain.ChangeItem{createNode("REQ-1", "SecurityRequirement", "", map[string]any{"title": strings.Repeat("x", 201)})})
	if !errors.Is(err, graph.ErrInvalid) || !strings.Contains(err.Error(), "longer than 200") {
		t.Fatalf("long title accepted: %v", err)
	}
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{createNode("REL-1", "Release", "", map[string]any{"version": "5.2"}), createNode("REQ-1", "Requirement", "", map[string]any{"title": "ok"})}); err != nil {
		t.Fatal(err)
	}

	// republish the domain with a stricter instance: the existing NodeType nodes are patched
	d2 := *d
	d2.Instances = slices.Clone(d.Instances)
	for i, in := range d2.Instances {
		if in.Name == "dotted-version" {
			d2.Instances[i].Values = map[string]any{"pattern": "^[0-9]+$", "message": "integers only"}
		}
	}
	if _, err := SyncDomain(ctx, g, &d2); err != nil {
		t.Fatal(err)
	}
	head, err = g.BranchHead(ctx, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c = newChange()
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{createNode("REL-2", "Release", "", map[string]any{"version": "5.2"})}); !errors.Is(err, graph.ErrInvalid) || !strings.Contains(err.Error(), "integers only") {
		t.Fatalf("the republished instance must apply: %v", err)
	}
}

func TestNodeTypeEmbedsResolvedAlgorithms(t *testing.T) {
	d := almFromFile(t)
	var req methodology.NodeType
	for _, n := range d.NodeTypes {
		if n.Name == "Requirement" {
			req = n
		}
	}
	m := nodeTypeProps(d.Schema, req)
	vs, _ := m["validators"].([]any)
	if len(vs) != 1 || vs[0].(map[string]any)["code"] == "" || vs[0].(map[string]any)["params"].(map[string]any)["max"] != 200.0 {
		t.Fatalf("validators must be embedded resolved: %v", m["validators"])
	}
	found := false
	for _, tr := range m["lifecycle"].(map[string]any)["transitions"].([]any) {
		tm := tr.(map[string]any)
		if tm["name"] == "approve" {
			acts, _ := tm["actionAlgos"].([]any)
			found = len(acts) == 1 && acts[0].(map[string]any)["params"].(map[string]any)["property"] == "approvedAt"
		}
	}
	if !found {
		t.Fatalf("the approve action must be embedded resolved: %v", m["lifecycle"])
	}
}
