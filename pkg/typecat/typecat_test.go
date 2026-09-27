package typecat

import (
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
)

func parse(t *testing.T, src string) *methodology.Domain {
	t.Helper()
	d, err := methodology.ParseDomain([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func fromFile(t *testing.T, name string) *methodology.Domain {
	t.Helper()
	src, err := os.ReadFile("../../domains/" + name + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	return parse(t, string(src))
}

func TestTypeRef(t *testing.T) {
	r, err := domain.ParseTypeRef("alm@Requirement")
	if err != nil || r != (domain.TypeRef{Namespace: "alm", Name: "Requirement"}) || r.String() != "alm@Requirement" {
		t.Fatalf("parse: %+v %v", r, err)
	}
	if r, _ := domain.QualifyIn("alm", "Need"); r.String() != "alm@Need" {
		t.Fatalf("a bare name is a type of the domain: %s", r)
	}
	if r, _ := domain.QualifyIn("alm", "organisation@OrgUnit"); r.String() != "organisation@OrgUnit" {
		t.Fatalf("a qualified name keeps its namespace: %s", r)
	}
	for _, bad := range []string{"", "@X", "alm@", "a@b@c"} {
		if _, err := domain.ParseTypeRef(bad); !errors.Is(err, domain.ErrInvalidRef) {
			t.Fatalf("%q must be refused: %v", bad, err)
		}
	}
}

func TestBuiltinDomainsAreAlwaysThere(t *testing.T) {
	c := Builtin()
	for _, ref := range []string{"methodology@Agent", "methodology@MethodologyVersion", "domain@NodeType", "domain@DomainVersion", "organisation@OrgUnit", "organisation@Policy"} {
		if _, ok := c.Type(ref); !ok {
			t.Fatalf("%s is built in", ref)
		}
	}
	if a, _ := c.Type("methodology@Agent"); a.Editor != "agent" {
		t.Fatalf("editor of the meta type: %+v", a)
	}
	if err := c.CheckLink("methodology@defines", "methodology@MethodologyVersion", "methodology@Agent"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"methodology", "organisation", "platform"} {
		if _, err := New(parse(t, "name: "+name+"\nversion: 1.0.0\nnodeTypes: [X]\n")); !errors.Is(err, ErrInvalid) {
			t.Fatalf("a domain cannot take the name of a frozen domain: %v", err)
		}
	}
	// a published version of the domain meta-domain replaces the shipped one
	c2, err := New(parse(t, "name: domain\nversion: 1.1.0\nnodeTypes: [DomainVersion, NodeType, Glossary]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !c2.HasNodeType("domain@Glossary") || c2.HasNodeType("domain@LinkType") || c2.Domains()["domain"] != "1.1.0" {
		t.Fatalf("the domain version in force: %v", c2.Domains())
	}
}

func TestRepositoryDomains(t *testing.T) {
	c, err := New(fromFile(t, "alm"))
	if err != nil {
		t.Fatal(err)
	}
	sec, ok := c.Type("alm@SecurityRequirement")
	if !ok {
		t.Fatal("alm@SecurityRequirement")
	}
	if !slices.Equal(sec.Ancestors, []domain.TypeRef{{Namespace: "alm", Name: "NonFunctionalRequirement"}, {Namespace: "alm", Name: "Requirement"}}) {
		t.Fatalf("ancestors, nearest first: %v", sec.Ancestors)
	}
	if sec.Lifecycle == nil || sec.Lifecycle.Name != "requirement" || !sec.ChangeControlled {
		t.Fatalf("the lifecycle is inherited: %+v", sec.Lifecycle)
	}
	if len(sec.Validators) == 0 || sec.Validators[0].Property != "title" {
		t.Fatalf("the validators are inherited: %+v", sec.Validators)
	}
	if sec.Properties[0] != "title" || !slices.Contains(sec.Properties, "category") {
		t.Fatalf("the properties, inherited first: %v", sec.Properties)
	}
	var facets []string
	for _, s := range sec.Search {
		if s.Facet {
			facets = append(facets, s.Property)
		}
	}
	if !slices.Contains(facets, "category") || !slices.Contains(facets, "priority") {
		t.Fatalf("the search declarations add up: %+v", sec.Search)
	}
	if u, _ := c.Type("organisation@OrgUnit"); u.Editor != "unit" {
		t.Fatalf("editor: %+v", u)
	}
	if got := c.Supertypes()["alm@SecurityRequirement"]; !slices.Equal(got, []string{"alm@NonFunctionalRequirement", "alm@Requirement"}) {
		t.Fatalf("supertypes: %v", got)
	}
	if c.Domains()["alm"] == "" {
		t.Fatal("the version of each domain is known")
	}

	// the existence rule
	if err := c.CheckNode("alm", "alm@Requirement"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ ns, typ string }{{"alm", "alm@Nope"}, {"alm", "Requirement"}, {"organisation", "alm@Requirement"}} {
		if err := c.CheckNode(tc.ns, tc.typ); err == nil {
			t.Fatalf("%s in %s must be refused", tc.typ, tc.ns)
		}
	}
	// links: known type, ends accepting subtypes, any source when the link type names none
	if err := c.CheckLink("alm@verifies", "alm@TestCase", "alm@SecurityRequirement"); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckLink("alm@verifies", "alm@Need", "alm@Requirement"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong source: %v", err)
	}
	if err := c.CheckLink("organisation@owner", "alm@Component", "organisation@OrgUnit"); err != nil {
		t.Fatalf("a link across domains: %v", err)
	}
	if err := c.CheckLink("alm@nope", "alm@Need", "alm@Need"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown link type: %v", err)
	}
}

func TestCrossDomainReferences(t *testing.T) {
	base := parse(t, `
name: base
version: 1.0.0
lifecycles:
  - {name: simple, initial: open, states: [{name: open, editable: true}, {name: done}], transitions: [{name: close, from: open, to: done}]}
nodeTypes:
  - {name: Item, lifecycle: simple, editor: item, properties: [title]}
`)
	ext := parse(t, `
name: ext
version: 2.0.0
nodeTypes:
  - {name: Ticket, extends: base@Item, properties: [severity]}
  - {name: Folder, document: {contains: [Ticket, base@Item]}}
linkTypes:
  - {name: blocks, from: Ticket, to: base@Item}
`)
	c, err := New(base, ext)
	if err != nil {
		t.Fatal(err)
	}
	tk, _ := c.Type("ext@Ticket")
	if tk.Lifecycle == nil || tk.Lifecycle.Name != "simple" || tk.Editor != "item" || !slices.Equal(tk.Properties, []string{"title", "severity"}) {
		t.Fatalf("a type extends a type of another domain: %+v", tk)
	}
	if f, _ := c.Type("ext@Folder"); f.Document == nil || !slices.Equal(f.Document.Contains, []string{"ext@Ticket", "base@Item"}) {
		t.Fatalf("document references are qualified: %+v", f.Document)
	}
	if err := c.CheckLink("ext@blocks", "ext@Ticket", "ext@Ticket"); err != nil {
		t.Fatalf("a subtype is accepted where its supertype is: %v", err)
	}

	if _, err := New(parse(t, "name: bad\nversion: 1.0.0\nnodeTypes: [{name: A, extends: other@B}]\n")); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown supertype: %v", err)
	}
	if _, err := New(parse(t, "name: bad\nversion: 1.0.0\nnodeTypes: [{name: A, extends: B}, {name: B, extends: A}]\n")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle: %v", err)
	}
	if _, err := New(parse(t, "name: bad\nversion: 1.0.0\nnodeTypes: [A]\nlinkTypes: [{name: l, to: x@Y}]\n")); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown link end: %v", err)
	}
}
