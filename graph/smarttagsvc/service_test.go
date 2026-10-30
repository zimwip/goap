package smarttagsvc_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/graph/smarttagsvc"
	"github.com/zimwip/goap/lib/pgtest"
	"github.com/zimwip/goap/lib/platform"
)

func stores(t *testing.T) map[string]smarttagsvc.Store {
	t.Helper()
	ctx := context.Background()
	db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := platform.MigrateSQLite(ctx, db, "smarttags", smarttagsvc.SQLiteMigrations, "migrations_sqlite"); err != nil {
		t.Fatal(err)
	}
	all := map[string]smarttagsvc.Store{"memory": smarttagsvc.NewMemoryStore(), "sqlite": smarttagsvc.SQLStore{DB: db}}
	// PostgreSQL when GOAP_TEST_PG_DSN is set
	if os.Getenv("GOAP_TEST_PG_DSN") != "" {
		all["postgres"] = smarttagsvc.SQLStore{DB: stdlib.OpenDBFromPool(pgtest.Pool(t, smarttagsvc.Migrations)), Dollar: true}
	}
	return all
}

func as(subject string) context.Context {
	return authz.With(context.Background(), authz.Principal{Subject: subject})
}

func ref(id string, v int) domain.NodeRef {
	return domain.NodeRef{ID: domain.NodeID(id), Version: domain.Version(v)}
}

func TestSmartTags(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) { testSmartTags(t, st) })
	}
}

func testSmartTags(t *testing.T, st smarttagsvc.Store) {
	known := map[domain.NodeRef]bool{ref("A", 1): true, ref("A", 2): true, ref("B", 1): true, ref("C", 3): true}
	s := &smarttagsvc.Service{Store: st, Exists: func(_ context.Context, refs []domain.NodeRef) error {
		for _, r := range refs {
			if !known[r] {
				return fmt.Errorf("no version %s", r)
			}
		}
		return nil
	}}
	alice, bob, carol := as("alice"), as("bob"), as("carol")
	// a selection of exact versions, kept sorted and without duplicates
	tag, err := s.Create(alice, smarttagsvc.New{Name: " Release 1 ", Description: "what shipped", Versions: []domain.NodeRef{ref("B", 1), ref("A", 1), ref("B", 1)}})
	if err != nil {
		t.Fatal(err)
	}
	if tag.Name != "Release 1" || tag.Owner != "alice" || fmt.Sprint(tag.Versions) != "[A@v1 B@v1]" {
		t.Fatalf("created = %+v", tag)
	}
	// refused: no name, a negative version, a version that does not exist, an anonymous caller
	for _, bad := range []func() error{
		func() error { _, err := s.Create(alice, smarttagsvc.New{Name: " "}); return err },
		func() error {
			_, err := s.Create(alice, smarttagsvc.New{Name: "x", Versions: []domain.NodeRef{{ID: "A", Version: -1}}})
			return err
		},
		func() error {
			_, err := s.Create(alice, smarttagsvc.New{Name: "x", Versions: []domain.NodeRef{ref("Z", 1)}})
			return err
		},
	} {
		if err := bad(); !errors.Is(err, smarttagsvc.ErrInvalid) {
			t.Fatalf("want invalid, got %v", err)
		}
	}
	if _, err := s.Create(context.Background(), smarttagsvc.New{Name: "x"}); !errors.Is(err, smarttagsvc.ErrAnonymous) {
		t.Fatalf("anonymous: %v", err)
	}
	// nobody else sees it until it is shared
	if _, err := s.Get(bob, tag.ID); !errors.Is(err, smarttagsvc.ErrNotFound) {
		t.Fatalf("bob reads an unshared tag: %v", err)
	}
	if ts, _ := s.List(bob, "", ""); len(ts) != 0 {
		t.Fatalf("bob lists %+v", ts)
	}
	// shared to read with bob, to edit with carol
	if _, err := s.Share(alice, tag.ID, "bob", smarttagsvc.AccessRead); err != nil {
		t.Fatal(err)
	}
	if tag, err = s.Share(alice, tag.ID, "carol", smarttagsvc.AccessEdit); err != nil || len(tag.Shares) != 2 {
		t.Fatalf("shares = %+v, %v", tag.Shares, err)
	}
	if got, err := s.Get(bob, tag.ID); err != nil || len(got.Shares) != 1 || got.Shares[0].Subject != "bob" || got.AccessOf("bob") != smarttagsvc.AccessRead {
		t.Fatalf("bob sees %+v, %v", got, err)
	}
	if _, err := s.Update(bob, tag.ID, smarttagsvc.Patch{Name: "mine"}); !errors.Is(err, smarttagsvc.ErrForbidden) {
		t.Fatalf("a reader edits: %v", err)
	}
	if _, err := s.Share(carol, tag.ID, "dave", smarttagsvc.AccessRead); !errors.Is(err, smarttagsvc.ErrForbidden) {
		t.Fatalf("an editor shares: %v", err)
	}
	// an editor changes the selection; the change is the smart tag's own
	tag, err = s.Update(carol, tag.ID, smarttagsvc.Patch{Add: []domain.NodeRef{ref("C", 3), ref("A", 2)}, Remove: []domain.NodeRef{ref("B", 1)}, Description: "", SetDescription: true})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(tag.Versions) != "[A@v1 A@v2 C@v3]" || tag.Description != "" || tag.By != "carol" || tag.Name != "Release 1" {
		t.Fatalf("updated = %+v", tag)
	}
	if _, err := s.Update(carol, tag.ID, smarttagsvc.Patch{Add: []domain.NodeRef{ref("Z", 9)}}); !errors.Is(err, smarttagsvc.ErrInvalid) {
		t.Fatalf("adding a missing version: %v", err)
	}
	// listed by the node it selects
	if ts, _ := s.List(bob, "C", ""); len(ts) != 1 || ts[0].ID != tag.ID {
		t.Fatalf("bob lists by C: %+v", ts)
	}
	if ts, _ := s.List(bob, "B", ""); len(ts) != 0 {
		t.Fatalf("B is no longer selected: %+v", ts)
	}
	other, err := s.Create(bob, smarttagsvc.New{Name: "Investigation", Versions: []domain.NodeRef{ref("B", 1)}})
	if err != nil {
		t.Fatal(err)
	}
	if ts, _ := s.List(bob, "", ""); len(ts) != 2 || ts[0].ID != other.ID {
		t.Fatalf("bob lists by name: %+v", ts)
	}
	// a share withdrawn, a delete by its owner only
	if _, err := s.Share(alice, tag.ID, "bob", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(bob, tag.ID); !errors.Is(err, smarttagsvc.ErrNotFound) {
		t.Fatalf("bob after the share is withdrawn: %v", err)
	}
	if err := s.Delete(carol, tag.ID); !errors.Is(err, smarttagsvc.ErrForbidden) {
		t.Fatalf("an editor deletes: %v", err)
	}
	if err := s.Delete(alice, tag.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(alice, tag.ID); !errors.Is(err, smarttagsvc.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if ts, _ := s.List(carol, "", ""); len(ts) != 0 {
		t.Fatalf("carol after delete: %+v", ts)
	}
}

func TestSmartTagFolders(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) { testSmartTagFolders(t, st) })
	}
}

// testSmartTagFolders: trees of smart tags, entries naming a node without its version, the directory of a project
// (ADR 0178).
func testSmartTagFolders(t *testing.T, st smarttagsvc.Store) {
	members := map[string][]string{"PROJ-A": {"alice", "bob"}}
	s := &smarttagsvc.Service{Store: st, MayAccessProject: func(ctx context.Context, project string) (bool, error) {
		for _, m := range members[project] {
			if authz.From(ctx).Subject == m {
				return true, nil
			}
		}
		return false, nil
	}}
	alice, bob, carol := as("alice"), as("bob"), as("carol")
	must := func(tag smarttagsvc.SmartTag, err error) smarttagsvc.SmartTag {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return tag
	}
	// a project's directory: a root of the project, a folder in it, the node itself and an exact version
	docs := must(s.Create(alice, smarttagsvc.New{Name: "Docs", Project: "PROJ-A"}))
	specs := must(s.Create(bob, smarttagsvc.New{Name: "Specs", Parent: docs.ID, Versions: []domain.NodeRef{ref("A", 0), ref("A", 2)}}))
	if specs.Project != "PROJ-A" || specs.Parent != docs.ID || specs.Access != smarttagsvc.AccessEdit || fmt.Sprint(specs.Versions) != "[A@v0 A@v2]" {
		t.Fatalf("specs = %+v", specs)
	}
	if _, err := s.Create(carol, smarttagsvc.New{Name: "Mine", Project: "PROJ-A"}); !errors.Is(err, smarttagsvc.ErrForbidden) {
		t.Fatalf("a root in a project carol does not work on: %v", err)
	}
	if _, err := s.Create(carol, smarttagsvc.New{Name: "Mine", Parent: docs.ID}); !errors.Is(err, smarttagsvc.ErrNotFound) {
		t.Fatalf("a sub-tag carol cannot read the parent of: %v", err)
	}
	if _, err := s.Create(bob, smarttagsvc.New{Name: "x", Parent: docs.ID, Project: "PROJ-B"}); !errors.Is(err, smarttagsvc.ErrInvalid) {
		t.Fatalf("a sub-tag in another project: %v", err)
	}
	if ts, err := s.List(bob, "", "PROJ-A"); err != nil || len(ts) != 2 {
		t.Fatalf("the directory of PROJ-A: %+v %v", ts, err)
	}
	if _, err := s.List(carol, "", "PROJ-A"); !errors.Is(err, smarttagsvc.ErrForbidden) {
		t.Fatalf("carol lists PROJ-A: %v", err)
	}
	if ts, _ := s.List(bob, "A", ""); len(ts) != 1 || ts[0].ID != specs.ID {
		t.Fatalf("bob lists by A: %+v", ts)
	}
	// a sub-tag is shared with its root; a root with sub-tags is not deleted
	if _, err := s.Share(alice, specs.ID, "carol", smarttagsvc.AccessRead); !errors.Is(err, smarttagsvc.ErrInvalid) && !errors.Is(err, smarttagsvc.ErrForbidden) {
		t.Fatalf("a sub-tag shared: %v", err)
	}
	must(s.Share(alice, docs.ID, "carol", smarttagsvc.AccessRead))
	if got, err := s.Get(carol, specs.ID); err != nil || got.Access != smarttagsvc.AccessRead {
		t.Fatalf("carol reads the sub-tag through its root: %+v %v", got, err)
	}
	if _, err := s.Update(carol, specs.ID, smarttagsvc.Patch{Name: "x"}); !errors.Is(err, smarttagsvc.ErrForbidden) {
		t.Fatalf("a reader of the tree edits: %v", err)
	}
	if err := s.Delete(alice, docs.ID); !errors.Is(err, smarttagsvc.ErrInvalid) {
		t.Fatalf("a root with a sub-tag deleted: %v", err)
	}
	if err := s.Delete(bob, docs.ID); !errors.Is(err, smarttagsvc.ErrForbidden) {
		t.Fatalf("a root deleted by a member, not its owner: %v", err)
	}
	// moves: within the project, never below itself
	drafts := must(s.Create(bob, smarttagsvc.New{Name: "Drafts", Parent: specs.ID}))
	if _, err := s.Update(bob, specs.ID, smarttagsvc.Patch{Parent: drafts.ID, SetParent: true}); !errors.Is(err, smarttagsvc.ErrInvalid) {
		t.Fatalf("moved below itself: %v", err)
	}
	personal := must(s.Create(bob, smarttagsvc.New{Name: "Personal"}))
	if _, err := s.Update(bob, drafts.ID, smarttagsvc.Patch{Parent: personal.ID, SetParent: true}); !errors.Is(err, smarttagsvc.ErrInvalid) {
		t.Fatalf("moved out of its project: %v", err)
	}
	drafts = must(s.Update(bob, drafts.ID, smarttagsvc.Patch{Parent: docs.ID, SetParent: true}))
	if drafts.Parent != docs.ID {
		t.Fatalf("drafts = %+v", drafts)
	}
	if _, err := s.Update(alice, drafts.ID, smarttagsvc.Patch{Parent: "", SetParent: true}); !errors.Is(err, smarttagsvc.ErrForbidden) {
		t.Fatalf("a sub-tag made a root by another than its creator: %v", err)
	}
	// the trees of a user hold their sub-tags, with the tags they made in others' trees
	notes := must(s.Create(bob, smarttagsvc.New{Name: "Notes", Parent: personal.ID, Versions: []domain.NodeRef{ref("B", 0)}}))
	names := func(ts []smarttagsvc.SmartTag) (out []string) {
		for _, x := range ts {
			out = append(out, x.Name)
		}
		return out
	}
	if ts, _ := s.List(bob, "", ""); fmt.Sprint(names(ts)) != "[Drafts Notes Personal Specs]" {
		t.Fatalf("bob's trees: %v", names(ts))
	}
	if ts, _ := s.List(alice, "", ""); len(ts) != 3 { // Docs and its two folders
		t.Fatalf("alice's trees: %+v", ts)
	}
	must(s.Update(bob, notes.ID, smarttagsvc.Patch{Remove: []domain.NodeRef{ref("B", 0)}}))
	if err := s.Delete(bob, notes.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(alice, drafts.ID); err != nil { // a sub-tag: whoever edits the tree
		t.Fatal(err)
	}
}
