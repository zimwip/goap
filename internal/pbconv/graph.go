// Package pbconv converts between domain types and their protobuf messages.
package pbconv

import (
	"encoding/json"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
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
	return &graphv1.Node{Id: string(n.ID), Version: int32(n.Version), Namespace: n.Namespace, Key: n.Key, Type: n.Type, Props: Struct(n.Properties),
		Deleted: n.Deleted, ChangeId: string(n.ChangeID), CreatedAt: Time(n.CreatedAt),
		Branch: domain.BranchOf(n.Branch), Parents: versionsToPB(n.Parents), Reason: n.Reason, State: n.State,
		ChangeImpact: string(n.ChangeImpact), Comment: n.Comment, Execution: n.Execution, Joined: n.Joined,
		Owner: string(n.Owner), Project: string(n.Project)}
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
	return domain.Node{ID: domain.NodeID(n.Id), Version: domain.Version(n.Version), Namespace: n.Namespace, Key: n.Key, Type: n.Type, Properties: Map(n.Props),
		Deleted: n.Deleted, ChangeID: domain.ChangeID(n.ChangeId), CreatedAt: FromTime(n.CreatedAt),
		Branch: n.Branch, Parents: versionsFromPB(n.Parents), Reason: n.Reason, State: n.State,
		ChangeImpact: domain.ChangeImpactID(n.ChangeImpact), Comment: n.Comment, Execution: n.Execution, Joined: n.Joined,
		Owner: domain.NodeID(n.Owner), Project: domain.NodeID(n.Project)}
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
	return &graphv1.NodeView{Node: NodeToPB(v.Node), Latest: int32(v.Latest), Out: LinksToPB(v.Out), In: LinksToPB(v.In), Frozen: v.Frozen}
}

func ViewFromPB(v *graphv1.NodeView) domain.NodeView {
	return domain.NodeView{Node: NodeFromPB(v.Node), Latest: domain.Version(v.Latest), Out: LinksFromPB(v.Out), In: LinksFromPB(v.In), Frozen: v.Frozen}
}

func BaselineToPB(b domain.Baseline) *graphv1.Baseline {
	nodes := make(map[string]int32, len(b.Nodes))
	for id, v := range b.Nodes {
		nodes[string(id)] = int32(v)
	}
	return &graphv1.Baseline{Id: string(b.ID), Name: b.Name, ParentId: string(b.ParentID), ChangeId: string(b.ChangeID), Nodes: nodes, CreatedAt: Time(b.CreatedAt),
		Branch: domain.BranchOf(b.Branch), Namespace: domain.NamespaceOf(b.Namespace), MergedFrom: string(b.MergedFrom)}
}

func TagToPB(t domain.Tag) *graphv1.Tag {
	return &graphv1.Tag{Id: string(t.ID), Name: t.Name, Namespace: t.Namespace, ChangeId: string(t.ChangeID), BaselineId: string(t.BaselineID), By: t.By, CreatedAt: Time(t.CreatedAt)}
}

func TagFromPB(t *graphv1.Tag) domain.Tag {
	return domain.Tag{ID: domain.TagID(t.Id), Name: t.Name, Namespace: t.Namespace, ChangeID: domain.ChangeID(t.ChangeId), BaselineID: domain.BaselineID(t.BaselineId), By: t.By, CreatedAt: FromTime(t.CreatedAt)}
}

func BaselineFromPB(b *graphv1.Baseline) domain.Baseline {
	nodes := make(map[domain.NodeID]domain.Version, len(b.Nodes))
	for id, v := range b.Nodes {
		nodes[domain.NodeID(id)] = domain.Version(v)
	}
	return domain.Baseline{ID: domain.BaselineID(b.Id), Name: b.Name, ParentID: domain.BaselineID(b.ParentId), ChangeID: domain.ChangeID(b.ChangeId), Nodes: nodes, CreatedAt: FromTime(b.CreatedAt),
		Branch: b.Branch, Namespace: b.Namespace}
}

func ItemToPB(it domain.ChangeItem) *graphv1.ChangeItem {
	out := &graphv1.ChangeItem{Id: string(it.ID), Kind: string(it.Kind), Type: it.Type, Status: string(it.Status),
		Data: Struct(it.Data), ProducedBy: it.ProducedBy, CreatedAt: Time(it.CreatedAt)}
	for _, d := range it.DerivedFrom {
		out.DerivedFrom = append(out.DerivedFrom, string(d))
	}
	for _, d := range it.Supersedes {
		out.Supersedes = append(out.Supersedes, string(d))
	}
	out.Execution = it.Execution
	out.Flow = it.Flow
	if e := it.FlowEvent; e != nil {
		out.FlowEvent = FlowEventToPB(*e)
	}
	if e := it.DecisionEvent; e != nil {
		out.DecisionEvent = DecisionEventToPB(*e)
	}
	if d := it.Decision; d != nil {
		out.Decision = &graphv1.Decision{Item: string(d.Item), Accept: d.Accept, Comment: d.Comment}
	}
	return out
}

func ItemFromPB(it *graphv1.ChangeItem) domain.ChangeItem {
	out := domain.ChangeItem{ID: domain.ItemID(it.Id), Kind: domain.ItemKind(it.Kind), Type: it.Type, Status: domain.ItemStatus(it.Status),
		Data: Map(it.Data), ProducedBy: it.ProducedBy, CreatedAt: FromTime(it.CreatedAt)}
	for _, d := range it.DerivedFrom {
		out.DerivedFrom = append(out.DerivedFrom, domain.ItemID(d))
	}
	for _, d := range it.Supersedes {
		out.Supersedes = append(out.Supersedes, domain.ItemID(d))
	}
	out.Execution = it.Execution
	out.Flow = it.Flow
	if e := it.FlowEvent; e != nil {
		fe := FlowEventFromPB(e)
		out.FlowEvent = &fe
	}
	if e := it.DecisionEvent; e != nil {
		de := DecisionEventFromPB(e)
		out.DecisionEvent = &de
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

func ChangeToPB(c domain.Change) *graphv1.Change {
	return &graphv1.Change{Id: string(c.ID), Title: c.Title, Intent: c.Intent, Methodology: c.Methodology, Goal: c.Goal, Namespace: c.Namespace, ParentId: string(c.ParentID), OwnerOrg: c.OwnerOrg, Status: string(c.Status),
		BaselineId: string(c.BaselineID), ResultBaselineId: string(c.ResultBaselineID), Data: Struct(c.Data), Items: ItemsToPB(c.Items), CreatedAt: Time(c.CreatedAt),
		Branch: domain.BranchOf(c.Branch), Nodes: ChangeImpactsToPB(c.Nodes), ProjectId: c.ProjectID, Lifecycle: c.Lifecycle, State: c.State}
}

func ChangeFromPB(c *graphv1.Change) domain.Change {
	if c == nil {
		return domain.Change{}
	}
	return domain.Change{ID: domain.ChangeID(c.Id), Title: c.Title, Intent: c.Intent, Methodology: c.Methodology, Goal: c.Goal, Namespace: c.Namespace, ParentID: domain.ChangeID(c.ParentId), OwnerOrg: c.OwnerOrg, Status: domain.ChangeStatus(c.Status),
		BaselineID: domain.BaselineID(c.BaselineId), ResultBaselineID: domain.BaselineID(c.ResultBaselineId), Data: Map(c.Data), Items: ItemsFromPB(c.Items), CreatedAt: FromTime(c.CreatedAt),
		Branch: c.Branch, Nodes: ChangeImpactsFromPB(c.Nodes), ProjectID: c.ProjectId, Lifecycle: c.Lifecycle, State: c.State}
}

func BranchToPB(b domain.Branch) *graphv1.Branch {
	return &graphv1.Branch{Name: b.Name, Parent: b.Parent, ForkBaseline: string(b.ForkBaseline), Head: string(b.Head),
		Origin: b.Origin, Status: b.Status, CreatedAt: Time(b.CreatedAt), Namespace: b.Namespace, Description: b.Description}
}

func ChangeImpactToPB(cn domain.ChangeImpact) *graphv1.ChangeImpact {
	out := &graphv1.ChangeImpact{Id: string(cn.ID), Key: cn.Key, Type: cn.Type, Intent: string(cn.Intent), Rationale: cn.Rationale,
		Pre: RefPtrToPB(cn.Pre), Post: RefPtrToPB(cn.Post), Landed: RefPtrToPB(cn.Landed), Review: string(cn.Review),
		Via: string(cn.Via), Recheck: cn.Recheck, ProducedBy: cn.ProducedBy, Execution: cn.Execution, CreatedAt: Time(cn.CreatedAt),
		Flow: cn.Flow, Superseded: cn.Superseded}
	for _, r := range cn.Reviews {
		out.Reviews = append(out.Reviews, &graphv1.Review{Status: string(r.Status), By: r.By, Comment: r.Comment, At: Time(r.At), Flow: r.Flow, Execution: r.Execution, Superseded: r.Superseded})
	}
	for _, id := range cn.DerivedFrom {
		out.DerivedFrom = append(out.DerivedFrom, string(id))
	}
	for _, id := range cn.Items {
		out.Items = append(out.Items, string(id))
	}
	return out
}

func ChangeImpactFromPB(cn *graphv1.ChangeImpact) domain.ChangeImpact {
	if cn == nil {
		return domain.ChangeImpact{}
	}
	out := domain.ChangeImpact{ID: domain.ChangeImpactID(cn.Id), Key: cn.Key, Type: cn.Type, Intent: domain.NodeIntent(cn.Intent), Rationale: cn.Rationale,
		Pre: RefPtrFromPB(cn.Pre), Post: RefPtrFromPB(cn.Post), Landed: RefPtrFromPB(cn.Landed), Review: domain.NodeReview(cn.Review),
		Via: domain.ChangeImpactID(cn.Via), Recheck: cn.Recheck, ProducedBy: cn.ProducedBy, Execution: cn.Execution, CreatedAt: FromTime(cn.CreatedAt),
		Flow: cn.Flow, Superseded: cn.Superseded}
	for _, r := range cn.Reviews {
		out.Reviews = append(out.Reviews, domain.Review{Status: domain.NodeReview(r.Status), By: r.By, Comment: r.Comment, At: FromTime(r.At), Flow: r.Flow, Execution: r.Execution, Superseded: r.Superseded})
	}
	for _, id := range cn.DerivedFrom {
		out.DerivedFrom = append(out.DerivedFrom, domain.ItemID(id))
	}
	for _, id := range cn.Items {
		out.Items = append(out.Items, domain.ItemID(id))
	}
	return out
}

func ChangeImpactsToPB(cns []domain.ChangeImpact) []*graphv1.ChangeImpact {
	out := make([]*graphv1.ChangeImpact, len(cns))
	for i, cn := range cns {
		out[i] = ChangeImpactToPB(cn)
	}
	return out
}

func ChangeImpactsFromPB(cns []*graphv1.ChangeImpact) []domain.ChangeImpact {
	if len(cns) == 0 {
		return nil
	}
	out := make([]domain.ChangeImpact, len(cns))
	for i, cn := range cns {
		out[i] = ChangeImpactFromPB(cn)
	}
	return out
}

func EditsToPB(edits []graph.NodeEdit) []*graphv1.NodeEdit {
	out := make([]*graphv1.NodeEdit, len(edits))
	for i, e := range edits {
		pe := &graphv1.NodeEdit{Key: e.Key, Type: e.Type, Pre: RefPtrToPB(e.Pre), Props: Struct(e.Props), Retire: e.Retire, Rationale: e.Rationale, Owner: e.Owner}
		for _, l := range e.Links {
			pe.Links = append(pe.Links, &graphv1.LinkEdit{Type: l.Type, To: RefPtrToPB(l.To), ToKey: l.ToKey, Props: Struct(l.Props)})
		}
		for _, id := range e.RemoveLinks {
			pe.RemoveLinks = append(pe.RemoveLinks, string(id))
		}
		out[i] = pe
	}
	return out
}

func EditsFromPB(edits []*graphv1.NodeEdit) []graph.NodeEdit {
	out := make([]graph.NodeEdit, len(edits))
	for i, pe := range edits {
		e := graph.NodeEdit{Key: pe.Key, Type: pe.Type, Pre: RefPtrFromPB(pe.Pre), Props: Map(pe.Props), Retire: pe.Retire, Rationale: pe.Rationale, Owner: pe.Owner}
		for _, l := range pe.Links {
			e.Links = append(e.Links, graph.LinkEdit{Type: l.Type, To: RefPtrFromPB(l.To), ToKey: l.ToKey, Props: Map(l.Props)})
		}
		for _, id := range pe.RemoveLinks {
			e.RemoveLinks = append(e.RemoveLinks, domain.LinkID(id))
		}
		out[i] = e
	}
	return out
}

// ImpactEventToPB converts an event of the impact log (ADR 0029).
func ImpactEventToPB(e domain.ImpactEvent) *graphv1.ImpactEvent {
	out := &graphv1.ImpactEvent{Id: e.ID, ChangeId: string(e.Change), Seq: int32(e.Seq), ImpactId: string(e.Impact), Op: string(e.Op),
		Flow: e.Flow, Execution: e.Execution, By: e.By, At: Time(e.At), Post: RefPtrToPB(e.Post), Pre: RefPtrToPB(e.Pre),
		Landed: RefPtrToPB(e.Landed), Stale: e.Stale}
	if e.State != nil {
		out.State = ChangeImpactToPB(*e.State)
	}
	if r := e.Review; r != nil {
		out.Review = &graphv1.Review{Status: string(r.Status), By: r.By, Comment: r.Comment, At: Time(r.At), Flow: r.Flow, Execution: r.Execution, Superseded: r.Superseded}
	}
	return out
}

// StructuresToPB converts the structures of the graph (ADR 0054).
func StructuresToPB(s domain.Structures) *graphv1.GetStructuresResponse {
	out := &graphv1.GetStructuresResponse{}
	for _, x := range s {
		st := &graphv1.Structure{Kind: x.Kind, Type: x.Type, Namespace: x.Namespace, Parent: x.Parent, Root: x.Root, SelfParent: x.SelfParent,
			Types: x.Types, DefaultProperty: x.Default}
		if len(x.Bootstrap) > 0 {
			st.Bootstrap, _ = structpb.NewStruct(x.Bootstrap)
		}
		out.Structures = append(out.Structures, st)
	}
	return out
}

// StructuresFromPB converts the structures of the graph (ADR 0054).
func StructuresFromPB(r *graphv1.GetStructuresResponse) domain.Structures {
	var out domain.Structures
	for _, st := range r.GetStructures() {
		x := domain.StructureSet{Structure: domain.Structure{Kind: st.GetKind(), Type: st.GetType(), Namespace: st.GetNamespace(),
			Parent: st.GetParent(), Root: st.GetRoot(), SelfParent: st.GetSelfParent(), Default: st.GetDefaultProperty()}, Types: st.GetTypes()}
		if b := st.GetBootstrap(); b != nil {
			x.Bootstrap = b.AsMap()
		}
		out = append(out, x)
	}
	return out
}
