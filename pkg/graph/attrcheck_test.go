package graph

import (
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestCheckAttributeValue(t *testing.T) {
	enum := domain.AttributeCheck{Name: "p", Type: "enum", Enum: "prio", Values: []string{"low", "high"}}
	for _, c := range []struct {
		a  domain.AttributeCheck
		v  any
		ok bool
	}{
		{domain.AttributeCheck{Type: "string"}, "x", true},
		{domain.AttributeCheck{Type: "string"}, 3, false},
		{domain.AttributeCheck{Type: "number"}, 3.5, true},
		{domain.AttributeCheck{Type: "number"}, "12", true},
		{domain.AttributeCheck{Type: "number"}, "abc", false},
		{domain.AttributeCheck{Type: "boolean"}, true, true},
		{domain.AttributeCheck{Type: "boolean"}, "true", false},
		{domain.AttributeCheck{Type: "date"}, "2026-10-04", true},
		{domain.AttributeCheck{Type: "date"}, "yesterday", false},
		{enum, "low", true},
		{enum, "mid", false},
		{enum, nil, true},
		{enum, "", true},
		{domain.AttributeCheck{}, []any{1}, true},
		{domain.AttributeCheck{Type: "json"}, map[string]any{"a": 1}, true},
	} {
		if err := checkAttributeValue(c.a, c.v); (err == nil) != c.ok {
			t.Errorf("%+v %v: %v", c.a, c.v, err)
		}
	}
}
