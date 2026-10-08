package methodology

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain/def"
	"gopkg.in/yaml.v3"
)

// A methodology adds tabs to the view of its changes (ADR 0098): each has a title, an editor name and change object
// types that are qualified and known.
func TestAdditions(t *testing.T) {
	d := testDomain()
	d.ChangeObjectTypes = []def.ChangeObjectType{{Name: "Risk", Key: def.KeyType{Kind: def.KeySequence, Prefix: "RISK"}}}
	m := refMethodology()
	if err := yaml.Unmarshal([]byte(`
tabs:
  - {title: Risks, editor: risk-register, objects: [alm@Risk, execution@Fact]}
`), &m.Additions); err != nil {
		t.Fatal(err)
	}
	if issues := m.Resolve(def.DomainTypes(d)).Validate(); len(issues) > 0 {
		t.Fatal(issues)
	}
	if tabs := m.TabsOf(); len(tabs) != 1 || tabs[0].Editor != "risk-register" || tabs[0].Objects[1] != "execution@Fact" {
		t.Fatalf("tabs %+v", tabs)
	}
	m.Additions.Tabs = append(m.Additions.Tabs, ChangeTab{Editor: "Bad Name", Objects: []string{"Risk", "alm@Nope"}}, ChangeTab{Title: "empty"})
	got := m.Resolve(def.DomainTypes(d)).Validate()
	for _, want := range []string{"additions.tabs[1].title", "additions.tabs[1].editor", "additions.tabs[1].objects[0]", "additions.tabs[1].objects[1]", "additions.tabs[2].objects"} {
		found := false
		for _, i := range got {
			found = found || i.Path == want
		}
		if !found {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	if !strings.Contains(got.Error(), "unknown change object type alm@Nope") {
		t.Errorf("an unknown type: %v", got)
	}
}
