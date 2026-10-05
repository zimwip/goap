package methodology

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/domain/def"
)

func testDomain() *def.Domain {
	return &def.Domain{Name: "alm", Version: "1", Schema: def.Schema{
		NodeTypes: []def.NodeType{{Name: "Requirement"}, {Name: "TestCase"}},
		LinkTypes: []def.LinkType{{Name: "verifies", From: "TestCase", To: "Requirement"}},
	}}
}

func refMethodology() *Methodology {
	return &Methodology{
		Name: "m", Version: "1", Namespace: "alm",
		Conditions: []Condition{{Name: "c", Expr: `changeImpacts.exists(n, n.hasPost && n.post.out.exists(l, l.type == "alm@verifies"))`}},
		Actions:    []Action{{Name: "a", Kind: KindHuman, Effects: map[string]bool{"c": true}}},
		Goals:      []Goal{{Name: "g", Pre: map[string]bool{"c": true}}},
	}
}

func TestResolveTypes(t *testing.T) {
	m := refMethodology().Resolve(def.DomainTypes(testDomain()))
	if issues := m.Validate(); len(issues) > 0 {
		t.Fatal(issues)
	}
	if issues := refMethodology().ValidateStored(); len(issues) > 0 {
		t.Fatal(issues)
	}
	m.Namespace = ""
	if issues := m.ValidateStored(); len(issues) != 1 || issues[0].Path != "namespace" {
		t.Fatalf("a stored methodology names its target namespace: %v", issues)
	}
}

func TestReferenceLint(t *testing.T) {
	m := refMethodology()
	m.Conditions[0].Expr = `changeImpacts.exists(n, n.hasPost && n.post.out.exists(l, l.type == "alm@satisfies")) && changeImpacts.exists(n, "alm@Component" in n.types) && changeImpacts.exists(n, n.type == "Requirement")`
	m.Actions[0].Expects = &condition.Expectation{ForEach: "changeImpacts", Produce: condition.ProduceSpec{Op: "create_node", NodeType: "crm@Customer"}}
	got := m.Resolve(def.DomainTypes(testDomain())).Validate().Error()
	for _, want := range []string{"unknown link type alm@satisfies", "unknown node type alm@Component", `"Requirement" must be qualified`, "unknown node type crm@Customer"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	m.Actions[0].Expects.Produce.NodeType = "organisation@OrgUnit" // built in
	if !strings.Contains(m.Resolve(def.DomainTypes(testDomain())).Validate().Error(), "a methodology creates nodes of its namespace") {
		t.Error("a methodology creates nodes of its target namespace only")
	}
}
