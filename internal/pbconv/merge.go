package pbconv

import (
	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
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
