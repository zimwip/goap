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
			s.Baselines, s.Truncated = domain.BaselineLineage(heads, b.ID, mergedInto(ctx, tx), q.Limit)
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
		gap, err := lineageGap(ctx, tx, from, to)
		if err != nil {
			return err
		}
		if added, err = stackChanges(ctx, tx, gap.added); err != nil {
			return err
		}
		removed, err = stackChanges(ctx, tx, gap.removed)
		return err
	})
	return added, removed, err
}

// gap is what lies between two baselines: the changes of the stack of the second that the first does not hold (added)
// and the other way round (removed), and every change of a baseline only one of them reaches (changes), whose nodes
// are all the two states may differ on.
type gap struct {
	added, removed, changes []domain.ChangeID
}

// MaxDiffChanges bounds the changes between two baselines a difference walks: further apart, a difference of their
// histories means nothing to read, and it is refused.
const MaxDiffChanges = 1000

// lineageGap walks the lineages of two baselines (the edges of domain.BaselineLineage: parent, merged head and, for
// one taking a change in, the baselines of that change's own branch written before it) newest first by order, each
// baseline painted with the side(s) reaching it, and stops once every baseline left to walk is reached from both: a
// baseline's order is above the orders of the ones it follows from, so its paint is final when it is walked. The
// changes of the baselines only one side reaches, less the ones of a baseline the other side reaches, make the
// difference.
func lineageGap(ctx context.Context, tx Tx, from, to domain.BaselineID) (g gap, err error) {
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
		return g, err
	}
	if err := reach(to, sideTo); err != nil {
		return g, err
	}
	var seen []walked
	landed := map[domain.ChangeID][]domain.Baseline{}
	for len(todo) > 0 && slices.ContainsFunc(todo, func(id domain.BaselineID) bool { return paint[id] != both }) {
		if len(seen) >= MaxDiffChanges {
			return g, invalidf("baselines %s and %s are more than %d changes apart: compare closer baselines", from, to, MaxDiffChanges)
		}
		id := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		b, side := headers[id], paint[id]
		seen = append(seen, walked{b, side})
		for _, next := range []domain.BaselineID{b.ParentID, b.MergedFrom} {
			if err := reach(next, side); err != nil {
				return g, err
			}
		}
		// a change open after its merge (ADR 0166) lands on a branch more than once: its earlier landings there are in
		// the stack of a later one, which they precede
		if b.ChangeID != "" && side != both {
			landings, ok := landed[b.ChangeID]
			if !ok {
				if landings, err = tx.ChangeBaselineHeaders(ctx, namespace, []domain.ChangeID{b.ChangeID}); err != nil {
					return g, err
				}
				landed[b.ChangeID] = landings
			}
			for _, l := range landings {
				if l.ID != b.ID && l.Seq < b.Seq && l.Branch == b.Branch {
					headers[l.ID] = l
					if err := reach(l.ID, side); err != nil {
						return g, err
					}
				}
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
		if c == "" || w.side == both {
			continue
		}
		if !slices.Contains(g.changes, c) {
			g.changes = append(g.changes, c)
		}
		switch {
		case w.side == sideTo && !in[sideFrom][c] && !slices.Contains(g.added, c):
			g.added = append(g.added, c)
		case w.side == sideFrom && !in[sideTo][c] && !slices.Contains(g.removed, c):
			g.removed = append(g.removed, c)
		}
	}
	// the sub-changes merged into a change are in every stack it is in (ADR 0103): the branch of a change holds no
	// baseline (ADR 0140), the stacks name them through their parent
	merged := mergedInto(ctx, tx)
	expand := func(ids []domain.ChangeID) []domain.ChangeID {
		var out []domain.ChangeID
		for _, id := range ids {
			for _, m := range merged(id) {
				if !slices.Contains(out, m) && !slices.Contains(ids, m) {
					out = append(out, m)
				}
			}
			out = append(out, id)
		}
		return out
	}
	g.added, g.removed, g.changes = expand(g.added), expand(g.removed), expand(g.changes)
	return g, nil
}

// cachedHeader reads the header of a baseline through the cache of the graph's guard when there is one.
func cachedHeader(ctx context.Context, tx Tx, id domain.BaselineID) (domain.Baseline, error) {
	if gt, ok := unwrapGuard(tx); ok {
		return gt.header(ctx, id)
	}
	return tx.BaselineHeader(ctx, id)
}

// mergedInto names the sub-changes merged into a change, recursively, oldest first (a change the graph no longer holds
// has none).
func mergedInto(ctx context.Context, tx Tx) func(domain.ChangeID) []domain.ChangeID {
	cache := map[domain.ChangeID][]domain.ChangeID{}
	var walk func(id domain.ChangeID, depth int) []domain.ChangeID
	walk = func(id domain.ChangeID, depth int) []domain.ChangeID {
		if out, ok := cache[id]; ok || depth > 64 {
			return out
		}
		// the headers alone: their status and order are what counts, not their impacts
		subs, err := tx.ChangeHeads(ctx, ChangesFilter{Parent: id})
		if err != nil {
			return nil
		}
		slices.SortStableFunc(subs, func(a, b domain.Change) int { return a.CreatedAt.Compare(b.CreatedAt) })
		var out []domain.ChangeID
		for _, sc := range subs {
			if sc.Status != domain.ChangeApplied {
				continue
			}
			out = append(out, walk(sc.ID, depth+1)...)
			out = append(out, sc.ID)
		}
		cache[id] = out
		return out
	}
	return func(id domain.ChangeID) []domain.ChangeID { return walk(id, 0) }
}

// stackChanges names the changes of a stack; a change the graph no longer holds (purged) keeps its id alone.
func stackChanges(ctx context.Context, tx Tx, ids []domain.ChangeID) ([]domain.StackChange, error) {
	heads, err := changeHeads(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.StackChange, 0, len(ids))
	for _, id := range ids {
		sc := domain.StackChange{ID: id}
		if c, ok := heads[id]; ok {
			sc.Parent, sc.Title, sc.Status, sc.OwnerOrg = c.ParentID, c.Title, c.Status, c.OwnerOrg
		}
		out = append(out, sc)
	}
	return out, nil
}
