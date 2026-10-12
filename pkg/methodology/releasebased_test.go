package methodology

import "testing"

// The release-based methodology integrates sdlc (ADR 0106): it declares the integration, its guardian and the events
// of the integrated changes it follows.
func TestReleaseBasedMethodology(t *testing.T) {
	m, err := LoadFile("../../methodologies/release-based.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if issues := m.ValidateStored(); issues.HasErrors() {
		t.Fatal(issues)
	}
	if _, err := m.Compile(); err != nil {
		t.Fatal(err)
	}
	if !m.IntegratesChangesOf("sdlc") || m.IntegratesChangesOf("release-based") || m.IntegratesChangesOf("risk-management") {
		t.Fatalf("integrates %+v", m.Integrates)
	}
	if g := m.MergeGuard("sdlc"); g != `integrator.attached && (integrator.state == "integrating")` {
		t.Fatalf("merge guard %q", g)
	}
	for _, s := range m.On {
		if s.Origin() != FromIntegrated {
			t.Fatalf("subscription %+v", s)
		}
	}
}

func TestIntegrationIssues(t *testing.T) {
	base := func() *Methodology {
		return &Methodology{Name: "rel", Namespace: "alm", Goals: []Goal{{Name: "g", Pre: map[string]bool{"x": true}}},
			Conditions: []Condition{{Name: "x", Expr: "true"}}}
	}
	cases := map[string]func(m *Methodology){
		"integrates[0].methodologies":    func(m *Methodology) { m.Integrates = []Integration{{}} },
		"integrates[0].methodologies[0]": func(m *Methodology) { m.Integrates = []Integration{{Methodologies: []string{"rel"}}} },
		"integrates[0].attach":           func(m *Methodology) { m.Integrates = []Integration{{Methodologies: []string{"sdlc"}, Attach: "now"}} },
		"guardian":                       func(m *Methodology) { m.Guardian = &Guardian{Merge: "true"} },
		"guardian.merge": func(m *Methodology) {
			m.Integrates = []Integration{{Methodologies: []string{"sdlc"}}}
			m.Guardian = &Guardian{Merge: "integrator.state =="}
		},
		"on[0].from": func(m *Methodology) { m.On = []Subscription{{Event: "change.committed", From: "integrated"}} },
	}
	for path, mut := range cases {
		m := base()
		mut(m)
		found := false
		for _, is := range m.Validate() {
			found = found || is.Path == path
		}
		if !found {
			t.Errorf("%s: no issue (%v)", path, m.Validate())
		}
	}
	m := base()
	m.Integrates = []Integration{{Methodologies: []string{"*"}}}
	if !m.IntegratesChangesOf("sdlc") || m.IntegratesChangesOf("rel") {
		t.Fatal("* integrates every other methodology")
	}
}
