// Package pbconv converts between domain types and their protobuf messages.
package pbconv

import (
	"encoding/json"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/pkg/domain"
)

// Struct converts a map to a protobuf Struct (nil for an empty map).
func Struct(m map[string]any) *structpb.Struct {
	if len(m) == 0 {
		return nil
	}
	s, err := structpb.NewStruct(m)
	if err == nil {
		return s
	}
	// normalize through JSON (typed slices, custom types…)
	var norm map[string]any
	b, _ := json.Marshal(m)
	_ = json.Unmarshal(b, &norm)
	s, _ = structpb.NewStruct(norm)
	return s
}

// Map converts a protobuf Struct to a map (nil for nil).
func Map(s *structpb.Struct) map[string]any {
	if s == nil || len(s.Fields) == 0 {
		return nil
	}
	return s.AsMap()
}

// Time converts a timestamp (nil for zero).
func Time(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// FromTime converts a protobuf timestamp.
func FromTime(t *timestamppb.Timestamp) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.AsTime()
}

func RefToPB(r domain.NodeRef) *graphv1.NodeRef {
	return &graphv1.NodeRef{Id: string(r.ID), Version: int32(r.Version)}
}

func RefPtrToPB(r *domain.NodeRef) *graphv1.NodeRef {
	if r == nil {
		return nil
	}
	return RefToPB(*r)
}

func RefFromPB(r *graphv1.NodeRef) domain.NodeRef {
	if r == nil {
		return domain.NodeRef{}
	}
	return domain.NodeRef{ID: domain.NodeID(r.Id), Version: domain.Version(r.Version)}
}

func RefPtrFromPB(r *graphv1.NodeRef) *domain.NodeRef {
	if r == nil || r.Id == "" {
		return nil
	}
	ref := RefFromPB(r)
	return &ref
}

func NodeToPB(n domain.Node) *graphv1.Node {
	return &graphv1.Node{Id: string(n.ID), Version: int32(n.Version), Key: n.Key, Type: n.Type, Props: Struct(n.Properties),
		Deleted: n.Deleted, ChangeId: string(n.ChangeID), CreatedAt: Time(n.CreatedAt),
		Branch: domain.BranchOf(n.Branch), Parents: versionsToPB(n.Parents), Reason: n.Reason}
}

func versionsToPB(vs []domain.Version) []int32 {
	var out []int32
	for _, v := range vs {
		out = append(out, int32(v))
	}
	return out
}

func NodeFromPB(n *graphv1.Node) domain.Node {
	if n == nil {
		return domain.Node{}
	}
	return domain.Node{ID: domain.NodeID(n.Id), Version: domain.Version(n.Version), Key: n.Key, Type: n.Type, Properties: Map(n.Props),
		Deleted: n.Deleted, ChangeID: domain.ChangeID(n.ChangeId), CreatedAt: FromTime(n.CreatedAt),
		Branch: n.Branch, Parents: versionsFromPB(n.Parents), Reason: n.Reason}
}

func versionsFromPB(vs []int32) []domain.Version {
	var out []domain.Version
	for _, v := range vs {
		out = append(out, domain.Version(v))
	}
	return out
}

func NodesToPB(ns []domain.Node) []*graphv1.Node {
	out := make([]*graphv1.Node, len(ns))
	for i, n := range ns {
		out[i] = NodeToPB(n)
	}
	return out
}

func NodesFromPB(ns []*graphv1.Node) []domain.Node {
	out := make([]domain.Node, len(ns))
	for i, n := range ns {
		out[i] = NodeFromPB(n)
	}
	return out
}

func LinkToPB(l domain.Link) *graphv1.Link {
	return &graphv1.Link{Id: string(l.ID), Type: l.Type, From: RefToPB(l.From), To: RefToPB(l.To), Props: Struct(l.Properties), ChangeId: string(l.ChangeID)}
}

func LinkFromPB(l *graphv1.Link) domain.Link {
	return domain.Link{ID: domain.LinkID(l.Id), Type: l.Type, From: RefFromPB(l.From), To: RefFromPB(l.To), Properties: Map(l.Props), ChangeID: domain.ChangeID(l.ChangeId)}
}

func LinksToPB(ls []domain.Link) []*graphv1.Link {
	out := make([]*graphv1.Link, len(ls))
	for i, l := range ls {
		out[i] = LinkToPB(l)
	}
	return out
}

func LinksFromPB(ls []*graphv1.Link) []domain.Link {
	out := make([]domain.Link, len(ls))
	for i, l := range ls {
		out[i] = LinkFromPB(l)
	}
	return out
}

func ViewToPB(v domain.NodeView) *graphv1.NodeView {
	return &graphv1.NodeView{Node: NodeToPB(v.Node), Latest: int32(v.Latest), Out: LinksToPB(v.Out), In: LinksToPB(v.In)}
}

func ViewFromPB(v *graphv1.NodeView) domain.NodeView {
	return domain.NodeView{Node: NodeFromPB(v.Node), Latest: domain.Version(v.Latest), Out: LinksFromPB(v.Out), In: LinksFromPB(v.In)}
}

func BaselineToPB(b domain.Baseline) *graphv1.Baseline {
	nodes := make(map[string]int32, len(b.Nodes))
	for id, v := range b.Nodes {
		nodes[string(id)] = int32(v)
	}
	return &graphv1.Baseline{Id: string(b.ID), Name: b.Name, ParentId: string(b.ParentID), ChangeId: string(b.ChangeID), Nodes: nodes, CreatedAt: Time(b.CreatedAt),
		Branch: domain.BranchOf(b.Branch)}
}

func BaselineFromPB(b *graphv1.Baseline) domain.Baseline {
	nodes := make(map[domain.NodeID]domain.Version, len(b.Nodes))
	for id, v := range b.Nodes {
		nodes[domain.NodeID(id)] = domain.Version(v)
	}
	return domain.Baseline{ID: domain.BaselineID(b.Id), Name: b.Name, ParentID: domain.BaselineID(b.ParentId), ChangeID: domain.ChangeID(b.ChangeId), Nodes: nodes, CreatedAt: FromTime(b.CreatedAt),
		Branch: b.Branch}
}

func endpointToPB(e domain.Endpoint) *graphv1.Endpoint {
	return &graphv1.Endpoint{Node: RefPtrToPB(e.Node), Item: string(e.Item)}
}

func endpointFromPB(e *graphv1.Endpoint) domain.Endpoint {
	if e == nil {
		return domain.Endpoint{}
	}
	return domain.Endpoint{Node: RefPtrFromPB(e.Node), Item: domain.ItemID(e.Item)}
}

func ItemToPB(it domain.ChangeItem) *graphv1.ChangeItem {
	out := &graphv1.ChangeItem{Id: string(it.ID), Kind: string(it.Kind), Type: it.Type, Status: string(it.Status), Target: RefPtrToPB(it.Target),
		Data: Struct(it.Data), ProducedBy: it.ProducedBy, CreatedAt: Time(it.CreatedAt)}
	for _, d := range it.DerivedFrom {
		out.DerivedFrom = append(out.DerivedFrom, string(d))
	}
	for _, d := range it.Supersedes {
		out.Supersedes = append(out.Supersedes, string(d))
	}
	if p := it.Proposal; p != nil {
		pp := &graphv1.Proposal{Op: string(p.Op)}
		if p.Node != nil {
			pp.Node = &graphv1.NodeDraft{Base: RefPtrToPB(p.Node.Base), Key: p.Node.Key, Type: p.Node.Type, Props: Struct(p.Node.Properties),
				From: RefPtrToPB(p.Node.From), Ancestor: RefPtrToPB(p.Node.Ancestor)}
		}
		if p.Link != nil {
			pp.Link = &graphv1.LinkDraft{LinkId: string(p.Link.LinkID), Type: p.Link.Type, From: endpointToPB(p.Link.From), To: endpointToPB(p.Link.To), Props: Struct(p.Link.Properties)}
		}
		out.Proposal = pp
	}
	if d := it.Decision; d != nil {
		out.Decision = &graphv1.Decision{Item: string(d.Item), Accept: d.Accept, Comment: d.Comment}
	}
	return out
}

func ItemFromPB(it *graphv1.ChangeItem) domain.ChangeItem {
	out := domain.ChangeItem{ID: domain.ItemID(it.Id), Kind: domain.ItemKind(it.Kind), Type: it.Type, Status: domain.ItemStatus(it.Status),
		Target: RefPtrFromPB(it.Target), Data: Map(it.Data), ProducedBy: it.ProducedBy, CreatedAt: FromTime(it.CreatedAt)}
	for _, d := range it.DerivedFrom {
		out.DerivedFrom = append(out.DerivedFrom, domain.ItemID(d))
	}
	for _, d := range it.Supersedes {
		out.Supersedes = append(out.Supersedes, domain.ItemID(d))
	}
	if p := it.Proposal; p != nil {
		dp := &domain.Proposal{Op: domain.ProposalOp(p.Op)}
		if p.Node != nil {
			dp.Node = &domain.NodeDraft{Base: RefPtrFromPB(p.Node.Base), Key: p.Node.Key, Type: p.Node.Type, Properties: Map(p.Node.Props),
				From: RefPtrFromPB(p.Node.From), Ancestor: RefPtrFromPB(p.Node.Ancestor)}
		}
		if p.Link != nil {
			dp.Link = &domain.LinkDraft{LinkID: domain.LinkID(p.Link.LinkId), Type: p.Link.Type, From: endpointFromPB(p.Link.From), To: endpointFromPB(p.Link.To), Properties: Map(p.Link.Props)}
		}
		out.Proposal = dp
	}
	if d := it.Decision; d != nil {
		out.Decision = &domain.Decision{Item: domain.ItemID(d.Item), Accept: d.Accept, Comment: d.Comment}
	}
	return out
}

func ItemsToPB(its []domain.ChangeItem) []*graphv1.ChangeItem {
	out := make([]*graphv1.ChangeItem, len(its))
	for i, it := range its {
		out[i] = ItemToPB(it)
	}
	return out
}

func ItemsFromPB(its []*graphv1.ChangeItem) []domain.ChangeItem {
	out := make([]domain.ChangeItem, len(its))
	for i, it := range its {
		out[i] = ItemFromPB(it)
	}
	return out
}

func ChangeToPB(c domain.ChangeSet) *graphv1.ChangeSet {
	return &graphv1.ChangeSet{Id: string(c.ID), Title: c.Title, Intent: c.Intent, Methodology: c.Methodology, Goal: c.Goal, Status: string(c.Status),
		BaselineId: string(c.BaselineID), ResultBaselineId: string(c.ResultBaselineID), Data: Struct(c.Data), Items: ItemsToPB(c.Items), CreatedAt: Time(c.CreatedAt),
		Branch: domain.BranchOf(c.Branch)}
}

func ChangeFromPB(c *graphv1.ChangeSet) domain.ChangeSet {
	if c == nil {
		return domain.ChangeSet{}
	}
	return domain.ChangeSet{ID: domain.ChangeID(c.Id), Title: c.Title, Intent: c.Intent, Methodology: c.Methodology, Goal: c.Goal, Status: domain.ChangeStatus(c.Status),
		BaselineID: domain.BaselineID(c.BaselineId), ResultBaselineID: domain.BaselineID(c.ResultBaselineId), Data: Map(c.Data), Items: ItemsFromPB(c.Items), CreatedAt: FromTime(c.CreatedAt),
		Branch: c.Branch}
}

func BranchToPB(b domain.Branch) *graphv1.Branch {
	return &graphv1.Branch{Name: b.Name, Parent: b.Parent, ForkBaseline: string(b.ForkBaseline), Head: string(b.Head),
		Origin: b.Origin, Status: b.Status, CreatedAt: Time(b.CreatedAt)}
}

func BranchFromPB(b *graphv1.Branch) domain.Branch {
	if b == nil {
		return domain.Branch{}
	}
	return domain.Branch{Name: b.Name, Parent: b.Parent, ForkBaseline: domain.BaselineID(b.ForkBaseline), Head: domain.BaselineID(b.Head),
		Origin: b.Origin, Status: b.Status, CreatedAt: FromTime(b.CreatedAt)}
}
