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

func nodeMapToPB(m map[domain.ItemID]domain.NodeID) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range m {
		out[string(k)] = string(v)
	}
	return out
}

func nodeMapFromPB(m map[string]string) map[domain.ItemID]domain.NodeID {
	if len(m) == 0 {
		return nil
	}
	out := map[domain.ItemID]domain.NodeID{}
	for k, v := range m {
		out[domain.ItemID(k)] = domain.NodeID(v)
	}
	return out
}

func FlowEventToPB(e domain.FlowEvent) *graphv1.FlowEvent {
	return &graphv1.FlowEvent{Op: e.Op, Flow: e.Flow, Parent: e.Parent, ForkAfter: string(e.ForkAfter), FromStep: int32(e.FromStep),
		Execution: e.Execution, Process: e.Process, Reason: e.Reason, Stale: itemIDsToPB(e.Stale), By: e.By,
		Branch: e.Branch, Items: itemIDsToPB(e.Items), Merged: e.Merged, Nodes: nodeMapToPB(e.Nodes)}
}

func FlowEventFromPB(e *graphv1.FlowEvent) domain.FlowEvent {
	return domain.FlowEvent{Op: e.Op, Flow: e.Flow, Parent: e.Parent, ForkAfter: domain.ItemID(e.ForkAfter), FromStep: int(e.FromStep),
		Execution: e.Execution, Process: e.Process, Reason: e.Reason, Stale: itemIDsFromPB(e.Stale), By: e.By,
		Branch: e.Branch, Items: itemIDsFromPB(e.Items), Merged: e.Merged, Nodes: nodeMapFromPB(e.Nodes)}
}

func FlowToPB(f domain.Flow) *graphv1.Flow {
	return &graphv1.Flow{Id: f.ID, Parent: f.Parent, ForkAfter: string(f.ForkAfter), FromStep: int32(f.FromStep), Execution: f.Execution,
		Process: f.Process, Reason: f.Reason, Status: string(f.Status), Stale: itemIDsToPB(f.Stale), OpenedAt: Time(f.OpenedAt),
		DecidedAt: Time(f.DecidedAt), DecidedBy: f.DecidedBy, Branch: f.Branch, Branches: f.Branches, Materialized: itemIDsToPB(f.Materialized),
		Merged: itemIDsToPB(f.Merged), CompetesWith: f.CompetesWith}
}

func FlowFromPB(f *graphv1.Flow) domain.Flow {
	if f == nil {
		return domain.Flow{}
	}
	return domain.Flow{ID: f.Id, Parent: f.Parent, ForkAfter: domain.ItemID(f.ForkAfter), FromStep: int(f.FromStep), Execution: f.Execution,
		Process: f.Process, Reason: f.Reason, Status: domain.FlowStatus(f.Status), Stale: itemIDsFromPB(f.Stale), OpenedAt: FromTime(f.OpenedAt),
		DecidedAt: FromTime(f.DecidedAt), DecidedBy: f.DecidedBy, Branch: f.Branch, Branches: f.Branches, Materialized: itemIDsFromPB(f.Materialized),
		Merged: itemIDsFromPB(f.Merged), CompetesWith: f.CompetesWith}
}
