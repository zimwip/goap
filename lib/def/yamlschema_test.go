package def

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// rustYAMLSchema is the shape of a domain as yaml.v3 writes it (ADR 0163): the fields of each struct in their order,
// their YAML and JSON names and whether they are omitted when empty, so that the Rust graph service writes the YAML
// export of a domain from its stored JSON as `Domain.YAML` does.
const rustYAMLSchema = "../../rust/crates/graph-service/src/domain_yaml.gen.json"

// yamlZero names the fields whose emptiness makes a value implementing yaml.v3's IsZeroer empty.
var yamlZero = map[string][]string{
	"form.Form": {"fields", "title", "description"},
}

type yamlField struct {
	YAML string    `json:"yaml"`
	JSON string    `json:"json"`
	Omit bool      `json:"omit,omitempty"`
	Type *yamlType `json:"type"`
}

type yamlType struct {
	Kind string    `json:"kind"`
	Name string    `json:"name,omitempty"`
	Elem *yamlType `json:"elem,omitempty"`
}

type yamlStruct struct {
	Fields []yamlField `json:"fields"`
	Zero   []string    `json:"zero,omitempty"`
}

func yamlSchemaOf(t *testing.T) map[string]*yamlStruct {
	types := map[string]*yamlStruct{}
	zeroer := reflect.TypeOf((*yaml.IsZeroer)(nil)).Elem()
	var typeOf func(reflect.Type) *yamlType
	var fields func(reflect.Type) []yamlField
	fields = func(rt reflect.Type) []yamlField {
		var out []yamlField
		for i := range rt.NumField() {
			f := rt.Field(i)
			if f.PkgPath != "" {
				continue
			}
			ytag, jtag := f.Tag.Get("yaml"), f.Tag.Get("json")
			if ytag == "-" {
				continue
			}
			yname, yopts, _ := strings.Cut(ytag, ",")
			jname, _, _ := strings.Cut(jtag, ",")
			if strings.Contains(yopts, "inline") {
				if !f.Anonymous || jtag != "" {
					t.Fatalf("%s.%s: inline in YAML only", rt, f.Name)
				}
				out = append(out, fields(f.Type)...)
				continue
			}
			if strings.Contains(yopts, "flow") {
				t.Fatalf("%s.%s: flow style not written by the Rust service", rt, f.Name)
			}
			if f.Anonymous && jtag == "" {
				t.Fatalf("%s.%s: embedded in JSON only", rt, f.Name)
			}
			if yname == "" {
				yname = strings.ToLower(f.Name)
			}
			if jname == "" {
				jname = f.Name
			}
			if jtag == "-" {
				jname = ""
			}
			out = append(out, yamlField{YAML: yname, JSON: jname, Omit: strings.Contains(yopts, "omitempty"), Type: typeOf(f.Type)})
		}
		return out
	}
	typeOf = func(rt reflect.Type) *yamlType {
		switch rt.Kind() {
		case reflect.String:
			return &yamlType{Kind: "string"}
		case reflect.Bool:
			return &yamlType{Kind: "bool"}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return &yamlType{Kind: "int"}
		case reflect.Float32, reflect.Float64:
			return &yamlType{Kind: "float"}
		case reflect.Interface:
			return &yamlType{Kind: "any"}
		case reflect.Slice:
			return &yamlType{Kind: "slice", Elem: typeOf(rt.Elem())}
		case reflect.Map:
			if rt.Key().Kind() != reflect.String {
				t.Fatalf("%s: a map keyed by other than strings", rt)
			}
			return &yamlType{Kind: "map", Elem: typeOf(rt.Elem())}
		case reflect.Pointer:
			return &yamlType{Kind: "ptr", Elem: typeOf(rt.Elem())}
		case reflect.Struct:
			name := rt.String()
			if _, ok := types[name]; !ok {
				s := &yamlStruct{}
				types[name] = s
				s.Fields = fields(rt)
				if rt.Implements(zeroer) || reflect.PointerTo(rt).Implements(zeroer) {
					z, ok := yamlZero[name]
					if !ok {
						t.Fatalf("%s implements IsZero: name its fields in yamlZero", name)
					}
					s.Zero = z
				}
			}
			return &yamlType{Kind: "struct", Name: name}
		}
		t.Fatalf("%s: a kind the Rust service does not write", rt)
		return nil
	}
	if root := typeOf(reflect.TypeOf(Domain{})); root.Name != "def.Domain" {
		t.Fatalf("root %s", root.Name)
	}
	return types
}

func TestRustYAMLSchemaIsCurrent(t *testing.T) {
	raw, err := json.MarshalIndent(map[string]any{
		"comment": "Code generated from lib/def (GOAP_UPDATE_RUST=1 go test ./lib/def/ -run TestRustYAMLSchemaIsCurrent). DO NOT EDIT.",
		"root":    "def.Domain",
		"types":   yamlSchemaOf(t),
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want := string(raw) + "\n"
	if os.Getenv("GOAP_UPDATE_RUST") != "" {
		if err := os.WriteFile(rustYAMLSchema, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(rustYAMLSchema)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatal(fmt.Sprintf("%s is not the shape of def.Domain: run GOAP_UPDATE_RUST=1 go test ./lib/def/ -run TestRustYAMLSchemaIsCurrent", rustYAMLSchema))
	}
}
