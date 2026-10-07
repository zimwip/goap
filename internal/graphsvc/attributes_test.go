package graphsvc_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/adapter"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/criticality"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/llmcfg"
	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/typecat"
)

// jsonKeys lists the JSON names of the exported fields of a struct type, embedded structs flattened.
func jsonKeys(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" || !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			out = append(out, jsonKeys(f.Type)...)
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, name)
	}
	return out
}

// The properties the platform writes on the nodes of the built-in domains (seeds, sign-in, the settings of the web)
// are attributes of their types (strict attributes): the nodes built from fully populated values carry no other key,
// and the structs that become properties through their JSON carry no field the type does not declare.
func TestBuiltinNodePropsAreAttributes(t *testing.T) {
	cat := typecat.Builtin()
	keys := func(m map[string]any) []string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		return ks
	}
	cases := []struct {
		typ  string
		keys []string
	}{
		// built by hand: every key the constructors can put
		{access.NodeTypeUser, keys(access.User{Subject: "s", DisplayName: "n", Email: "e@x.y", Locale: "en"}.Props())},
		{access.NodeTypePolicy, keys(access.PolicyProps(authz.Policy{Rule: "r", Resource: "x", Action: "y", Effect: "allow"}))},
		{access.NodeTypeAssignment, keys(access.Assignment{Roles: []string{"r"}, Description: "d"}.Props())},
		{access.NodeTypeProjectUnit, keys(access.ProjectUnit{Name: "n", Description: "d", Kind: "k", Status: "s", Methodologies: []string{"m"}}.Props())},
		{access.NodeTypeCriticalityPolicy, keys(access.CriticalityProps(criticality.C2, criticality.Policy{Oracles: []string{"tool"}, Sampling: true, SignatoryRole: "r", MaxDerogation: time.Hour}))},
		{access.NodeTypeRole, keys(access.Role{Name: "n", Description: "d"}.Props())},
		{access.NodeTypeOrgUnit, []string{"name", "kind", "description", access.PropWaitingUnit}},
		// structs written through their JSON
		{domain.TypeAdapterDef, jsonKeys(reflect.TypeFor[adapter.Def]())},
		// the unit is the owner of the node, not a property (Instance.Props)
		{"organisation@Adapter", slices.DeleteFunc(jsonKeys(reflect.TypeFor[adapter.Instance]()), func(k string) bool { return k == "unit" })},
		{"platform@MCP", jsonKeys(reflect.TypeFor[mcp.Def]())},
		{llmcfg.NodeTypeProvider, jsonKeys(reflect.TypeFor[llmcfg.Provider]())},
		{llmcfg.NodeTypeModel, jsonKeys(reflect.TypeFor[llmcfg.Model]())},
		{llmcfg.NodeTypeAlias, jsonKeys(reflect.TypeFor[llmcfg.Alias]())},
		{llmcfg.NodeTypeBehavior, jsonKeys(reflect.TypeFor[llmcfg.Behavior]())},
	}
	for _, c := range cases {
		names, open := cat.AttributeNames(c.typ)
		if open || len(names) == 0 {
			t.Errorf("%s: no closed type in the catalogue (names %v, open %v)", c.typ, names, open)
			continue
		}
		for _, k := range c.keys {
			if !slices.Contains(names, k) {
				t.Errorf("%s: the platform writes %q, which is no attribute of the type", c.typ, k)
			}
		}
	}
}
