package opgen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The catalogue in contracts/operations is the one the annotations of graph/graph give: run `go generate ./contracts/operations/`
// after adding, renaming or removing an annotation.
func TestCatalogueIsCurrent(t *testing.T) {
	ops, err := Scan(filepath.Join("..", "..", "graph", "graph"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := Render("operations", ops)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join("..", "..", "contracts", "operations", "operations_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("contracts/operations/operations_gen.go is out of date: run go generate ./contracts/operations/")
	}
}

func scanSource(t *testing.T, src string) ([]Operation, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "g.go"), []byte("package graph\n\n"+src), 0o644); err != nil {
		t.Fatal(err)
	}
	return Scan(dir)
}

const header = `
import "context"

type Graph struct{}

var operations struct{ ImpactReview, ImpactUpdate string }

func (g *Graph) allow(ctx context.Context, id string, op string) error { return nil }
`

func TestScan(t *testing.T) {
	ops, err := scanSource(t, header+`
// Review judges an impact.
//
//goap:operation impact.review reviewer
func (g *Graph) Review(ctx context.Context, id string) error {
	if err := g.allow(ctx, id, operations.ImpactReview); err != nil {
		return err
	}
	return nil
}

// Batch reviews several: the same operation, not declared again.
func (g *Graph) Batch(ctx context.Context, id string) error {
	return g.allow(ctx, id, operations.ImpactReview)
}
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Code != "impact.review" || ops[0].Kind != "reviewer" || ops[0].Method != "Graph.Review" || ops[0].Doc != "Review judges an impact." {
		t.Fatalf("ops = %+v", ops)
	}
	if Const("impact.link-create") != "ImpactLinkCreate" || Const("change.merge") != "ChangeMerge" {
		t.Fatal("Const")
	}
}

func TestScanRefuses(t *testing.T) {
	method := func(name, directive, body string) string {
		return "\n// " + name + " does it.\n//\n" + directive + "\nfunc (g *Graph) " + name + "(ctx context.Context, id string) error {\n" + body + "\n\treturn nil\n}\n"
	}
	asks := "\tg.allow(ctx, id, operations.ImpactReview)"
	for name, tc := range map[string]struct{ src, want string }{
		"duplicate code": {
			method("A", "//goap:operation impact.review reviewer", asks) + method("B", "//goap:operation impact.review reviewer", asks),
			"operation impact.review declared twice",
		},
		"same constant": {
			method("A", "//goap:operation impact.link.create doer", "\tg.allow(ctx, id, operations.ImpactLinkCreate)") + method("B", "//goap:operation impact.link-create doer", "\tg.allow(ctx, id, operations.ImpactLinkCreate)"),
			"give the same constant ImpactLinkCreate",
		},
		"unknown kind": {method("A", "//goap:operation impact.review judge", asks), `unknown kind "judge"`},
		"bad code":     {method("A", "//goap:operation Review reviewer", asks), `operation code "Review"`},
		"missing kind": {method("A", "//goap:operation impact.review", asks), "want //goap:operation <code> <kind>"},
		"never asks":   {method("A", "//goap:operation impact.update doer", ""), "never asks for it"},
		"wrong ask":    {method("A", "//goap:operation impact.update doer", asks), "operations.ImpactReview, which no //goap:operation annotation declares"},
		"not a method": {"\n// F does it.\n//\n//goap:operation impact.review reviewer\nfunc F() {}\n", "only an exported method of *Graph"},
		"loose":        {"\n//goap:operation impact.review reviewer\nvar x int\n", "documents no method"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := scanSource(t, header+tc.src)
			if err == nil {
				t.Fatal("want an error")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q, want %q", err, tc.want)
			}
		})
	}
}
