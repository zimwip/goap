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
	tag, err := s.Create(alice, " Release 1 ", "what shipped", []domain.NodeRef{ref("B", 1), ref("A", 1), ref("B", 1)})
	if err != nil {
		t.Fatal(err)
	}
	if tag.Name != "Release 1" || tag.Owner != "alice" || fmt.Sprint(tag.Versions) != "[A@v1 B@v1]" {
		t.Fatalf("created = %+v", tag)
	}
	// refused: no name, a draft, a version that does not exist, an anonymous caller
	for _, bad := range []func() error{
		func() error { _, err := s.Create(alice, " ", "", nil); return err },
		func() error { _, err := s.Create(alice, "x", "", []domain.NodeRef{{ID: "A"}}); return err },
		func() error { _, err := s.Create(alice, "x", "", []domain.NodeRef{ref("Z", 1)}); return err },
	} {
		if err := bad(); !errors.Is(err, smarttagsvc.ErrInvalid) {
			t.Fatalf("want invalid, got %v", err)
		}
	}
	if _, err := s.Create(context.Background(), "x", "", nil); !errors.Is(err, smarttagsvc.ErrAnonymous) {
		t.Fatalf("anonymous: %v", err)
	}
	// nobody else sees it until it is shared
	if _, err := s.Get(bob, tag.ID); !errors.Is(err, smarttagsvc.ErrNotFound) {
		t.Fatalf("bob reads an unshared tag: %v", err)
	}
	if ts, _ := s.List(bob, ""); len(ts) != 0 {
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
	if ts, _ := s.List(bob, "C"); len(ts) != 1 || ts[0].ID != tag.ID {
		t.Fatalf("bob lists by C: %+v", ts)
	}
	if ts, _ := s.List(bob, "B"); len(ts) != 0 {
		t.Fatalf("B is no longer selected: %+v", ts)
	}
	other, err := s.Create(bob, "Investigation", "", []domain.NodeRef{ref("B", 1)})
	if err != nil {
		t.Fatal(err)
	}
	if ts, _ := s.List(bob, ""); len(ts) != 2 || ts[0].ID != other.ID {
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
	if ts, _ := s.List(carol, ""); len(ts) != 0 {
		t.Fatalf("carol after delete: %+v", ts)
	}
}
