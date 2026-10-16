package condition

import (
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/journal"
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

// Without vetos or objectives nothing is asked of the lifecycle, and a met objective needs no derogation.
func TestGateObjectiveMet(t *testing.T) {
	bb := domain.Blackboard{}
	res, err := CheckGate(domain.Transition{Name: "t", Objectives: []domain.Criterion{{Name: "docs", Expr: `world["docs"]`}}}, bb, map[string]bool{"docs": true}, "")
	if err != nil || !res.Passed() || res.WithReserve() {
		t.Fatalf("%+v %v", res, err)
	}
}
