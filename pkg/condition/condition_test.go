package condition

import (
	"testing"
)

func TestCompileErrors(t *testing.T) {
	if _, err := Compile([]Definition{{Name: "x", Expr: "size(changeImpacts)"}}); err == nil {
		t.Fatal("non bool expression must be rejected")
	}
	if _, err := Compile([]Definition{{Name: "x", Expr: "unknown_var"}}); err == nil {
		t.Fatal("unknown variable must be rejected")
	}
}
