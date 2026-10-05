package def

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// A state is landable unless flagged notLandable (ADR 0078); the issues name what could never land.
func TestLifecycleLandable(t *testing.T) {
	lc := &domain.Lifecycle{Name: "l", Initial: "a", States: []domain.LifecycleState{{Name: "a", NotLandable: true}, {Name: "b"}, {Name: "c", NotLandable: true, Final: true}},
		Transitions: []domain.Transition{{Name: "t", From: "a", To: "b"}, {Name: "u", From: "b", To: "c"}}}
	if lc.Landable("a") || !lc.Landable("b") || !lc.Landable("") || !lc.Landable("unknown") {
		t.Fatal("only a flagged state is not landable; no state or an unknown one is landable")
	}
	var none *domain.Lifecycle
	if !none.Landable("x") {
		t.Fatal("no lifecycle: always landable")
	}
	is := strings.Join(lc.Issues(), "; ")
	if !strings.Contains(is, "state c is final and not landable") || strings.Contains(is, "state a") {
		t.Fatalf("issues: %s", is)
	}
	lc = &domain.Lifecycle{Name: "l", Initial: "a", States: []domain.LifecycleState{{Name: "a", NotLandable: true}, {Name: "b", NotLandable: true}, {Name: "c"}},
		Transitions: []domain.Transition{{Name: "t", From: "a", To: "b"}}}
	is = strings.Join(lc.Issues(), "; ")
	if !strings.Contains(is, "state a is not landable and cannot reach a landable state") || !strings.Contains(is, "state b is not landable and cannot reach") {
		t.Fatalf("issues: %s", is)
	}
	lc = &domain.Lifecycle{Name: "l", Initial: "a", States: []domain.LifecycleState{{Name: "a", NotLandable: true}}}
	if is := strings.Join(lc.Issues(), "; "); !strings.Contains(is, "at least one landable state required") {
		t.Fatalf("issues: %s", is)
	}
}
