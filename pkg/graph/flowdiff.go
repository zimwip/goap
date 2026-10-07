package graph

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// DiffFlows compares the impacts two flows of a change see (ADR 0083): each flow reads its impacts through its drafts (ADR
// 0079), the innermost draft of its chain, so a node an option did not touch is the main flow's draft on both sides. An
// impact is matched by node (a node created independently on both flows, with two ids, by type and key), then it is added
// (the right flow only), removed (the left flow only) or modified (both, with a different state, owner, property or
// link). Identical impacts are counted, not listed. The flows are "" or "main" for the main flow, else an option of the
// change (open or decided); the level is written (every impact but the rejected ones) or accepted (the accepted ones).
func (g *Graph) DiffFlows(ctx context.Context, id domain.ChangeID, left, right, level string) (out domain.FlowDiff, err error) {
	if level == "" {
		level = ViewWritten
	}
	if level != ViewWritten && level != ViewAccepted {
		return out, fmt.Errorf("flows are compared at %s or %s, not %q: %w", ViewWritten, ViewAccepted, level, ErrInvalid)
	}
	out = domain.FlowDiff{Change: id, Level: level, Impacts: []domain.ImpactDiff{}}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		lf, err := diffFlow(c, left)
		if err != nil {
			return err
		}
		rf, err := diffFlow(c, right)
		if err != nil {
			return err
		}
		out.Left, out.Right = flowName(lf), flowName(rf)
		ls, err := g.flowImpactShapes(ctx, tx, c, lf, level)
		if err != nil {
			return err
		}
		rs, err := g.flowImpactShapes(ctx, tx, c, rf, level)
		if err != nil {
			return err
		}
		matchImpacts(&out, ls, rs)
		return nil
	})
	return
}

// diffFlow resolves a flow named by a caller: "" and "main" are the main flow, anything else an option of the change.
func diffFlow(c domain.Change, flow string) (string, error) {
	if flow == "" || flow == domain.MainFlow {
		return "", nil
	}
	if f, ok := c.Flow(flow); !ok || f.Option == nil {
		return "", fmt.Errorf("option %s of change %s: %w", flow, c.ID, ErrNotFound)
	}
	return flow, nil
}

func flowName(flow string) string {
	if flow == "" {
		return domain.MainFlow
	}
	return flow
}

// impactShape is an impact as a flow sees it, with the shape of its node.
type impactShape struct {
	side  domain.ImpactSide
	node  domain.NodeID
	key   string
	typ   string
	shape domain.NodeShape
}

// flowImpactShapes reads the impacts a flow sees at a level, each with its draft as a shape (a planned one: the version it
// starts from, a planned creation: nothing but its key and type).
func (g *Graph) flowImpactShapes(ctx context.Context, tx Tx, c domain.Change, flow, level string) ([]impactShape, error) {
	seen, err := g.newFlowNodes(tx, c, flow).nodes(ctx)
	if err != nil {
		return nil, err
	}
	reader, err := g.newDraftReader(ctx, tx, c, flow, seen)
	if err != nil {
		return nil, err
	}
	keys := map[domain.NodeRef]string{}
	keyOf := func(ref domain.NodeRef) string {
		if k, ok := keys[ref]; ok {
			return k
		}
		k := string(ref.ID)
		if n, err := reader.node(ctx, tx, ref); err == nil {
			k = n.Key
		}
		keys[ref] = k
		return k
	}
	var out []impactShape
	for _, cn := range seen {
		if cn.Review == domain.ReviewRejected || (level == ViewAccepted && cn.Review != domain.ReviewAccepted) {
			continue
		}
		is := impactShape{node: nodeOf(cn), key: cn.Key, typ: cn.Type,
			side: domain.ImpactSide{Impact: cn.ID, Flow: cn.Flow, Intent: cn.Intent, Review: cn.Review, Drafted: cn.Post != nil}}
		is.shape = domain.NodeShape{Key: cn.Key, Type: cn.Type}
		ref := cn.Post
		if ref == nil {
			ref = cn.Pre
		}
		if ref != nil {
			n, err := reader.node(ctx, tx, *ref)
			if err != nil {
				return nil, err
			}
			is.key, is.typ = n.Key, n.Type
			is.shape = domain.NodeShape{Key: n.Key, Type: n.Type, State: n.State, Owner: n.Owner, Properties: n.Properties}
			links, err := reader.outLinks(ctx, tx, *ref)
			if err != nil {
				return nil, err
			}
			for _, l := range links {
				is.shape.Links = append(is.shape.Links, domain.LinkShape{Type: l.Type, To: l.To.ID, ToKey: keyOf(l.To), Properties: l.Properties})
			}
			is.side.State, is.side.Owner = n.State, n.Owner
		}
		out = append(out, is)
	}
	return out, nil
}

// matchImpacts pairs the impacts of the two flows (by node, then by type and key) and fills the diff.
func matchImpacts(out *domain.FlowDiff, left, right []impactShape) {
	byNode := map[domain.NodeID]int{}
	for i, r := range right {
		if r.node != "" {
			byNode[r.node] = i
		}
	}
	byName := func(s impactShape) string { return s.typ + "\x00" + s.key }
	rightByName := map[string]int{}
	for i, r := range right {
		rightByName[byName(r)] = i
	}
	paired := map[int]bool{}
	var pairs [][2]int // left index, right index (-1: none)
	var unmatched []int
	for i, l := range left {
		if j, ok := byNode[l.node]; ok && l.node != "" && !paired[j] {
			paired[j] = true
			pairs = append(pairs, [2]int{i, j})
			continue
		}
		unmatched = append(unmatched, i)
	}
	for _, i := range unmatched {
		if j, ok := rightByName[byName(left[i])]; ok && !paired[j] {
			paired[j] = true
			pairs = append(pairs, [2]int{i, j})
			continue
		}
		pairs = append(pairs, [2]int{i, -1})
	}
	for j := range right {
		if !paired[j] {
			pairs = append(pairs, [2]int{-1, j})
		}
	}
	for _, p := range pairs {
		var d domain.ImpactDiff
		switch {
		case p[1] < 0:
			l := left[p[0]]
			d = domain.ImpactDiff{Node: l.node, Key: l.key, Type: l.typ, Category: domain.DiffRemoved, Left: &l.side}
		case p[0] < 0:
			r := right[p[1]]
			d = domain.ImpactDiff{Node: r.node, Key: r.key, Type: r.typ, Category: domain.DiffAdded, Right: &r.side}
		default:
			l, r := left[p[0]], right[p[1]]
			changes := domain.DiffShapes(l.shape, r.shape)
			if len(changes) == 0 {
				out.Identical++
				continue
			}
			d = domain.ImpactDiff{Node: r.node, Key: r.key, Type: r.typ, Category: domain.DiffModified, Left: &l.side, Right: &r.side, Changes: changes}
		}
		out.Impacts = append(out.Impacts, d)
	}
	slices.SortFunc(out.Impacts, func(a, b domain.ImpactDiff) int {
		if c := strings.Compare(a.Key, b.Key); c != 0 {
			return c
		}
		return strings.Compare(a.Type, b.Type)
	})
}
