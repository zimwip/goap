package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// A created OrgUnit/ProjectUnit/User must name exactly one parent/membership link, except the two roots
// (ADR 0040, created by the bootstrap, ADR 0054). Checked before anything is written (checkRequiredParent).
func TestCommitRequiresAParent(t *testing.T) { forEachRepo(t, testCommitRequiresAParent) }

func testCommitRequiresAParent(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	const ns = "organisation"

	// the root unit is the bootstrap's (ADR 0054)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	org, err := g.NodeByKey(ctx, ns, domain.DefaultOrg)
	if err != nil {
		t.Fatal(err)
	}
	base, err := g.BranchHead(ctx, ns, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}

	// no parent at all: refused
	if _, err := g.Commit(ctx, Commit{Namespace: ns, Title: "team", Baseline: base.ID, By: "t",
		Edits: []NodeEdit{{Key: "TEAM-A", Type: NodeTypeOrgUnit, Props: map[string]any{"name": "A"}}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("OrgUnit with no part_of = %v, want ErrInvalid", err)
	}

	// exactly one parent: accepted
	orgRef := org.Ref()
	if _, err := g.Commit(ctx, Commit{Namespace: ns, Title: "team", Baseline: base.ID, By: "t",
		Edits: []NodeEdit{{Key: "TEAM-A", Type: NodeTypeOrgUnit, Props: map[string]any{"name": "A"},
			Links: []LinkEdit{{Type: LinkPartOf, To: &orgRef}}}}}); err != nil {
		t.Fatalf("OrgUnit with one part_of: %v", err)
	}

	// the root itself needs none
	head, err := g.BranchHead(ctx, ns, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit(ctx, Commit{Namespace: ns, Title: "rename root", Baseline: head.ID, By: "t",
		Edits: []NodeEdit{{Pre: refPtr(org.Ref()), Props: map[string]any{"name": "Default org"}}}}); err != nil {
		t.Fatalf("renaming the root: %v", err)
	}

	// a User needs exactly one member_of
	head, err = g.BranchHead(ctx, ns, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit(ctx, Commit{Namespace: ns, Title: "user", Baseline: head.ID, By: "t",
		Edits: []NodeEdit{{Key: "USR:alice", Type: domain.TypeUser, Props: map[string]any{"subject": "alice"}}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("User with no member_of = %v, want ErrInvalid", err)
	}
}

// A modify edit that adds a second member_of without removing the first is rejected after the write, not
// silently accepted — the safety net for a client that built its edit without knowing about the existing
// link, e.g. from a baseline snapshot that predates it (checkParentInvariant, ADR 0040: EnsureUser writes
// member_of by import, which does not itself advance any baseline).
func TestCommitRejectsASecondMembership(t *testing.T) {
	forEachRepo(t, testCommitRejectsASecondMembership)
}

func testCommitRejectsASecondMembership(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	const ns = "organisation"

	orgA, err := g.CreateNode(ctx, NewNode{Namespace: ns, Key: "ORG-A", Type: NodeTypeOrgUnit, Properties: map[string]any{"name": "A"}})
	if err != nil {
		t.Fatal(err)
	}
	orgB, err := g.CreateNode(ctx, NewNode{Namespace: ns, Key: "ORG-B", Type: NodeTypeOrgUnit, Properties: map[string]any{"name": "B"}})
	if err != nil {
		t.Fatal(err)
	}
	user, err := g.CreateNode(ctx, NewNode{Namespace: ns, Key: "USR:alice", Type: domain.TypeUser, Properties: map[string]any{"subject": "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	// member_of ORG-A, a raw link attributed to a change of its own (no new node version, ADR 0049)
	if _, err := g.Link(ctx, testChange(t, g, ns), domain.LinkMemberOf, user.Ref(), orgA.Ref(), nil); err != nil {
		t.Fatal(err)
	}
	base, err := g.BranchHead(ctx, ns, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}

	// a client that never saw the existing member_of ORG-A (e.g. read it from an older baseline) adds
	// member_of ORG-B without removing it: must be refused, not leave alice with two organisations.
	userRef := user.Ref()
	orgBRef := orgB.Ref()
	if _, err := g.Commit(ctx, Commit{Namespace: ns, Title: "move", Baseline: base.ID, By: "t",
		Edits: []NodeEdit{{Pre: &userRef, Links: []LinkEdit{{Type: domain.LinkMemberOf, To: &orgBRef}}}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("adding a second member_of = %v, want ErrInvalid", err)
	}

	// the correct move (remove the old, add the new) succeeds
	links, err := g.OutLinksOf(ctx, user.Ref())
	if err != nil {
		t.Fatal(err)
	}
	var oldLink domain.LinkID
	for _, l := range links {
		if l.Type == domain.LinkMemberOf {
			oldLink = l.ID
		}
	}
	if oldLink == "" {
		t.Fatal("alice must already be member_of ORG-A")
	}
	if _, err := g.Commit(ctx, Commit{Namespace: ns, Title: "move", Baseline: base.ID, By: "t",
		Edits: []NodeEdit{{Pre: &userRef, RemoveLinks: []domain.LinkID{oldLink}, Links: []LinkEdit{{Type: domain.LinkMemberOf, To: &orgBRef}}}}}); err != nil {
		t.Fatalf("move (remove old, add new): %v", err)
	}
}
