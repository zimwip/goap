package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// versioned is a ChangeLifecycles knowing the published versions of methodology m, each with its lifecycle.
type versioned struct {
	*lifecycles
	latest string
	of     map[string]*domain.Lifecycle // version -> lifecycle
	asked  []string                     // the references Lifecycle was asked about
}

func (v *versioned) MethodologyVersion(_ context.Context, ref string) (string, error) {
	name, version := domain.SplitMethodologyRef(ref)
	if name != "m" {
		return "", nil
	}
	if version == "" {
		return v.latest, nil
	}
	if _, ok := v.of[version]; !ok {
		return "", fmt.Errorf("no version %s: %w", version, ErrInvalid)
	}
	return version, nil
}

func (v *versioned) Lifecycle(_ context.Context, ref string) (*domain.Lifecycle, error) {
	v.asked = append(v.asked, ref)
	_, version := domain.SplitMethodologyRef(ref)
	if version == "" {
		version = v.latest
	}
	return v.of[version], nil
}

func TestChangeMethodologyVersion(t *testing.T) { forEachRepo(t, testChangeMethodologyVersion) }

// A change follows the version of its methodology it was created with, a new change the latest published one, a
// sub-change its parent's; an upgrade moves an open change to another version (ADR 0113).
func testChangeMethodologyVersion(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	flow := func(states ...string) *domain.Lifecycle {
		lc := &domain.Lifecycle{Name: "flow", Initial: states[0]}
		for _, s := range states {
			lc.States = append(lc.States, domain.LifecycleState{Name: s})
		}
		return lc
	}
	v := &versioned{lifecycles: &lifecycles{}, latest: "1", of: map[string]*domain.Lifecycle{
		"1": flow("open", "review"), "2": flow("open", "review", "done"), "3": flow("start"),
		"4": {Name: "other", Initial: "open", States: []domain.LifecycleState{{Name: "open"}}}}}
	w.g.Lifecycles = v
	g := w.g

	old, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "old", Methodology: "m", BaselineID: w.base.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	if old.MethodologyVersion != "1" || old.MethodologyRef() != "m@1" {
		t.Fatalf("a new change takes the latest version: %q", old.MethodologyVersion)
	}
	if v.asked[len(v.asked)-1] != "m@1" {
		t.Fatalf("its lifecycle is the one of that version: %v", v.asked)
	}

	v.latest = "2" // a new version is published
	cur, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "new", Methodology: "m", BaselineID: w.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	if cur.MethodologyVersion != "2" {
		t.Fatalf("a change created after the publication takes it: %q", cur.MethodologyVersion)
	}
	if got, _ := g.Change(ctx, old.ID); got.MethodologyVersion != "1" {
		t.Fatalf("a change keeps its version: %q", got.MethodologyVersion)
	}
	sub, err := g.CreateChange(ctx, NewChange{Title: "sub", Methodology: "m", ParentID: old.ID})
	if err != nil {
		t.Fatal(err)
	}
	if sub.MethodologyVersion != "1" {
		t.Fatalf("a sub-change of the same methodology follows its parent: %q", sub.MethodologyVersion)
	}
	pinned, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "pinned", Methodology: "m", MethodologyVersion: "1", BaselineID: w.base.ID})
	if err != nil || pinned.MethodologyVersion != "1" {
		t.Fatalf("a change may name its version: %q %v", pinned.MethodologyVersion, err)
	}
	if _, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "bad", Methodology: "m", MethodologyVersion: "9", BaselineID: w.base.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown version is refused: %v", err)
	}

	// the upgrade: to the latest, journaled; refused when the change could not stand where it is
	if _, err := g.UpgradeChange(ctx, old.ID, "3"); !errors.Is(err, ErrConflict) {
		t.Fatalf("version 3 has no state open: %v", err)
	}
	if _, err := g.UpgradeChange(ctx, old.ID, "4"); !errors.Is(err, ErrConflict) {
		t.Fatalf("version 4 follows another lifecycle: %v", err)
	}
	up, err := g.UpgradeChange(ctx, old.ID, "")
	if err != nil || up.MethodologyVersion != "2" || up.State != "open" {
		t.Fatalf("upgraded to the latest: %+v %v", up, err)
	}
	entries, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: old.ID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		var h domain.HeaderEdit
		if e.Type == domain.LogChange+".updated" && json.Unmarshal(e.Payload, &h) == nil {
			if hv, ok := h.Fields[HeaderMethodologyVersion]; ok && hv.From == "1" && hv.To == "2" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("the upgrade is journaled: %+v", entries)
	}
	if again, err := g.UpgradeChange(ctx, old.ID, "2"); err != nil || again.MethodologyVersion != "2" {
		t.Fatalf("upgrading to the version it follows changes nothing: %v", err)
	}
	if got, _ := g.Change(ctx, sub.ID); got.MethodologyVersion != "1" {
		t.Fatalf("its sub-change keeps its own: %q", got.MethodologyVersion)
	}
	abandoned := domain.ChangeAbandoned
	if _, err := g.UpdateChange(ctx, cur.ID, ChangePatch{Status: &abandoned}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.UpgradeChange(ctx, cur.ID, "1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a closed change keeps its version: %v", err)
	}
}
