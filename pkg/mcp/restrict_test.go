package mcp_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/zimwip/goap/pkg/mcp"
)

func toolNames(d mcp.Def) []string {
	var out []string
	for _, t := range d.Tools {
		out = append(out, t.Name)
	}
	return out
}

func TestRestrictionsAddUpAlongTheChain(t *testing.T) {
	def := mcp.Def{Name: "docs", Tools: []mcp.Tool{{Name: "list", ReadOnly: true}, {Name: "read", ReadOnly: true}, {Name: "write"}, {Name: "delete"}}}
	cases := []struct {
		name  string
		chain []mcp.Adapter // nearest first
		want  []string
	}{
		{"no restriction", []mcp.Adapter{{Unit: "U", Adapter: "a"}}, []string{"list", "read", "write", "delete"}},
		{"deny", []mcp.Adapter{{Unit: "U", Deny: []string{"delete"}}}, []string{"list", "read", "write"}},
		{"read-only", []mcp.Adapter{{Unit: "U", ReadOnly: true}}, []string{"list", "read"}},
		{"allow-lists intersect", []mcp.Adapter{{Unit: "U", Tools: []string{"read", "write"}}, {Unit: "P", Tools: []string{"list", "read"}}}, []string{"read"}},
		{"a child cannot widen", []mcp.Adapter{{Unit: "U", Tools: []string{"write"}}, {Unit: "P", ReadOnly: true}}, nil},
		{"disabled by an ancestor", []mcp.Adapter{{Unit: "U", Adapter: "a"}, {Unit: "P", Disabled: true}}, nil},
		{"deny and allow", []mcp.Adapter{{Unit: "U", Deny: []string{"read"}}, {Unit: "P", Tools: []string{"list", "read"}}}, []string{"list"}},
	}
	for _, c := range cases {
		var r mcp.Restriction
		for _, a := range c.chain {
			r.Add(a)
		}
		if got := toolNames(r.Apply(def)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: tools = %v, want %v", c.name, got, c.want)
		}
	}
	var r mcp.Restriction
	r.Add(mcp.Adapter{Unit: "U", Adapter: "a"})
	r.Add(mcp.Adapter{Unit: "P", Deny: []string{"delete"}})
	if !reflect.DeepEqual(r.By, []string{"P"}) {
		t.Fatalf("restricted by %v, want the restricting units only", r.By)
	}
}

func TestAdapterMayOnlyRestrict(t *testing.T) {
	def := mcp.Def{Name: "docs", Tools: []mcp.Tool{{Name: "read"}}}
	for _, a := range []mcp.Adapter{
		{MCP: "docs", Disabled: true},
		{MCP: "docs", Tools: []string{"read"}},
		{MCP: "docs", Adapter: "impl", ReadOnly: true},
	} {
		if err := a.Validate(def); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
	for _, a := range []mcp.Adapter{
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

func TestBuiltinDefinitions(t *testing.T) {
	defs := mcp.BuiltinDefs()
	adapters := mcp.BuiltinAdapterDefs()
	if len(defs) != len(mcp.BuiltinNames) || len(adapters) != len(defs) {
		t.Fatalf("%d definitions, %d adapters for %d built-ins", len(defs), len(adapters), len(mcp.BuiltinNames))
	}
	for i, d := range defs {
		if err := d.Validate(); err != nil {
			t.Fatal(err)
		}
		if !mcp.IsBuiltin(d.Name) || adapters[i].MCP != d.Name || adapters[i].Connector != d.Name {
			t.Fatalf("adapter %+v does not implement %s", adapters[i], d.Name)
		}
		if err := adapters[i].Validate(); err != nil {
			t.Fatal(err)
		}
		if err := mcp.BuiltinAdapter("ORG-DEFAULT", d.Name).Validate(d); err != nil {
			t.Fatal(err)
		}
	}
	if mcp.IsBuiltin("document-repository") {
		t.Fatal("document-repository is not built in")
	}
}

func TestCallContextKeepsWhatItDoesNotSet(t *testing.T) {
	ctx := mcp.WithCall(context.Background(), mcp.CallContext{Change: "C1", Process: "P1"})
	ctx = mcp.WithCall(ctx, mcp.CallContext{Unit: "U"})
	if got := mcp.CallFrom(ctx); got != (mcp.CallContext{Unit: "U", Change: "C1", Process: "P1"}) {
		t.Fatalf("call context = %+v", got)
	}
}

func TestScopes(t *testing.T) {
	for _, s := range []string{"", mcp.ScopeAction, mcp.ScopeAgent, mcp.ScopeBoth} {
		if err := (mcp.Def{Name: "x", Scope: s}).Validate(); err != nil {
			t.Fatalf("%q: %v", s, err)
		}
	}
	if err := (mcp.Def{Name: "x", Scope: "step"}).Validate(); !errors.Is(err, mcp.ErrInvalid) {
		t.Fatalf("unknown scope accepted: %v", err)
	}
	if !mcp.ForActions("") || !mcp.ForAgents("") || mcp.ForActions(mcp.ScopeAgent) || mcp.ForAgents(mcp.ScopeAction) {
		t.Fatal("scope rules")
	}
	// starting other agents is the agent level's business only
	for _, d := range mcp.BuiltinDefs() {
		if want := d.Name == mcp.BuiltinScheduler; (d.Scope == mcp.ScopeAgent) != want {
			t.Fatalf("%s has scope %q", d.Name, d.Scope)
		}
	}
}
