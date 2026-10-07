package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/verify"
)

// An action that declares an independent verification cannot accept what it produced; another action may, and the
// states are facts of the change (ADR 0075).
func TestIndependentVerification(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	g.ReviewPolicy = verify.Policy{}
	c, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Namespace: "alm", Title: "t", BaselineID: base, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	p := &Process{ChangeID: c.ID}
	run := func(ctx context.Context, action, execution, code string) error {
		t.Helper()
		res, err := dsl.Run(ctx, dsl.Job{Language: "javascript", Action: action, Code: code}, nopHost{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.applyNodeOps(ctx, p, res.Nodes, action, execution)
		return err
	}
	vctx := withVerify(ctx, methodology.Action{Name: "write", Kind: methodology.KindHuman, Verify: &methodology.Verify{Oracle: methodology.OracleHuman}})
	err = run(vctx, "write", "e1", `const r = ctx.impactNode("REQ-1", "why"); ctx.writeNode(r, { props: { title: "A" } }); ctx.impactNodeReview(r, true, "mine");`)
	if err == nil || !strings.Contains(err.Error(), "may not verify") {
		t.Fatalf("the producer reviews its own effect: %v", err)
	}
	ch, _ := g.Change(ctx, c.ID)
	if len(ch.Nodes) != 1 || ch.Nodes[0].Review != domain.ReviewProposed {
		t.Fatalf("nothing accepted: %+v", ch.Nodes)
	}
	bb, _ := g.Blackboard(ctx, c.ID)
	if s := verify.Subjects(bb.Change.Items); len(s) != 1 || s[0].State != verify.Produced || !s[0].Independent || s[0].Oracle != "human" || s[0].Producer != "write" {
		t.Fatalf("produced: %+v", s)
	}
	if err := run(ctx, "check", "e2", `ctx.impactNodeReview("REQ-1", true, "checked");`); err != nil {
		t.Fatalf("another action verifies: %v", err)
	}
	bb, _ = g.Blackboard(ctx, c.ID)
	if s := verify.Subjects(bb.Change.Items); len(s) != 1 || s[0].State != verify.Accepted || s[0].By != "check" {
		t.Fatalf("accepted: %+v", s)
	}
	entries, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: c.ID, Types: []string{"fact.verification"}})
	if err != nil || len(entries) != 3 { // produced, verified, accepted
		t.Fatalf("log: %d %v", len(entries), err)
	}

	// an action with no verification is not constrained
	if err := run(ctx, "plain", "e3", `const r = ctx.impactNodeCreate("TestCase", "TST-9", "why"); ctx.writeNode(r, { props: { title: "T" } }); ctx.impactNodeReview(r, true, "mine");`); err != nil {
		t.Fatalf("no verify declared: %v", err)
	}
}
