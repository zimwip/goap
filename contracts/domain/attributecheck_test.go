package domain

import "testing"

func TestAttributeCheckCode(t *testing.T) {
	c := AttributeCheck{Name: "code", Type: "code"}
	for _, ok := range []any{nil, "", "plain text", "---\nlanguage: go\n---\n```go\nfunc F() {}\n```\n"} {
		if err := c.Check(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []any{42, "---\nlanguage: go\n", "---\nnot a pair\n---\nx"} {
		if err := c.Check(bad); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
}
