package engine

import (
	"context"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/engine/condition"
	"github.com/zimwip/goap/engine/methodology"
)

// AssessmentStates is how the assessment of each subject of a change reads now (ADR 0116): the states its
// methodology declares (assessments), else the platform's defaults, evaluated against the change. Nothing is stored.
func (e *Engine) AssessmentStates(ctx context.Context, changeID domain.ChangeID) ([]condition.AssessmentState, error) {
	bb, err := e.blackboard(ctx, changeID, "")
	if err != nil {
		return nil, err
	}
	var m *methodology.Compiled
	res := condition.Result{}
	if bb.Change.Methodology != "" {
		m, err = e.Methodologies.Methodology(ctx, bb.Change.MethodologyRef()) // the version the change follows (ADR 0113)
		if err != nil {
			return nil, err
		}
		bb.Supertypes = e.supertypesOf(m)
		res = m.Conditions.Evaluate(bb)
	}
	return m.AssessmentStates(bb, res), nil
}
