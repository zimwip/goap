package mcp

import (
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/dsl"
)

var docs = Def{Name: "document-repository", Tools: []Tool{
	{Name: "list", InputSchema: map[string]any{"properties": map[string]any{"path": map[string]any{}}}},
	{Name: "read", InputSchema: map[string]any{"properties": map[string]any{"path": map[string]any{}}, "required": []any{"path"}}},
	{Name: "search", InputSchema: map[string]any{"properties": map[string]any{"query": map[string]any{}}}},
}}

func TestValidate(t *testing.T) {
	if err := docs.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Def{{Name: "Bad"}, {Name: "x", Tools: []Tool{{Name: "a"}, {Name: "a"}}}, {Name: "x", Tools: []Tool{{Name: "A b"}}}} {
		if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v accepted: %v", bad, err)
		}
	}
	ok := Adapter{MCP: docs.Name, Domain: "platform", Algorithm: "localfs-docs"}
	if err := ok.Validate(docs); err != nil {
		t.Fatal(err)
	}
	other := ok
	other.MCP = "ticketing"
	if err := other.Validate(docs); !errors.Is(err, ErrInvalid) {
		t.Fatalf("adapter of another MCP = %v", err)
	}
	if m, tool, err := SplitTool("document-repository/read"); err != nil || m != "document-repository" || tool != "read" {
		t.Fatalf("split = %q %q %v", m, tool, err)
	}
	if _, _, err := SplitTool("read"); err == nil {
		t.Fatal("unqualified tool accepted")
	}
}

func TestCheckArgs(t *testing.T) {
	read, _ := docs.Tool("read")
	if err := read.CheckArgs(map[string]any{"path": "a"}); err != nil {
		t.Fatal(err)
	}
	if err := read.CheckArgs(map[string]any{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing argument = %v", err)
	}
}

func TestNodeProps(t *testing.T) {
	back, err := DefFromProps(docs.Props())
	if err != nil || back.Name != docs.Name || len(back.Tools) != 3 {
		t.Fatalf("def round trip = %+v, %v", back, err)
	}
	a := Adapter{Unit: "ORG-A", MCP: "docs", Domain: "platform", Version: "1.0.0", Algorithm: "x", Params: map[string]any{"root": "/r"}}
	props := a.Props()
	if _, has := props["unit"]; has {
		t.Fatal("the unit is carried by the owner link, not by the properties")
	}
	got, err := AdapterFromProps("ORG-A", props)
	if err != nil || got.Unit != "ORG-A" || got.Algorithm != "x" || got.Params["root"] != "/r" || got.Version != "1.0.0" {
		t.Fatalf("adapter round trip = %+v, %v", got, err)
	}
}

func TestAdapterTemplateFollowsTheMCPAndTheConnector(t *testing.T) {
	ops := []Operation{
		{Name: "list_dir", InputSchema: map[string]any{"properties": map[string]any{"path": map[string]any{}}}},
		{Name: "read_file", Description: "Read a text file", InputSchema: map[string]any{"properties": map[string]any{"path": map[string]any{}}}},
	}
	code := AdapterTemplate(docs, "localfs", ops)
	for _, want := range []string{
		`case "list":`, `ctx.call("list_dir", { path: ctx.args().path })`,
		`case "read":`, `ctx.call("read_file", { path: ctx.args().path })`,
		`case "search":`, `not implemented: search`, "read_file(path) - Read a text file",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("template misses %q:\n%s", want, code)
		}
	}
	if err := (algo.Algorithm{Name: "a", Type: algo.UsageAdapter, Language: algo.JavaScript, Code: code, MCP: "document-repository", Connector: "localfs"}).Issues(); len(err) != 0 {
		t.Fatal(err)
	}
	if err := dsl.CheckAlgorithmCode(algo.JavaScript, code); err != nil {
		t.Fatalf("the generated code does not compile: %v\n%s", err, code)
	}
}

func TestAdapterParamsFollowTheConnector(t *testing.T) {
	schema := map[string]any{"required": []any{"root"}, "properties": map[string]any{
		"root":    map[string]any{"type": "string", "description": "directory"},
		"retries": map[string]any{"type": "integer"},
	}}
	ps := AdapterParams(schema, []string{"token"})
	if len(ps) != 3 || ps[0].Name != "retries" || ps[0].Type != algo.ParamNumber || ps[1].Name != "root" || !ps[1].Required || ps[2].Type != algo.ParamSecret {
		t.Fatalf("params = %+v", ps)
	}
}
