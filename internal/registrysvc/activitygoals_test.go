package registrysvc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
)

// End-to-end: a change scoped to a real Activity (a methodology@Process node) is gated at Apply by that
// process's own compiled goal condition (architecture plan "Activity concept"), resolved from the registry
// through Service.ActivityGoalsMet - no fake callback, the real methodology compile-and-evaluate path.
func TestActivityGoalsMetGatesApply(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	store := graphWithDomains{NewGraphStore(g), NewMemoryStore()}
	reg := &Service{Store: store}
	g.Guardians = map[string]graph.Guardian{GuardianName: Guardian{Service: reg}}
	g.DefaultGuardian = GuardianName

	m := methodology.Methodology{
		Name: "shipping", Version: "1", Namespace: "alm",
		Conditions: []methodology.Condition{{Name: "signed", Expr: `artifacts.exists(a, a.type == "signoff")`}},
		Processes: []methodology.Process{{Name: "ship", Steps: []methodology.Step{
			{Name: "confirm", Done: map[string]bool{"signed": true}},
		}}},
	}
	if err := store.Save(ctx, Record{Methodology: m, Status: StatusPublished}); err != nil {
		t.Fatal(err)
	}
	activityRef := MethodologyVersionKey(m.Name, m.Version) + "/process/ship"

	base, err := g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: "ship it", BaselineID: base.ID, Data: map[string]any{DataActivity: activityRef}})
	if err != nil {
		t.Fatal(err)
	}

	// the process's goal ("signed") is not yet met: Apply refuses with the activity's own message
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, graph.ErrInvalid) || !strings.Contains(err.Error(), "goal of activity") {
		t.Fatalf("goal unmet: %v", err)
	}

	// an unrelated artifact still doesn't satisfy it
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindArtifact, Type: "note", Status: domain.ItemAccepted}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, graph.ErrInvalid) || !strings.Contains(err.Error(), "goal of activity") {
		t.Fatalf("unrelated artifact: %v", err)
	}

	// the signoff artifact satisfies the process's goal: Apply succeeds
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindArtifact, Type: "signoff", Status: domain.ItemAccepted}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatalf("goal met: %v", err)
	}
}

// An unresolvable ActivityRef (unknown methodology, or an item name that doesn't exist in it) is a hard error,
// not a silent pass: a stale or malformed reference must not quietly disable the gate it was meant to enforce.
func TestActivityGoalsMetRefusesAnUnresolvableRef(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	store := graphWithDomains{NewGraphStore(g), NewMemoryStore()}
	reg := &Service{Store: store}
	g.Guardians = map[string]graph.Guardian{GuardianName: Guardian{Service: reg}}
	g.DefaultGuardian = GuardianName

	base, err := g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: "x", BaselineID: base.ID, Data: map[string]any{DataActivity: "MV:no-such@1/process/p"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); err == nil {
		t.Fatal("an unresolvable activity ref must refuse, not silently pass")
	}
}
