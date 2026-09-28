package graph

import (
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
