package registrysvc

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/zimwip/goap/internal/pgtest"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/methodology"
)

func stores(t *testing.T) map[string]func(t *testing.T) Store {
	return map[string]func(t *testing.T) Store{
		"memory":   func(*testing.T) Store { return NewMemoryStore() },
		"postgres": func(t *testing.T) Store { return PostgresStore{Pool: pgtest.Pool(t, Migrations)} },
	}
}

func as(roles ...string) context.Context {
	return authz.With(context.Background(), authz.Principal{Subject: "mia", Org: "acme", Roles: roles})
}

func example(t *testing.T) methodology.Methodology {
	t.Helper()
	data, err := os.ReadFile("../../methodologies/impact-analysis.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := methodology.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return *m
}

func TestLifecycle(t *testing.T) {
	for name, mk := range stores(t) {
		t.Run(name, func(t *testing.T) {
			enf, _ := authz.NewCasbin(nil)
			s := &Service{Store: mk(t), Authz: enf}
			ctx := as("methodologist")
			m := example(t)
			m.Version = "2.0.0"

			// ABAC: contributors may not edit methodologies
			if _, _, err := s.Save(as("contributor"), m); !errors.Is(err, authz.ErrForbidden) {
				t.Fatalf("contributor save: %v", err)
			}
			// a broken draft is stored with its issues
			broken := m
			broken.Conditions = append([]methodology.Condition(nil), m.Conditions...)
			broken.Conditions[0].Expr = "impacts +"
			_, issues, err := s.Save(ctx, broken)
			if err != nil || len(issues) == 0 || issues[0].Path != "conditions[0].expr" {
				t.Fatalf("expected an expr issue, got %v %v", issues, err)
			}
			if _, err := s.Publish(ctx, m.Name, m.Version); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid draft published: %v", err)
			}
			// fix and publish
			if _, issues, err := s.Save(ctx, m); err != nil || len(issues) > 0 {
				t.Fatalf("save: %v %v", issues, err)
			}
			rec, err := s.Publish(ctx, m.Name, m.Version)
			if err != nil || rec.Status != StatusPublished {
				t.Fatalf("publish: %v %v", rec.Status, err)
			}
			// structured storage round trip
			got, err := s.Get(ctx, m.Name, "")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(normalize(got.Methodology), normalize(m)) {
				t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got.Methodology.Actions[2], m.Actions[2])
			}
			if _, err := got.Methodology.Compile(); err != nil {
				t.Fatal(err)
			}
			// published versions are immutable
			if _, _, err := s.Save(ctx, m); !errors.Is(err, ErrImmutable) {
				t.Fatalf("published version modified: %v", err)
			}
			// new draft version, list, export / import
			if _, err := s.CreateVersion(ctx, m.Name, "2.0.0", "2.1.0"); err != nil {
				t.Fatal(err)
			}
			all, _ := s.Versions(ctx, true)
			latest, _ := s.Versions(ctx, false)
			if len(all) != 2 || len(latest) != 1 || latest[0].Methodology.Version != "2.0.0" {
				t.Fatalf("list: %d all, latest %+v", len(all), latest)
			}
			out, file, err := s.Export(ctx, m.Name, "2.1.0")
			if err != nil || file != "impact-analysis-2.1.0.yaml" {
				t.Fatalf("export: %s %v", file, err)
			}
			if err := s.Delete(ctx, m.Name, "2.1.0"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Import(ctx, out, true); err != nil {
				t.Fatalf("re-import: %v", err)
			}
			// deleting a published version archives it
			if err := s.Delete(ctx, m.Name, "2.0.0"); err != nil {
				t.Fatal(err)
			}
			if r, _ := s.Get(ctx, m.Name, "2.0.0"); r.Status != StatusArchived {
				t.Fatalf("expected archived, got %s", r.Status)
			}
			if r, err := s.Get(ctx, m.Name, ""); err != nil || r.Methodology.Version != "2.1.0" {
				t.Fatalf("latest published: %v %v", r.Methodology.Version, err)
			}
			if c, err := s.Methodology(ctx, m.Name); err != nil || c.Version != "2.1.0" {
				t.Fatalf("engine port: %v", err)
			}
		})
	}
}

// normalize clears the differences that storage legitimately introduces
// (YAML ints become JSON numbers in params).
func normalize(m methodology.Methodology) methodology.Methodology {
	m.Actions = append([]methodology.Action(nil), m.Actions...)
	for i := range m.Actions {
		if p := m.Actions[i].Params; p != nil {
			cp := map[string]any{}
			for k, v := range p {
				if n, ok := v.(int); ok {
					v = float64(n)
				}
				if l, ok := v.([]any); ok {
					v = append([]any(nil), l...)
				}
				cp[k] = v
			}
			m.Actions[i].Params = cp
		}
	}
	return m
}

func TestAgentsAndScriptsRoundTrip(t *testing.T) {
	for name, mk := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s := &Service{Store: mk(t)}
			data, _ := os.ReadFile("../../methodologies/test-design.yaml")
			if _, _, err := s.Import(as("admin"), data, true); err != nil {
				t.Fatal(err)
			}
			want, _ := methodology.Parse(data)
			got, err := s.Get(context.Background(), "test-design", "")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Methodology.Agents, want.Agents) || got.Methodology.Actions[1].Code != want.Actions[1].Code ||
				got.Methodology.Actions[3].Utility != want.Actions[3].Utility {
				t.Fatalf("agents/scripts not stored:\n%+v", got.Methodology.Agents)
			}
			all, err := s.List(context.Background())
			if err != nil || len(all) != 1 || len(all[0].AgentList()) != 3 {
				t.Fatalf("engine port list: %v", err)
			}
		})
	}
}
