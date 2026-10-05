package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/builtins"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/risk"
	"github.com/zimwip/goap/pkg/verify"
)

// An acceptance with a reserve stands on a derogation in force that covers the effect; expiry closes the derogation
// and sends what it covered back to review (ADR 0075 §2).
func TestDerogationReserveAndExpiry(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	g.ReviewPolicy = verify.Policy{}
	c, err := g.CreateChange(ctx, graph.NewChange{Namespace: "alm", Title: "t", BaselineID: base, OwnBranch: true})
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
	if err := run(vctx, "write", "e1", `const r = ctx.impactNode("REQ-1", "why"); ctx.writeNode(r, { props: { title: "A" } });`); err != nil {
		t.Fatal(err)
	}

	// no derogation: refused, nothing written
	err = run(ctx, "check", "e2", `ctx.impactNodeReviewWithReserve("REQ-1", "DRG-1", "good enough");`)
	if err == nil || !strings.Contains(err.Error(), "no derogation DRG-1") {
		t.Fatalf("a reserve without a derogation: %v", err)
	}
	if ch, _ := g.Change(ctx, c.ID); ch.Nodes[0].Review != domain.ReviewProposed {
		t.Fatalf("refused review wrote: %+v", ch.Nodes[0])
	}

	// a derogation on another target does not cover it
	sign := func(key, target, expires string) {
		t.Helper()
		if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: risk.KindDerogation, Data: map[string]any{"key": key, "rule": "tests pass", "target": target,
			"reason": "deadline", "signatory": "alice", "expires": expires}}}); err != nil {
			t.Fatal(err)
		}
	}
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	sign("DRG-1", "REQ-9", expires)
	if err := run(ctx, "check", "e2", `ctx.impactNodeReviewWithReserve("REQ-1", "DRG-1", "good enough");`); err == nil {
		t.Fatal("a derogation on another target covers nothing")
	}
	sign("DRG-1", "REQ-1", expires)
	// once it ran out it covers nothing either
	e.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if err := run(ctx, "check", "e2", `ctx.impactNodeReviewWithReserve("REQ-1", "DRG-1", "good enough");`); err == nil {
		t.Fatal("an expired derogation covers nothing")
	}
	e.now = nil
	if err := run(ctx, "check", "e2", `ctx.impactNodeReviewWithReserve("REQ-1", "DRG-1", "good enough");`); err != nil {
		t.Fatalf("with a derogation in force: %v", err)
	}
	bb, _ := g.Blackboard(ctx, c.ID)
	s := verify.Subjects(bb.Change.Items)
	if len(s) != 1 || s[0].State != verify.AcceptedWithReserve || s[0].Derogation != "DRG-1" || s[0].By != "check" {
		t.Fatalf("accepted with reserve: %+v", s)
	}
	if bb.Change.Nodes[0].Review != domain.ReviewAccepted {
		t.Fatalf("review: %s", bb.Change.Nodes[0].Review)
	}

	// before the expiry nothing happens
	if keys, err := ExpireDerogations(ctx, g, c.ID, time.Now()); err != nil || len(keys) != 0 {
		t.Fatalf("not yet: %v %v", keys, err)
	}
	later := time.Now().Add(2 * time.Hour)
	keys, err := ExpireDerogations(ctx, g, c.ID, later)
	if err != nil || len(keys) != 1 || keys[0] != "DRG-1" {
		t.Fatalf("expiry: %v %v", keys, err)
	}
	bb, _ = g.Blackboard(ctx, c.ID)
	if ds := risk.Derogations(bb.Change); len(ds) != 1 || ds[0].Status != risk.DerogationClosed || ds[0].Versions != 3 {
		t.Fatalf("closed: %+v", ds)
	}
	if bb.Change.Nodes[0].Review != domain.ReviewProposed {
		t.Fatalf("the covered effect is reviewed again: %s", bb.Change.Nodes[0].Review)
	}
	if s := verify.Subjects(bb.Change.Items); len(s) != 1 || s[0].State != verify.Produced || s[0].Producer != "write" || !s[0].Independent {
		t.Fatalf("verification starts over: %+v", s)
	}
	if keys, _ := ExpireDerogations(ctx, g, c.ID, later); len(keys) != 0 {
		t.Fatalf("closed once: %v", keys)
	}
	// the producer still may not verify its own effect
	if err := run(ctx, "write", "e3", `ctx.impactNodeReview("REQ-1", true, "mine");`); err == nil {
		t.Fatal("independence survives the expiry")
	}
}

// The builtin derogation.expire sweeps the live changes (ADR 0075 §2).
func TestDerogationExpireBuiltin(t *testing.T) {
	ctx := context.Background()
	_, g, base := setup(t)
	c, err := g.CreateChange(ctx, graph.NewChange{Namespace: "alm", Title: "t", BaselineID: base, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: risk.KindDerogation, Data: map[string]any{"key": "DRG-1", "rule": "r", "target": "REQ-1",
		"reason": "x", "signatory": "alice", "expires": time.Now().Add(150 * time.Millisecond).UTC().Format(time.RFC3339Nano)}}}); err != nil {
		t.Fatal(err)
	}
	b := DefaultBuiltins()
	if !b.HasBuiltin(builtins.DerogationExpire) {
		t.Fatal("derogation.expire is not registered")
	}
	time.Sleep(200 * time.Millisecond)
	res, err := expireAllDerogations(ctx, ActionContext{Graph: g})
	if err != nil || !strings.HasPrefix(res.Output, "1 derogation") {
		t.Fatalf("sweep: %q %v", res.Output, err)
	}
	bb, _ := g.Blackboard(ctx, c.ID)
	if ds := risk.Derogations(bb.Change); len(ds) != 1 || ds[0].Open() {
		t.Fatalf("closed: %+v", ds)
	}
}

// The change of a methodology that names a default criticality starts with it in Change.Data (ADR 0075 §3); one that
// names none carries none, the platform default applying.
func TestChangeStartsWithTheCriticalityOfItsMethodology(t *testing.T) {
	ctx := context.Background()
	e, g, _ := setup(t)
	for _, tc := range []struct{ crit, want string }{{"C3", "C3"}, {"", ""}} {
		m := &methodology.Compiled{Methodology: &methodology.Methodology{Name: "m", Namespace: "alm", Criticality: tc.crit}}
		id, err := e.resolveChange(ctx, &Process{Goal: "g"}, m, AttachRequest{})
		if err != nil {
			t.Fatal(err)
		}
		c, err := g.Change(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := c.Data[domain.DataCriticality].(string); got != tc.want {
			t.Errorf("methodology criticality %q: change has %q", tc.crit, got)
		}
	}
}
