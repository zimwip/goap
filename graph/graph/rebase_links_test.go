package graph

import (
	"context"
	"slices"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// A rebase merges the links of a node only when both sides changed its link set (ADR 0146): a side that left it alone
// takes the other's links as they are; both changed, the links are merged one by one.
func TestRebaseMergesLinkSets(t *testing.T) { forEachRepo(t, testRebaseMergesLinkSets) }

func testRebaseMergesLinkSets(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	needs := map[string]domain.Node{}
	for _, k := range []string{"NEED-2", "NEED-3", "NEED-4"} {
		needs[k] = must[domain.Node](t)(importNode(ctx, g, newNode{Key: k, Type: "Need", Properties: map[string]any{"title": k}}))
	}
	head := func() domain.BaselineID {
		return must[domain.Baseline](t)(g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch)).ID
	}
	req := func() domain.NodeRef {
		return must[domain.Node](t)(g.NodeByKey(ctx, domain.DefaultNamespace, "REQ-1")).Ref()
	}
	targets := func() []string {
		var out []string
		at := req()
		if err := g.repo.InTx(ctx, func(tx Tx) error {
			ls, err := tx.OutLinks(ctx, at)
			for _, l := range ls {
				out = append(out, string(l.To.ID))
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		slices.Sort(out)
		return out
	}
	want := func(keys ...string) []string {
		out := []string{string(f.need.ID)}
		for _, k := range keys {
			out = append(out, string(needs[k].ID))
		}
		slices.Sort(out)
		return out
	}

	// the change edits a property, the target adds a link: the target's links stand
	c, _ := propChange(t, g, head(), "props", req(), map[string]any{"title": "PSP v2"})
	must[domain.Baseline](t)(g.CommitChange(ctx, c.ID, ""))
	must[domain.Link](t)(importLink(ctx, g, "satisfies", req(), needs["NEED-2"].Ref(), nil))
	must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	if n := must[domain.Node](t)(g.NodeByKey(ctx, domain.DefaultNamespace, "REQ-1")); n.Properties["title"] != "PSP v2" {
		t.Fatalf("the change's property: %v", n.Properties)
	}
	if got := targets(); !slices.Equal(got, want("NEED-2")) {
		t.Fatalf("the target's link stands: %v", got)
	}

	// both add a link: both stand
	c = must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "link", BaselineID: head(), OwnBranch: true}))
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: req().ID, Rationale: "link"}))
	must[domain.Link](t)(g.ImpactLinkCreate(ctx, c.ID, cn.ID, LinkWrite{Type: "satisfies", To: needs["NEED-3"].Ref()}, "", ""))
	if err := g.acceptImpact(ctx, c.ID, cn.ID, ""); err != nil {
		t.Fatal(err)
	}
	must[domain.Baseline](t)(g.CommitChange(ctx, c.ID, ""))
	must[domain.Link](t)(importLink(ctx, g, "satisfies", req(), needs["NEED-4"].Ref(), nil))
	must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	if got := targets(); !slices.Equal(got, want("NEED-2", "NEED-3", "NEED-4")) {
		t.Fatalf("both sides' links stand: %v", got)
	}
}
