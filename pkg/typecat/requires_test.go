package typecat

import (
	"reflect"
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// The built-in domains flag these types, no more, no less (ADR 0068).
func TestBuiltinAdminOnly(t *testing.T) {
	flagged := map[string]bool{"organisation@OrgUnit": true, "organisation@User": true, "organisation@ProjectUnit": true,
		"organisation@Adapter": true, "platform@AdapterDef": true, "organisation@Policy": true, "organisation@Assignment": true,
		"organisation@CriticalityPolicy": true}
	c := Builtin()
	for _, d := range Builtins() {
		for _, n := range d.NodeTypes {
			typ := d.Name + "@" + n.Name
			if got := c.AdminOnly(typ); got != flagged[typ] {
				t.Errorf("%s: adminOnly %v, want %v", typ, got, flagged[typ])
			}
		}
	}
}

// A node type declares the links its nodes must carry (ADR 0065): the built-in organisation domain requires one
// member_of of a User, a subtype inherits and may redefine them, and a bad declaration is an issue.
func TestRequires(t *testing.T) {
	c := Builtin()
	want := []domain.RequiredLink{{Link: "organisation@member_of", Count: 1}}
	if got := c.Requires("organisation@User"); !reflect.DeepEqual(got, want) {
		t.Fatalf("User requires %+v", got)
	}
	if got := c.Requires("organisation@OrgUnit"); len(got) != 0 {
		t.Fatalf("OrgUnit requires %+v", got)
	}
	d := parse(t, `
name: hr
version: 1.0.0
nodeTypes:
  - {name: Badge, requires: [{link: owner}, {link: site, count: 2}]}
  - {name: Guest, extends: Badge, requires: [{link: site, count: 1}]}
linkTypes:
  - {name: owner, from: Badge}
  - {name: site, from: Badge}
`)
	if issues := d.Validate(); len(issues) > 0 {
		t.Fatalf("valid: %v", issues)
	}
	hr, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hr.Requires("hr@Guest"), []domain.RequiredLink{{Link: "hr@owner", Count: 1}, {Link: "hr@site", Count: 1}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Guest requires %+v, want %+v", got, want)
	}
	bad := parse(t, `
name: hr
version: 1.0.0
nodeTypes:
  - {name: Badge, requires: [{link: nope}, {link: ""}]}
`)
	var paths []string
	for _, is := range bad.Validate() {
		paths = append(paths, is.Path)
	}
	for _, p := range []string{"nodeTypes[0].requires[0].link", "nodeTypes[0].requires[1].link"} {
		if !slices.Contains(paths, p) {
			t.Errorf("no issue on %s: %v", p, paths)
		}
	}
}
