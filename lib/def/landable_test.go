package def

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// A state is committable unless flagged notCommittable (ADR 0078); the issues name what could never commit.
func TestLifecycleCommittable(t *testing.T) {
	lc := &domain.Lifecycle{Name: "l", Initial: "a", States: []domain.LifecycleState{{Name: "a", NotCommittable: true}, {Name: "b"}, {Name: "c", NotCommittable: true, Final: true}},
		Transitions: []domain.Transition{{Name: "t", From: "a", To: "b"}, {Name: "u", From: "b", To: "c"}}}
	if lc.Committable("a") || !lc.Committable("b") || !lc.Committable("") || !lc.Committable("unknown") {
		t.Fatal("only a flagged state is not committable; no state or an unknown one is committable")
	}
	var none *domain.Lifecycle
	if !none.Committable("x") {
		t.Fatal("no lifecycle: always committable")
	}
	is := strings.Join(lc.Issues(), "; ")
	if !strings.Contains(is, "state c is final and not committable") || strings.Contains(is, "state a") {
		t.Fatalf("issues: %s", is)
	}
	lc = &domain.Lifecycle{Name: "l", Initial: "a", States: []domain.LifecycleState{{Name: "a", NotCommittable: true}, {Name: "b", NotCommittable: true}, {Name: "c"}},
		Transitions: []domain.Transition{{Name: "t", From: "a", To: "b"}}}
	is = strings.Join(lc.Issues(), "; ")
	if !strings.Contains(is, "state a is not committable and cannot reach a committable state") || !strings.Contains(is, "state b is not committable and cannot reach") {
		t.Fatalf("issues: %s", is)
	}
	lc = &domain.Lifecycle{Name: "l", Initial: "a", States: []domain.LifecycleState{{Name: "a", NotCommittable: true}}}
	if is := strings.Join(lc.Issues(), "; "); !strings.Contains(is, "at least one committable state required") {
		t.Fatalf("issues: %s", is)
	}
}
