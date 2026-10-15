package domainpb

import (
	"reflect"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
)

// The vetos and objectives of a gate survive the PB round trip (ADR 0075 §3).
func TestGateCriteriaRoundTripThroughPB(t *testing.T) {
	ls := []domain.Lifecycle{{Name: "l", Initial: "a", States: []domain.LifecycleState{{Name: "a"}, {Name: "b"}}, Transitions: []domain.Transition{{Name: "go", From: "a", To: "b",
		Vetos:      []domain.Criterion{{Name: "safe", Expr: `world["safe"]`, Description: "no blocker"}},
		Objectives: []domain.Criterion{{Name: "docs", Expr: `world["docs"]`}}}}}}
	if back := lifecyclesFromPB(LifecyclesToPB(ls)); !reflect.DeepEqual(back, ls) {
		t.Fatalf("lifecycle changed through PB:\n%+v\n%+v", ls, back)
	}
}

func TestIssuesRoundTripKeepSeverity(t *testing.T) {
	in := def.Issues{{Path: "goal", Message: "w", Severity: def.SeverityWarning}, {Path: "name", Message: "e", Activity: "p/s"}}
	out := IssuesFromPB(IssuesToPB(in))
	if len(out) != 2 || out[0] != in[0] || out[1] != in[1] || !out[0].IsWarning() || out[1].IsWarning() {
		t.Fatalf("%v", out)
	}
}

// The enums of a domain survive the PB round trip.
func TestEnumsRoundTripThroughPB(t *testing.T) {
	d := def.Domain{Name: "alm", Version: "1", Schema: def.Schema{Enums: []def.Enum{{Name: "level", Values: []def.EnumValue{{Value: "low", Label: "Low"}}}}}}
	if back := DomainFromPB(DomainToPB(def.Record{Domain: d})); !reflect.DeepEqual(back.Enums, d.Enums) {
		t.Fatalf("enums: %+v", back.Enums)
	}
}
