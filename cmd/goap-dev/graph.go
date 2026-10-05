package main

import (
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/criticality"
	"github.com/zimwip/goap/pkg/decision"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/verify"
)

// graphPart is the graph and what judges who may do what on it.
type graphPart struct {
	g          *graph.Graph
	directory  *access.Directory
	authorizer *access.Authorizer
}

// buildGraph opens the graph on its repository, compacts its old baselines in the background and wires the access
// hooks. The registry hooks (types, landing gate, lifecycles) come with buildRegistry.
func buildGraph(e *env, st stores) (*graphPart, error) {
	g := graph.New(st.graph)
	g.Caller = graphsvc.Caller           // the principal behind each event of the impact logs (ADR 0029)
	g.DecisionPolicy = decision.Policy{} // confidence, rounds and deadline settle the decision points (ADR 0067)
	g.ReviewPolicy = verify.Policy{}     // a verifier is not the producer (ADR 0075)
	// baselines written whole before they were stored as deltas are compacted, once, in the background (ADR 0032)
	go func() {
		if n, err := g.CompactBaselines(e.ctx); err != nil {
			e.log.Error("compact baselines", "err", err)
		} else if n > 0 {
			e.log.Info("baselines compacted", "rewritten", n)
		}
	}()
	directory := &access.Directory{Graph: g}
	authorizer, err := access.NewAuthorizer(directory)
	if err != nil {
		return nil, err
	}
	g.Authorizer = graphsvc.TransitionAuthorizer(authorizer)
	g.ChangeAuthorizer = graphsvc.ChangeTransitionAuthorizer(authorizer)
	g.ItemAuthorizer = graphsvc.ItemAuthorizer(authorizer, directory.CriticalityResolver())
	g.ItemPolicy = criticality.ItemPolicy(directory.CriticalityResolver()) // the oracle and the lifetime a level accepts (ADR 0075 §3)
	g.Facets = map[string]graph.BlackboardFacet{domain.FacetCriticalityPolicy: directory.CriticalityFacet()}
	g.Validators = []graph.NodeValidator{access.AdminFloorValidator{}}
	return &graphPart{g: g, directory: directory, authorizer: authorizer}, nil
}
