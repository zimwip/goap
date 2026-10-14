package graph

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// chainGate is a guardian chain of two methodologies (ADR 0106): "release" integrates "sdlc", exclusively (a change
// of sdlc merges only once attached to a release).
type chainGate struct{ asked []string }

func (*chainGate) MethodologyVersion(context.Context, string) (string, error)   { return "", nil }
func (*chainGate) Lifecycle(context.Context, string) (*domain.Lifecycle, error) { return nil, nil }
func (*chainGate) DefaultGoal(context.Context, string) (string, error)          { return "", nil }
func (*chainGate) Guard(context.Context, domain.Blackboard, string, string, string) (bool, error) {
	return true, nil
}
func (*chainGate) Gate(context.Context, domain.Blackboard, domain.Transition, string) (domain.GateResult, error) {
	return domain.GateResult{}, nil
}
func (*chainGate) Merge(context.Context, domain.Change, domain.Blackboard) (bool, bool, error) {
	return false, false, nil
}
func (*chainGate) Guardians(_ context.Context, m, _ string) ([]domain.GuardianRef, error) {
	if m == "sdlc" {
		return []domain.GuardianRef{{Methodology: "release"}, {Methodology: "sdlc"}}, nil
	}
	return []domain.GuardianRef{{Methodology: m}}, nil
}
func (*chainGate) Integrates(_ context.Context, integrator, m string) (bool, error) {
	return integrator == "release" && m == "sdlc", nil
}
func (g *chainGate) CheckGuardians(_ context.Context, bb domain.Blackboard, _ string, ancestors []domain.Change) (string, error) {
	g.asked = append(g.asked, string(bb.Change.ID))
	if len(ancestors) == 0 || ancestors[0].Methodology != "release" {
		return `release refuses: integrator.attached`, nil
	}
	return "", nil
}

func TestAttachChange(t *testing.T) { forEachRepo(t, testAttachChange) }

func testAttachChange(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newOrgWorld(t, repo)
	g := w.g
	gate := &chainGate{}
	g.Lifecycles = gate

	release := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "R1", Methodology: "release", BaselineID: w.base.ID, OwnBranch: true}))
	cand := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "fix", Methodology: "sdlc", BaselineID: w.base.ID, OwnBranch: true}))
	names := func(c domain.Change) []string {
		var out []string
		for _, r := range c.Guardians {
			out = append(out, r.Methodology)
		}
		return out
	}
	if got := names(cand); !slices.Equal(got, []string{"release", "sdlc"}) {
		t.Fatalf("a root change of sdlc gets the chain of its project: %v", got)
	}
	if got := names(must[domain.Change](t)(g.Change(ctx, cand.ID))); !slices.Equal(got, []string{"release", "sdlc"}) {
		t.Fatalf("the chain is stored on the change: %v", got)
	}

	pre := w.cmp3.Ref()
	added := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, cand.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "fix"}}))
	if _, err := g.edit(ctx, cand.ID, added[0].ID, edit{Properties: map[string]any{"title": "fixed"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.accept(ctx, cand.ID, added[0].ID, "u", "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.CommitChange(ctx, cand.ID, ""); err != nil {
		t.Fatal(err)
	}
	// the outer guardian holds the merge of a change not attached to a release
	if _, err := g.IntegrateChange(ctx, cand.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("an exclusive integrator refuses the merge of an unattached change: %v", err)
	}

	// a change of a methodology the parent does not integrate is not attached
	other := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "other", Methodology: "release", BaselineID: w.base.ID, OwnBranch: true}))
	if _, err := g.AttachChange(ctx, other.ID, release.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("attach outside integrates: %v", err)
	}
	if _, err := g.AttachChange(ctx, cand.ID, cand.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("attach to itself: %v", err)
	}

	att, err := g.AttachChange(ctx, cand.ID, release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if att.ParentID != release.ID || !slices.Equal(names(att), []string{"release", "sdlc"}) {
		t.Fatalf("attached: parent %s, chain %v", att.ParentID, names(att))
	}
	rel := must[domain.Change](t)(g.Change(ctx, release.ID))
	if b := must[domain.Branch](t)(g.Branch(ctx, att.Namespace, att.Branch)); b.Parent != rel.Branch {
		t.Fatalf("the attached change forks from the branch of its parent %s: %+v", rel.Branch, b)
	}
	if v := must[domain.NodeView](t)(g.ChangeNodeViewByKey(ctx, cand.ID, "", domain.DefaultNamespace, "CMP-3")); v.Properties["title"] != "fixed" {
		t.Fatalf("what the change committed is rebased onto its parent: %v", v.Properties)
	}
	if _, err := g.AttachChange(ctx, cand.ID, release.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("attached twice: %v", err)
	}

	// detached, it is a root change again, its chain recomputed
	det, err := g.DetachChange(ctx, cand.ID)
	if err != nil {
		t.Fatal(err)
	}
	if det.ParentID != "" || !slices.Equal(names(det), []string{"release", "sdlc"}) {
		t.Fatalf("detached: parent %s, chain %v", det.ParentID, names(det))
	}
	if _, err := g.DetachChange(ctx, cand.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("detach a root change: %v", err)
	}

	// attached again, it merges into the release, which holds its work
	if _, err := g.AttachChange(ctx, cand.ID, release.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := g.IntegrateChange(ctx, cand.ID, nil); err != nil {
		t.Fatal(err)
	}
	if v := must[domain.NodeView](t)(g.ChangeNodeViewByKey(ctx, release.ID, "", domain.DefaultNamespace, "CMP-3")); v.Properties["title"] != "fixed" {
		t.Fatalf("the release holds the work of its candidate: %v", v.Properties)
	}
	if n := must[domain.Node](t)(g.NodeByKey(ctx, domain.DefaultNamespace, "CMP-3")); n.Properties["title"] == "fixed" {
		t.Fatalf("main moved before the release merged: %+v", n)
	}
	if !slices.Contains(gate.asked, string(cand.ID)) {
		t.Fatalf("the outer guardians were asked: %v", gate.asked)
	}
}
