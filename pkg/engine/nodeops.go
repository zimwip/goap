package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/graph"
)

// impactWriteRetries bounds the retry of a single change-impact write on
// graph.ErrConflict (ADR 0031, gap 6): with concurrent processes now sharing
// one change's main branch by default, a transient version clash on a node
// the graph layer re-validates internally (e.g. a concurrent WriteNode) is
// expected rather than rare. graph.ErrConflict is a single sentinel shared by
// several non-transient conditions too (an unresolved merge, a flow that
// needs a decision, a key already in use); retrying those is harmless (the
// graph layer re-checks live state on every call, so a non-transient cause
// fails identically each time) but not effective — this is a best-effort net
// for the genuinely transient case, not a fix for every ErrConflict cause.
const impactWriteRetries = 3

func retryOnConflict[T any](call func() (T, error)) (T, error) {
	var (
		out T
		err error
	)
	for attempt := 0; attempt < impactWriteRetries; attempt++ {
		out, err = call()
		if err == nil || !errors.Is(err, graph.ErrConflict) {
			return out, err
		}
	}
	return out, err
}

// ChangeImpactsFromBlackboard builds the change impact snapshot given to scripts.
func ChangeImpactsFromBlackboard(bb domain.Blackboard) []dsl.ChangeImpact {
	view := func(r *domain.NodeRef) *dsl.Node {
		if r == nil {
			return nil
		}
		n := &dsl.Node{ID: string(r.ID), Version: int(r.Version)}
		if v, ok := bb.Nodes[*r]; ok {
			n.Key, n.Type, n.State, n.Props = v.Key, v.Type, v.State, v.Properties
		}
		return n
	}
	end := func(r domain.NodeRef) dsl.LinkEnd {
		e := dsl.LinkEnd{ID: string(r.ID), Version: int(r.Version)}
		if n, ok := bb.Neighbors[r]; ok {
			e.Key, e.Type = n.Key, n.Type
		} else if v, ok := bb.Nodes[r]; ok {
			e.Key, e.Type = v.Key, v.Type
		}
		return e
	}
	out := make([]dsl.ChangeImpact, 0, len(bb.Change.Nodes))
	for _, cn := range bb.Change.Nodes {
		x := dsl.ChangeImpact{ID: string(cn.ID), Key: cn.Key, Type: cn.Type, Intent: string(cn.Intent), Rationale: cn.Rationale, Review: string(cn.Review),
			Planned: cn.Post == nil, Pre: view(cn.Pre), Post: view(cn.Post), Landed: view(cn.Landed), Items: []string{}, Links: []dsl.Link{}}
		if cn.Post != nil {
			for _, l := range bb.Nodes[*cn.Post].Out {
				x.Links = append(x.Links, dsl.Link{ID: string(l.ID), Type: l.Type, From: dsl.LinkEnd{ID: string(cn.Post.ID), Version: int(cn.Post.Version), Key: cn.Key, Type: cn.Type}, To: end(l.To)})
			}
		}
		for _, id := range cn.Items {
			x.Items = append(x.Items, string(id))
		}
		out = append(out, x)
	}
	return out
}

// applyNodeOps applies the change impact operations a script buffered, in order:
// a declaration adds a change impact, a write creates the next version of its
// node on the change branch, a review accepts or rejects it. References ("#nN")
// name the change impacts declared earlier by the same script; a key names a
// change impact the process sees. On a flow branch the process sees the change
// nodes of its flow, and what it declares and writes stays on the flow until it
// is adopted (ADR 0025).
func (e *Engine) applyNodeOps(ctx context.Context, p *Process, ops []dsl.NodeOp, producedBy, execution string) ([]domain.ChangeImpactID, error) {
	if len(ops) == 0 {
		return nil, nil
	}
	if p.ChangeID == "" {
		return nil, fmt.Errorf("process %s has no change attached: call goap-scheduler/attach first (ADR 0031)", p.ID)
	}
	bb, err := e.Graph.BlackboardIn(ctx, p.ChangeID, p.Flow)
	if err != nil {
		return nil, err
	}
	nodes, _, err := e.Graph.BaselineGraph(ctx, bb.Change.BaselineID)
	if err != nil {
		return nil, err
	}
	baseline := map[string]domain.NodeRef{}
	for _, n := range nodes {
		baseline[n.Key] = n.Ref()
	}
	byKey := map[string]domain.ChangeImpactID{} // stored change impacts, then the ones this script declares
	posts := map[domain.ChangeImpactID]domain.NodeRef{}
	for _, cn := range bb.Change.Nodes {
		if len(cn.Items) > 0 {
			continue // derived from items: decided through them
		}
		byKey[cn.Key] = cn.ID
		if cn.Post != nil {
			posts[cn.ID] = *cn.Post
		}
	}
	local := map[string]domain.ChangeImpactID{}
	resolve := func(s string) (domain.ChangeImpactID, error) {
		if strings.HasPrefix(s, "#") {
			if id, ok := local[s]; ok {
				return id, nil
			}
			return "", fmt.Errorf("unknown change impact reference %q", s)
		}
		if id, ok := byKey[s]; ok {
			return id, nil
		}
		return "", fmt.Errorf("no change impact for %q: declare it with impactNode or createNode", s)
	}
	target := func(s string) (domain.NodeRef, error) { // a link target: a node written by the change, else the reference baseline
		if strings.HasPrefix(s, "#") {
			id, err := resolve(s)
			if err != nil {
				return domain.NodeRef{}, err
			}
			if ref, ok := posts[id]; ok {
				return ref, nil
			}
			return domain.NodeRef{}, fmt.Errorf("%s is not written yet: write it before linking to it", s)
		}
		if id, ok := byKey[s]; ok {
			if ref, ok := posts[id]; ok {
				return ref, nil
			}
		}
		if ref, ok := baseline[s]; ok {
			return ref, nil
		}
		return domain.NodeRef{}, fmt.Errorf("unknown node %q", s)
	}
	// a bare type or link type written by an action (LLM output, script, human input) is one of the target
	// namespace of the change (ADR 0012): "Requirement" in a change of alm is alm@Requirement
	qualify := func(t string) string {
		if t == "" || strings.Contains(t, domain.TypeSep) || bb.Change.Namespace == "" {
			return t
		}
		return bb.Change.Namespace + domain.TypeSep + t
	}
	var declared []domain.ChangeImpactID
	for i, op := range ops {
		fail := func(err error) error { return fmt.Errorf("change impact operation %d (%s): %w", i, op.Op, err) }
		switch op.Op {
		case "declare":
			cn := domain.ChangeImpact{Intent: domain.NodeIntent(op.Intent), Key: op.Key, Type: qualify(op.Type), Rationale: op.Rationale, ProducedBy: producedBy, Execution: execution, Flow: p.Flow}
			if cn.Intent == domain.IntentModified {
				ref, ok := baseline[op.Key]
				if !ok {
					return declared, fail(fmt.Errorf("unknown node %q in the reference baseline", op.Key))
				}
				cn.Pre = &ref
			}
			added, err := retryOnConflict(func() ([]domain.ChangeImpact, error) {
				return e.Graph.AddNodes(ctx, p.ChangeID, []domain.ChangeImpact{cn})
			})
			if err != nil {
				return declared, fail(err)
			}
			local[op.Ref], byKey[added[0].Key] = added[0].ID, added[0].ID
			declared = append(declared, added[0].ID)
		case "write":
			id, err := resolve(op.Node)
			if err != nil {
				return declared, fail(err)
			}
			w := graph.NodeWrite{Properties: op.Props, State: op.State, Retire: op.Retire, Flow: p.Flow, Execution: execution}
			for _, l := range op.Links {
				to, err := target(l.To)
				if err != nil {
					return declared, fail(err)
				}
				w.AddLinks = append(w.AddLinks, graph.LinkWrite{Type: qualify(l.Type), To: to})
			}
			for _, l := range op.RemoveLinks {
				w.RemoveLinks = append(w.RemoveLinks, domain.LinkID(l))
			}
			cn, err := retryOnConflict(func() (domain.ChangeImpact, error) {
				return e.Graph.WriteNode(ctx, p.ChangeID, id, w)
			})
			if err != nil {
				return declared, fail(err)
			}
			if cn.Post != nil {
				posts[id] = *cn.Post
			}
		case "review":
			id, err := resolve(op.Node)
			if err != nil {
				return declared, fail(err)
			}
			status := domain.ReviewRejected
			if op.Accept {
				status = domain.ReviewAccepted
			}
			if _, err := retryOnConflict(func() (domain.ChangeImpact, error) {
				return e.Graph.ReviewNodeOn(ctx, p.ChangeID, p.Flow, execution, id, status, producedBy, op.Comment)
			}); err != nil {
				return declared, fail(err)
			}
		default:
			return declared, fail(fmt.Errorf("unknown operation"))
		}
	}
	return declared, nil
}
