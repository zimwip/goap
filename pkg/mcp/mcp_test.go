package mcp

import (
	"context"
	"errors"
	"testing"
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
}

func TestCallContextKeepsWhatItDoesNotSet(t *testing.T) {
	ctx := WithCall(context.Background(), CallContext{Change: "C1", Process: "P1"})
	ctx = WithCall(ctx, CallContext{Unit: "U"})
	if got := CallFrom(ctx); got != (CallContext{Unit: "U", Change: "C1", Process: "P1"}) {
		t.Fatalf("call context = %+v", got)
	}
}

func TestScopes(t *testing.T) {
	for _, s := range []string{"", ScopeAction, ScopeAgent, ScopeBoth} {
		if err := (Def{Name: "x", Scope: s}).Validate(); err != nil {
			t.Fatalf("%q: %v", s, err)
		}
	}
	if err := (Def{Name: "x", Scope: "step"}).Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown scope accepted: %v", err)
	}
	if !ForActions("") || !ForAgents("") || ForActions(ScopeAgent) || ForAgents(ScopeAction) {
		t.Fatal("scope rules")
	}
}
