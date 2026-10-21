package main

import (
	"github.com/zimwip/goap/contracts/criticality"
	"github.com/zimwip/goap/contracts/decision"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/verify"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/graphsnap"
	"github.com/zimwip/goap/lib/llmcfg"
)

// graphPart is the graph and what judges who may do what on it.
type graphPart struct {
	g *graph.Graph
	// reader is the one reader of the graph every view of the process shares (ADR 0145)
	reader     *graphsnap.Reader
	directory  *access.Directory
	authorizer *access.Authorizer
}

// buildGraph opens the graph on its repository and wires the access hooks. The registry hooks (types, commit gate, lifecycles) come with buildRegistry.
func buildGraph(e *env, st stores) (*graphPart, error) {
	g := graph.New(st.graph)
	g.Caller = graphsvc.Caller           // the principal behind each event of the impact logs (ADR 0029)
	g.EndUser = graphsvc.EndUser         // the person the events of the changes are for (ADR 0114)
	g.DecisionPolicy = decision.Policy{} // confidence, rounds and deadline settle the decision points (ADR 0067)
	g.ReviewPolicy = verify.Policy{}     // a verifier is not the producer (ADR 0075)
	g.Resolvers = graphsvc.Resolvers()   // the sources of the logical URLs beyond its namespaces (ADR 0153)
	reader := graphsnap.NewReader(g)
	directory := &access.Directory{Graph: g, Reader: reader}
	authorizer, err := access.NewAuthorizer(directory)
	if err != nil {
		return nil, err
	}
	g.Authorizer = graphsvc.TransitionAuthorizer(authorizer)
	g.ChangeAuthorizer = graphsvc.ChangeTransitionAuthorizer(authorizer)
	g.ProjectMoveGate = graphsvc.ProjectMoveGate(authorizer, directory)
	g.MethodologyGate = graphsvc.MethodologyGate(directory)
	g.ItemAuthorizer = graphsvc.ItemAuthorizer(authorizer, directory.CriticalityResolver())
	g.SeatAuthorizer = graphsvc.SeatAuthorizer(authorizer)
	g.ItemPolicy = criticality.ItemPolicy(directory.CriticalityResolver()) // the oracle and the lifetime a level accepts (ADR 0075 §3)
	g.Facets = map[string]graph.BlackboardFacet{domain.FacetCriticalityPolicy: directory.CriticalityFacet()}
	g.Validators = []graph.NodeValidator{access.AdminFloorValidator{}, access.ProjectScopeValidator{}, access.ProjectOwnerValidator{},
		llmcfg.ProtectedAliasValidator{}}
	return &graphPart{g: g, reader: reader, directory: directory, authorizer: authorizer}, nil
}
