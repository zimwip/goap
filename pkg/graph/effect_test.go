package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// Effects are set on a change impact or an item of the change (ADR 0107): a subject the change does not have, or another
// effect, is refused; the blackboard folds them.
func TestEffects(t *testing.T) { forEachRepo(t, testEffects) }

func testEffects(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	c, _ := f.g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "c", BaselineID: f.base.ID})
	pre := f.req.Ref()
	ns, err := f.g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "reword"}})
	if err != nil {
		t.Fatal(err)
	}
	mark := func(subject, id string, effects map[string]any) error {
		_, err := f.g.AddItems(ctx, c.ID, []domain.ChangeItem{domain.EffectMark{Subject: subject, ID: id, Effects: effects}.Item()})
		return err
	}
	for _, bad := range []struct {
		subject, id string
		effects     map[string]any
	}{
		{domain.EffectOnImpact, "nope", map[string]any{"formulated": true}},
		{domain.EffectOnItem, "nope", map[string]any{"assessed": true}},
		{domain.EffectOnImpact, string(ns[0].ID), map[string]any{"not a name": true}},
		{domain.EffectOnImpact, string(ns[0].ID), map[string]any{"x": []any{1}}},
		{"node", string(ns[0].ID), map[string]any{"x": true}},
	} {
		if err := mark(bad.subject, bad.id, bad.effects); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: expected invalid, got %v", bad, err)
		}
	}
	if err := mark(domain.EffectOnImpact, string(ns[0].ID), map[string]any{"formulated": true}); err != nil {
		t.Fatal(err)
	}
	risk1, err := f.g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindArtifact, Type: "note", Data: map[string]any{"key": "R1", "text": "v1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := mark(domain.EffectOnItem, string(risk1[0].ID), map[string]any{"assessed": "high"}); err != nil {
		t.Fatal(err)
	}
	effect, _ := f.g.Change(ctx, c.ID)
	if err := mark(domain.EffectOnItem, string(effect.Items[len(effect.Items)-1].ID), map[string]any{"x": true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an effect on an effect: %v", err)
	}
	// a later version of the record carries the effects of the record
	risk2, err := f.g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindArtifact, Type: "note", Data: map[string]any{"key": "R1", "text": "v2"}}})
	if err != nil {
		t.Fatal(err)
	}
	bb, err := f.g.Blackboard(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	fx := bb.Change.Effects()
	if e := fx.OfImpact(ns[0].ID)["formulated"]; e.Value != true {
		t.Fatalf("impact effects: %+v", fx.OfImpact(ns[0].ID))
	}
	if e := fx.OfItem(risk2[0])["assessed"]; e.Value != "high" || e.On != risk1[0].ID {
		t.Fatalf("record effects: %+v", fx.OfItem(risk2[0]))
	}
}
