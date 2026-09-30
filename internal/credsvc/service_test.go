package credsvc_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/internal/credsvc"
	"github.com/zimwip/goap/internal/pgtest"
	"github.com/zimwip/goap/internal/platform"
)

func stores(t *testing.T) map[string]credsvc.Store {
	t.Helper()
	ctx := context.Background()
	db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := platform.MigrateSQLite(ctx, db, "credentials", credsvc.SQLiteMigrations, "migrations_sqlite"); err != nil {
		t.Fatal(err)
	}
	all := map[string]credsvc.Store{"memory": credsvc.NewMemoryStore(), "sqlite": credsvc.SQLStore{DB: db}}
	if os.Getenv("GOAP_TEST_PG_DSN") != "" {
		all["postgres"] = credsvc.SQLStore{DB: stdlib.OpenDBFromPool(pgtest.Pool(t, credsvc.Migrations)), Dollar: true}
	}
	return all
}

// A subject registers once, logs in with the right password and is refused with a wrong one; a duplicate
// registration is refused; changing the password takes effect immediately.
func TestServiceRegisterVerifySetPassword(t *testing.T) {
	for name, store := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := &credsvc.Service{Store: store}

			if err := s.Register(ctx, "alice", "correct horse"); err != nil {
				t.Fatalf("register: %v", err)
			}
			if err := s.Register(ctx, "alice", "another password"); !errors.Is(err, credsvc.ErrExists) {
				t.Fatalf("duplicate register = %v, want ErrExists", err)
			}
			if ok, err := s.Verify(ctx, "alice", "correct horse"); err != nil || !ok {
				t.Fatalf("verify right password: %v, %v", ok, err)
			}
			if ok, err := s.Verify(ctx, "alice", "wrong password"); err != nil || ok {
				t.Fatalf("verify wrong password: %v, %v", ok, err)
			}
			if ok, err := s.Verify(ctx, "bob", "whatever"); err != nil || ok {
				t.Fatalf("verify unknown subject: %v, %v", ok, err)
			}
			if exists, err := s.Exists(ctx, "alice"); err != nil || !exists {
				t.Fatalf("exists alice: %v, %v", exists, err)
			}
			if exists, err := s.Exists(ctx, "bob"); err != nil || exists {
				t.Fatalf("exists bob: %v, %v", exists, err)
			}

			if err := s.SetPassword(ctx, "alice", "new password"); err != nil {
				t.Fatalf("set password: %v", err)
			}
			if ok, err := s.Verify(ctx, "alice", "correct horse"); err != nil || ok {
				t.Fatalf("old password still works: %v, %v", ok, err)
			}
			if ok, err := s.Verify(ctx, "alice", "new password"); err != nil || !ok {
				t.Fatalf("new password: %v, %v", ok, err)
			}
			if err := s.SetPassword(ctx, "carol", "whatever1"); !errors.Is(err, credsvc.ErrNotFound) {
				t.Fatalf("set password of unknown subject = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestServiceValidation(t *testing.T) {
	s := &credsvc.Service{Store: credsvc.NewMemoryStore()}
	ctx := context.Background()
	if err := s.Register(ctx, "", "whatever1"); !errors.Is(err, credsvc.ErrInvalid) {
		t.Fatalf("empty subject = %v, want ErrInvalid", err)
	}
	if err := s.Register(ctx, "dave", "short"); !errors.Is(err, credsvc.ErrInvalid) {
		t.Fatalf("short password = %v, want ErrInvalid", err)
	}
}
