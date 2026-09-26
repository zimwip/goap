package mcp

import (
	"errors"
	"reflect"
	"testing"
)

func TestMapArguments(t *testing.T) {
	args := map[string]any{"path": "/a", "opts": map[string]any{"deep": 1.0}}
	tmpl := map[string]any{
		"file":   "$.path",
		"mode":   "r",
		"deep":   "$.opts.deep",
		"absent": "$.nope",
		"nested": map[string]any{"p": "$.path", "q": "$.nope"},
		"list":   []any{"$.path", "$.nope", "x"},
		"all":    "$",
	}
	got := MapArguments(tmpl, args)
	want := map[string]any{
		"file": "/a", "mode": "r", "deep": 1.0,
		"nested": map[string]any{"p": "/a"},
		"list":   []any{"/a", "x"},
		"all":    args,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if !reflect.DeepEqual(MapArguments(nil, args), args) {
		t.Fatal("nil template must pass through")
	}
}

func TestPick(t *testing.T) {
	res := map[string]any{"a": map[string]any{"b": "x", "c": map[string]any{"d": 1.0}}}
	if v, err := Pick(res, "a.b"); err != nil || !reflect.DeepEqual(v, map[string]any{"value": "x"}) {
		t.Fatalf("scalar = %v, %v", v, err)
	}
	if v, err := Pick(res, "a.c"); err != nil || !reflect.DeepEqual(v, map[string]any{"d": 1.0}) {
		t.Fatalf("object = %v, %v", v, err)
	}
	if v, err := Pick(res, ""); err != nil || !reflect.DeepEqual(v, res) {
		t.Fatalf("all = %v, %v", v, err)
	}
	if _, err := Pick(res, "a.z"); err == nil {
		t.Fatal("missing path accepted")
	}
}

func TestValidate(t *testing.T) {
	def := Def{Name: "document-repository", Tools: []Tool{{Name: "read"}, {Name: "list"}}}
	if err := def.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Def{{Name: "Bad"}, {Name: "x", Tools: []Tool{{Name: "a"}, {Name: "a"}}}, {Name: "x", Tools: []Tool{{Name: "A b"}}}} {
		if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v accepted: %v", bad, err)
		}
	}
	ok := Adapter{MCP: def.Name, Connector: "localfs", Tools: []ToolMapping{{Tool: "read", Operation: "read_file"}}}
	if err := ok.Validate(def); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Adapter{
		{MCP: def.Name, Connector: "localfs", Tools: []ToolMapping{{Tool: "nope", Operation: "x"}}},
		{MCP: def.Name, Connector: "localfs", Tools: []ToolMapping{{Tool: "read", Operation: "x"}, {Tool: "read", Operation: "y"}}},
		{MCP: def.Name, Connector: "localfs", Tools: []ToolMapping{{Tool: "read"}}},
		{MCP: def.Name, Connector: "Bad Name"},
	} {
		if err := bad.Validate(def); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v accepted: %v", bad, err)
		}
	}
	if m, tool, err := SplitTool("document-repository/read"); err != nil || m != "document-repository" || tool != "read" {
		t.Fatalf("split = %q %q %v", m, tool, err)
	}
	if _, _, err := SplitTool("read"); err == nil {
		t.Fatal("unqualified tool accepted")
	}
}
