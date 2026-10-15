package registrysvc

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/typecat"
)

const refsYAML = `
name: refs
version: "1"
namespace: alm
goal: done
roles: [{name: analyst}]
conditions:
  - {name: framed, expr: "true"}
  - {name: analysed, expr: "true"}
  - {name: impact_ok, scope: impact, expr: "true"}
  - {name: all_ok, all: impact_ok}
actions:
  - {name: frame, kind: human, effects: {framed: true}, roles: [analyst]}
  - {name: analyse, kind: human, pre: {framed: true, "state:open": true}, effects: {analysed: true, all_ok: true}}
goals: [{name: done, pre: {analysed: true}}]
agents:
  - {name: a, planner: goap, actions: [analyse, other/x, frame], goals: [done]}
processes:
  - name: deliver
    steps:
      - {name: framing, action: frame, roles: {responsible: analyst}}
      - {name: analysis, pre: {"step:deliver/framing": true}, done: {analysed: true}, actions: [analyse]}
`

// typedStore is a store over a graph checking the built-in meta-domain (strict attributes and link types).
func typedStore() (*GraphStore, *graph.Graph) {
	g := graph.New(graph.NewMemory())
	cat := typecat.Builtin()
	g.Types = func() graph.TypeCatalog { return cat }
	return NewGraphStore(g), g
}

// linksOf lists the reference links leaving a node of the head of the methodology namespace, suspect ones included,
// as "<type> <target key relative to the version> <attributes>".
func linksOf(t *testing.T, g *graph.Graph, hk, from string) []string {
	t.Helper()
	ctx := context.Background()
	head, err := g.BranchHead(ctx, NamespaceMethodology, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	nodes, links, err := withSuspects{g}.BaselineGraph(ctx, head.ID)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[domain.NodeID]string{}
	var src domain.NodeID
	for _, n := range nodes {
		keys[n.ID] = n.Key
		if n.Key == hk+"/"+from || (from == "" && n.Key == hk) {
			src = n.ID
		}
	}
	var out []string
	for _, l := range links {
		if l.From.ID == src && l.Type != linkDefines {
			out = append(out, strings.TrimPrefix(l.Type, "methodology@")+" "+strings.TrimPrefix(keys[l.To.ID], hk+"/")+" "+canon(t, l.Properties))
		}
	}
	slices.Sort(out)
	return out
}

func node(t *testing.T, g *graph.Graph, key string) domain.Node {
	t.Helper()
	n, err := g.NodeByKey(context.Background(), NamespaceMethodology, key)
	if err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	return n
}

// The references between the elements of a version are links (ADR 0119): a condition is the output of an action
// (produces) and the input of another (requires), the same node; the names that resolve to no element stay in the
// properties; the definition reads back as it was saved.
func TestReferencesAreLinks(t *testing.T) {
	ctx := context.Background()
	s, g := typedStore()
	m, err := methodology.Parse([]byte(refsYAML))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	save := func(m methodology.Methodology) {
		t.Helper()
		if err := s.Save(ctx, Record{Methodology: m, Status: StatusDraft, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ctx, m.Name, m.Version)
		if err != nil {
			t.Fatal(err)
		}
		if canon(t, got.Methodology) != canon(t, m) {
			t.Fatalf("the definition does not read back:\n have %s\n want %s", canon(t, got.Methodology), canon(t, m))
		}
	}
	save(*m)
	hk := MethodologyVersionKey(m.Name, m.Version)
	expect := func(from string, want ...string) {
		t.Helper()
		if got := linksOf(t, g, hk, from); !slices.Equal(got, want) {
			t.Fatalf("links of %s:\n have %q\n want %q", from, got, want)
		}
	}
	expect("action/frame", `assigns role/analyst {"as":"run","position":0}`, `produces condition/framed {"value":true}`)
	expect("action/analyse", `produces condition/all_ok {"value":true}`, `produces condition/analysed {"value":true}`, `requires condition/framed {"value":true}`)
	expect("goal/done", `requires condition/analysed {"value":true}`)
	expect("agent/a", `pursues goal/done {"position":0}`, `uses action/analyse {"position":0}`, `uses action/frame {"position":2}`)
	expect("condition/all_ok", `aggregates condition/impact_ok {"as":"all"}`)
	expect("process/deliver", `sub_activity step/deliver/analysis null`, `sub_activity step/deliver/framing null`)
	expect("step/deliver/framing", `assigns role/analyst {"as":"responsible"}`, `performed_by action/frame {"as":"action"}`)
	expect("step/deliver/analysis", `performed_by action/analyse {"as":"alternative","position":0}`, `produces condition/analysed {"value":true}`)
	expect("", `main_goal goal/done null`)

	// what names nothing of the version stays a property
	if p := node(t, g, hk+"/action/analyse").Properties; canon(t, p["pre"]) != `{"state:open":true}` || p["effects"] != nil {
		t.Fatalf("the action keeps only the names it cannot link: %v", p)
	}
	if p := node(t, g, hk+"/agent/a").Properties; canon(t, p["actions"]) != `["other/x"]` || p["goals"] != nil {
		t.Fatalf("the agent keeps only the names it cannot link: %v", p)
	}
	if p := node(t, g, hk+"/process/deliver").Properties; p["steps"] != nil {
		t.Fatalf("the steps of a process are nodes, not a property: %v", p)
	}

	// a value that changes replaces its link; an edited condition keeps the references to it (suspect)
	m.Actions[1].Pre = map[string]bool{"framed": false, "state:open": true}
	m.Conditions[0].Description = "The change is framed"
	save(*m)
	expect("action/analyse", `produces condition/all_ok {"value":true}`, `produces condition/analysed {"value":true}`, `requires condition/framed {"value":false}`)
	expect("action/frame", `assigns role/analyst {"as":"run","position":0}`, `produces condition/framed {"value":true}`)

	// an element that leaves the definition drops its links, and the names of it become properties again
	m.Conditions = slices.Delete(m.Conditions, 0, 1)
	save(*m)
	expect("condition/framed")
	if p := node(t, g, hk+"/action/frame").Properties; canon(t, p["effects"]) != `{"framed":true}` {
		t.Fatalf("a name the version no longer declares is a property again: %v", p)
	}
}

// By default no element of a methodology stands alone (ADR 0119): one nothing refers to and that refers to nothing is
// an issue, which refuses the publication.
func TestIsolatedElementsAreIssues(t *testing.T) {
	m, err := methodology.Parse([]byte(refsYAML + `
  - {name: unused, steps: [{name: alone}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if issues := connectivityIssues(*m); len(issues) != 0 {
		t.Fatalf("a process is connected to its steps: %v", issues)
	}
	m.Conditions = append(m.Conditions, methodology.Condition{Name: "orphan", Expr: "true"})
	m.Roles = append(m.Roles, methodology.Role{Name: "nobody"})
	m.Agents = append(m.Agents, methodology.Agent{Name: "all", Planner: "goap"})
	var got []string
	for _, i := range connectivityIssues(*m) {
		got = append(got, i.Path)
	}
	if want := []string{"conditions[4].name", "roles[1].name"}; !slices.Equal(got, want) {
		t.Fatalf("isolated elements: %q, want %q (an agent naming no action plans over all of them)", got, want)
	}
}

// The methodologies of the repository have no isolated element.
func TestRepositoryMethodologiesAreConnected(t *testing.T) {
	files, _ := filepath.Glob("../../methodologies/*.yaml")
	more, _ := filepath.Glob("../../methodologies/examples/*.yaml")
	for _, f := range append(files, more...) {
		m, err := methodology.LoadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if issues := connectivityIssues(*m); len(issues) > 0 {
			t.Errorf("%s: %v", f, issues)
		}
	}
}
