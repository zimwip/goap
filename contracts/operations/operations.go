// Package operations is the catalogue of the operations a person performs on a change (ADR 0103): checking a node out,
// editing its draft, withdrawing an impact, reviewing it, committing and merging the change. The list comes from the
// code and is fixed: each operation is a method of pkg/graph annotated with `//goap:operation <code> <kind>`, and
// goap-opgen (internal/opgen) generates operations_gen.go from those annotations, refusing a duplicate code. A
// methodology's roles name the operations they allow (methodology.Role.Operations), by code or by kind; the graph asks
// its OperationAuthorizer before each one. A leaf package: no project import.
package operations

//go:generate go run ../../platform/cmd/goap-opgen -src ../../graph/graph -out operations_gen.go

import (
	"slices"
	"sort"
)

// Kind classes an operation by who performs it: the doer changes the work (drafts, impacts, commits), the reviewer
// judges it (accepts or rejects an impact) and never changes it.
type Kind string

// The kinds of operation.
const (
	Doer     Kind = "doer"
	Reviewer Kind = "reviewer"
)

// Kinds lists the kinds of operation.
func Kinds() []Kind { return []Kind{Doer, Reviewer} }

// Operation is an entry of the catalogue: its code, its kind and the method of pkg/graph that performs it.
type Operation struct {
	Code   string
	Kind   Kind
	Method string
	Doc    string
}

// All lists the operations of the catalogue, by code.
func All() []Operation { return slices.Clone(catalog) }

// Lookup returns the operation of a code.
func Lookup(code string) (Operation, bool) {
	for _, o := range catalog {
		if o.Code == code {
			return o, true
		}
	}
	return Operation{}, false
}

// Known reports whether name is the code of an operation or a kind of operation, what a role may list.
func Known(name string) bool {
	_, ok := Lookup(name)
	return ok || slices.Contains(Kinds(), Kind(name))
}

// Expand resolves what a role lists (codes and kinds) to the codes of the operations it allows, sorted; the names it
// does not know are left out (the compiler reports them).
func Expand(names []string) []string {
	set := map[string]bool{}
	for _, n := range names {
		for _, o := range catalog {
			if o.Code == n || string(o.Kind) == n {
				set[o.Code] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Allows reports whether what a role lists allows the operation of code: the code itself or its kind.
func Allows(names []string, code string) bool {
	o, ok := Lookup(code)
	return ok && (slices.Contains(names, code) || slices.Contains(names, string(o.Kind)))
}
