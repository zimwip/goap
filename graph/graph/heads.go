package graph

import (
	"context"
	"sync"
	"time"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/cache"
)

// headAge is how long a view takes the head of a lasting branch as it was read when nothing tells the graph of the
// commits of other processes sharing its store: a commit of this graph moves the head at once (Graph.headMoved).
const headAge = time.Second

// followedHeadAge is the same when the graph follows the baseline events of the other processes (FollowHeads): an event
// moves the head at once, a lost one is caught up then.
const followedHeadAge = 30 * time.Second

// maxHeads bounds the lasting branches whose head is kept.
const maxHeads = 1024

// keptBranch is the head of a lasting branch as the graph holds it in memory, moved by its own commits and the baseline
// events of the other processes rather than read again (the way a change view follows its log, ADR 0153).
type keptBranch struct {
	// head is the header of the head ("" id: unknown, read at the next view), at when it was last known to be it
	head domain.Baseline
	at   time.Time
	// gen counts the moves told: a head read from the store is kept only when no move came while it was read
	gen uint64
	// epoch names the selection kept (Graph.selections): a move whose nodes are not all told starts another, which
	// leaves out what was kept before
	epoch uint64
}

// keptHeads are the heads of the lasting branches the views read, by branch (guardBranchKey).
type keptHeads struct {
	mu       sync.Mutex
	branches map[string]*keptBranch
	// follows: the baseline events of the other processes reach the graph (FollowHeads)
	follows bool
}

func (h *keptHeads) branch(key string) *keptBranch {
	if h.branches == nil {
		h.branches = map[string]*keptBranch{}
	}
	b, ok := h.branches[key]
	if !ok {
		if len(h.branches) >= maxHeads {
			clear(h.branches)
		}
		b = &keptBranch{epoch: 1}
		h.branches[key] = b
	}
	return b
}

func (h *keptHeads) age() time.Duration {
	if h.follows {
		return followedHeadAge
	}
	return headAge
}

// keptSel is the version a node has at the head of a branch (0: the head does not hold it), kept for the epoch of the
// branch it was known in.
type keptSel struct {
	epoch   uint64
	version domain.Version
}

// selKey is the key of a node's kept selection on a branch.
func selKey(branch string, id domain.NodeID) string {
	return branch + "\x00" + string(id)
}

// unknownVersion is a move of a node whose version the commit does not know (a version joined it did not read): what
// is kept of the node goes.
const unknownVersion domain.Version = -2

// headMove is what a commit did to the head of a branch: its new header and the versions it moved there (0: a node it
// no longer holds); whole when moves holds every node whose version changed since the parent.
type headMove struct {
	head  domain.Baseline
	moves map[domain.NodeID]domain.Version
	whole bool
}

// headMoved moves the kept head of a branch by a commit, of this graph or told by an event: from the head it holds to
// the commit's, the nodes it moved kept with their new version; a commit not following the head held leaves it unknown.
// A move older than the head held is one already applied.
func (g *Graph) headMoved(key string, m headMove) {
	g.heads.mu.Lock()
	defer g.heads.mu.Unlock()
	b, ok := g.heads.branches[key]
	if !ok {
		return // no view reads it: the next one reads its head
	}
	if b.head.ID != "" && (m.head.ID == b.head.ID || m.head.Seq <= b.head.Seq) {
		return
	}
	b.gen++
	if b.head.ID == "" || b.head.ID != m.head.ParentID {
		b.head, b.epoch = domain.Baseline{}, b.epoch+1
		return
	}
	b.head, b.at = m.head, time.Now()
	if !m.whole {
		b.epoch++
		return
	}
	for id, v := range m.moves {
		if v == unknownVersion {
			g.selections.Remove(selKey(key, id))
		} else {
			g.selections.Put(selKey(key, id), keptSel{epoch: b.epoch, version: v})
		}
	}
}

// FollowHeads makes the graph keep the heads of its lasting branches until the baseline events of the other processes
// sharing its store move them (HeadEvent), followedHeadAge at most: the composition of a graph with a bus.
func (g *Graph) FollowHeads() {
	g.heads.mu.Lock()
	g.heads.follows = true
	g.heads.mu.Unlock()
}

// HeadEvent takes a baseline event (a domain.BaselineEvent, subject domain.SubjectBaselineAdvanced) and moves the head
// kept of its branch: the event carries the header and, when whole, the versions the commit moved. An event of a
// commit of this graph comes back once its head moved: it is left alone.
func (g *Graph) HeadEvent(ev domain.BaselineEvent) {
	if ev.ID == "" || ev.Seq == 0 {
		return // an event of an older graph: the head is read again after followedHeadAge
	}
	moves := make(map[domain.NodeID]domain.Version, len(ev.Set)+len(ev.Removed))
	for id, v := range ev.Set {
		moves[id] = v
	}
	for _, id := range ev.Removed {
		moves[id] = 0
	}
	g.headMoved(guardBranchKey(ev.Namespace, ev.Branch), headMove{head: ev.Header(), moves: moves, whole: ev.Whole})
}

// headHeader is branchHeadHeader for a view of a lasting branch at its head: the head kept, read again once it is older
// than its age or unknown; read in the transaction when it wrote a branch itself.
func (g *Graph) headHeader(ctx context.Context, tx Tx, namespace, name string) (domain.Baseline, error) {
	gt, ok := unwrapGuard(tx)
	if !ok || len(gt.moved) > 0 {
		return branchHeadHeader(ctx, tx, namespace, name)
	}
	key := guardBranchKey(namespace, domain.BranchOf(name))
	g.heads.mu.Lock()
	b := g.heads.branch(key)
	// the generation and the time before the read: a move between them makes what it reads older than they say
	gen, at, kept := b.gen, time.Now(), b.head
	fresh := kept.ID != "" && time.Since(b.at) < g.heads.age()
	g.heads.mu.Unlock()
	if fresh {
		return kept, nil
	}
	head, err := branchHeadHeader(ctx, tx, namespace, name)
	if err != nil || head.ID == "" {
		return head, err
	}
	g.heads.mu.Lock()
	defer g.heads.mu.Unlock()
	if b.gen == gen && g.heads.branches[key] == b {
		if head.ID != kept.ID {
			b.epoch++
		}
		b.head, b.at = head, at
	}
	return head, nil
}

// keptSelection is the node of a key and its version at the head of its branch when the graph keeps them (selectKey):
// the baseline must be the head held.
func (g *Graph) keptSelection(b domain.Baseline, key string) (domain.NodeID, domain.Version, bool) {
	id, ok := g.keyIDs.Get(nsKey(b.Namespace, key))
	if !ok {
		return "", 0, false
	}
	bk := guardBranchKey(b.Namespace, b.Branch)
	g.heads.mu.Lock()
	defer g.heads.mu.Unlock()
	kb, ok := g.heads.branches[bk]
	if !ok || kb.head.ID != b.ID {
		return "", 0, false
	}
	s, ok := g.selections.Get(selKey(bk, id))
	if !ok || s.epoch != kb.epoch {
		return "", 0, false
	}
	return id, s.version, true
}

// keepSelection keeps the node of a key and its version at a baseline read from the store, when the baseline is the
// head held (a selection at a baseline never changes: what is kept for the head is moved with it).
func (g *Graph) keepSelection(b domain.Baseline, key string, id domain.NodeID, v domain.Version) {
	g.keyIDs.Put(nsKey(b.Namespace, key), id)
	bk := guardBranchKey(b.Namespace, b.Branch)
	g.heads.mu.Lock()
	defer g.heads.mu.Unlock()
	if kb, ok := g.heads.branches[bk]; ok && kb.head.ID == b.ID {
		g.selections.Put(selKey(bk, id), keptSel{epoch: kb.epoch, version: v})
	}
}

// forgetSelections drops what the graph keeps of the heads (a purge deleted nodes).
func (g *Graph) forgetSelections() {
	g.keyIDs.Clear()
	g.heads.mu.Lock()
	for _, b := range g.heads.branches {
		b.epoch++
	}
	g.heads.mu.Unlock()
}

func selSize(k string, _ keptSel) int64 { return cache.StructBytes + int64(len(k)) }

func keyIDSize(k string, id domain.NodeID) int64 { return cache.SlotBytes + int64(len(k)+len(id)) }
