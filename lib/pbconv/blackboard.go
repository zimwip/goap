package pbconv

import (
	"encoding/json"

	"github.com/zimwip/goap/contracts/domain"
	registryv1 "github.com/zimwip/goap/contracts/gen/goap/registry/v1"
)

// BlackboardToPB is the blackboard of a change as the graph sends it to the service judging it (ADR 0168): every facet
// the JSON of its value.
func BlackboardToPB(bb domain.Blackboard) (*registryv1.Blackboard, error) {
	out := &registryv1.Blackboard{Change: ChangeToPB(bb.Change), At: Time(bb.At)}
	for _, v := range bb.Nodes {
		out.Nodes = append(out.Nodes, ViewToPB(v))
	}
	for _, n := range bb.Neighbors {
		out.Neighbors = append(out.Neighbors, NodeToPB(n))
	}
	if len(bb.Vars) > 0 {
		raw, err := json.Marshal(bb.Vars)
		if err != nil {
			return nil, err
		}
		out.Vars = raw
	}
	if len(bb.Supertypes) > 0 {
		out.Supertypes = make(map[string]*registryv1.Names, len(bb.Supertypes))
		for t, s := range bb.Supertypes {
			out.Supertypes[t] = &registryv1.Names{Names: s}
		}
	}
	// the facets GetBlackboard carries go as its fields, the others as JSON
	rest := make(map[string]any, len(bb.Facets))
	for name, v := range bb.Facets {
		switch name {
		case domain.FacetOptions, domain.FacetActiveOption, domain.FacetDecisionPoints:
		default:
			rest[name] = v
		}
	}
	for _, f := range domain.OptionsOf(bb) {
		out.Options = append(out.Options, FlowToPB(f))
	}
	out.ActiveOption = domain.ActiveOptionOf(bb)
	for _, d := range domain.DecisionPointsOf(bb) {
		out.DecisionPoints = append(out.DecisionPoints, DecisionPointToPB(d))
	}
	facets, err := domain.EncodeFacets(rest)
	if err != nil {
		return nil, err
	}
	out.Facets = facets
	return out, nil
}

// BlackboardFromPB reads a blackboard back, each facet as the type registered for it (domain.RegisterFacet).
func BlackboardFromPB(in *registryv1.Blackboard) (domain.Blackboard, error) {
	bb := domain.Blackboard{Change: ChangeFromPB(in.GetChange()), Nodes: map[domain.NodeRef]domain.NodeView{},
		Neighbors: map[domain.NodeRef]domain.Node{}, At: FromTime(in.GetAt())}
	for _, v := range in.GetNodes() {
		nv := ViewFromPB(v)
		bb.Nodes[nv.Ref()] = nv
	}
	for _, n := range in.GetNeighbors() {
		nn := NodeFromPB(n)
		bb.Neighbors[nn.Ref()] = nn
	}
	if len(in.GetVars()) > 0 {
		if err := json.Unmarshal(in.GetVars(), &bb.Vars); err != nil {
			return domain.Blackboard{}, err
		}
	}
	if len(in.GetSupertypes()) > 0 {
		bb.Supertypes = make(map[string][]string, len(in.GetSupertypes()))
		for t, s := range in.GetSupertypes() {
			bb.Supertypes[t] = s.GetNames()
		}
	}
	facets, err := domain.DecodeFacets(in.GetFacets())
	if err != nil {
		return domain.Blackboard{}, err
	}
	var options []domain.Flow
	for _, f := range in.GetOptions() {
		options = append(options, FlowFromPB(f))
	}
	var points []domain.DecisionPoint
	for _, d := range in.GetDecisionPoints() {
		points = append(points, DecisionPointFromPB(d))
	}
	facets[domain.FacetOptions], facets[domain.FacetActiveOption], facets[domain.FacetDecisionPoints] = options, in.GetActiveOption(), points
	bb.Facets = facets
	return bb, nil
}
