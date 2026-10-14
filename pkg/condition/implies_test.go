package condition

import (
	"slices"
	"testing"
)

func TestImpliedConditions(t *testing.T) {
	defs, ok := Library("assessments")
	if !ok {
		t.Fatal("no assessments library")
	}
	defs = append(defs,
		Definition{Name: "same_rated", Expr: `assessments.exists(x, x.current && "description" == x.key)`},
		Definition{Name: "both", Expr: `change.title != "" && assessments.exists(a, a.key == "description" && a.current && a.answered)`},
		Definition{Name: "other", Expr: `assessments.exists(a, a.key == "title" && a.current)`},
	)
	s, err := Compile(defs)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{
		"description_accepted": {"description_rated", "same_rated"},
		"description_reviewed": {"description_rated", "same_rated"},
		"both":                 {"description_rated", "description_reviewed", "same_rated"},
		"description_rated":    nil, // same_rated is the same condition, not a prerequisite
		"other":                nil,
	} {
		if got := s.Implied(name); !slices.Equal(got, want) {
			t.Errorf("%s implies %v, want %v", name, got, want)
		}
	}
	if !Implies(`a && b`, `b`) || Implies(`a || b`, `b`) || Implies(`x.exists(i, i.a)`, `y.exists(i, i.a)`) {
		t.Fatal("conjunctions only")
	}
}
