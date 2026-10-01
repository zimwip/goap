package credsvc_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// A session accepts its tokens until it is ended, expires, or its subject changes their password; signing out
// everywhere ends every session of the subject, and only theirs (ADR 0045).
func TestSessions(t *testing.T) {
	for name, store := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := &credsvc.Service{Store: store}
			if err := s.Register(ctx, "alice", "correct horse"); err != nil {
				t.Fatal(err)
			}
			active := func(id, subject string) bool {
				t.Helper()
				ok, err := s.SessionActive(ctx, id, subject)
				if err != nil {
					t.Fatal(err)
				}
				return ok
			}
			start := func(subject string, d time.Duration) string {
				t.Helper()
				id, err := s.StartSession(ctx, subject, d)
				if err != nil {
					t.Fatal(err)
				}
				return id
			}
			a1, a2, b := start("alice", time.Hour), start("alice", time.Hour), start("bob", time.Hour)
			if a1 == a2 || !active(a1, "alice") || !active(a2, "alice") || !active(b, "bob") {
				t.Fatal("new sessions must be distinct and active")
			}
			if active(a1, "bob") || active("unknown", "alice") || active("", "alice") {
				t.Fatal("a session is someone's, and an unknown one is not active")
			}
			// signing out ends that session only
			if err := s.EndSession(ctx, a1); err != nil {
				t.Fatal(err)
			}
			if active(a1, "alice") || !active(a2, "alice") {
				t.Fatal("signing out ends that session only")
			}
			if err := s.EndSession(ctx, a1); err != nil {
				t.Fatalf("ending an ended session again: %v", err)
			}
			// a new password ends every session of its subject, not the others'
			if err := s.SetPassword(ctx, "alice", "battery staple"); err != nil {
				t.Fatal(err)
			}
			if active(a2, "alice") || !active(b, "bob") {
				t.Fatal("a new password ends the subject's sessions only")
			}
			// everywhere
			b2 := start("bob", time.Hour)
			if err := s.EndSessions(ctx, "bob"); err != nil {
				t.Fatal(err)
			}
			if active(b, "bob") || active(b2, "bob") {
				t.Fatal("signing out everywhere ends every session")
			}
			// expiry
			short := start("alice", 50*time.Millisecond)
			time.Sleep(80 * time.Millisecond)
			if active(short, "alice") {
				t.Fatal("an expired session is not active")
			}
		})
	}
}
