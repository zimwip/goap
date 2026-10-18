package graph

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

func TestSetChangeMethodology(t *testing.T) { forEachRepo(t, testSetChangeMethodology) }

// A change created with no methodology takes one later, as a creation would have given it: the latest version, the
// main goal, the lifecycle in its initial state; the gate decides whether it applies; it is taken once.
func testSetChangeMethodology(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	v := &versioned{lifecycles: &lifecycles{goal: "ship"}, latest: "2", of: map[string]*domain.Lifecycle{
		"2": {Name: "flow", Initial: "open", States: []domain.LifecycleState{{Name: "open"}}}}}
	w.g.Lifecycles = v
	g := w.g
	c, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "bare", BaselineID: w.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	if c.Methodology != "" || c.Lifecycle != "" {
		t.Fatalf("created with none: %+v", c)
	}
	if _, err := g.SetChangeMethodology(ctx, c.ID, "unknown"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown methodology: %v", err)
	}
	g.MethodologyGate = func(_ context.Context, ch domain.Change, m string) error {
		if ch.ProjectID != "PROJ-ROOT" || m != "m" {
			return fmt.Errorf("%s does not apply: %w", m, ErrInvalid)
		}
		return nil
	}
	got, err := g.SetChangeMethodology(ctx, c.ID, "m")
	if err != nil {
		t.Fatal(err)
	}
	if got.Methodology != "m" || got.MethodologyVersion != "2" || got.Goal != "ship" || got.Lifecycle != "flow" || got.State != "open" {
		t.Fatalf("took %+v", got)
	}
	if read, err := g.Change(ctx, c.ID); err != nil || read.MethodologyRef() != "m@2" || read.State != "open" {
		t.Fatalf("stored %+v %v", read, err)
	}
	log, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range log {
		if e.Type == domain.LogChange+".updated" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no change.updated entry: %+v", log)
	}
	if _, err := g.SetChangeMethodology(ctx, c.ID, "m"); !errors.Is(err, ErrConflict) {
		t.Fatalf("taken twice: %v", err)
	}
	// the gate refuses a methodology that does not apply to the project
	other, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "other", BaselineID: w.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	g.MethodologyGate = func(context.Context, domain.Change, string) error { return fmt.Errorf("no: %w", ErrInvalid) }
	if _, err := g.SetChangeMethodology(ctx, other.ID, "m"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("gate: %v", err)
	}
}
