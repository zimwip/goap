package registrysvc

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/typecat"
)

// jsonFields lists the JSON names of the fields of a struct type, embedded structs flattened.
func jsonFields(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" || !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			out = append(out, jsonFields(f.Type)...)
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, name)
	}
	return out
}

// Every property the registry writes on the nodes of a methodology version is an attribute of its type of the
// built-in meta-domain (strict attributes): the header, the elements (their position, removed mark included) and the
// steps. The properties are the JSON fields of the definition structs, so a new field of one fails here until the
// meta-domain declares it.
func TestDefinitionPropertiesAreAttributes(t *testing.T) {
	cat := typecat.Builtin()
	extra := []string{"position", "removed"}
	cases := []struct {
		typ    string
		fields []string
		drop   []string // fields that are no property of the node
		add    []string
	}{
		{TypeMethodologyVersion, jsonFields(reflect.TypeFor[methodology.Methodology]()),
			[]string{"conditions", "actions", "goals", "agents", "processes", "methods", "roles"}, metaKeys},
		{"methodology@Condition", jsonFields(reflect.TypeFor[methodology.Condition]()), nil, extra},
		{"methodology@Action", jsonFields(reflect.TypeFor[methodology.Action]()), nil, extra},
		{"methodology@Goal", jsonFields(reflect.TypeFor[methodology.Goal]()), nil, extra},
		{"methodology@Agent", jsonFields(reflect.TypeFor[methodology.Agent]()), nil, extra},
		{"methodology@Process", jsonFields(reflect.TypeFor[methodology.Process]()), nil, extra},
		{"methodology@Method", jsonFields(reflect.TypeFor[methodology.Method]()), nil, extra},
		{"methodology@Role", jsonFields(reflect.TypeFor[methodology.Role]()), nil, extra},
		// a step's pre / done are renamed input / output, its sub-steps are nodes of their own (stepNodeProps)
		{"methodology@Step", jsonFields(reflect.TypeFor[methodology.Step]()), []string{"pre", "done", "steps"}, []string{"input", "output"}},
		{"methodology@MethodStep", jsonFields(reflect.TypeFor[methodology.Step]()), []string{"pre", "done", "steps"}, []string{"input", "output"}},
	}
	for _, c := range cases {
		names, open := cat.AttributeNames(c.typ)
		if open {
			t.Errorf("%s is open: the check below would be void", c.typ)
		}
		for _, f := range append(slices.DeleteFunc(slices.Clone(c.fields), func(f string) bool { return slices.Contains(c.drop, f) }), c.add...) {
			if !slices.Contains(names, f) {
				t.Errorf("%s: the registry writes %q, which is no attribute of the type", c.typ, f)
			}
		}
	}
}
