package graph

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// A commit rebases the drafts it writes onto the head (ADR 0165): the moves of a lifecycle the change made are replayed
// over the head's state and checked, a state both sides moved stays the change's only when the lifecycle allows it, and
// the merge that follows is a fast-forward writing no version.
func TestRebaseDraftsAtCommit(t *testing.T) { forEachRepo(t, testRebaseDraftsAtCommit) }

func testRebaseDraftsAtCommit(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	lc := domain.Lifecycle{Initial: "a",
		States: []domain.LifecycleState{{Name: "a"}, {Name: "mid", NotCommittable: true}, {Name: "b"}, {Name: "c"}, {Name: "d"}},
		Transitions: []domain.Transition{
			{Name: "start", From: "a", To: "mid"}, {Name: "finish", From: "mid", To: "b"},
			{Name: "toC", From: "a", To: "c"}, {Name: "toD", From: "a", To: "d"}, {Name: "cToB", From: "c", To: "b"},
		}}
	types := testTypes{"Item": {Lifecycle: &lc}}
	g.Types = func() TypeCatalog { return types }
	seed := func(key string) domain.Node {
		return must[domain.Node](t)(seedNode(ctx, g, newNode{Key: key, Type: "Item", Properties: map[string]any{"title": key}, State: "a"}))
	}
	one, two, three := seed("IT-1"), seed("IT-2"), seed("IT-3")
	base := pinCommit(t, g, "", one.Ref(), two.Ref(), three.Ref())
	work := func(title string, n domain.Node, props map[string]any, moves ...string) (domain.Change, domain.ChangeImpactID) {
		t.Helper()
		c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: title, ForkCommit: base.ID}))
		ref := n.Ref()
		id := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &ref, Rationale: title}}))[0].ID
		if props != nil {
			if _, err := g.edit(ctx, c.ID, id, edit{Properties: props}); err != nil {
				t.Fatal(err)
			}
		}
		for _, to := range moves {
			if _, err := g.ImpactNodeTransition(ctx, c.ID, NodeTransition{NodeCheckout: NodeCheckout{Impact: id}, To: to}); err != nil {
				t.Fatal(err)
			}
		}
		must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, id, "u", "ok"))
		return c, id
	}

	// only the change moved the state, through a state that cannot commit: its two moves are replayed over the head
	a1, _ := work("A1", one, map[string]any{"note": "from A"})
	b1, _ := work("B1", one, nil, "mid", "b")
	// both moved it, the lifecycle allowing the head's state to reach the change's: the one move replayed
	a2, _ := work("A2", two, nil, "c")
	b2, _ := work("B2", two, nil, "mid", "b")
	// both moved it, the lifecycle forbidding the move: a conflict of states
	a3, _ := work("A3", three, nil, "c")
	b3, i3 := work("B3", three, nil, "d")
	for _, c := range []domain.Change{a1, a2, a3} {
		must[domain.Commit](t)(g.apply(ctx, c.ID, ""))
	}
	heads := map[domain.NodeID]domain.Version{}
	for _, n := range []domain.Node{one, two, three} {
		heads[n.ID] = must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: n.ID})).Version
	}

	for _, c := range []domain.Change{b1, b2} {
		res := must[domain.Commit](t)(g.apply(ctx, c.ID, ""))
		if res.Kind != domain.CommitKindFastForward {
			t.Fatalf("%s merges as a fast-forward: %+v", c.Title, res)
		}
	}
	got := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: one.ID}))
	if got.State != "b" || got.Properties["note"] != "from A" || got.ChangeID != b1.ID || !slices.Equal(got.Parents, []domain.Version{heads[one.ID]}) ||
		got.Reason != domain.ReasonRevise {
		t.Fatalf("B1 keeps A1's edit, moves it to b and revises A1's version, its branch took it (ADR 0177): %+v", got)
	}
	if got := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: two.ID})); got.State != "b" || !slices.Equal(got.Parents, []domain.Version{heads[two.ID]}) {
		t.Fatalf("B2 moves IT-2 from c to b: %+v", got)
	}

	_, err := g.apply(ctx, b3.ID, "")
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "IT-3 (state)") {
		t.Fatalf("a state the lifecycle cannot reach is a conflict: %v", err)
	}
	cn := must[domain.ChangeImpact](t)(g.seenImpact(ctx, b3.ID, i3, ""))
	if cn.Review != domain.ReviewProposed || cn.Pre == nil || cn.Pre.Version != heads[three.ID] {
		t.Fatalf("the impact goes back to proposed, from the head's version: %+v", cn)
	}
	// the rebase is kept: the draft starts from the head's state, the conflict named
	st := must[RebaseState](t)(g.RebaseState(ctx, b3.ID))
	if len(st.Conflicts) != 1 || !slices.Equal(st.Conflicts[0].Conflicts, []string{domain.ConflictState}) {
		t.Fatalf("rebase state: %+v", st)
	}
	if d := must[domain.Node](t)(g.ChangeNode(ctx, b3.ID, "", domain.DraftRef(three.ID))); d.State != "c" {
		t.Fatalf("the rebased draft is in the head's state: %+v", d)
	}
	if n := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: three.ID})); n.Version != heads[three.ID] {
		t.Fatalf("nothing committed: %+v", n)
	}
}
