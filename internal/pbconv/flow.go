package pbconv

import (
	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/pkg/domain"
)

func itemIDsToPB(ids []domain.ItemID) []string {
	var out []string
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

func itemIDsFromPB(ids []string) []domain.ItemID {
	var out []domain.ItemID
	for _, id := range ids {
		out = append(out, domain.ItemID(id))
	}
	return out
}

func FlowEventToPB(e domain.FlowEvent) *graphv1.FlowEvent {
	return &graphv1.FlowEvent{Op: e.Op, Flow: e.Flow, Parent: e.Parent, ForkAfter: string(e.ForkAfter),
		Stale: itemIDsToPB(e.Stale), By: e.By, StaleRuns: e.StaleRuns, Origin: Struct(e.Origin), Option: optionToPB(e.Option), Comment: e.Comment}
}

func optionToPB(o *domain.OptionSpec) *graphv1.Option {
	if o == nil {
		return nil
	}
	return &graphv1.Option{Name: o.Name, Hypothesis: o.Hypothesis}
}

func optionFromPB(o *graphv1.Option) *domain.OptionSpec {
	if o == nil {
		return nil
	}
	return &domain.OptionSpec{Name: o.Name, Hypothesis: o.Hypothesis}
}

func FlowEventFromPB(e *graphv1.FlowEvent) domain.FlowEvent {
	return domain.FlowEvent{Op: e.Op, Flow: e.Flow, Parent: e.Parent, ForkAfter: domain.ItemID(e.ForkAfter),
		Stale: itemIDsFromPB(e.Stale), By: e.By, StaleRuns: e.StaleRuns, Origin: Map(e.Origin), Option: optionFromPB(e.Option), Comment: e.Comment}
}

func FlowToPB(f domain.Flow) *graphv1.Flow {
	return &graphv1.Flow{Id: f.ID, Parent: f.Parent, ForkAfter: string(f.ForkAfter), Origin: Struct(f.Origin), Status: string(f.Status), Stale: itemIDsToPB(f.Stale), OpenedAt: Time(f.OpenedAt),
		DecidedAt: Time(f.DecidedAt), DecidedBy: f.DecidedBy, CompetesWith: f.CompetesWith, StaleRuns: f.StaleRuns,
		Option: optionToPB(f.Option), OptionStatus: optionStatus(f), Evaluation: f.Evaluation, Active: f.Active}
}

func optionStatus(f domain.Flow) string {
	if f.Option == nil {
		return ""
	}
	return f.OptionStatus()
}

func FlowFromPB(f *graphv1.Flow) domain.Flow {
	if f == nil {
		return domain.Flow{}
	}
	return domain.Flow{ID: f.Id, Parent: f.Parent, ForkAfter: domain.ItemID(f.ForkAfter), Origin: Map(f.Origin), Status: domain.FlowStatus(f.Status), Stale: itemIDsFromPB(f.Stale), OpenedAt: FromTime(f.OpenedAt),
		DecidedAt: FromTime(f.DecidedAt), DecidedBy: f.DecidedBy, CompetesWith: f.CompetesWith, StaleRuns: f.StaleRuns,
		Option: optionFromPB(f.Option), Evaluation: f.Evaluation, Evaluated: f.OptionStatus == domain.OptionEvaluated, Active: f.Active}
}
