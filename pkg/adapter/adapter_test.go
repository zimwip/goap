package adapter

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/mcp"
)

var docs = mcp.Def{Name: "document-repository", Tools: []mcp.Tool{
	{Name: "list", InputSchema: map[string]any{"properties": map[string]any{"path": map[string]any{}}}},
	{Name: "read", InputSchema: map[string]any{"properties": map[string]any{"path": map[string]any{}}, "required": []any{"path"}}},
	{Name: "search", InputSchema: map[string]any{"properties": map[string]any{"query": map[string]any{}}}},
}}

func toolNames(d mcp.Def) []string {
	var out []string
	for _, t := range d.Tools {
		out = append(out, t.Name)
	}
	return out
}

func TestInstanceValidate(t *testing.T) {
	ok := Instance{MCP: docs.Name, Adapter: "localfs-docs"}
	if err := ok.Validate(docs); err != nil {
		t.Fatal(err)
	}
	other := ok
	other.MCP = "ticketing"
	if err := other.Validate(docs); !errors.Is(err, mcp.ErrInvalid) {
		t.Fatalf("adapter of another MCP = %v", err)
	}
}

func TestNodeProps(t *testing.T) {
	a := Instance{Unit: "ORG-A", MCP: "docs", Adapter: "x", Params: map[string]any{"root": "/r"}}
	props := a.Props()
	if _, has := props["unit"]; has {
		t.Fatal("the unit is carried by the owner of the node, not by the properties")
	}
	got, err := FromProps("ORG-A", props)
	if err != nil || got.Unit != "ORG-A" || got.Adapter != "x" || got.Params["root"] != "/r" {
		t.Fatalf("adapter round trip = %+v, %v", got, err)
	}
}

func TestRestrictionsAddUpAlongTheChain(t *testing.T) {
	def := mcp.Def{Name: "docs", Tools: []mcp.Tool{{Name: "list", ReadOnly: true}, {Name: "read", ReadOnly: true}, {Name: "write"}, {Name: "delete"}}}
	cases := []struct {
		name  string
		chain []Instance // nearest first
		want  []string
	}{
		{"no restriction", []Instance{{Unit: "U", Adapter: "a"}}, []string{"list", "read", "write", "delete"}},
		{"deny", []Instance{{Unit: "U", Deny: []string{"delete"}}}, []string{"list", "read", "write"}},
		{"read-only", []Instance{{Unit: "U", ReadOnly: true}}, []string{"list", "read"}},
		{"allow-lists intersect", []Instance{{Unit: "U", Tools: []string{"read", "write"}}, {Unit: "P", Tools: []string{"list", "read"}}}, []string{"read"}},
		{"a child cannot widen", []Instance{{Unit: "U", Tools: []string{"write"}}, {Unit: "P", ReadOnly: true}}, nil},
		{"disabled by an ancestor", []Instance{{Unit: "U", Adapter: "a"}, {Unit: "P", Disabled: true}}, nil},
		{"deny and allow", []Instance{{Unit: "U", Deny: []string{"read"}}, {Unit: "P", Tools: []string{"list", "read"}}}, []string{"list"}},
	}
	for _, c := range cases {
		var r Restriction
		for _, a := range c.chain {
			r.Add(a)
		}
		if got := toolNames(r.Apply(def)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: tools = %v, want %v", c.name, got, c.want)
		}
	}
	var r Restriction
	r.Add(Instance{Unit: "U", Adapter: "a"})
	r.Add(Instance{Unit: "P", Deny: []string{"delete"}})
	if !reflect.DeepEqual(r.By, []string{"P"}) {
		t.Fatalf("restricted by %v, want the restricting units only", r.By)
	}
}

func TestAdapterMayOnlyRestrict(t *testing.T) {
	def := mcp.Def{Name: "docs", Tools: []mcp.Tool{{Name: "read"}}}
	for _, a := range []Instance{
		{MCP: "docs", Disabled: true},
		{MCP: "docs", Tools: []string{"read"}},
		{MCP: "docs", Adapter: "impl", ReadOnly: true},
	} {
		if err := a.Validate(def); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
	for _, a := range []Instance{
		{MCP: "docs"},                             // neither implements nor restricts
		{MCP: "docs", Deny: []string{"write"}},    // unknown tool
		{MCP: "docs", Adapter: "Bad", Tools: nil}, // bad name
		{MCP: "other", Adapter: "impl"},           // another MCP
		{MCP: "docs", Tools: []string{"missing"}}, // unknown tool
		{MCP: "docs", Adapter: "impl", Deny: []string{""}},
	} {
		if err := a.Validate(def); !errors.Is(err, mcp.ErrInvalid) {
			t.Errorf("%+v accepted: %v", a, err)
		}
	}
}

func TestTemplateFollowsTheMCPAndTheConnector(t *testing.T) {
	ops := []Operation{
		{Name: "list_dir", InputSchema: map[string]any{"properties": map[string]any{"path": map[string]any{}}}},
		{Name: "read_file", Description: "Read a text file", InputSchema: map[string]any{"properties": map[string]any{"path": map[string]any{}}}},
	}
	code := Template(docs, "localfs", ops)
	for _, want := range []string{
		`case "list":`, `ctx.call("list_dir", { path: ctx.args().path })`,
		`case "read":`, `ctx.call("read_file", { path: ctx.args().path })`,
		`case "search":`, `not implemented: search`, "read_file(path) - Read a text file",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("template misses %q:\n%s", want, code)
		}
	}
	d := Def{Name: "a", MCP: "document-repository", Connector: "localfs", Language: algo.JavaScript, Code: code}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := dsl.CheckAlgorithmCode(algo.JavaScript, code); err != nil {
		t.Fatalf("the generated code does not compile: %v\n%s", err, code)
	}
}

func TestParamsFollowTheConnector(t *testing.T) {
	schema := map[string]any{"required": []any{"root"}, "properties": map[string]any{
		"root":    map[string]any{"type": "string", "description": "directory"},
		"retries": map[string]any{"type": "integer"},
	}}
	ps := Params(schema, []string{"token"})
	if len(ps) != 3 || ps[0].Name != "retries" || ps[0].Type != algo.ParamNumber || ps[1].Name != "root" || !ps[1].Required || ps[2].Type != ParamSecret {
		t.Fatalf("params = %+v", ps)
	}
}

func TestDef(t *testing.T) {
	d := Def{Name: "localfs-docs", MCP: "docs", Connector: "localfs", Language: algo.JavaScript, Code: "return 1",
		Params: []algo.Param{{Name: "root", Type: algo.ParamString, Required: true}, {Name: "token", Type: ParamSecret}}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	back, err := DefFromProps(d.Props())
	if err != nil || back.Name != d.Name || back.Connector != "localfs" || len(back.Params) != 2 || back.Params[0].Name != "root" || !back.Params[0].Required {
		t.Fatalf("round trip = %+v, %v", back, err)
	}
	for name, bad := range map[string]Def{
		"name":      {Name: "Bad", MCP: "docs", Connector: "c", Language: algo.JavaScript, Code: "1"},
		"no mcp":    {Name: "a", Connector: "c", Language: algo.JavaScript, Code: "1"},
		"connector": {Name: "a", MCP: "docs", Language: algo.JavaScript, Code: "1"},
		"language":  {Name: "a", MCP: "docs", Connector: "c", Language: "lua", Code: "1"},
		"code":      {Name: "a", MCP: "docs", Connector: "c", Language: algo.JavaScript},
		"param":     {Name: "a", MCP: "docs", Connector: "c", Language: algo.JavaScript, Code: "1", Params: []algo.Param{{Name: "x", Type: "vault"}}},
	} {
		if err := bad.Validate(); !errors.Is(err, mcp.ErrInvalid) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}

func TestSecretParamsAreNotReadableByTheScript(t *testing.T) {
	d := Def{Name: "a", MCP: "docs", Connector: "fs", Language: algo.JavaScript, Code: "return {seen: String(ctx.param(\"token\"))}",
		Params: []algo.Param{{Name: "root", Type: algo.ParamString, Required: true}, {Name: "token", Type: ParamSecret, Required: true}}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	config, secrets, issues := d.Resolve(map[string]any{"root": "/r", "token": "env:T"})
	if len(issues) != 0 {
		t.Fatal(issues)
	}
	if config["root"] != "/r" || config["token"] != nil || secrets["token"] != "env:T" || len(secrets) != 1 {
		t.Fatalf("resolve = %v %v", config, secrets)
	}
	// the hub binds only the configuration
	b := algo.Bound{Instance: "test", Algorithm: d.Name, Language: d.Language, Code: d.Code, Params: config}
	out, err := dsl.RunAdapter(context.Background(), b, dsl.AdapterInput{})
	if err != nil || out.Result["seen"] != "null" {
		t.Fatalf("script sees the secret: %+v, %v", out, err)
	}
}

func TestResolveChecksTheValues(t *testing.T) {
	d := Def{Name: "a", MCP: "docs", Connector: "fs", Language: algo.JavaScript, Code: "1",
		Params: []algo.Param{{Name: "root", Type: algo.ParamString, Required: true}, {Name: "token", Type: ParamSecret, Required: true}, {Name: "other", Type: ParamSecret}}}
	for name, values := range map[string]map[string]any{
		"secret missing":    {"root": "/r"},
		"secret not a ref":  {"root": "/r", "token": 3},
		"empty secret":      {"root": "/r", "token": ""},
		"param missing":     {"token": "env:T"},
		"undeclared":        {"root": "/r", "token": "env:T", "extra": 1},
		"wrong param value": {"root": 1, "token": "env:T"},
	} {
		if _, _, issues := d.Resolve(values); len(issues) == 0 {
			t.Errorf("%s accepted", name)
		}
	}
	if _, secrets, issues := d.Resolve(map[string]any{"root": "/r", "token": "env:T"}); len(issues) != 0 || len(secrets) != 1 {
		t.Fatalf("optional secret: %v %v", secrets, issues)
	}
}
