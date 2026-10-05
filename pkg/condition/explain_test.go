package condition

import (
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestExplain(t *testing.T) {
	set, err := Compile([]Definition{{Name: "ready", Expr: `change.status == "open" && size(vars.todo) > 0`}, {Name: "bad", Expr: `vars.x.y == 1`}})
	if err != nil {
		t.Fatal(err)
	}
	bb := domain.Blackboard{Vars: map[string]any{"todo": []any{"a"}}}
	bb.Change.Status = "open"
	ex, ok := set.Explain("ready", bb)
	if !ok || !ex.Value || ex.Error != "" {
		t.Fatalf("%+v", ex)
	}
	if ex.Root.Value != "true" || len(ex.Root.Terms) != 2 || ex.Root.Terms[0].Value != "true" {
		t.Fatalf("root %+v", ex.Root)
	}
	if ex.Inputs["change"] == "" || ex.Inputs["vars"] != `{"todo":["a"]}` || len(ex.Inputs) != 2 {
		t.Fatalf("inputs %v", ex.Inputs)
	}
	if ex, _ = set.Explain("bad", bb); ex.Error == "" {
		t.Fatalf("an error is expected: %+v", ex)
	}
	if _, ok := set.Explain("nope", bb); ok {
		t.Fatal("unknown condition")
	}
	t.Logf("%+v", ex)
}
