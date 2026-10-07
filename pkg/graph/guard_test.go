package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// The bootstrap (ADR 0054) writes the root unit and the root project in one applied change: the root unit owns both,
// both were created in the root project, the root project is its own parent. Running it again changes nothing.
func TestBootstrap(t *testing.T) { forEachRepo(t, testBootstrap) }

func testBootstrap(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	org := must[domain.Node](t)(g.NodeByKey(ctx, NamespaceOrganisation, rootOrg(g)))
	proj := must[domain.Node](t)(g.NodeByKey(ctx, NamespaceOrganisation, rootProject(g)))
	for _, n := range []domain.Node{org, proj} {
		if n.Owner != org.ID || n.Project != proj.ID || n.ChangeID == "" {
			t.Fatalf("%s: owner %s project %s change %s", n.Key, n.Owner, n.Project, n.ChangeID)
		}
	}
	if org.Type != NodeTypeOrgUnit || proj.Type != NodeTypeProjectUnit {
		t.Fatalf("roots: %+v %+v", org, proj)
	}
	c := must[domain.Change](t)(g.Change(ctx, org.ChangeID))
	if c.Status != domain.ChangeApplied || c.OwnerOrg != rootOrg(g) || c.ProjectID != rootProject(g) || c.ResultBaselineID == "" {
		t.Fatalf("bootstrap change: %+v", c)
	}
	head := must[domain.Baseline](t)(g.BranchHead(ctx, NamespaceOrganisation, domain.MainBranch))
	if !head.Contains(org.Ref()) || !head.Contains(proj.Ref()) {
		t.Fatalf("the head of the organisation holds the roots: %+v", head.Nodes)
	}
	links := must[[]domain.Link](t)(g.OutLinksOf(ctx, proj.Ref()))
	if len(links) != 1 || links[0].Type != LinkProjectPartOf || links[0].To != proj.Ref() || links[0].ChangeID != c.ID {
		t.Fatalf("the root project is its own parent: %+v", links)
	}
	// again, on this graph and on another one over the same storage: nothing changes
	before := must[[]domain.Change](t)(g.Changes(ctx))
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := New(repo).Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if after := must[[]domain.Change](t)(g.Changes(ctx)); len(after) != len(before) {
		t.Fatalf("bootstrapped twice: %d changes, then %d", len(before), len(after))
	}
}

// The guard (ADR 0054) refuses, whatever the storage, a write that names no change or a change that does not exist,
// and a change that names no unit or project, or names nodes that are not of their structures. Nothing of a refused
// transaction is kept.
func TestGuardRefusesWritesOutsideAChange(t *testing.T) {
	forEachRepo(t, testGuardRefusesWritesOutsideAChange)
}

func testGuardRefusesWritesOutsideAChange(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	org := must[domain.Node](t)(g.NodeByKey(ctx, NamespaceOrganisation, rootOrg(g)))
	proj := must[domain.Node](t)(g.NodeByKey(ctx, NamespaceOrganisation, rootProject(g)))
	write := func(fn func(tx Tx) error) error { return g.repo.InTx(ctx, fn) }
	boot := must[domain.Change](t)(g.Change(ctx, org.ChangeID))
	boot.Items, boot.Nodes = nil, nil
	node := func(key string, change domain.ChangeID) domain.Node {
		return domain.Node{ID: domain.NodeID(g.newID()), Version: 1, Branch: domain.MainBranch, Reason: domain.ReasonCreate, Key: key, Type: "Note",
			ChangeID: change, CreatedAt: g.now()}
	}
	for name, fn := range map[string]func(tx Tx) error{
		"a node version without a change": func(tx Tx) error { return tx.PutNode(ctx, node("N-1", "")) },
		"a node version of a missing change": func(tx Tx) error {
			return tx.PutNode(ctx, domain.Node{ID: domain.NodeID(g.newID()), Version: 1, Branch: domain.MainBranch, Key: "N-2", Type: "Note",
				ChangeID: domain.ChangeID(g.newID()), Owner: org.ID, Project: proj.ID, CreatedAt: g.now()})
		},
		"a link without a change": func(tx Tx) error {
			return tx.PutLink(ctx, domain.Link{ID: domain.LinkID(g.newID()), Type: LinkProjectPartOf, From: proj.Ref(), To: proj.Ref()})
		},
		"a branch membership without a change": func(tx Tx) error { return tx.JoinBranch(ctx, org.Ref(), "elsewhere", "") },
		"a change without a unit": func(tx Tx) error {
			return tx.PutChange(ctx, domain.Change{ID: domain.ChangeID(g.newID()), Title: "x", Status: domain.ChangeDraft, ProjectID: rootProject(g)})
		},
		"a change held by a project": func(tx Tx) error {
			c := boot
			c.ID, c.OwnerOrg = domain.ChangeID(g.newID()), rootProject(g)
			return tx.PutChange(ctx, c)
		},
		"a change acting in a unit": func(tx Tx) error {
			c := boot
			c.ID, c.ProjectID = domain.ChangeID(g.newID()), rootOrg(g)
			return tx.PutChange(ctx, c)
		},
		"a version owned by a project": func(tx Tx) error {
			n := node("N-3", org.ChangeID)
			n.Owner, n.Project = proj.ID, proj.ID
			return tx.PutNode(ctx, n)
		},
		"a node created in a unit": func(tx Tx) error {
			n := node("N-4", org.ChangeID)
			n.Owner, n.Project = org.ID, org.ID
			return tx.PutNode(ctx, n)
		},
	} {
		if err := write(fn); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	for _, key := range []string{"N-1", "N-2", "N-3", "N-4"} {
		if _, err := g.NodeByKey(ctx, domain.DefaultNamespace, key); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s was kept: %v", key, err)
		}
	}
}

// A node is created in the project of the change that creates it and owned by the unit holding it; its next versions
// keep the project and the owner, unless a change transfers it to another unit (ADR 0054).
func TestOwnerAndProjectOfNodes(t *testing.T) { forEachRepo(t, testOwnerAndProjectOfNodes) }

func testOwnerAndProjectOfNodes(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	root := must[domain.Node](t)(g.NodeByKey(ctx, NamespaceOrganisation, rootOrg(g)))
	rootProj := must[domain.Node](t)(g.NodeByKey(ctx, NamespaceOrganisation, rootProject(g)))
	ns := NamespaceOrganisation
	commit := func(c Commit) CommitResult {
		t.Helper()
		c.Namespace, c.By = ns, "t"
		if c.Title == "" {
			c.Title = "edit"
		}
		return must[CommitResult](t)(g.Commit(ctx, c))
	}
	commit(Commit{ProjectID: "PROJ-ROOT", Edits: []NodeEdit{
		{Key: "TEAM", Type: NodeTypeOrgUnit, Props: map[string]any{"name": "Team"}, Links: []LinkEdit{{Type: LinkPartOf, To: refPtr(root.Ref())}}},
		{Key: "PROJ-A", Type: NodeTypeProjectUnit, Props: map[string]any{"name": "A"}, Links: []LinkEdit{{Type: LinkProjectPartOf, To: refPtr(rootProj.Ref())}}},
	}})
	team := must[domain.Node](t)(g.NodeByKey(ctx, ns, "TEAM"))
	pa := must[domain.Node](t)(g.NodeByKey(ctx, ns, "PROJ-A"))
	if team.Owner != root.ID || team.Project != rootProj.ID {
		t.Fatalf("a change naming no unit: the root unit; the bootstrap names the root project: %+v", team)
	}

	commit(Commit{OwnerOrg: "TEAM", ProjectID: "PROJ-A", Edits: []NodeEdit{{Key: "NOTE", Type: "Note", Props: map[string]any{"title": "one"}}}})
	note := must[domain.Node](t)(g.NodeByKey(ctx, ns, "NOTE"))
	if note.Owner != team.ID || note.Project != pa.ID {
		t.Fatalf("created by TEAM in PROJ-A: %+v", note)
	}
	// edited by a change of the root unit in the root project: still TEAM's, still created in PROJ-A
	commit(Commit{ProjectID: "PROJ-ROOT", Edits: []NodeEdit{{Pre: refPtr(note.Ref()), Props: map[string]any{"title": "two"}}}})
	note = must[domain.Node](t)(g.NodeByKey(ctx, ns, "NOTE"))
	if note.Version != 2 || note.Owner != team.ID || note.Project != pa.ID {
		t.Fatalf("the next version keeps owner and project: %+v", note)
	}
	// transferred to the root unit
	commit(Commit{ProjectID: "PROJ-ROOT", Edits: []NodeEdit{{Pre: refPtr(note.Ref()), Owner: rootOrg(g)}}})
	note = must[domain.Node](t)(g.NodeByKey(ctx, ns, "NOTE"))
	if note.Version != 3 || note.Owner != root.ID || note.Project != pa.ID {
		t.Fatalf("transferred to the root unit: %+v", note)
	}
	if v2 := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: note.ID, Version: 2})); v2.Owner != team.ID {
		t.Fatalf("the earlier version stays TEAM's: %+v", v2)
	}
	// only a unit owns
	if _, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Namespace: ns, Title: "x", By: "t", Edits: []NodeEdit{{Pre: refPtr(note.Ref()), Owner: "PROJ-A"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a project as the owner: %v", err)
	}
}

// A change names the project it acts in (ADR 0091): none is defaulted, a sub-change inherits its parent's.
func TestChangeNeedsAProject(t *testing.T) { forEachRepo(t, testChangeNeedsAProject) }

func testChangeNeedsAProject(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	base := must[domain.Baseline](t)(g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch))
	if _, err := g.CreateChange(ctx, NewChange{Title: "x", BaselineID: base.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a change naming no project: %v", err)
	}
	if _, err := g.CreateChange(ctx, NewChange{Title: "x", BaselineID: base.ID, ProjectID: "PROJ-NOPE"}); err == nil {
		t.Fatal("a change naming an unknown project was created")
	}
	if _, err := g.Commit(ctx, Commit{Title: "x", By: "t", Edits: []NodeEdit{{Key: "N-X", Type: "Design"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a commit naming no project: %v", err)
	}
	if _, err := g.MergeBranch(ctx, MergeRequest{From: "a", Into: "b"}); !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrNotFound) {
		t.Fatalf("a merge naming no project: %v", err)
	}
	parent := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "p", BaselineID: base.ID, ProjectID: rootProject(g), OwnBranch: true}))
	if parent.ProjectID != rootProject(g) {
		t.Fatalf("project = %s", parent.ProjectID)
	}
	sub := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "s", ParentID: parent.ID}))
	if sub.ProjectID != parent.ProjectID {
		t.Fatalf("a sub-change inherits the project of its parent: %s", sub.ProjectID)
	}
}

// The storage repeats the rule (ADR 0054): a node version, a link or a branch membership without a change is refused
// by the database itself, not only by the guard.
func TestStorageRequiresAChange(t *testing.T) { forEachRepo(t, testStorageRequiresAChange) }

func testStorageRequiresAChange(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	org := must[domain.Node](t)(g.NodeByKey(ctx, NamespaceOrganisation, rootOrg(g)))
	var exec func(q string, args ...any) error
	switch r := repo.(type) {
	case *SQLite:
		exec = func(q string, args ...any) error { _, err := r.db.ExecContext(ctx, q, args...); return err }
	case *Postgres:
		exec = func(q string, args ...any) error { _, err := r.pool.Exec(ctx, q, args...); return err }
	default:
		t.Skip("no database constraints in memory: the guard alone")
	}
	ph := func(i int) string { // the placeholder of the dialect
		if _, ok := repo.(*Postgres); ok {
			return "$" + string(rune('0'+i))
		}
		return "?"
	}
	for name, q := range map[string]string{
		"a version without a change": `INSERT INTO node_version (node_id, version, props, change_id, owner_id, created_at) VALUES (` + ph(1) + `, 99, '{}', NULL, ` + ph(2) + `, '2026-01-01T00:00:00Z')`,
		"a version without an owner": `INSERT INTO node_version (node_id, version, props, change_id, owner_id, created_at) VALUES (` + ph(1) + `, 99, '{}', ` + ph(2) + `, NULL, '2026-01-01T00:00:00Z')`,
	} {
		arg2 := any(string(org.ID))
		if name == "a version without an owner" {
			arg2 = string(org.ChangeID)
		}
		if err := exec(q, string(org.ID), arg2); err == nil {
			t.Errorf("%s: accepted by the database", name)
		}
	}
	if err := exec(`INSERT INTO node_branch (node_id, version, branch, change_id) VALUES (`+ph(1)+`, 1, 'x', NULL)`, string(org.ID)); err == nil {
		t.Error("a branch membership without a change: accepted by the database")
	}
	if err := exec(`INSERT INTO node_version (node_id, version, props, change_id, owner_id, created_at) VALUES (`+ph(1)+`, 99, '{}', `+ph(2)+`, `+ph(1)+`, '2026-01-01T00:00:00Z')`,
		string(org.ID), g.newID()); err == nil {
		t.Error("a version of a missing change: accepted by the database")
	}
}

// Every baseline is the result of a change (ADR 0056); what precedes the first change of a namespace is the empty
// state, which nothing stores.
func TestGuardBaselineNeedsAChange(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		g := New(repo)
		if err := g.Bootstrap(ctx); err != nil {
			t.Fatal(err)
		}
		org := must[domain.Node](t)(g.NodeByKey(ctx, NamespaceOrganisation, rootOrg(g)))
		put := func(b domain.Baseline) error {
			b.ID, b.Namespace, b.Branch, b.CreatedAt = domain.BaselineID(g.newID()), "scratch", domain.MainBranch, g.now()
			return g.repo.InTx(ctx, func(tx Tx) error { return tx.PutBaseline(ctx, b) })
		}
		for name, b := range map[string]domain.Baseline{
			"empty":       {Name: "e"},
			"with nodes":  {Name: "n", Nodes: map[domain.NodeID]domain.Version{org.ID: 1}},
			"with parent": {Name: "p", ParentID: must[domain.Baseline](t)(g.BranchHead(ctx, NamespaceOrganisation, domain.MainBranch)).ID},
		} {
			if err := put(b); !errors.Is(err, ErrInvalid) {
				t.Fatalf("%s, no change: err = %v, want ErrInvalid", name, err)
			}
		}
		if err := put(domain.Baseline{Name: "after the bootstrap", ChangeID: org.ChangeID}); err != nil {
			t.Fatalf("a baseline a change produced: %v", err)
		}
		if err := put(domain.Baseline{Name: "ghost", ChangeID: domain.ChangeID(g.newID())}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("a baseline of a change that does not exist: %v", err)
		}
	})
}

// A version is immutable once written (ADR 0079): the version and its outgoing links are written in one transaction, a
// later one adds no link to it, whatever the storage. There is no operation that edits, freezes or drops a version.
func TestGuardKeepsVersionsImmutable(t *testing.T) {
	forEachRepo(t, testGuardKeepsVersionsImmutable)
}

func testGuardKeepsVersionsImmutable(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	org := must[domain.Node](t)(g.NodeByKey(ctx, NamespaceOrganisation, rootOrg(g)))
	boot := must[domain.Change](t)(g.Change(ctx, org.ChangeID))
	write := func(fn func(tx Tx) error) error { return g.repo.InTx(ctx, fn) }
	node := domain.Node{ID: domain.NodeID(g.newID()), Version: 1, Branch: domain.MainBranch, Reason: domain.ReasonCreate, Namespace: "notes",
		Key: "N-1", Type: "Note", ChangeID: boot.ID, CreatedAt: g.now()}
	link := domain.Link{ID: domain.LinkID(g.newID()), Type: "refines", From: node.Ref(), To: node.Ref(), ChangeID: boot.ID}
	if err := write(func(tx Tx) error {
		if err := tx.PutNode(ctx, node); err != nil {
			return err
		}
		return tx.PutLink(ctx, link)
	}); err != nil {
		t.Fatalf("a version and its links are written together: %v", err)
	}
	if err := write(func(tx Tx) error {
		return tx.PutLink(ctx, domain.Link{ID: domain.LinkID(g.newID()), Type: "refines", From: node.Ref(), To: node.Ref(), ChangeID: boot.ID})
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a link added to a version written earlier: %v", err)
	}
	if out := must[[]domain.Link](t)(g.OutLinksOf(ctx, node.Ref())); len(out) != 1 {
		t.Fatalf("the links of the version are unchanged: %+v", out)
	}
}
