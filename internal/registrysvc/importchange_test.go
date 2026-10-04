package registrysvc

import (
	"context"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/graph"
)

// Importing a methodology and publishing it is one change, and the aliases it needs are proposed in one change.
func TestImportPublishedIsOneChange(t *testing.T) {
	ctx := authz.With(context.Background(), authz.Principal{Subject: "system:registry", Roles: []string{"admin"}})
	g := graph.New(graph.NewMemory())
	reg := &Service{Store: graphWithDomains{NewGraphStore(g), NewMemoryStore()}}
	if _, err := reg.SeedDomains(ctx, "../../domains"); err != nil {
		t.Fatal(err)
	}
	src := []byte(`
name: imp
version: "1"
namespace: alm
agents:
  - {name: a1, planner: goap, model: alias-one, actions: [x]}
  - {name: a2, planner: goap, model: alias-two, actions: [x]}
conditions: [{name: c, expr: "true"}]
actions:
  - {name: x, kind: human, effects: {c: true}}
goals: [{name: g, pre: {c: true}}]
`)
	r, _, err := reg.Import(ctx, src, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusPublished || r.PublishedAt.IsZero() {
		t.Fatalf("imported as published: %+v", r)
	}
	cs, err := g.Changes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var methodologies, aliases int
	for _, c := range cs {
		switch {
		case strings.HasPrefix(c.Title, "Methodology imp"):
			methodologies++
		case strings.HasPrefix(c.Title, "Aliases needed by imp"):
			aliases++
			if len(c.Nodes) != 2 {
				t.Fatalf("both aliases in the one change: %d", len(c.Nodes))
			}
		}
	}
	if methodologies != 1 || aliases != 1 {
		t.Fatalf("one change for the import (%d) and one for its aliases (%d)", methodologies, aliases)
	}
}
