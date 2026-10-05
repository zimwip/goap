package prefssvc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/protobuf/types/known/structpb"

	preferencesv1 "github.com/zimwip/goap/gen/goap/preferences/v1"
	"github.com/zimwip/goap/gen/goap/preferences/v1/preferencesv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pgtest"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/prefssvc"
	"github.com/zimwip/goap/pkg/authz"
)

func stores(t *testing.T) map[string]prefssvc.Store {
	t.Helper()
	ctx := context.Background()
	db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := platform.MigrateSQLite(ctx, db, "preferences", prefssvc.SQLiteMigrations, "migrations_sqlite"); err != nil {
		t.Fatal(err)
	}
	all := map[string]prefssvc.Store{"memory": prefssvc.NewMemoryStore(), "sqlite": prefssvc.SQLStore{DB: db}}
	// PostgreSQL when GOAP_TEST_PG_DSN is set
	if os.Getenv("GOAP_TEST_PG_DSN") != "" {
		all["postgres"] = prefssvc.SQLStore{DB: stdlib.OpenDBFromPool(pgtest.Pool(t, prefssvc.Migrations)), Dollar: true}
	}
	return all
}

func TestService(t *testing.T) {
	ctx := context.Background()
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s := &prefssvc.Service{Store: st}
			if p, err := s.Get(ctx, "alice"); err != nil || len(p) != 0 {
				t.Fatalf("empty = %v, %v", p, err)
			}
			if _, err := s.Set(ctx, "alice", prefssvc.Prefs{"theme": "dark", "voiceEnabled": false}); err != nil {
				t.Fatal(err)
			}
			// a patch merges; nil clears a key
			p, err := s.Set(ctx, "alice", prefssvc.Prefs{"usagePeriod": "30d", "voiceEnabled": nil})
			if err != nil || p["theme"] != "dark" || p["usagePeriod"] != "30d" || len(p) != 2 {
				t.Fatalf("merged = %v, %v", p, err)
			}
			// each user has their own
			if p, _ := s.Get(ctx, "bob"); len(p) != 0 {
				t.Fatalf("bob sees %v", p)
			}
			// the document is opaque: any key and value of JSON is kept, the interface judges them
			if p, err := s.Set(ctx, "alice", prefssvc.Prefs{"anything": map[string]any{"nested": []any{1, "x"}}, "theme": "neon"}); err != nil || p["theme"] != "neon" {
				t.Fatalf("opaque write = %v, %v", p, err)
			}
			if _, err := s.Set(ctx, "alice", prefssvc.Prefs{"theme": "dark", "anything": nil}); err != nil {
				t.Fatal(err)
			}
			// what is not JSON, or too large, is refused and changes nothing
			for _, bad := range []prefssvc.Prefs{{"f": func() {}}, {"big": strings.Repeat("x", prefssvc.MaxDocumentBytes)}} {
				if _, err := s.Set(ctx, "alice", bad); !errors.Is(err, prefssvc.ErrInvalid) {
					t.Fatalf("%.40v: %v", bad, err)
				}
			}
			if p, _ := s.Get(ctx, "alice"); p["theme"] != "dark" {
				t.Fatalf("after refused writes = %v", p)
			}
			if _, err := s.Get(ctx, ""); !errors.Is(err, prefssvc.ErrAnonymous) {
				t.Fatalf("anonymous: %v", err)
			}
			if err := s.Reset(ctx, "alice"); err != nil {
				t.Fatal(err)
			}
			if p, _ := s.Get(ctx, "alice"); len(p) != 0 {
				t.Fatalf("after reset = %v", p)
			}
		})
	}
}

// The service answers the caller about themselves only.
func TestHandlerIsPerCaller(t *testing.T) {
	ctx := context.Background()
	h := &prefssvc.Handler{Service: &prefssvc.Service{Store: prefssvc.NewMemoryStore()}}
	path, handler := preferencesv1connect.NewPreferencesServiceHandler(h)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := preferencesv1connect.NewPreferencesServiceClient(srv.Client(), srv.URL)
	as := func(subject string, r connect.AnyRequest) {
		identity.SetHeaders(authz.Principal{Subject: subject}, r.Header())
	}

	v, _ := structpb.NewStruct(map[string]any{"theme": "light"})
	set := connect.NewRequest(&preferencesv1.SetPreferencesRequest{Values: v})
	as("alice", set)
	if _, err := cl.SetPreferences(ctx, set); err != nil {
		t.Fatal(err)
	}
	get := connect.NewRequest(&preferencesv1.GetPreferencesRequest{})
	as("alice", get)
	if r, err := cl.GetPreferences(ctx, get); err != nil || r.Msg.Values.AsMap()["theme"] != "light" {
		t.Fatalf("alice = %v, %v", r, err)
	}
	get = connect.NewRequest(&preferencesv1.GetPreferencesRequest{})
	as("bob", get)
	if r, err := cl.GetPreferences(ctx, get); err != nil || len(r.Msg.Values.AsMap()) != 0 {
		t.Fatalf("bob = %v, %v", r, err)
	}
	get = connect.NewRequest(&preferencesv1.GetPreferencesRequest{})
	if _, err := cl.GetPreferences(ctx, get); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous: %v", err)
	}
	bad, _ := structpb.NewStruct(map[string]any{"big": strings.Repeat("x", prefssvc.MaxDocumentBytes)})
	set = connect.NewRequest(&preferencesv1.SetPreferencesRequest{Values: bad})
	as("alice", set)
	if _, err := cl.SetPreferences(ctx, set); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid: %v", err)
	}
}
