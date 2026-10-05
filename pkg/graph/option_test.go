package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// Options (ADR 0009 §3, ADR 0032 §6): two hypotheses explored at once on the same node, the change working on the
// active one, compared, then one selected: its version joins the change branch as is, the other is rejected.
func TestOptions(t *testing.T) { forEachRepo(t, testOptions) }

func testOptions(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "PSP", BaselineID: f.base.ID, OwnBranch: true}))
	pre := f.req.Ref()

	a := must[domain.Flow](t)(g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "stripe", Hypothesis: "Stripe covers every market", Activate: true, By: "u"}))
	if a.Option == nil || !a.Active || a.OptionStatus() != domain.OptionExploring {
		t.Fatalf("option A: %+v", a)
	}
	if _, err := g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "Stripe"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("two open options with one name: %v", err)
	}
	// with A active, a call that names no flow works on A
	write := func(title string) domain.ChangeImpact {
		t.Helper()
		added := must[[]domain.ChangeImpact](t)(g.AddNodes(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: title}}))
		return must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, added[0].ID, edit{Properties: map[string]any{"title": title}}))
	}
	ia := write("Use Stripe")
	if ia.Flow != a.ID {
		t.Fatalf("declared on the active option: %+v", ia)
	}
	b := must[domain.Flow](t)(g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "adyen", Hypothesis: "Adyen is cheaper"}))
	if active := must[string](t)(g.ActivateOption(ctx, c.ID, b.ID, "u")); active != b.ID {
		t.Fatalf("active %q", active)
	}
	ib := write("Use Adyen")
	if ib.Flow != b.ID {
		t.Fatalf("declared on option B: %+v", ib)
	}
	// the blackboard of the change is the active option's; "main" names the main flow
	if bb := must[domain.Blackboard](t)(g.Blackboard(ctx, c.ID)); len(bb.Change.Nodes) != 1 || bb.Change.Nodes[0].ID != ib.ID {
		t.Fatalf("blackboard on the active option: %+v", bb.Change.Nodes)
	}
	if bb := must[domain.Blackboard](t)(g.BlackboardIn(ctx, c.ID, domain.MainFlow)); len(bb.Change.Nodes) != 0 {
		t.Fatalf("main flow: %+v", bb.Change.Nodes)
	}
	// the graph the change reads is the active option's
	nodes, _ := must2(t)(g.ChangeGraph(ctx, c.ID, ""))
	title := ""
	for _, n := range nodes {
		if n.ID == pre.ID {
			title, _ = n.Properties["title"].(string)
		}
	}
	if title != "Use Adyen" {
		t.Fatalf("change graph on option B: REQ-1 title %q", title)
	}
	// two versions of REQ-1 at once, each on its option, both children of v1
	cmp := must[OptionComparison](t)(g.CompareOptions(ctx, c.ID, ViewWritten, false))
	if len(cmp.Options) != 2 || len(cmp.Nodes) != 1 || cmp.Nodes[0].Key != "REQ-1" || *cmp.Nodes[0].Main != pre {
		t.Fatalf("comparison: %+v", cmp)
	}
	va, vb := cmp.Nodes[0].Options[a.ID], cmp.Nodes[0].Options[b.ID]
	if va == nil || vb == nil || *va == *vb || cmp.Nodes[0].Props[a.ID]["title"] != "Use Stripe" || cmp.Nodes[0].Props[b.ID]["title"] != "Use Adyen" {
		t.Fatalf("option versions: %+v", cmp.Nodes[0])
	}
	// nothing is accepted yet
	if acc := must[OptionComparison](t)(g.CompareOptions(ctx, c.ID, ViewAccepted, false)); len(acc.Nodes) != 0 {
		t.Fatalf("accepted comparison: %+v", acc.Nodes)
	}
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("apply with open options: %v", err)
	}
	// the decision: A is evaluated, accepted on its own flow and selected
	must[domain.Flow](t)(g.EvaluateOption(ctx, c.ID, a.ID, "u", "covers every market"))
	must[string](t)(g.ActivateOption(ctx, c.ID, a.ID, "u"))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, ia.ID, "u", "ok"))
	sel := must[domain.Flow](t)(g.SelectOption(ctx, c.ID, a.ID, "u"))
	if sel.OptionStatus() != domain.OptionSelected || sel.Active {
		t.Fatalf("selected: %+v", sel)
	}
	opts := must[[]domain.Flow](t)(g.Options(ctx, c.ID))
	if len(opts) != 2 || opts[1].OptionStatus() != domain.OptionRejected {
		t.Fatalf("options: %+v", opts)
	}
	// option A's version joined the change branch as is: no adopt copy
	cur := must[domain.Change](t)(g.Change(ctx, c.ID))
	if cur.ActiveOption() != "" {
		t.Fatalf("no option is active after the decision: %q", cur.ActiveOption())
	}
	vs := must[[]domain.Node](t)(g.Versions(ctx, pre.ID))
	for _, v := range vs {
		if v.Reason == domain.ReasonAdopt {
			t.Fatalf("an adopted option is not copied: %+v", vs)
		}
	}
	res := must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	if !res.Contains(*va) {
		t.Fatalf("the selected option lands as its own version %s: %v", va, res.Nodes)
	}
	head := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: pre.ID}))
	if head.Ref() != *va || head.Properties["title"] != "Use Stripe" {
		t.Fatalf("main: %+v", head)
	}
}

// Option intent (derive/revise/refine): refine is a sub-branch of another open option, not of the main flow.
func TestOptionRefine(t *testing.T) { forEachRepo(t, testOptionRefine) }

func testOptionRefine(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "PSP", BaselineID: f.base.ID, OwnBranch: true}))

	if _, err := g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "stripe", Intent: "bogus"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown intent: %v", err)
	}

	a := must[domain.Flow](t)(g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "stripe", Hypothesis: "Stripe covers every market", Intent: domain.IntentDerive, By: "u"}))
	if a.Option.Intent != domain.IntentDerive || a.Parent != "" {
		t.Fatalf("derive option: %+v", a)
	}

	// refine: a sub-branch of the open option A, not of main
	sub := must[domain.Flow](t)(g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "stripe-eu", Hypothesis: "Stripe, EU entities only", Parent: a.ID, Intent: domain.IntentRefine, By: "u"}))
	if sub.Parent != a.ID || sub.Option.Intent != domain.IntentRefine {
		t.Fatalf("refine option: %+v", sub)
	}

	// a parent that isn't an open option of this change is refused
	if _, err := g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "other", Parent: "no-such-flow"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown parent: %v", err)
	}
	must[domain.Flow](t)(g.RejectOption(ctx, c.ID, a.ID, "u"))
	if _, err := g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "other", Parent: a.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("discarded parent: %v", err)
	}
}
