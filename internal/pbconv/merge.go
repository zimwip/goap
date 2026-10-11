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

// MergePlanFromPB is the inverse of MergePlanToPB.
func MergePlanFromPB(p *graphv1.MergePlan) graph.MergePlan {
	out := graph.MergePlan{From: p.GetFrom(), Into: p.GetInto(), IntoHead: domain.BaselineID(p.GetIntoHead())}
	for _, c := range p.GetCandidates() {
		out.Candidates = append(out.Candidates, graph.MergeCandidate{Node: domain.NodeID(c.GetNode()), Key: c.GetKey(), Type: c.GetType(), Kind: c.GetKind(),
			Ancestor: RefPtrFromPB(c.GetAncestor()), Ours: RefPtrFromPB(c.GetOurs()), Theirs: RefFromPB(c.GetTheirs()), Deleted: c.GetDeleted(),
			Base: Map(c.GetBase()), OursProps: Map(c.GetOursProps()), TheirsProps: Map(c.GetTheirsProps()), Merged: Map(c.GetMerged()), Conflicts: c.GetConflicts()})
	}
	return out
}

// ResolutionsToPB converts the resolutions of the conflicts of a merge, by node id.
func ResolutionsToPB(rs map[domain.NodeID]graph.Resolution) map[string]*graphv1.Resolution {
	out := make(map[string]*graphv1.Resolution, len(rs))
	for id, r := range rs {
		out[string(id)] = &graphv1.Resolution{Props: Struct(r.Props), Skip: r.Skip}
	}
	return out
}

// BranchFromPB converts a branch (without its creation time).
func BranchFromPB(b *graphv1.Branch) domain.Branch {
	return domain.Branch{Name: b.GetName(), Parent: b.GetParent(), ForkBaseline: domain.BaselineID(b.GetForkBaseline()), Head: domain.BaselineID(b.GetHead()),
		Origin: b.GetOrigin(), Status: b.GetStatus(), Namespace: b.GetNamespace(), Description: b.GetDescription(), RebasedFrom: domain.BaselineID(b.GetRebasedFrom())}
}
