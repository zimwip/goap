package graph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/typecat"
)

// declareItemTypes makes the graph judge the items by a catalogue in which a test domain declares the given item
// types (ADR 0128), next to the built-in ones.
func declareItemTypes(t *testing.T, g *Graph, names ...string) {
	t.Helper()
	d := &def.Domain{Name: "itemtest", Version: "1.0.0"}
	for _, n := range names {
		d.ItemTypes = append(d.ItemTypes, def.ItemType{Name: n})
	}
	declareDomain(t, g, d)
}

func declareDomain(t *testing.T, g *Graph, d *def.Domain) {
	t.Helper()
	cat, err := typecat.New(d)
	if err != nil {
		t.Fatal(err)
	}
	g.Types = func() TypeCatalog { return cat }
}

// The graph stores only the items of a type a domain in force declares, and checks the keys of their data the type
// declares as attributes; the other keys stay free (ADR 0128).
func TestItemTypesDeclaredByDomains(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		f := newFixture(t, repo)
		g := f.g
		c, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "t", ForkCommit: f.base.ID})
		if err != nil {
			t.Fatal(err)
		}
		// untyped graph: the built-in domains declare the types of the change mechanism and of the use cases
		if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Type: domain.TypeArtifact, Target: "note"}}); err != nil {
			t.Fatalf("a built-in type: %v", err)
		}
		if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Type: "testResult"}}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "no domain in force declares it") {
			t.Fatalf("an undeclared type: %v", err)
		}
		declareDomain(t, g, &def.Domain{Name: "itemtest", Version: "1.0.0", Schema: def.Schema{
			Enums: []def.Enum{{Name: "verdict", Values: []def.EnumValue{{Value: "pass"}, {Value: "fail"}}}},
			ItemTypes: []def.ItemType{{Name: "testResult", Description: "The result of a test run", Attributes: []def.Attribute{
				{Name: "verdict", Type: def.AttrEnum, Enum: "verdict"}, {Name: "count", Type: def.AttrNumber}}}},
		}})
		if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Type: "testResult", Data: map[string]any{"verdict": "pass", "count": 3, "free": true}}}); err != nil {
			t.Fatalf("a declared type: %v", err)
		}
		for _, data := range []map[string]any{{"verdict": "maybe"}, {"count": "many"}} {
			if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Type: "testResult", Data: data}}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("data %v: %v", data, err)
			}
		}
		var found *typecat.ItemType
		for _, x := range g.ItemTypes() {
			if x.Name == "testResult" {
				found = x
			}
		}
		if found == nil || found.Domain != "itemtest" || found.Description != "The result of a test run" || len(found.Attributes) != 2 || len(found.Attributes[0].Values) != 2 {
			t.Fatalf("listed: %+v", found)
		}
	})
}
