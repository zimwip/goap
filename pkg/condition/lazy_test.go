package condition

import (
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// The variables of the decision and risk concepts are computed when an expression reads them (ADR 0064).
func TestActivationIsLazy(t *testing.T) {
	bb := domain.Blackboard{DecisionPoints: []domain.DecisionPoint{{ID: "p", Status: domain.PointBlocked}}}
	act := Activation(bb)
	for _, name := range []string{"options", "decisionPoints", "questions", "risks", "actions"} {
		if _, ok := act[name].(func() any); !ok {
			t.Errorf("%s is computed eagerly", name)
		}
	}
	if pts, _ := Resolve(act["decisionPoints"]).([]any); len(pts) != 1 {
		t.Fatalf("decisionPoints %v", pts)
	}

	// an expression over one variable gets it, and a set that reads none of them never computes them
	set, err := Compile([]Definition{{Name: "pending", Expr: `decisionPoints.exists(d, d.status != "decided")`}, {Name: "plain", Expr: `items.size() == 0`}})
	if err != nil {
		t.Fatal(err)
	}
	if st := set.Evaluate(bb); !st.State["pending"] || !st.State["plain"] || len(st.Errors) > 0 {
		t.Fatalf("%v %v", st.State, st.Errors)
	}
}

func TestLibraries(t *testing.T) {
	for _, n := range LibraryNames {
		defs, ok := Library(n)
		if !ok || len(defs) == 0 {
			t.Fatalf("library %s", n)
		}
		if _, err := Compile(defs); err != nil {
			t.Fatalf("library %s: %v", n, err)
		}
	}
	if _, ok := Library("nope"); ok {
		t.Fatal("unknown library")
	}
}
