// Package pbconv converts between domain types and their protobuf messages.
package pbconv

import (
	"encoding/json"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zimwip/goap/contracts/domain"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/lib/typecat"
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
		Owner: string(n.Owner), Project: string(n.Project), Draft: n.IsDraft(), Origins: refsToPB(n.Origins)}
}

func refsToPB(rs []domain.NodeRef) []*graphv1.NodeRef {
	var out []*graphv1.NodeRef
	for _, r := range rs {
		out = append(out, RefToPB(r))
	}
	return out
}

func refsFromPB(rs []*graphv1.NodeRef) []domain.NodeRef {
	var out []domain.NodeRef
	for _, r := range rs {
		out = append(out, RefFromPB(r))
	}
	return out
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
		Owner: domain.NodeID(n.Owner), Project: domain.NodeID(n.Project), Origins: refsFromPB(n.Origins)}
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
	return &graphv1.NodeView{Node: NodeToPB(v.Node), Latest: int32(v.Latest), Out: LinksToPB(v.Out), In: LinksToPB(v.In), NotCommittable: v.NotCommittable}
}

func ViewFromPB(v *graphv1.NodeView) domain.NodeView {
	return domain.NodeView{Node: NodeFromPB(v.Node), Latest: domain.Version(v.Latest), Out: LinksFromPB(v.Out), In: LinksFromPB(v.In), NotCommittable: v.NotCommittable}
}

func BaselineToPB(b domain.Baseline) *graphv1.Baseline {
	nodes := make(map[string]int32, len(b.Nodes))
	for id, v := range b.Nodes {
		nodes[string(id)] = int32(v)
	}
	return &graphv1.Baseline{Id: string(b.ID), Name: b.Name, ParentId: string(b.ParentID), ChangeId: string(b.ChangeID), Nodes: nodes, CreatedAt: Time(b.CreatedAt),
		Branch: domain.BranchOf(b.Branch), Namespace: domain.NamespaceOf(b.Namespace), MergedFrom: string(b.MergedFrom), Kind: b.Kind, RebasedFrom: string(b.RebasedFrom)}
}

// ChangeStackToPB converts a change-stack (ADR 0133).
func ChangeStackToPB(s domain.ChangeStack) *graphv1.ChangeStack {
	out := &graphv1.ChangeStack{Changes: StackChangesToPB(s.Changes), Truncated: s.Truncated}
	for _, v := range s.Versions {
		pv := &graphv1.VersionStep{Version: int32(v.Version), Branch: v.Branch, Reason: v.Reason, Parents: versionsToPB(v.Parents), Change: string(v.Change), CreatedAt: Time(v.CreatedAt)}
		for _, j := range v.Joined {
			pv.Joined = append(pv.Joined, &graphv1.VersionJoin{Branch: j.Branch, Change: string(j.Change)})
		}
		out.Versions = append(out.Versions, pv)
	}
	for _, b := range s.Baselines {
		out.Baselines = append(out.Baselines, &graphv1.BaselineStep{Id: string(b.ID), Name: b.Name, Branch: b.Branch, Kind: b.Kind, Change: string(b.Change),
			Parent: string(b.Parent), MergedFrom: string(b.MergedFrom), RebasedFrom: string(b.RebasedFrom), CreatedAt: Time(b.CreatedAt), Merged: changeIDs(b.Merged)})
	}
	return out
}

// StackChangesToPB converts the changes of a stack.
func StackChangesToPB(cs []domain.StackChange) []*graphv1.StackChange {
	out := make([]*graphv1.StackChange, 0, len(cs))
	for _, c := range cs {
		out = append(out, &graphv1.StackChange{Id: string(c.ID), Parent: string(c.Parent), Title: c.Title, Status: string(c.Status)})
	}
	return out
}

func TagToPB(t domain.Tag) *graphv1.Tag {
	return &graphv1.Tag{Id: string(t.ID), Name: t.Name, Namespace: t.Namespace, NodeId: string(t.Node), Version: int32(t.Version), By: t.By, CreatedAt: Time(t.CreatedAt)}
}

func TagFromPB(t *graphv1.Tag) domain.Tag {
	return domain.Tag{ID: domain.TagID(t.Id), Name: t.Name, Namespace: t.Namespace, Node: domain.NodeID(t.NodeId), Version: domain.Version(t.Version), By: t.By, CreatedAt: FromTime(t.CreatedAt)}
}

func BaselineFromPB(b *graphv1.Baseline) domain.Baseline {
	nodes := make(map[domain.NodeID]domain.Version, len(b.Nodes))
	for id, v := range b.Nodes {
		nodes[domain.NodeID(id)] = domain.Version(v)
	}
	return domain.Baseline{ID: domain.BaselineID(b.Id), Name: b.Name, ParentID: domain.BaselineID(b.ParentId), ChangeID: domain.ChangeID(b.ChangeId), Nodes: nodes, CreatedAt: FromTime(b.CreatedAt),
		Branch: b.Branch, Namespace: b.Namespace, MergedFrom: domain.BaselineID(b.MergedFrom), Kind: b.Kind, RebasedFrom: domain.BaselineID(b.RebasedFrom)}
}

func ItemToPB(it domain.ChangeItem) *graphv1.ChangeItem {
	out := &graphv1.ChangeItem{Id: string(it.ID), Type: string(it.Type), Target: it.Target, Status: string(it.Status),
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
	out := domain.ChangeItem{ID: domain.ItemID(it.Id), Type: domain.ItemType(it.Type), Target: it.Target, Status: domain.ItemStatus(it.Status),
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
		Branch: domain.BranchOf(c.Branch), Nodes: ChangeImpactsToPB(c.Nodes), ProjectId: c.ProjectID, Lifecycle: c.Lifecycle, State: c.State, Guardians: guardiansToPB(c.Guardians),
		DependsOn: changeIDsToPB(c.DependsOn), MethodologyVersion: c.MethodologyVersion, Seq: c.Seq}
}

func ChangeFromPB(c *graphv1.Change) domain.Change {
	if c == nil {
		return domain.Change{}
	}
	return domain.Change{ID: domain.ChangeID(c.Id), Title: c.Title, Intent: c.Intent, Methodology: c.Methodology, Goal: c.Goal, Namespace: c.Namespace, ParentID: domain.ChangeID(c.ParentId), OwnerOrg: c.OwnerOrg, Status: domain.ChangeStatus(c.Status),
		BaselineID: domain.BaselineID(c.BaselineId), ResultBaselineID: domain.BaselineID(c.ResultBaselineId), Data: Map(c.Data), Items: ItemsFromPB(c.Items), CreatedAt: FromTime(c.CreatedAt),
		Branch: c.Branch, Nodes: ChangeImpactsFromPB(c.Nodes), ProjectID: c.ProjectId, Lifecycle: c.Lifecycle, State: c.State, Guardians: guardiansFromPB(c.Guardians),
		DependsOn: ChangeIDsFromPB(c.DependsOn), MethodologyVersion: c.MethodologyVersion, Seq: c.Seq}
}

func BranchToPB(b domain.Branch) *graphv1.Branch {
	return &graphv1.Branch{Name: b.Name, Parent: b.Parent, ForkBaseline: string(b.ForkBaseline), Head: string(b.Head),
		Origin: b.Origin, Status: b.Status, CreatedAt: Time(b.CreatedAt), Namespace: b.Namespace, Description: b.Description, RebasedFrom: string(b.RebasedFrom)}
}

func ChangeImpactToPB(cn domain.ChangeImpact) *graphv1.ChangeImpact {
	out := &graphv1.ChangeImpact{Id: string(cn.ID), Key: cn.Key, Type: cn.Type, Intent: string(cn.Intent), Rationale: cn.Rationale,
		Pre: RefPtrToPB(cn.Pre), Post: RefPtrToPB(cn.Post), Committed: RefPtrToPB(cn.Committed), Review: string(cn.Review),
		Via: string(cn.Via), Recheck: cn.Recheck, ProducedBy: cn.ProducedBy, Execution: cn.Execution, CreatedAt: Time(cn.CreatedAt),
		Flow: cn.Flow, Superseded: cn.Superseded}
	if v := cn.Validation; v != nil {
		out.Validation = &graphv1.ImpactValidation{Valid: v.Valid, Message: v.Message, Modified: v.Modified}
	}
	for _, r := range cn.Reviews {
		out.Reviews = append(out.Reviews, &graphv1.Review{Status: string(r.Status), By: r.By, Comment: r.Comment, At: Time(r.At), Flow: r.Flow, Execution: r.Execution, Superseded: r.Superseded, ReviewId: r.ReviewID})
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
		Pre: RefPtrFromPB(cn.Pre), Post: RefPtrFromPB(cn.Post), Committed: RefPtrFromPB(cn.Committed), Review: domain.NodeReview(cn.Review),
		Via: domain.ChangeImpactID(cn.Via), Recheck: cn.Recheck, ProducedBy: cn.ProducedBy, Execution: cn.Execution, CreatedAt: FromTime(cn.CreatedAt),
		Flow: cn.Flow, Superseded: cn.Superseded}
	if v := cn.Validation; v != nil {
		out.Validation = &domain.ImpactValidation{Valid: v.Valid, Message: v.Message, Modified: v.Modified}
	}
	for _, r := range cn.Reviews {
		out.Reviews = append(out.Reviews, domain.Review{Status: domain.NodeReview(r.Status), By: r.By, Comment: r.Comment, At: FromTime(r.At), Flow: r.Flow, Execution: r.Execution, Superseded: r.Superseded, ReviewID: r.ReviewId})
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

func EditsToPB(edits []graphapi.NodeEdit) []*graphv1.NodeEdit {
	out := make([]*graphv1.NodeEdit, len(edits))
	for i, e := range edits {
		pe := &graphv1.NodeEdit{Key: e.Key, Type: e.Type, Pre: RefPtrToPB(e.Pre), Props: Struct(e.Props), Rationale: e.Rationale, Owner: e.Owner, State: e.State}
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

func EditsFromPB(edits []*graphv1.NodeEdit) []graphapi.NodeEdit {
	out := make([]graphapi.NodeEdit, len(edits))
	for i, pe := range edits {
		e := graphapi.NodeEdit{Key: pe.Key, Type: pe.Type, Pre: RefPtrFromPB(pe.Pre), Props: Map(pe.Props), Rationale: pe.Rationale, Owner: pe.Owner, State: pe.State}
		for _, l := range pe.Links {
			e.Links = append(e.Links, graphapi.LinkEdit{Type: l.Type, To: RefPtrFromPB(l.To), ToKey: l.ToKey, Props: Map(l.Props)})
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
		Committed: RefPtrToPB(e.Committed), Stale: e.Stale}
	if e.State != nil {
		out.State = ChangeImpactToPB(*e.State)
	}
	if r := e.Review; r != nil {
		out.Review = &graphv1.Review{Status: string(r.Status), By: r.By, Comment: r.Comment, At: Time(r.At), Flow: r.Flow, Execution: r.Execution, Superseded: r.Superseded, ReviewId: r.ReviewID}
	}
	return out
}

// ItemTypesToPB converts the item types the domains in force declare (ADR 0128), with what the code of this process
// adds to them (the permission asked of their writer, whether a meaning is coded).
func ItemTypesToPB(types []*typecat.ItemType) *graphv1.ListItemTypesResponse {
	out := &graphv1.ListItemTypesResponse{}
	for _, x := range types {
		t := domain.ItemType(x.Name)
		pb := &graphv1.ItemTypeInfo{Type: x.Name, Domain: x.Domain, DomainVersion: x.Version, Description: x.Description, Code: domain.HasItemCode(t)}
		if p, ok := domain.ItemPermissionOf(t); ok {
			pb.Permission, pb.SubjectField = p.Permission, p.SubjectField
		}
		out.Types = append(out.Types, pb)
	}
	return out
}

// StructuresToPB converts the structures of the graph (ADR 0054).
func StructuresToPB(s domain.Structures) *graphv1.GetStructuresResponse {
	out := &graphv1.GetStructuresResponse{}
	for _, x := range s {
		st := &graphv1.Structure{Kind: x.Kind, Type: x.Type, Namespace: x.Namespace, Parent: x.Parent, Root: x.Root, SelfParent: x.SelfParent,
			Types: x.Types}
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
			Parent: st.GetParent(), Root: st.GetRoot(), SelfParent: st.GetSelfParent()}, Types: st.GetTypes()}
		if b := st.GetBootstrap(); b != nil {
			x.Bootstrap = b.AsMap()
		}
		out = append(out, x)
	}
	return out
}

// RestructuredToPB and RestructuredFromPB carry the result of a merge or a split (ADR 0077).
func RestructuredToPB(r graphapi.Restructured) *graphv1.ImpactNodeRestructureResponse {
	out := &graphv1.ImpactNodeRestructureResponse{Successors: ChangeImpactsToPB(r.Successors), Sources: ChangeImpactsToPB(r.Sources), Parents: ChangeImpactsToPB(r.Parents)}
	for _, s := range r.Suspect {
		out.Suspect = append(out.Suspect, &graphv1.SuspectLink{From: RefToPB(s.From), FromKey: s.FromKey, Type: s.Type, To: RefToPB(s.To), ToKey: s.ToKey})
	}
	return out
}

func RestructuredFromPB(r *graphv1.ImpactNodeRestructureResponse) graphapi.Restructured {
	out := graphapi.Restructured{Successors: ChangeImpactsFromPB(r.GetSuccessors()), Sources: ChangeImpactsFromPB(r.GetSources()), Parents: ChangeImpactsFromPB(r.GetParents())}
	for _, s := range r.GetSuspect() {
		out.Suspect = append(out.Suspect, graphapi.SuspectLink{From: RefFromPB(s.From), FromKey: s.FromKey, Type: s.Type, To: RefFromPB(s.To), ToKey: s.ToKey})
	}
	return out
}

func guardiansToPB(gs []domain.GuardianRef) []*graphv1.GuardianRef {
	var out []*graphv1.GuardianRef
	for _, g := range gs {
		out = append(out, &graphv1.GuardianRef{Methodology: g.Methodology, Version: g.Version})
	}
	return out
}

func guardiansFromPB(gs []*graphv1.GuardianRef) []domain.GuardianRef {
	var out []domain.GuardianRef
	for _, g := range gs {
		out = append(out, domain.GuardianRef{Methodology: g.Methodology, Version: g.Version})
	}
	return out
}

func changeIDsToPB(ids []domain.ChangeID) []string {
	var out []string
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

// ChangeIDsFromPB converts a list of change ids.
func ChangeIDsFromPB(ids []string) []domain.ChangeID {
	var out []domain.ChangeID
	for _, id := range ids {
		out = append(out, domain.ChangeID(id))
	}
	return out
}

// DependenciesToPB converts dependencies between changes (ADR 0108).
func DependenciesToPB(ds []domain.Dependency) []*graphv1.Dependency {
	var out []*graphv1.Dependency
	for _, d := range ds {
		out = append(out, &graphv1.Dependency{Id: string(d.ID), Title: d.Title, Methodology: d.Methodology, Status: string(d.Status),
			State: d.State, ParentId: string(d.ParentID), Satisfied: d.Satisfied, Reason: d.Reason})
	}
	return out
}

// DependenciesFromPB converts dependencies between changes (ADR 0108).
func DependenciesFromPB(ds []*graphv1.Dependency) []domain.Dependency {
	var out []domain.Dependency
	for _, d := range ds {
		out = append(out, domain.Dependency{ID: domain.ChangeID(d.Id), Title: d.Title, Methodology: d.Methodology, Status: domain.ChangeStatus(d.Status),
			State: d.State, ParentID: domain.ChangeID(d.ParentId), Satisfied: d.Satisfied, Reason: d.Reason})
	}
	return out
}

// changeIDs converts change ids.
func changeIDs(ids []domain.ChangeID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}
