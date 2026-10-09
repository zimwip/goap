package pbconv

import (
	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/pkg/domain"
)

// BlackboardToPB is the wire form of a blackboard: the change, its nodes and the built-in facets (options, active
// option, decision points, change objects).
func BlackboardToPB(bb domain.Blackboard) *graphv1.GetBlackboardResponse {
	out := &graphv1.GetBlackboardResponse{Change: ChangeToPB(bb.Change), ActiveOption: domain.ActiveOptionOf(bb), At: Time(bb.At),
		Objects: ChangeObjectsToPB(domain.ObjectsOf(bb))}
	for _, f := range domain.OptionsOf(bb) {
		out.Options = append(out.Options, FlowToPB(f))
	}
	for _, d := range domain.DecisionPointsOf(bb) {
		out.DecisionPoints = append(out.DecisionPoints, DecisionPointToPB(d))
	}
	for _, v := range bb.Nodes {
		out.Nodes = append(out.Nodes, ViewToPB(v))
	}
	for _, n := range bb.Neighbors {
		out.Neighbors = append(out.Neighbors, NodeToPB(n))
	}
	return out
}

// BlackboardFromPB reads a blackboard from the wire: the facets are the built-in ones.
func BlackboardFromPB(r *graphv1.GetBlackboardResponse) domain.Blackboard {
	var options []domain.Flow
	for _, f := range r.GetOptions() {
		options = append(options, FlowFromPB(f))
	}
	var points []domain.DecisionPoint
	for _, d := range r.GetDecisionPoints() {
		points = append(points, DecisionPointFromPB(d))
	}
	bb := domain.Blackboard{Change: ChangeFromPB(r.GetChange()), Nodes: map[domain.NodeRef]domain.NodeView{}, Neighbors: map[domain.NodeRef]domain.Node{},
		Facets: map[string]any{domain.FacetOptions: options, domain.FacetActiveOption: r.GetActiveOption(), domain.FacetDecisionPoints: points,
			domain.FacetObjects: ChangeObjectsFromPB(r.GetObjects())},
		At: FromTime(r.GetAt())}
	for _, v := range r.GetNodes() {
		nv := ViewFromPB(v)
		bb.Nodes[nv.Ref()] = nv
	}
	for _, n := range r.GetNeighbors() {
		nn := NodeFromPB(n)
		bb.Neighbors[nn.Ref()] = nn
	}
	return bb
}
