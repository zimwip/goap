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

type recorder struct{ subjects []string }

func (r *recorder) Publish(_ context.Context, subject string, _ any) error {
	r.subjects = append(r.subjects, subject)
	return nil
}

// Importing a published domain or methodology is one write and one event, as one change.
func TestImportPublishedIsOneEvent(t *testing.T) {
	ctx := authz.With(context.Background(), authz.Principal{Subject: "system:registry", Roles: []string{"admin"}})
	ev := &recorder{}
	reg := &Service{Store: graphWithDomains{NewGraphStore(graph.New(graph.NewMemory())), NewMemoryStore()}, Events: ev}
	r, _, err := reg.ImportDomain(ctx, []byte("name: imp\nversion: \"1\"\nnodeTypes: [{name: A}]\n"), true)
	if err != nil || r.Status != StatusPublished || r.PublishedAt.IsZero() {
		t.Fatalf("imported as published: %+v %v", r, err)
	}
	if len(ev.subjects) != 1 || !strings.HasSuffix(ev.subjects[0], "domain.published") {
		t.Fatalf("one event: %v", ev.subjects)
	}
	// a domain with issues is kept as a draft and refused
	ev.subjects = nil
	r, issues, err := reg.ImportDomain(ctx, []byte("name: bad\nversion: \"1\"\nnodeTypes: [{name: A, extends: Nope}]\n"), true)
	if err == nil || len(issues) == 0 || r.Status != StatusDraft {
		t.Fatalf("a domain with issues stays a draft: %+v %v %v", r, issues, err)
	}
}
