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
	for _, ref := range []string{"methodology@Agent", "methodology@MethodologyVersion", "organisation@OrgUnit", "organisation@Policy", "platform@MCP"} {
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
	if c.HasNodeType("domain@NodeType") {
		t.Fatal("domain definitions are not graph data (ADR 0023)")
	}
	for _, name := range []string{"methodology", "organisation", "platform"} {
		if _, err := New(parse(t, "name: "+name+"\nversion: 1.0.0\nnodeTypes: [X]\n")); !errors.Is(err, ErrInvalid) {
			t.Fatalf("a domain cannot take the name of a built-in domain: %v", err)
		}
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
	if err := c.CheckLink("organisation@member_of", "organisation@User", "organisation@OrgUnit"); err != nil {
		t.Fatalf("a link to a supertype: %v", err)
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

// TestUserSatisfiesOrgUnit checks ADR 0039: User extends OrgUnit in the built-in organisation domain, so a
// User node satisfies an OrgUnit-typed link end (Assignment's assigns_org link can target a unit, a team or
// a person uniformly), without member_of folding User into the part_of unit tree.
func TestUserSatisfiesOrgUnit(t *testing.T) {
	c := Builtin()
	ut, ok := c.Type("organisation@User")
	if !ok || !ut.Is(domain.TypeRef{Namespace: "organisation", Name: "OrgUnit"}) {
		t.Fatalf("organisation@User must extend organisation@OrgUnit: %+v", ut)
	}
	if err := c.CheckLink("organisation@assigns_org", "organisation@Assignment", "organisation@User"); err != nil {
		t.Fatalf("a User satisfies the OrgUnit end of assigns_org: %v", err)
	}
	if err := c.CheckLink("organisation@assigns_org", "organisation@Assignment", "organisation@OrgUnit"); err != nil {
		t.Fatalf("an OrgUnit satisfies the OrgUnit end of assigns_org: %v", err)
	}
	// part_of and project_part_of share a shape (child -> parent) but not a name, on purpose: the
	// catalogue merges same-named link types declared for several end pairs into "accepts any pair"
	// (pkg/typecat.New), which would erase part_of's OrgUnit-only constraint. A ProjectUnit must stay
	// rejected by part_of.
	if err := c.CheckLink("organisation@part_of", "organisation@ProjectUnit", "organisation@ProjectUnit"); err == nil {
		t.Fatalf("part_of must still reject a ProjectUnit: its OrgUnit-only constraint must not have been erased")
	}
	if err := c.CheckLink("organisation@project_part_of", "organisation@ProjectUnit", "organisation@ProjectUnit"); err != nil {
		t.Fatalf("project_part_of accepts a ProjectUnit at both ends: %v", err)
	}
}

// TestActivitySpecializesIsTransitive checks that Process/Step/Method/MethodStep/Action all extend
// methodology@Activity (architecture plan "Activity concept"), so the generic specializes link (declared
// from/to Activity) accepts any of them, at either end, without naming each one - Action satisfies it through
// Activity transitively, the same way User satisfies OrgUnit above.
func TestActivitySpecializesIsTransitive(t *testing.T) {
	c := Builtin()
	activity := domain.TypeRef{Namespace: "methodology", Name: "Activity"}
	for _, name := range []string{"Process", "Step", "Method", "MethodStep", "Action"} {
		typ, ok := c.Type("methodology@" + name)
		if !ok || !typ.Is(activity) {
			t.Fatalf("methodology@%s must extend methodology@Activity: %+v", name, typ)
		}
	}
	for _, pair := range [][2]string{{"Action", "Action"}, {"Method", "Action"}, {"Process", "Process"}, {"Step", "MethodStep"}} {
		if err := c.CheckLink("methodology@specializes", "methodology@"+pair[0], "methodology@"+pair[1]); err != nil {
			t.Fatalf("specializes(%s, %s): %v", pair[0], pair[1], err)
		}
	}
	if err := c.CheckLink("methodology@specializes", "methodology@Role", "methodology@Action"); err == nil {
		t.Fatal("a Role is not an Activity: specializes must still reject it")
	}
}

func TestComposeFlag(t *testing.T) {
	c := Builtin()
	for _, ref := range []string{"methodology@defines", "methodology@sub_activity"} {
		l, ok := c.LinkType(ref)
		if !ok || !l.Compose {
			t.Fatalf("%s: compose = %v (known %v), want true", ref, ok && l.Compose, ok)
		}
	}
	if l, ok := c.LinkType("methodology@specializes"); !ok || l.Compose {
		t.Fatalf("specializes: compose = %v, want false", l != nil && l.Compose)
	}
}

// The structures (ADR 0054) are the types the domains tag: the built-in organisation domain tags the organisation and
// the project, users are units through subtyping, and no other domain may tag a structure again.
func TestStructures(t *testing.T) {
	c := Builtin()
	org, ok := c.Structure(domain.StructureOrganisation)
	if !ok || org != domain.BuiltinStructures[domain.StructureOrganisation] {
		t.Fatalf("organisation = %+v %v", org, ok)
	}
	proj, ok := c.Structure(domain.StructureProject)
	if !ok || proj != domain.BuiltinStructures[domain.StructureProject] {
		t.Fatalf("project = %+v %v", proj, ok)
	}
	if !c.IsA("organisation@User", org.Type) || c.IsA(proj.Type, org.Type) || c.IsA("organisation@Nope", org.Type) {
		t.Fatal("IsA")
	}
	other := parse(t, `
name: hr
version: 1.0.0
nodeTypes:
  - {name: Team, structure: {kind: organisation, parent: within, root: HR-ROOT}}
linkTypes:
  - {name: within, from: Team, to: Team}
`)
	if issues := other.Validate(); len(issues) > 0 {
		t.Fatalf("the domain itself is valid: %v", issues)
	}
	if _, err := New(other); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a second organisation structure: %v", err)
	}
}

// A structure tag names a known kind, a root and a parent link type of its domain from and to the tagged type.
func TestStructureTagIssues(t *testing.T) {
	d := parse(t, `
name: hr
version: 1.0.0
nodeTypes:
  - {name: Team, structure: {kind: department, parent: nope}}
  - {name: Site, structure: {kind: project, parent: near, root: S-1}}
  - {name: Other}
linkTypes:
  - {name: near, from: Site, to: Other}
`)
	var paths []string
	for _, is := range d.Validate() {
		paths = append(paths, is.Path)
	}
	for _, want := range []string{"nodeTypes[0].structure.kind", "nodeTypes[0].structure.root", "nodeTypes[0].structure.parent", "nodeTypes[1].structure.parent"} {
		if !slices.Contains(paths, want) {
			t.Errorf("no issue on %s: %v", want, paths)
		}
	}
}
