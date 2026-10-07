package pbconv

import (
	"reflect"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestFlowDiffRoundTrip(t *testing.T) {
	in := domain.FlowDiff{Change: "c1", Left: "main", Right: "f1", Level: "written", Identical: 2, Impacts: []domain.ImpactDiff{{
		Node: "n1", Key: "REQ-1", Type: "Requirement", Category: domain.DiffModified,
		Left:  &domain.ImpactSide{Impact: "i1", Intent: domain.IntentModified, Review: domain.ReviewAccepted, Drafted: true, State: "draft"},
		Right: &domain.ImpactSide{Impact: "i2", Flow: "f1", Intent: domain.IntentModified, Review: domain.ReviewProposed, Drafted: true, State: "approved", Owner: "ORG"},
		Changes: []domain.FieldChange{
			{Kind: domain.FieldProperty, Name: "title", Op: domain.OpChanged, Old: "a", New: "b"},
			{Kind: domain.FieldProperty, Name: "n", Op: domain.OpAdded, New: float64(3)},
			{Kind: domain.FieldProperty, Name: "tags", Op: domain.OpRemoved, Old: []any{"x"}},
			{Kind: domain.FieldLink, Name: "satisfies", Op: domain.OpAdded, Target: "NEED-1"},
		}}}}
	out := FlowDiffFromPB("c1", FlowDiffToPB(in))
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip:\n in %+v\nout %+v", in, out)
	}
}
