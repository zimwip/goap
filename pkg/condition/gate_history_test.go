package condition

import (
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/journal"
)

// The guards of the lifecycle of a change read its history as the conditions do (ADR 0109).
func TestGuardReadsTheHistory(t *testing.T) {
	expr := `"intake/collect" in history.last && history.last["intake/collect"].current`
	bb := domain.Blackboard{}
	if ok, err := CheckGuard(expr, bb, nil, "close", ""); err != nil || ok {
		t.Fatalf("nothing ran: %v %v", ok, err)
	}
	h := journal.History{Steps: []journal.StepRun{{Path: "intake/collect", Outcome: journal.OutcomeSucceeded, Current: true}}}
	bb = bb.WithFacet(domain.FacetHistory, h)
	if ok, err := CheckGuard(expr, bb, nil, "close", ""); err != nil || !ok {
		t.Fatalf("the step ran and stands: %v %v", ok, err)
	}
}
