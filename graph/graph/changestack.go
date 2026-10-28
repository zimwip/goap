package graph

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
)

// ChangeStackQuery names what a change-stack is asked of (ADR 0133): a node version (Node, Version 0: its latest on
// main) or a commit. Limit bounds the lineage (0: domain.DefaultStackLimit).
type ChangeStackQuery struct {
	Node   domain.NodeRef
	Commit domain.CommitID
	Limit  int
}

// ChangeStack returns the stack of changes producing a node version or a commit (ADR 0133): its lineage and the
// changes it is made of, oldest first. Nothing is stored for it: it is read from what every version and commit
// records (the change that wrote it, the merges that made it join a branch, the commits it follows from).
func (g *Graph) ChangeStack(ctx context.Context, q ChangeStackQuery) (s domain.ChangeStack, err error) {
	if (q.Node.ID == "") == (q.Commit == "") {
		return s, fmt.Errorf("a change-stack is of a node version or of a commit: %w", ErrInvalid)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if q.Commit != "" {
			b, err := tx.CommitHeader(ctx, q.Commit) // its lineage only: its state is not read
			if err != nil {
				return err
			}
			heads, err := tx.CommitHeaders(ctx, b.Namespace)
			if err != nil {
				return err
			}
			s.Commits, s.Truncated = domain.CommitLineage(heads, b.ID, mergedInto(ctx, tx), q.Limit)
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
		s.Changes, err = stackChanges(ctx, tx, domain.StackChangeIDs(s.Versions, s.Commits))
		return err
	})
	return s, err
}

// DiffStacks returns the changes the stack of commit to holds and the one of from does not (added), and the other
// way round (removed), each oldest first. The lineages are walked together from both commits, newest first, down to
// where they meet (ADR 0140): what lies under that is in both stacks, so the walk follows the distance between the two
// commits, not the length of the history.
func (g *Graph) DiffStacks(ctx context.Context, from, to domain.CommitID) (added, removed []domain.StackChange, err error) {
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

// gap is what lies between two commits: the changes of the stack of the second that the first does not hold (added)
// and the other way round (removed), and every change of a commit only one of them reaches (changes), whose nodes
// are all the two states may differ on.
type gap struct {
	added, removed, changes []domain.ChangeID
}

// MaxDiffChanges bounds the changes between two commits a difference walks: further apart, a difference of their
// histories means nothing to read, and it is refused.
const MaxDiffChanges = 1000

// lineageGap walks the lineages of two commits (the edges of domain.CommitLineage: parent, merged head and, for
// one taking a change in, the commits of that change's own branch written before it) newest first by order, each
// commit painted with the side(s) reaching it, and stops once every commit left to walk is reached from both: a
// commit's order is above the orders of the ones it follows from, so its paint is final when it is walked. The
// changes of the commits only one side reaches, less the ones of a commit the other side reaches, make the
// difference.
func lineageGap(ctx context.Context, tx Tx, from, to domain.CommitID) (g gap, err error) {
	const sideFrom, sideTo, both = 1, 2, 3
	type walked struct {
		b    domain.Commit
		side int
	}
	paint := map[domain.CommitID]int{}
	headers := map[domain.CommitID]domain.Commit{}
	var todo []domain.CommitID // ordered by Seq, the newest last
	var namespace string
	reach := func(id domain.CommitID, side int) error {
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
				return fmt.Errorf("commit %s is not of namespace %s: %w", id, namespace, ErrInvalid)
			}
			namespace, headers[id] = domain.NamespaceOf(b.Namespace), b
		}
		if paint[id] == 0 {
			i, _ := slices.BinarySearchFunc(todo, b.Seq, func(x domain.CommitID, seq int64) int { return cmp.Compare(headers[x].Seq, seq) })
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
	landed := map[domain.ChangeID][]domain.Commit{}
	for len(todo) > 0 && slices.ContainsFunc(todo, func(id domain.CommitID) bool { return paint[id] != both }) {
		if len(seen) >= MaxDiffChanges {
			return g, invalidf("commits %s and %s are more than %d changes apart: compare closer commits", from, to, MaxDiffChanges)
		}
		id := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		b, side := headers[id], paint[id]
		seen = append(seen, walked{b, side})
		for _, next := range []domain.CommitID{b.ParentID, b.MergedFrom} {
			if err := reach(next, side); err != nil {
				return g, err
			}
		}
		// a change open after its merge (ADR 0166) lands on a branch more than once: its earlier landings there are in
		// the stack of a later one, which they precede
		if b.ChangeID != "" && side != both {
			landings, ok := landed[b.ChangeID]
			if !ok {
				if landings, err = tx.ChangeCommitHeaders(ctx, namespace, []domain.ChangeID{b.ChangeID}); err != nil {
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
	// every commit painted counts, the ones left to walk too: a change a side reaches is in its stack
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
	// commit (ADR 0140), the stacks name them through their parent
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

// cachedHeader reads the header of a commit through the cache of the graph's guard when there is one.
func cachedHeader(ctx context.Context, tx Tx, id domain.CommitID) (domain.Commit, error) {
	if gt, ok := unwrapGuard(tx); ok {
		return gt.header(ctx, id)
	}
	return tx.CommitHeader(ctx, id)
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
