package graph

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

func impactByKey(t *testing.T, nodes []domain.ChangeImpact, key string) domain.ChangeImpact {
	t.Helper()
	for _, cn := range nodes {
		if cn.Key == key {
			return cn
		}
	}
	t.Fatalf("no impact %s in %+v", key, nodes)
	return domain.ChangeImpact{}
}

// The views of a change carry the validation of each draft: the message an acceptance would give, valid once edited.
func TestImpactValidationInViews(t *testing.T) {
	lc := newLevelsChange(t, map[string]any{"title": " ", "colour": "red"}, map[string]any{"title": "t", "colour": "red"}, nil)
	views := map[string]func() []domain.ChangeImpact{
		"change": func() []domain.ChangeImpact { return must[domain.Change](t)(lc.g.ChangeChecked(lc.ctx, lc.c.ID)).Nodes },
		"blackboard": func() []domain.ChangeImpact {
			return must[domain.Blackboard](t)(lc.g.Blackboard(lc.ctx, lc.c.ID)).Change.Nodes
		},
	}
	acceptErr := lc.accept(lc.a)
	if acceptErr == nil {
		t.Fatal("A is invalid")
	}
	want := strings.TrimSuffix(acceptErr.Error(), ": "+ErrInvalid.Error())
	for name, view := range views {
		a, b := impactByKey(t, view(), "A"), impactByKey(t, view(), "B")
		if a.Validation == nil || a.Validation.Valid || a.Validation.Message != want {
			t.Errorf("%s: A = %+v, want message %q", name, a.Validation, want)
		}
		if b.Validation == nil || !b.Validation.Valid || b.Validation.Message != "" {
			t.Errorf("%s: B = %+v", name, b.Validation)
		}
	}
	// an edit fixing the draft changes the log: the next view is valid
	if _, err := lc.g.ImpactNodeUpdate(lc.ctx, lc.c.ID, lc.a.ID, NodeUpdate{Properties: map[string]any{"title": "ok"}}); err != nil {
		t.Fatal(err)
	}
	for name, view := range views {
		if v := impactByKey(t, view(), "A").Validation; v == nil || !v.Valid {
			t.Errorf("%s: after the fix A = %+v", name, v)
		}
	}
	if err := lc.accept(lc.a); err != nil {
		t.Fatalf("accept after the fix: %v", err)
	}
	// and another edit breaks it again, the cache follows the log
	if _, err := lc.g.ImpactNodeUpdate(lc.ctx, lc.c.ID, lc.a.ID, NodeUpdate{Properties: map[string]any{"tag": "x", "colour": ""}}); err != nil {
		t.Fatal(err)
	}
	if v := impactByKey(t, views["change"](), "A").Validation; v == nil || v.Valid || !strings.Contains(v.Message, "a tag needs a colour") {
		t.Errorf("after breaking: %+v", v)
	}
}

// The validation is a read model: nothing stored carries it, and an impact with no draft (a committed change) has none.
func TestImpactValidationNotStoredAndAbsentWithoutDraft(t *testing.T) {
	lc := newLevelsChange(t, map[string]any{"title": "t", "colour": "red"}, map[string]any{"title": "t", "colour": "red"}, nil)
	viewed := must[domain.Change](t)(lc.g.ChangeChecked(lc.ctx, lc.c.ID))
	for _, cn := range viewed.Nodes {
		if cn.Validation == nil {
			t.Fatalf("%s has a draft and no validation", cn.Key)
		}
	}
	for _, cn := range must[[]domain.ChangeImpact](t)(lc.g.ListChangeImpacts(lc.ctx, lc.c.ID)) {
		if cn.Validation != nil {
			t.Fatalf("stored impact %s carries a validation", cn.Key)
		}
	}
	for _, cn := range []domain.ChangeImpact{lc.b, lc.a} {
		if err := lc.accept(cn); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := lc.g.Apply(lc.ctx, lc.c.ID, ""); err != nil {
		t.Fatal(err)
	}
	for _, cn := range must[domain.Change](t)(lc.g.ChangeChecked(lc.ctx, lc.c.ID)).Nodes {
		if cn.Validation != nil {
			t.Errorf("committed impact %s has a validation %+v", cn.Key, cn.Validation)
		}
	}
}

// An impact the change withdrew holds no draft any more: no validation.
func TestImpactValidationWithdrawn(t *testing.T) {
	lc := newLevelsChange(t, map[string]any{"title": " "}, map[string]any{"title": "t"}, nil)
	if err := lc.g.WithdrawImpact(lc.ctx, lc.c.ID, lc.a.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	for _, cn := range must[domain.Change](t)(lc.g.ChangeChecked(lc.ctx, lc.c.ID)).Nodes {
		if cn.Key == "A" && cn.Validation != nil {
			t.Errorf("withdrawn impact has a validation %+v", cn.Validation)
		}
	}
}
