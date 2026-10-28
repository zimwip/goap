package graph

import (
	"context"
	"sync"
	"time"

	"github.com/zimwip/goap/contracts/domain"
)

// headAge is how long a view takes the head of a lasting branch as it was read: a commit of this graph that moves
// the branch is seen at once (headMarks), one of another process sharing the store within headAge, as the access
// snapshot follows the organisation (access.CommitAge) but for a shorter while.
const headAge = time.Second

// keptHead is the head of a branch as a view read it: the mark of the branch then, and when it was read.
type keptHead struct {
	head domain.Baseline
	mark uint64
	at   time.Time
}

// headMarks count, per branch (guardBranchKey), the commits of this graph that moved it.
type headMarks struct {
	mu    sync.Mutex
	marks map[string]uint64
}

func (m *headMarks) of(key string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.marks[key]
}

func (m *headMarks) move(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.marks == nil {
		m.marks = map[string]uint64{}
	}
	m.marks[key]++
}

// headHeader is branchHeadHeader for a view of a lasting branch at its head: kept headAge, read again once a commit of
// this graph moved the branch; read in the transaction when it wrote a branch itself.
func (g *Graph) headHeader(ctx context.Context, tx Tx, namespace, name string) (domain.Baseline, error) {
	gt, ok := unwrapGuard(tx)
	if !ok || len(gt.moved) > 0 || g.heads == nil {
		return branchHeadHeader(ctx, tx, namespace, name)
	}
	key := guardBranchKey(namespace, domain.BranchOf(name))
	// the mark and the time before the read: a commit between them makes what it reads older than they say
	mark, at := g.headMarks.of(key), time.Now()
	if h, ok := g.heads.Get(key); ok && h.mark == mark && at.Sub(h.at) < headAge {
		return h.head, nil
	}
	head, err := branchHeadHeader(ctx, tx, namespace, name)
	if err == nil {
		g.heads.Put(key, keptHead{head: head, mark: mark, at: at})
	}
	return head, err
}
