package registrysvc

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llmcfg"
	"github.com/zimwip/goap/pkg/methodology"
)

// graphWithDomains is the store of a registry on a graph: the methodologies in the graph, the domains in a DomainStore
// (the Service finds it through the Store).
type graphWithDomains struct {
	*GraphStore
	DomainStore
}

func stores(t *testing.T) map[string]func(t *testing.T) Store {
	return map[string]func(t *testing.T) Store{
		"memory": func(*testing.T) Store { return NewMemoryStore() },
		"graph": func(*testing.T) Store {
			return graphWithDomains{NewGraphStore(graph.New(graph.NewMemory())), NewMemoryStore()}
		},
	}
}

func as(roles ...string) context.Context {
	return authz.With(context.Background(), authz.Principal{Subject: "mia", Org: "acme", Roles: roles})
}

func example(t *testing.T) methodology.Methodology {
	t.Helper()
	m, err := methodology.LoadFile("../../methodologies/examples/impact-analysis.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m.Types = nil // resolved by the registry from its own domains
	return *m
}

// withALM publishes the alm domain of the repository, which the example methodologies act on.
func withALM(t *testing.T, s *Service) {
	t.Helper()
	src, err := os.ReadFile("../../domains/alm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, issues, err := s.ImportDomain(as("admin"), src, true); err != nil || len(issues) > 0 {
		t.Fatalf("alm domain: %v %v", issues, err)
	}
}

func TestLifecycle(t *testing.T) {
	for name, mk := range stores(t) {
		t.Run(name, func(t *testing.T) {
			enf, _ := authz.NewCasbin(nil)
			s := &Service{Store: mk(t), Authz: enf}
			withALM(t, s)
			ctx := as("admin")
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
			if _, err := s.SeedDomains(as("admin"), "../../domains"); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile("../../methodologies/examples/test-design.yaml")
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
			if err != nil || len(all) != 1 || len(all[0].AgentList()) != len(want.Agents) {
				t.Fatalf("engine port list: %v", err)
			}
		})
	}
}

func pendingAliasImpacts(t *testing.T, g *graph.Graph, alias string) []domain.ChangeImpact {
	t.Helper()
	changes, err := g.Changes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	key, typ := llmcfg.AliasKey(alias), llmcfg.NodeTypeAlias
	var out []domain.ChangeImpact
	for _, c := range changes {
		if c.Namespace != llmcfg.NamespacePlatform {
			continue
		}
		for _, cn := range c.Nodes {
			if cn.Key == key && cn.Type == typ {
				out = append(out, cn)
			}
		}
	}
	return out
}

func TestPublishStubsMissingAlias(t *testing.T) {
	g := graph.New(graph.NewMemory())
	s := &Service{Store: graphWithDomains{NewGraphStore(g), NewMemoryStore()}}
	withALM(t, s)
	ctx := as("admin")
	m := example(t)
	m.Version = "9.0.0"
	m.Agents = []methodology.Agent{{Name: "plannertest", Planner: methodology.PlannerLLMScoring, Model: "unconfigured-test-alias"}}
	src, err := m.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Import(ctx, src, true); err != nil {
		t.Fatal(err)
	}
	impacts := pendingAliasImpacts(t, g, "unconfigured-test-alias")
	if len(impacts) != 1 || impacts[0].Review != domain.ReviewProposed || impacts[0].Intent != domain.IntentCreated {
		t.Fatalf("expected one pending alias impact, got %+v", impacts)
	}

	// republishing a new version referencing the same missing alias does not duplicate the pending stub
	m2 := m
	m2.Version = "9.1.0"
	src2, err := m2.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Import(ctx, src2, true); err != nil {
		t.Fatal(err)
	}
	if impacts := pendingAliasImpacts(t, g, "unconfigured-test-alias"); len(impacts) != 1 {
		t.Fatalf("expected the pending stub not to be duplicated, got %+v", impacts)
	}
}
