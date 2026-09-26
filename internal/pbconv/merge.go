package pbconv

import (
	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

func MergePlanToPB(p graph.MergePlan) *graphv1.MergePlan {
	out := &graphv1.MergePlan{From: p.From, Into: p.Into, IntoHead: string(p.IntoHead)}
	for _, c := range p.Candidates {
		out.Candidates = append(out.Candidates, &graphv1.MergeCandidate{Node: string(c.Node), Key: c.Key, Type: c.Type, Kind: c.Kind,
			Ancestor: RefPtrToPB(c.Ancestor), Ours: RefPtrToPB(c.Ours), Theirs: RefToPB(c.Theirs), Deleted: c.Deleted,
			Base: Struct(c.Base), OursProps: Struct(c.OursProps), TheirsProps: Struct(c.TheirsProps), Merged: Struct(c.Merged), Conflicts: c.Conflicts})
	}
	return out
}

func MergePlanFromPB(p *graphv1.MergePlan) graph.MergePlan {
	if p == nil {
		return graph.MergePlan{}
	}
	out := graph.MergePlan{From: p.From, Into: p.Into, IntoHead: domain.BaselineID(p.IntoHead)}
	for _, c := range p.Candidates {
		out.Candidates = append(out.Candidates, graph.MergeCandidate{Node: domain.NodeID(c.Node), Key: c.Key, Type: c.Type, Kind: c.Kind,
			Ancestor: RefPtrFromPB(c.Ancestor), Ours: RefPtrFromPB(c.Ours), Theirs: RefFromPB(c.Theirs), Deleted: c.Deleted,
			Base: Map(c.Base), OursProps: Map(c.OursProps), TheirsProps: Map(c.TheirsProps), Merged: Map(c.Merged), Conflicts: c.Conflicts})
	}
	return out
}
