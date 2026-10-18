package accesse2e

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/access"
)

// TestProjectOwnersAndAdministrators (ADR 0135): an owner holds every right of an administrator and those of
// possession (the flags, stopping the project); an administrator creates sub-projects; a member does neither; a
// project that had an owner keeps one.
func TestProjectOwnersAndAdministrators(t *testing.T) {
	ctx := context.Background()
	g := floorGraph(t)
	dir := &access.Directory{Graph: g, TTL: 1}
	g.Validators = append(g.Validators, access.ProjectScopeValidator{}, access.ProjectOwnerValidator{})
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"alice", "bob", "carol", "dave"} { // alice, the first, is the platform administrator
		if err := graphsvc.EnsureUser(ctx, g, u); err != nil {
			t.Fatal(err)
		}
	}
	ref := func(key string) *domain.NodeRef {
		n, err := g.NodeByKey(ctx, access.NamespaceOrganisation, key)
		if err != nil {
			t.Fatal(err)
		}
		r := n.Ref()
		return &r
	}
	commit := func(who string, edits ...graph.NodeEdit) error {
		head, err := g.BranchHead(ctx, access.NamespaceOrganisation, domain.MainBranch)
		if err != nil {
			t.Fatal(err)
		}
		_, err = g.Commit(authz.With(ctx, authz.Principal{Subject: who}), graph.Commit{ProjectID: access.DefaultProject, Namespace: access.NamespaceOrganisation,
			Title: "t", Baseline: head.ID, By: who, Edits: edits})
		return err
	}
	project := func(key, parent string) graph.NodeEdit {
		return graph.NodeEdit{Key: key, Type: access.NodeTypeProjectUnit, Props: access.ProjectUnit{Name: key}.Props(), Rationale: "t",
			Links: []graph.LinkEdit{{Type: access.LinkProjectPartOf, To: ref(parent)}}}
	}
	assign := func(user, proj string, a access.Assignment) graph.NodeEdit {
		return graph.NodeEdit{Key: access.AssignmentKey(access.UserKey(user), proj), Type: access.NodeTypeAssignment, Props: a.Props(), Rationale: "t",
			Links: []graph.LinkEdit{{Type: access.LinkAssignsOrg, To: ref(access.UserKey(user))}, {Type: access.LinkAssignsProject, ToKey: proj}}}
	}
	update := func(key string, props map[string]any) graph.NodeEdit {
		return graph.NodeEdit{Pre: ref(key), Props: props, Rationale: "t"}
	}
	forbidden := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("%s: want forbidden, got %v", what, err)
		}
	}

	// the platform administrator creates a project and gives it an owner (bob) and an administrator (carol)
	if err := commit("alice", project("PROJ-A", access.DefaultProject)); err != nil {
		t.Fatal(err)
	}
	if err := commit("alice", assign("bob", "PROJ-A", access.Assignment{Owner: true}), assign("carol", "PROJ-A", access.Assignment{Admin: true})); err != nil {
		t.Fatal(err)
	}
	// a platform Assignment carries no flag, whoever writes it
	if err := commit("alice", graph.NodeEdit{Key: access.PlatformAssignmentKey(access.UserKey("dave")), Type: access.NodeTypeAssignment,
		Props: access.Assignment{Owner: true}.Props(), Rationale: "t", Links: []graph.LinkEdit{{Type: access.LinkAssignsOrg, To: ref(access.UserKey("dave"))}}}); !errors.Is(err, graph.ErrInvalid) {
		t.Fatalf("flagged platform assignment: %v", err)
	}

	// the administrator creates a sub-project and gives roles; a member of nothing does neither
	if err := commit("carol", project("PROJ-A1", "PROJ-A")); err != nil {
		t.Fatalf("an administrator creates a sub-project: %v", err)
	}
	forbidden("a stranger creates a sub-project", commit("dave", project("PROJ-A2", "PROJ-A")))
	forbidden("an administrator creates a root project", commit("carol", project("PROJ-B", access.DefaultProject)))
	if err := commit("carol", assign("dave", "PROJ-A1", access.Assignment{Roles: []string{"developer"}})); err != nil {
		t.Fatalf("an administrator gives a role below: %v", err)
	}
	// the flags and stopping the project are the owner's
	forbidden("an administrator sets a flag", commit("carol", assign("dave", "PROJ-A", access.Assignment{Admin: true})))
	forbidden("an administrator makes themselves owner", commit("carol", update(access.AssignmentKey(access.UserKey("carol"), "PROJ-A"), map[string]any{access.PropOwner: true})))
	forbidden("an administrator stops the project", commit("carol", update("PROJ-A", map[string]any{access.PropStopped: true})))
	if err := commit("bob", update(access.AssignmentKey(access.UserKey("dave"), "PROJ-A1"), map[string]any{access.PropAdmin: true})); err != nil {
		t.Fatalf("the owner sets a flag below: %v", err)
	}
	if err := commit("bob", update("PROJ-A", map[string]any{access.PropStopped: true})); err != nil {
		t.Fatalf("the owner stops the project: %v", err)
	}
	// the parent has a new version: its sub-project and the Assignments naming it still reach it (suspect links)
	for _, p := range []string{"PROJ-A", "PROJ-A1"} {
		if stopped, err := dir.ProjectStopped(ctx, p); err != nil || !stopped {
			t.Fatalf("%s is stopped: %v %v", p, stopped, err)
		}
	}
	if snap, err := dir.Snapshot(ctx); err != nil || !slices.Contains(snap.ProjectChain("PROJ-A1"), "PROJ-A") {
		t.Fatalf("the sub-project keeps its parent once the parent is edited: %v", err)
	}
	if err := commit("bob", update("PROJ-A", map[string]any{access.PropStopped: nil})); err != nil {
		t.Fatalf("the owner restarts the project: %v", err)
	}

	// always an owner: the last one cannot go, a hand-off in one change can
	lastOwner := access.AssignmentKey(access.UserKey("bob"), "PROJ-A")
	if err := commit("bob", update(lastOwner, map[string]any{access.PropOwner: nil})); !errors.Is(err, graph.ErrInvalid) {
		t.Fatalf("removing the last owner: %v", err)
	}
	if err := commit("bob", update(access.AssignmentKey(access.UserKey("carol"), "PROJ-A"), map[string]any{access.PropOwner: true}), update(lastOwner, map[string]any{access.PropOwner: nil})); err != nil {
		t.Fatalf("handing the project over: %v", err)
	}
}
