package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCheckArgsAgainstTheInputSchema(t *testing.T) {
	var schema map[string]any
	_ = json.Unmarshal([]byte(`{"type":"object","required":["path"],"additionalProperties":false,"properties":{
		"path":{"type":"string"},"limit":{"type":"integer"},"ratio":{"type":"number"},"deep":{"type":"boolean"},
		"mode":{"type":"string","enum":["a","b"]},"tags":{"type":"array"},"opts":{"type":"object"},"any":{"type":["string","null"]}}}`), &schema)
	tool := Tool{Name: "fs/read", InputSchema: schema}
	for _, tc := range []struct {
		args map[string]any
		want string // "" = accepted
	}{
		{map[string]any{"path": "x"}, ""},
		{map[string]any{"path": "x", "limit": float64(3), "ratio": 0.5, "deep": true, "mode": "b", "tags": []any{}, "opts": map[string]any{}, "any": 2.0}, ""},
		{map[string]any{"path": "x", "limit": 3}, ""},   // a Go int from an in-process caller
		{map[string]any{"path": "x", "limit": nil}, ""}, // null: not checked
		{map[string]any{}, `missing argument "path"`},
		{map[string]any{"path": 1.0}, `argument "path" must be of type string`},
		{map[string]any{"path": "x", "limit": 2.5}, `"limit" must be of type integer`},
		{map[string]any{"path": "x", "mode": "c"}, `"mode" must be one of [a b]`},
		{map[string]any{"path": "x", "tags": "t"}, `"tags" must be of type array`},
		{map[string]any{"path": "x", "colour": "red"}, `unknown argument "colour" (expected: any, deep, limit, mode, opts, path, ratio, tags)`},
	} {
		err := tool.CheckArgs(tc.args)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want) || !errors.Is(err, ErrInvalid)) {
			t.Errorf("%v: err %v, want %q", tc.args, err, tc.want)
		}
	}
	// an open schema accepts what it does not declare
	delete(schema, "additionalProperties")
	if err := tool.CheckArgs(map[string]any{"path": "x", "colour": "red"}); err != nil {
		t.Fatal(err)
	}
}
