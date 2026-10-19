package graph

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
)

// ChangeStackQuery names what a change-stack is asked of (ADR 0133): a node version (Node, Version 0: its latest on
// main) or a baseline. Limit bounds the lineage (0: domain.DefaultStackLimit).
type ChangeStackQuery struct {
	Node     domain.NodeRef
	Baseline domain.BaselineID
	Limit    int
}

// ChangeStack returns the stack of changes producing a node version or a baseline (ADR 0133): its lineage and the
// changes it is made of, oldest first. Nothing is stored for it: it is read from what every version and baseline
// records (the change that wrote it, the merges that made it join a branch, the baselines it follows from).
func (g *Graph) ChangeStack(ctx context.Context, q ChangeStackQuery) (s domain.ChangeStack, err error) {
	if (q.Node.ID == "") == (q.Baseline == "") {
		return s, fmt.Errorf("a change-stack is of a node version or of a baseline: %w", ErrInvalid)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if q.Baseline != "" {
			b, err := tx.BaselineHeader(ctx, q.Baseline) // its lineage only: its state is not read
			if err != nil {
				return err
			}
			heads, err := tx.BaselineHeaders(ctx, b.Namespace)
			if err != nil {
				return err
			}
			s.Baselines, s.Truncated = domain.BaselineLineage(heads, b.ID, branchOfChange(ctx, tx), q.Limit)
		} else {
			n, err := tx.Node(ctx, q.Node)
			if err != nil {
				return err
			}
			vs, err := tx.Versions(ctx, n.ID)
			if err != nil {
				return err
			}
			joins, err := tx.VersionJoins(ctx, n.ID)
			if err != nil {
				return err
			}
			s.Versions, s.Truncated = domain.VersionLineage(vs, joins, n.Version, q.Limit)
		}
		s.Changes, err = stackChanges(ctx, tx, domain.StackChangeIDs(s.Versions, s.Baselines))
		return err
	})
	return s, err
}

// DiffStacks returns the changes the stack of baseline to holds and the one of from does not (added), and the other
// way round (removed), each oldest first. The lineages are walked together from both baselines, newest first, down to
// where they meet (ADR 0140): what lies under that is in both stacks, so the walk follows the distance between the two
// baselines, not the length of the history.
func (g *Graph) DiffStacks(ctx context.Context, from, to domain.BaselineID) (added, removed []domain.StackChange, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		ad, rm, err := diffLineages(ctx, tx, from, to)
		if err != nil {
			return err
		}
		if added, err = stackChanges(ctx, tx, ad); err != nil {
			return err
		}
		removed, err = stackChanges(ctx, tx, rm)
		return err
	})
	return added, removed, err
}

// diffLineages walks the lineages of two baselines (the edges of domain.BaselineLineage: parent, merged head and, for
// one taking a change in, the baselines of that change's own branch written before it) newest first by order, each
// baseline painted with the side(s) reaching it, and stops once every baseline left to walk is reached from both: a
// baseline's order is above the orders of the ones it follows from, so its paint is final when it is walked. The
// changes of the baselines only one side reaches, less the ones of a baseline the other side reaches, make the
// difference.
func diffLineages(ctx context.Context, tx Tx, from, to domain.BaselineID) (added, removed []domain.ChangeID, err error) {
	const sideFrom, sideTo, both = 1, 2, 3
	type walked struct {
		b    domain.Baseline
		side int
	}
	paint := map[domain.BaselineID]int{}
	headers := map[domain.BaselineID]domain.Baseline{}
	var todo []domain.BaselineID // ordered by Seq, the newest last
	var namespace string
	reach := func(id domain.BaselineID, side int) error {
		if id == "" || paint[id]|side == paint[id] {
			return nil
		}
		b, ok := headers[id]
		if !ok {
			var err error
			if b, err = cachedHeader(ctx, tx, id); err != nil {
				return err
			}
			if namespace != "" && domain.NamespaceOf(b.Namespace) != namespace {
				return fmt.Errorf("baseline %s is not of namespace %s: %w", id, namespace, ErrInvalid)
			}
			namespace, headers[id] = domain.NamespaceOf(b.Namespace), b
		}
		if paint[id] == 0 {
			i, _ := slices.BinarySearchFunc(todo, b.Seq, func(x domain.BaselineID, seq int64) int { return cmp.Compare(headers[x].Seq, seq) })
			todo = slices.Insert(todo, i, id)
		}
		paint[id] |= side
		return nil
	}
	if err := reach(from, sideFrom); err != nil {
		return nil, nil, err
	}
	if err := reach(to, sideTo); err != nil {
		return nil, nil, err
	}
	branches := map[domain.ChangeID]string{}
	ownBranch := func(id domain.ChangeID) (string, error) {
		if br, ok := branches[id]; ok {
			return br, nil
		}
		c, err := tx.ChangeHead(ctx, id)
		if err != nil && !isNotFound(err) {
			return "", err
		}
		branches[id] = c.Branch
		return c.Branch, nil
	}
	onBranch := map[string][]domain.Baseline{}
	var seen []walked
	for len(todo) > 0 && slices.ContainsFunc(todo, func(id domain.BaselineID) bool { return paint[id] != both }) {
		id := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		b, side := headers[id], paint[id]
		seen = append(seen, walked{b, side})
		for _, next := range []domain.BaselineID{b.ParentID, b.MergedFrom} {
			if err := reach(next, side); err != nil {
				return nil, nil, err
			}
		}
		// the work a change brought in matters only to a side alone: below a baseline both sides reach, all is both
		if side == both || b.Kind != domain.BaselineFastForward && b.Kind != domain.BaselineMerge || b.ChangeID == "" {
			continue
		}
		own, err := ownBranch(b.ChangeID)
		if err != nil {
			return nil, nil, err
		}
		if own == "" || domain.BranchOf(own) == domain.BranchOf(b.Branch) {
			continue
		}
		hs, ok := onBranch[domain.BranchOf(own)]
		if !ok {
			if hs, err = tx.BranchBaselineHeaders(ctx, b.Namespace, own); err != nil {
				return nil, nil, err
			}
			onBranch[domain.BranchOf(own)] = hs
		}
		for _, o := range hs {
			if o.CreatedAt.After(b.CreatedAt) {
				continue
			}
			if _, ok := headers[o.ID]; !ok {
				headers[o.ID] = o
			}
			if err := reach(o.ID, side); err != nil {
				return nil, nil, err
			}
		}
	}
	// oldest first, as a stack reads
	slices.SortStableFunc(seen, func(x, y walked) int { return x.b.CreatedAt.Compare(y.b.CreatedAt) })
	// every baseline painted counts, the ones left to walk too: a change a side reaches is in its stack
	in := map[int]map[domain.ChangeID]bool{sideFrom: {}, sideTo: {}}
	for id, side := range paint {
		for _, s := range []int{sideFrom, sideTo} {
			if c := headers[id].ChangeID; side&s != 0 && c != "" {
				in[s][c] = true
			}
		}
	}
	for _, w := range seen {
		c := w.b.ChangeID
		switch {
		case c == "" || w.side == both:
		case w.side == sideTo && !in[sideFrom][c] && !slices.Contains(added, c):
			added = append(added, c)
		case w.side == sideFrom && !in[sideTo][c] && !slices.Contains(removed, c):
			removed = append(removed, c)
		}
	}
	return added, removed, nil
}

// cachedHeader reads the header of a baseline through the cache of the graph's guard when there is one.
func cachedHeader(ctx context.Context, tx Tx, id domain.BaselineID) (domain.Baseline, error) {
	if gt, ok := unwrapGuard(tx); ok {
		return gt.header(ctx, id)
	}
	return tx.BaselineHeader(ctx, id)
}

// branchOfChange names the own branch of a change ("" when it has none or the graph no longer holds it). The heads of
// every change are read once, at the first call: a lineage asks it of every change of a namespace's history.
func branchOfChange(ctx context.Context, tx Tx) func(domain.ChangeID) string {
	var branches map[domain.ChangeID]string
	return func(id domain.ChangeID) string {
		if branches == nil {
			branches = map[domain.ChangeID]string{}
			if cs, err := tx.ChangeHeads(ctx); err == nil {
				for _, c := range cs {
					branches[c.ID] = c.Branch
				}
			}
		}
		return branches[id]
	}
}

// stackChanges names the changes of a stack; a change the graph no longer holds (purged) keeps its id alone.
func stackChanges(ctx context.Context, tx Tx, ids []domain.ChangeID) ([]domain.StackChange, error) {
	out := make([]domain.StackChange, 0, len(ids))
	for _, id := range ids {
		sc := domain.StackChange{ID: id}
		c, err := tx.ChangeHead(ctx, id)
		switch {
		case err == nil:
			sc.Parent, sc.Title, sc.Status, sc.OwnerOrg = c.ParentID, c.Title, c.Status, c.OwnerOrg
		case !isNotFound(err):
			return nil, err
		}
		out = append(out, sc)
	}
	return out, nil
}
