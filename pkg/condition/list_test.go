package condition

import (
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestListEvaluatesToPlainValues(t *testing.T) {
	if err := CheckList(`1 + 1`); err == nil {
		t.Fatal("a number is not a list")
	}
	l, err := CompileList(`vars.components.filter(c, c.build)`)
	if err != nil {
		t.Fatal(err)
	}
	bb := domain.Blackboard{Vars: map[string]any{"components": []any{
		map[string]any{"key": "api", "lang": "go", "build": true},
		map[string]any{"key": "doc", "lang": "md", "build": false},
	}}}
	got, err := l.Eval(bb)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if m, _ := got[0].(map[string]any); m["key"] != "api" || m["lang"] != "go" {
		t.Fatalf("item %#v", got[0])
	}
}
