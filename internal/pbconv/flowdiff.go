package pbconv

import (
	"encoding/json"

	"google.golang.org/protobuf/types/known/structpb"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/pkg/domain"
)

// value is a JSON value as a protobuf Value (nil: absent).
func value(v any) *structpb.Value {
	if v == nil {
		return nil
	}
	if out, err := structpb.NewValue(v); err == nil {
		return out
	}
	var norm any // typed slices, custom types...
	b, _ := json.Marshal(v)
	_ = json.Unmarshal(b, &norm)
	out, _ := structpb.NewValue(norm)
	return out
}

func fromValue(v *structpb.Value) any {
	if v == nil {
		return nil
	}
	return v.AsInterface()
}

func impactSideToPB(s *domain.ImpactSide) *graphv1.ImpactSide {
	if s == nil {
		return nil
	}
	return &graphv1.ImpactSide{Impact: string(s.Impact), Flow: s.Flow, Intent: string(s.Intent), Review: string(s.Review), Drafted: s.Drafted,
		State: s.State, Owner: string(s.Owner)}
}

func impactSideFromPB(s *graphv1.ImpactSide) *domain.ImpactSide {
	if s == nil {
		return nil
	}
	return &domain.ImpactSide{Impact: domain.ChangeImpactID(s.Impact), Flow: s.Flow, Intent: domain.NodeIntent(s.Intent), Review: domain.NodeReview(s.Review),
		Drafted: s.Drafted, State: s.State, Owner: domain.NodeID(s.Owner)}
}

// FlowDiffToPB converts the comparison of two flows of a change (ADR 0083).
func FlowDiffToPB(d domain.FlowDiff) *graphv1.DiffFlowsResponse {
	out := &graphv1.DiffFlowsResponse{Left: d.Left, Right: d.Right, Level: d.Level, Identical: int32(d.Identical)}
	for _, i := range d.Impacts {
		p := &graphv1.ImpactDiff{Node: string(i.Node), Key: i.Key, Type: i.Type, Category: i.Category, Left: impactSideToPB(i.Left), Right: impactSideToPB(i.Right)}
		for _, c := range i.Changes {
			p.Changes = append(p.Changes, &graphv1.FieldChange{Kind: c.Kind, Name: c.Name, Op: c.Op, Target: c.Target, Old: value(c.Old), New: value(c.New)})
		}
		out.Impacts = append(out.Impacts, p)
	}
	return out
}

// FlowDiffFromPB is the inverse of FlowDiffToPB.
func FlowDiffFromPB(change domain.ChangeID, r *graphv1.DiffFlowsResponse) domain.FlowDiff {
	out := domain.FlowDiff{Change: change, Left: r.Left, Right: r.Right, Level: r.Level, Identical: int(r.Identical), Impacts: []domain.ImpactDiff{}}
	for _, i := range r.Impacts {
		d := domain.ImpactDiff{Node: domain.NodeID(i.Node), Key: i.Key, Type: i.Type, Category: i.Category, Left: impactSideFromPB(i.Left), Right: impactSideFromPB(i.Right)}
		for _, c := range i.Changes {
			d.Changes = append(d.Changes, domain.FieldChange{Kind: c.Kind, Name: c.Name, Op: c.Op, Target: c.Target, Old: fromValue(c.Old), New: fromValue(c.New)})
		}
		out.Impacts = append(out.Impacts, d)
	}
	return out
}
