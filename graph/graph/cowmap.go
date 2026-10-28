package graph

import (
	"hash/maphash"
	"iter"
	"maps"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
)

// cowShards is the number of shards of a cowMap.
const cowShards = 64

var cowSeed = maphash.MakeSeed()

// cowMap is a map the copies of a view of a change share until they write (draftState.copy): its entries are split in
// shards by the hash of their key, and a write copies the index of the shards once and the shard it touches, not the
// map, so a write to a change of N impacts copies some N/64 entries, not N. A copy (share) holds the shards of the map
// it was made from; that one is never written again (a view is immutable once copied).
type cowMap[K ~string, V any] struct {
	shards *[cowShards]map[K]V
	n      int
	// idx: the index of the shards is the map's own; mine: the shards it copied since it was shared
	idx  bool
	mine uint64
}

// share is a copy of the map sharing every shard, which copies what it writes.
func (m cowMap[K, V]) share() cowMap[K, V] { return cowMap[K, V]{shards: m.shards, n: m.n} }

func shardOf[K ~string](k K) int { return int(maphash.String(cowSeed, string(k)) % cowShards) }

// get is the value of a key.
func (m *cowMap[K, V]) get(k K) (V, bool) {
	if m.shards == nil {
		var zero V
		return zero, false
	}
	v, ok := m.shards[shardOf(k)][k]
	return v, ok
}

func (m *cowMap[K, V]) has(k K) bool {
	_, ok := m.get(k)
	return ok
}

// len is the number of keys of the map.
func (m *cowMap[K, V]) len() int { return m.n }

// shard is the shard of a key, the map's own, to write to.
func (m *cowMap[K, V]) shard(k K) map[K]V {
	if !m.idx {
		next := new([cowShards]map[K]V)
		if m.shards != nil {
			*next = *m.shards
		}
		m.shards, m.idx, m.mine = next, true, 0
	}
	i := shardOf(k)
	if m.mine&(1<<i) == 0 {
		if m.shards[i] == nil {
			m.shards[i] = map[K]V{}
		} else {
			m.shards[i] = maps.Clone(m.shards[i])
		}
		m.mine |= 1 << i
	}
	return m.shards[i]
}

// set gives a key a value.
func (m *cowMap[K, V]) set(k K, v V) {
	s := m.shard(k)
	if _, ok := s[k]; !ok {
		m.n++
	}
	s[k] = v
}

// del removes a key.
func (m *cowMap[K, V]) del(k K) {
	if !m.has(k) {
		return
	}
	delete(m.shard(k), k)
	m.n--
}

// all iterates over the entries of the map, in no order.
func (m *cowMap[K, V]) all() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		if m.shards == nil {
			return
		}
		for _, s := range m.shards {
			for k, v := range s {
				if !yield(k, v) {
					return
				}
			}
		}
	}
}

// impactBlocks are the change impacts of a view of a change in blocks of domain.ImpactBlock that the copies of the view
// share as cowMap shares its shards: a write copies the index of the blocks once and the block it touches. Read as a
// domain.Impacts (list).
type impactBlocks struct {
	blocks [][]domain.ChangeImpact
	n      int
	// idx: the index of the blocks is the list's own; mine: the blocks it copied since it was shared
	idx  bool
	mine []bool
}

func (b impactBlocks) share() impactBlocks { return impactBlocks{blocks: b.blocks, n: b.n} }

// list is the impacts, shared: the view is not written while it is read (a frozen view is copied first).
func (b *impactBlocks) list() domain.Impacts { return domain.ImpactsOfBlocks(b.blocks, b.n) }

func (b *impactBlocks) at(i int) domain.ChangeImpact {
	return b.blocks[i/domain.ImpactBlock][i%domain.ImpactBlock]
}

// block is the block k, the list's own, to write to.
func (b *impactBlocks) block(k int) []domain.ChangeImpact {
	if !b.idx {
		b.blocks, b.mine, b.idx = slices.Clone(b.blocks), make([]bool, len(b.blocks)), true
	}
	if !b.mine[k] {
		nb := make([]domain.ChangeImpact, len(b.blocks[k]), domain.ImpactBlock)
		copy(nb, b.blocks[k])
		b.blocks[k], b.mine[k] = nb, true
	}
	return b.blocks[k]
}

func (b *impactBlocks) set(i int, cn domain.ChangeImpact) {
	b.block(i / domain.ImpactBlock)[i%domain.ImpactBlock] = cn
}

func (b *impactBlocks) append(cn domain.ChangeImpact) {
	if b.n%domain.ImpactBlock == 0 {
		if !b.idx {
			b.blocks, b.mine, b.idx = slices.Clone(b.blocks), make([]bool, len(b.blocks)), true
		}
		b.blocks, b.mine = append(b.blocks, make([]domain.ChangeImpact, 0, domain.ImpactBlock)), append(b.mine, true)
	}
	k := len(b.blocks) - 1
	b.blocks[k] = append(b.block(k), cn)
	b.n++
}

// reset makes the list the impacts of a slice, its own.
func (b *impactBlocks) reset(s []domain.ChangeImpact) {
	*b = impactBlocks{idx: true}
	for _, cn := range s {
		b.append(cn)
	}
}

// lastIndex is the position of the last impact of a list that satisfies f, -1 when none does.
func lastIndex(l domain.Impacts, f func(domain.ChangeImpact) bool) int {
	at := -1
	for i, cn := range l.All() {
		if f(cn) {
			at = i
		}
	}
	return at
}
