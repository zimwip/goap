package registrysvc

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
)

// The guardian chain (ADR 0106): a methodology integrating another frames its changes, its guardian checked after
// theirs, with `integrator` the nearest ancestor change of its methodology.
func TestGuardianChain(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	store := NewGraphStore(g)
	reg := &Service{Store: store, Applicable: func(context.Context, string) ([]string, error) {
		return []string{"portfolio", "release", "work"}, nil
	}}
	save := func(m methodology.Methodology) {
		t.Helper()
		if err := store.Save(ctx, Record{Methodology: m, Status: StatusPublished}); err != nil {
			t.Fatal(err)
		}
	}
	steps := func(name string) []methodology.Process {
		return []methodology.Process{{Name: name, Steps: []methodology.Step{{Name: "do"}}}}
	}
	save(methodology.Methodology{Name: "work", Version: "1", Namespace: "alm", Processes: steps("work")})
	save(methodology.Methodology{Name: "release", Version: "2", Namespace: "alm", Processes: steps("release"),
		Integrates: []methodology.Integration{{Methodologies: []string{"work"}, Exclusive: true}},
		Guardian: &methodology.Guardian{Merge: `integrator.state == "integrating"`,
			Transitions: map[string]string{"ready": `!integrator.attached || integrator.state != "frozen"`}}})
	save(methodology.Methodology{Name: "portfolio", Version: "1", Namespace: "alm", Processes: steps("portfolio"),
		Integrates: []methodology.Integration{{Methodologies: []string{"release"}}}})

	chain, err := reg.Guardians(ctx, "work", "PROJ-ROOT")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range chain {
		names = append(names, r.Methodology+"@"+r.Version)
	}
	if !slices.Equal(names, []string{"portfolio@1", "release@2", "work@1"}) {
		t.Fatalf("the chain follows integrates upwards, outermost first: %v", names)
	}
	if ok, _ := reg.Integrates(ctx, "release", "work"); !ok {
		t.Fatal("release integrates work")
	}
	if ok, _ := reg.Integrates(ctx, "portfolio", "work"); ok {
		t.Fatal("portfolio integrates release only")
	}

	c := domain.Change{ID: "c1", Methodology: "work", Guardians: chain}
	bb := domain.Blackboard{Change: c}
	refusal, err := reg.CheckGuardians(ctx, bb, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(refusal, "release requires integrator.attached") {
		t.Fatalf("an exclusive integrator refuses an unattached change: %q", refusal)
	}
	rel := domain.Change{ID: "r1", Methodology: "release", State: "screening"}
	if refusal, _ = reg.CheckGuardians(ctx, bb, "", []domain.Change{rel}); !strings.Contains(refusal, `integrator.state == "integrating"`) {
		t.Fatalf("the merge waits for the release to integrate: %q", refusal)
	}
	rel.State = "integrating"
	if refusal, _ = reg.CheckGuardians(ctx, bb, "", []domain.Change{rel}); refusal != "" {
		t.Fatalf("attached to an integrating release, it merges: %q", refusal)
	}
	// a transition guard by name; other transitions are free
	rel.State = "frozen"
	if refusal, _ = reg.CheckGuardians(ctx, bb, "ready", []domain.Change{rel}); refusal == "" {
		t.Fatal("a frozen release holds the transition ready")
	}
	if refusal, _ = reg.CheckGuardians(ctx, bb, "start", []domain.Change{rel}); refusal != "" {
		t.Fatalf("no guard for start: %q", refusal)
	}
	if refusal, _ = reg.CheckGuardians(ctx, bb, "ready", nil); refusal != "" {
		t.Fatalf("unattached, ready is free: %q", refusal)
	}

	// an integration cycle is reported on the methodology that closes it
	cyc := methodology.Methodology{Name: "work", Version: "2", Namespace: "alm", Processes: steps("work"),
		Integrates: []methodology.Integration{{Methodologies: []string{"portfolio"}}}}
	issues := reg.integrationIssues(ctx, &cyc)
	if len(issues) != 1 || !strings.Contains(issues[0].Message, "work integrates portfolio integrates release integrates work") {
		t.Fatalf("cycle: %+v", issues)
	}
	if issues := reg.integrationIssues(ctx, &methodology.Methodology{Name: "release", Version: "3",
		Integrates: []methodology.Integration{{Methodologies: []string{"work"}}}}); len(issues) != 0 {
		t.Fatalf("no cycle: %+v", issues)
	}

	// the composition (methodology.CompositionIssues): release follows a move of the work changes, which have no
	// lifecycle; its guardian names a transition they do not have
	rel3 := methodology.Methodology{Name: "release", Version: "3", Namespace: "alm", Processes: steps("release"),
		Integrates: []methodology.Integration{{Methodologies: []string{"work"}}},
		Guardian:   &methodology.Guardian{Transitions: map[string]string{"ready": "integrator.attached"}},
		On:         []methodology.Subscription{{Event: "change.transitioned", From: methodology.FromIntegrated}}}
	var got []string
	for _, i := range reg.compositionIssues(ctx, &rel3) {
		if !i.IsWarning() || i.Hint == "" {
			t.Fatalf("a composition issue is a warning with a hint: %+v", i)
		}
		got = append(got, i.Path)
	}
	if !slices.Equal(got, []string{"on[0].event", "guardian.transitions.ready"}) {
		t.Fatalf("composition: %v", got)
	}
}
