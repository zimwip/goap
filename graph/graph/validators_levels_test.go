package graph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/typecat"
)

// levelsGraph is judged by a domain plugging a validator at each level: an attribute (title), a node (a tag needs a
// title), an attribute of a link (weight) and a link (both ends of the same colour).
func levelsGraph(t *testing.T) (*Graph, domain.Baseline) {
	t.Helper()
	d, err := def.ParseDomain([]byte(`
name: lv
version: 1.0.0
algorithms:
  - {name: not-blank, type: property_validator, language: javascript, code: 'return String(ctx.value() || "").trim() !== "" ? "" : "title is blank"'}
  - {name: tag-needs-colour, type: node_validator, language: javascript, code: 'if (ctx.node().props.tag && !ctx.node().props.colour) ctx.fail("a tag needs a colour")'}
  - {name: positive, type: property_validator, language: javascript, code: 'if (ctx.value() !== undefined && ctx.value() !== null && Number(ctx.value()) <= 0) ctx.fail("weight must be positive")'}
  - name: same-colour
    type: link_validator
    language: javascript
    code: |
      const colour = (n) => (n.props || {}).colour;
      if (colour(ctx.from()) !== colour(ctx.to())) {
        ctx.fail("colours differ (" + ctx.link().type + ")");
      }
algorithmInstances:
  - {name: title-required, algorithm: not-blank}
  - {name: tag-rule, algorithm: tag-needs-colour}
  - {name: weight-positive, algorithm: positive}
  - {name: colours, algorithm: same-colour}
nodeTypes:
  - name: Item
    validators: [tag-rule]
    attributes: [{name: title, validators: [title-required]}, tag, colour]
linkTypes:
  - name: refs
    from: Item
    to: Item
    validators: [colours]
    attributes: [{name: weight, type: number, validators: [weight-positive]}]
`))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(d)
	if err != nil {
		t.Fatal(err)
	}
	g := New(NewMemory())
	g.Types = func() TypeCatalog { return cat }
	b, err := g.BranchHead(context.Background(), "lv", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	return g, b
}

type levelsChange struct {
	g      *Graph
	c      domain.Change
	a, b   domain.ChangeImpact
	ctx    context.Context
	t      *testing.T
	accept func(domain.ChangeImpact) error
}

// newLevelsChange drafts A and B of one change and a link A -> B carrying link.
func newLevelsChange(t *testing.T, a, b, link map[string]any) *levelsChange {
	t.Helper()
	ctx := context.Background()
	g, base := levelsGraph(t)
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Namespace: "lv", Title: "t", BaselineID: base.ID}))
	lc := &levelsChange{g: g, c: c, ctx: ctx, t: t}
	lc.b = must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Rationale: "r", Key: "B", Type: "lv@Item", Properties: b}))
	lc.a = must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Rationale: "r", Key: "A", Type: "lv@Item", Properties: a,
		Links: []LinkWrite{{Type: "lv@refs", To: *lc.b.Post, Properties: link}}}))
	lc.accept = func(cn domain.ChangeImpact) error {
		_, err := g.ImpactNodeReview(ctx, c.ID, cn.ID, domain.ReviewAccepted, "tester", "ok")
		return err
	}
	return lc
}

// The draft is refused at its review with the message, and accepted once valid.
func TestValidatorsOfEachLevelRefuseAtReview(t *testing.T) {
	ok := map[string]any{"title": "t", "colour": "red"}
	cases := []struct {
		name string
		a, b map[string]any
		link map[string]any
		want string
	}{
		{"attribute", map[string]any{"title": " ", "colour": "red"}, ok, nil, `property "title" is invalid: title is blank`},
		{"node", map[string]any{"title": "t", "tag": "x"}, ok, nil, "a tag needs a colour"},
		{"link attribute", ok, ok, map[string]any{"weight": -1}, `link lv@refs: property "weight" is invalid: weight must be positive`},
		{"link", ok, map[string]any{"title": "t", "colour": "blue"}, nil, "link lv@refs to B is invalid: colours differ (lv@refs) (validator colours)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lc := newLevelsChange(t, tc.a, tc.b, tc.link)
			if err := lc.accept(lc.b); err != nil {
				t.Fatalf("B is valid: %v", err)
			}
			err := lc.accept(lc.a)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// valid drafts are accepted and commit; a link validator also judges the version written at commit.
func TestValidatorsOfEachLevelAcceptValid(t *testing.T) {
	ok := map[string]any{"title": "t", "colour": "red", "tag": "x"}
	lc := newLevelsChange(t, ok, ok, map[string]any{"weight": 2})
	for _, cn := range []domain.ChangeImpact{lc.b, lc.a} {
		if err := lc.accept(cn); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := lc.g.Apply(lc.ctx, lc.c.ID, ""); err != nil {
		t.Fatal(err)
	}
}

// A target changed after the source was accepted is caught when the version commits.
func TestLinkValidatorRunsAtCommit(t *testing.T) {
	ok := map[string]any{"title": "t", "colour": "red"}
	lc := newLevelsChange(t, ok, ok, nil)
	for _, cn := range []domain.ChangeImpact{lc.b, lc.a} {
		if err := lc.accept(cn); err != nil {
			t.Fatal(err)
		}
	}
	// B turns blue and is accepted again: A, still accepted, no longer matches it
	if _, err := lc.g.ImpactNodeUpdate(lc.ctx, lc.c.ID, lc.b.ID, NodeUpdate{Properties: map[string]any{"colour": "blue"}}); err != nil {
		t.Fatal(err)
	}
	if err := lc.accept(lc.b); err != nil {
		t.Fatal(err)
	}
	_, err := lc.g.Apply(lc.ctx, lc.c.ID, "")
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "colours differ") {
		t.Fatalf("commit: %v", err)
	}
}

// Every level also judges a written version directly (checkWritten, what Apply runs).
func TestCheckVersionRunsEveryLevel(t *testing.T) {
	g, _ := levelsGraph(t)
	ctx := context.Background()
	ix := &typeIndex{cat: g.catalog()}
	to := domain.Node{ID: "n2", Version: 1, Key: "B", Type: "lv@Item", Properties: map[string]any{"colour": "blue"}}
	resolve := func(domain.NodeRef) (domain.Node, bool, error) { return to, true, nil }
	link := func(props map[string]any) []domain.Link {
		return []domain.Link{{Type: "lv@refs", To: to.Ref(), Properties: props}}
	}
	n := domain.Node{ID: "n1", Version: 1, Key: "A", Type: "lv@Item", Properties: map[string]any{"title": "t", "colour": "blue"}}
	if err := g.checkVersion(ctx, ix, n, link(map[string]any{"weight": 1}), resolve); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		n    domain.Node
		l    []domain.Link
		want string
	}{
		"attribute":      {domain.Node{Key: "A", Type: "lv@Item", Properties: map[string]any{"title": ""}}, nil, "title is blank"},
		"node":           {domain.Node{Key: "A", Type: "lv@Item", Properties: map[string]any{"tag": "x", "title": "t"}}, nil, "a tag needs a colour"},
		"link attribute": {n, link(map[string]any{"weight": 0}), "weight must be positive"},
		"link":           {domain.Node{ID: "n1", Key: "A", Type: "lv@Item", Properties: map[string]any{"title": "t", "colour": "red"}}, link(nil), "colours differ"},
	} {
		err := g.checkVersion(ctx, ix, tc.n, tc.l, resolve)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a target that cannot be resolved is left to the other checks
	gone := func(domain.NodeRef) (domain.Node, bool, error) { return domain.Node{}, false, nil }
	n.Properties["colour"] = "red"
	if err := g.checkVersion(ctx, ix, n, link(nil), gone); err != nil {
		t.Fatalf("unresolved target: %v", err)
	}
}
