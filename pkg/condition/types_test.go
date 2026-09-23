package condition

import (
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestTypesExposeSupertypes(t *testing.T) {
	ref := domain.NodeRef{ID: "n1", Version: 1}
	bb := domain.Blackboard{
		Change:     domain.ChangeSet{Items: []domain.ChangeItem{{ID: "i1", Kind: domain.KindImpact, Target: &ref}}},
		Nodes:      map[domain.NodeRef]domain.NodeView{ref: {Node: domain.Node{ID: "n1", Version: 1, Key: "SEC-1", Type: "SecurityRequirement"}}},
		Supertypes: map[string][]string{"SecurityRequirement": {"Requirement"}},
	}
	set, err := Compile([]Definition{{Name: "req", Expr: `impacts.exists(i, "Requirement" in i.target.types)`}})
	if err != nil {
		t.Fatal(err)
	}
	if res := set.Evaluate(bb); !res.State["req"] {
		t.Fatalf("subtype not matched: %+v", res)
	}
}
