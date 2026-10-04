package graph

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
)

// Baselines are stored as deltas (ADR 0032): a baseline keeps the entries that differ from its parent, and every
// checkpointEvery baselines along a parent chain one is stored whole (a checkpoint), which bounds what reading a
// baseline replays. The SQL stores use this; the memory store keeps whole maps.

// checkpointEvery is the longest chain of deltas: a baseline whose parent is that many deltas away from its
// checkpoint is stored whole.
var checkpointEvery = 50

// baselineEntry is one stored entry of a baseline: a node at a version, or, in a delta, a node that left it
// (removed, version is the one it had).
type baselineEntry struct {
	node    domain.NodeID
	version domain.Version
	removed bool
}

// storedEntries returns the depth of a new baseline (0: a checkpoint) and the entries to store for it: its whole
// content without a parent or at a checkpoint, else what differs from its parent. parent is nil for a baseline
// without one.
func storedEntries(parent *domain.Baseline, parentDepth int, nodes map[domain.NodeID]domain.Version) (int, []baselineEntry) {
	var out []baselineEntry
	depth := 0
	if parent == nil || parentDepth+1 >= checkpointEvery {
		for id, v := range nodes {
			out = append(out, baselineEntry{node: id, version: v})
		}
	} else {
		depth = parentDepth + 1
		for id, v := range nodes {
			if parent.Nodes[id] != v {
				out = append(out, baselineEntry{node: id, version: v})
			}
		}
		for id, v := range parent.Nodes {
			if _, ok := nodes[id]; !ok {
				out = append(out, baselineEntry{node: id, version: v, removed: true})
			}
		}
	}
	slices.SortFunc(out, func(a, b baselineEntry) int {
		switch {
		case a.node < b.node:
			return -1
		case a.node > b.node:
			return 1
		}
		return 0
	})
	return depth, out
}

// baselineEntriesSQL selects the entries in effect in a baseline (the first parameter), in both SQL dialects: the
// chain of the baseline up to its checkpoint (d = distance from the baseline), and for each node the entry of the
// nearest baseline of the chain that has one. The caller filters `rn = 1 AND NOT removed`.
func baselineEntriesSQL(param string) string {
	return fmt.Sprintf(`WITH RECURSIVE chain(id, parent_id, depth, d) AS (
		SELECT id, parent_id, depth, 0 FROM baseline WHERE id = %s
		UNION ALL
		SELECT b.id, b.parent_id, b.depth, c.d + 1 FROM baseline b JOIN chain c ON b.id = c.parent_id WHERE c.depth > 0
	), eff AS (
		SELECT e.node_id, e.version, e.removed, row_number() OVER (PARTITION BY e.node_id ORDER BY c.d) AS rn
		FROM chain c JOIN baseline_entry e ON e.baseline_id = c.id
	)`, param)
}

// baselineHeader is what compaction needs of a stored baseline.
type baselineHeader struct {
	id     domain.BaselineID
	parent domain.BaselineID
	depth  int
}

// baselineRewriter is implemented by the transactions of the stores that keep baselines as deltas (the SQL ones).
type baselineRewriter interface {
	baselineHeaders(ctx context.Context) ([]baselineHeader, error)
	// rewriteBaseline replaces the stored entries of a baseline, whose content does not change.
	rewriteBaseline(ctx context.Context, id domain.BaselineID, depth int, entries []baselineEntry) error
}

// CompactBaselines stores every baseline the way PutBaseline would today (ADR 0032 §3): the whole copies written
// before baselines were deltas become deltas, and the checkpoints fall every checkpointEvery baselines along each
// chain. The content of a baseline never changes, so it can run at any time; each baseline is rewritten in a
// transaction of its own. It returns how many were rewritten (0 on a store that keeps whole maps).
func (g *Graph) CompactBaselines(ctx context.Context) (int, error) {
	var headers []baselineHeader
	supported := true
	if err := g.repo.InTx(ctx, func(tx Tx) (err error) {
		rw, ok := rewriterOf(tx)
		if !ok {
			supported = false
			return nil
		}
		headers, err = rw.baselineHeaders(ctx)
		return err
	}); err != nil || !supported {
		return 0, err
	}
	byID := map[domain.BaselineID]baselineHeader{}
	for _, h := range headers {
		byID[h.id] = h
	}
	depth := map[domain.BaselineID]int{}
	var desired func(h baselineHeader, hops int) int
	desired = func(h baselineHeader, hops int) int {
		if d, ok := depth[h.id]; ok {
			return d
		}
		d := 0
		if p, ok := byID[h.parent]; ok && hops < 1<<20 {
			if pd := desired(p, hops+1); pd+1 < checkpointEvery {
				d = pd + 1
			}
		}
		depth[h.id] = d
		return d
	}
	n := 0
	for _, h := range headers { // parents first: desired walks up before a child is rewritten
		d := desired(h, 0)
		if d == h.depth {
			continue
		}
		err := g.repo.InTx(ctx, func(tx Tx) error {
			b, err := tx.Baseline(ctx, h.id)
			if err != nil {
				return err
			}
			var parent *domain.Baseline
			if d > 0 {
				p, err := tx.Baseline(ctx, h.parent)
				if err != nil {
					return err
				}
				parent = &p
			}
			_, entries := storedEntries(parent, d-1, b.Nodes)
			rw, _ := rewriterOf(tx)
			return rw.rewriteBaseline(ctx, h.id, d, entries)
		})
		if err != nil {
			return n, fmt.Errorf("compact baseline %s: %w", h.id, err)
		}
		n++
	}
	return n, nil
}

// rewriterOf returns the store transaction under tx when it keeps baselines as deltas.
func rewriterOf(tx Tx) (baselineRewriter, bool) {
	if ot, ok := tx.(*observedTx); ok {
		tx = ot.Tx
	}
	if gt, ok := tx.(*guardTx); ok { // rewriting a baseline's storage writes no node, link or change
		tx = gt.Tx
	}
	rw, ok := tx.(baselineRewriter)
	return rw, ok
}
