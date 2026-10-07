package graph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// Every operation on a node of a change resolves it the same way (ADR 0077, "Resolution").

func impactsOn(t *testing.T, g *Graph, id domain.ChangeID, key string) []domain.ChangeImpact {
	t.Helper()
	var out []domain.ChangeImpact
	for _, cn := range must[[]domain.ChangeImpact](t)(g.ListChangeImpacts(context.Background(), id)) {
		if cn.Key == key {
			out = append(out, cn)
		}
	}
	return out
}

func TestResolveCreate(t *testing.T) { forEachRepo(t, testResolveCreate) }

func testResolveCreate(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "create", BaselineID: f.base.ID}))

	// a key the reference baseline holds
	_, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-1", Type: "Requirement"})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "baseline") {
		t.Fatalf("create on a baseline key: %v", err)
	}
	// twice
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-9", Type: "Requirement", Rationale: "new"}))
	if cn.Intent != domain.IntentCreated || cn.Post == nil {
		t.Fatalf("created: %+v", cn)
	}
	_, err = g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-9", Type: "Requirement"})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("create twice: %v", err)
	}
	if got := impactsOn(t, g, c.ID, "REQ-9"); len(got) != 1 {
		t.Fatalf("one impact: %+v", got)
	}

	// a new node has no proposal (ADR 0077): the proposal is refused, and nothing is declared
	if _, err := g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentCreated, Key: "TST-9", Type: "TestCase", Rationale: "plan"}}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "ImpactNodeCreate") {
		t.Fatalf("a proposal of a creation: %v", err)
	}
	if got := impactsOn(t, g, c.ID, "TST-9"); len(got) != 0 {
		t.Fatalf("a refused proposal declares nothing: %+v", got)
	}
	// so a node that does not exist is not found by the other operations
	if _, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Key: "TST-9"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("checkout of a node that does not exist: %v", err)
	}
	// the creation logs one event: created, with its impact and its first version, and no proposal
	events := must[[]domain.ImpactEvent](t)(g.ChangeEvents(ctx, c.ID))
	var ops []domain.ImpactOp
	for _, e := range events {
		if e.Impact == cn.ID {
			ops = append(ops, e.Op)
		}
	}
	if len(ops) != 1 || ops[0] != domain.ImpactCreated || events[len(events)-1].State == nil || events[len(events)-1].Post == nil {
		t.Fatalf("a creation is one created event: %v", ops)
	}
	// edits follow: created, updated, ...
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "x"}}))
	ops = nil
	for _, e := range must[[]domain.ImpactEvent](t)(g.ChangeEvents(ctx, c.ID)) {
		if e.Impact == cn.ID {
			ops = append(ops, e.Op)
		}
	}
	if len(ops) != 2 || ops[0] != domain.ImpactCreated || ops[1] != domain.ImpactUpdated {
		t.Fatalf("created then updated: %v", ops)
	}
}

func TestResolveExistingNode(t *testing.T) { forEachRepo(t, testResolveExistingNode) }

func testResolveExistingNode(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "modify", BaselineID: f.base.ID}))

	// unknown nodes are in neither the baseline nor the change
	if _, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: "nope"}); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "neither") {
		t.Fatalf("checkout of an unknown node: %v", err)
	}
	if _, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Key: "NOPE-1"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("checkout of an unknown key: %v", err)
	}
	if _, err := g.ImpactNodeUpdate(ctx, c.ID, "", NodeUpdate{Key: "NOPE-1", Properties: map[string]any{"x": 1}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of an unknown node: %v", err)
	}
	if _, err := g.ImpactNodeTransition(ctx, c.ID, NodeTransition{NodeCheckout: NodeCheckout{Node: "nope"}, To: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("transition of an unknown node: %v", err)
	}
	if got := must[[]domain.ChangeImpact](t)(g.ListChangeImpacts(ctx, c.ID)); len(got) != 0 {
		t.Fatalf("a refused operation declares nothing: %+v", got)
	}

	// checkout by key declares the impact from the baseline
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Key: "REQ-1", Rationale: "why"}))
	if cn.Intent != domain.IntentModified || cn.Pre == nil || *cn.Pre != f.req.Ref() || cn.Rationale != "why" {
		t.Fatalf("auto-proposed from the baseline: %+v", cn)
	}
	// an update by node reuses it
	up := must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, "", NodeUpdate{Node: f.req.ID, Properties: map[string]any{"title": "T"}}))
	if up.ID != cn.ID {
		t.Fatalf("update reuses the impact: %s / %s", up.ID, cn.ID)
	}
	// create on the same key is a conflict, a second checkout too
	if _, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-1", Type: "Requirement"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("create on a node of the change: %v", err)
	}
	if _, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("checkout twice: %v", err)
	}
	if got := impactsOn(t, g, c.ID, "REQ-1"); len(got) != 1 {
		t.Fatalf("a node appears once: %+v", got)
	}
}

func TestResolveTransitionFromBaseline(t *testing.T) {
	forEachRepo(t, testResolveTransitionFromBaseline)
}

func testResolveTransitionFromBaseline(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorldG(t, repo, "")
	c := w.change(t, "move")
	cn, err := w.g.ImpactNodeTransition(ctx, c.ID, NodeTransition{NodeCheckout: NodeCheckout{Node: w.req1.ID, Rationale: "start"}, To: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	if cn.Intent != domain.IntentModified || cn.Pre == nil || *cn.Pre != w.req1.Ref() || cn.Post == nil {
		t.Fatalf("auto-proposed from the baseline: %+v", cn)
	}
	// the impact is reused by the next transition
	cn2, err := w.g.ImpactNodeTransition(ctx, c.ID, NodeTransition{NodeCheckout: NodeCheckout{Node: w.req1.ID}, To: "approved"})
	if err == nil && cn2.ID != cn.ID {
		t.Fatalf("reuse: %s / %s", cn2.ID, cn.ID)
	}
	if got := impactsOn(t, w.g, c.ID, w.req1.Key); len(got) != 1 {
		t.Fatalf("a node appears once: %+v", got)
	}
}
