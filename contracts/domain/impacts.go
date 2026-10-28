package domain

import (
	"iter"
	"slices"
)

// ImpactBlock is the number of change impacts a block of an Impacts list holds (the last one may hold fewer).
const ImpactBlock = 16

// Impacts is a list of change impacts, read in their order (All) or by position (At), never edited: the view of a
// change keeps its impacts in blocks of ImpactBlock that its copies share, so that a write copies the block it touches
// rather than the list (a change of N impacts written N times copied N² impacts); any other list is a slice cut in
// blocks (ImpactsOf). The zero value is the empty list.
type Impacts struct {
	blocks [][]ChangeImpact
	n      int
}

// ImpactsOf is the list of the impacts of a slice, which it shares: the slice must not be edited while the list is read.
func ImpactsOf(s []ChangeImpact) Impacts {
	if len(s) == 0 {
		return Impacts{}
	}
	blocks := make([][]ChangeImpact, 0, (len(s)+ImpactBlock-1)/ImpactBlock)
	for i := 0; i < len(s); i += ImpactBlock {
		blocks = append(blocks, s[i:min(i+ImpactBlock, len(s)):min(i+ImpactBlock, len(s))])
	}
	return Impacts{blocks: blocks, n: len(s)}
}

// ImpactsOfBlocks is the list of blocks of ImpactBlock impacts each but the last, which it shares: the holder of the
// blocks keeps them as they are while the list is read (a write of the view copies the block first).
func ImpactsOfBlocks(blocks [][]ChangeImpact, n int) Impacts { return Impacts{blocks: blocks, n: n} }

// Len is the number of impacts of the list.
func (l Impacts) Len() int { return l.n }

// At is the impact at a position of the list.
func (l Impacts) At(i int) ChangeImpact { return l.blocks[i/ImpactBlock][i%ImpactBlock] }

// All iterates over the impacts of the list in their order, with their positions.
func (l Impacts) All() iter.Seq2[int, ChangeImpact] {
	return func(yield func(int, ChangeImpact) bool) {
		i := 0
		for _, b := range l.blocks {
			for _, cn := range b {
				if !yield(i, cn) {
					return
				}
				i++
			}
		}
	}
}

// Index is the position of the impact of an id, -1 when the list holds none.
func (l Impacts) Index(id ChangeImpactID) int {
	for i, cn := range l.All() {
		if cn.ID == id {
			return i
		}
	}
	return -1
}

// Find is the position of the first impact that satisfies f, -1 when none does.
func (l Impacts) Find(f func(ChangeImpact) bool) int {
	for i, cn := range l.All() {
		if f(cn) {
			return i
		}
	}
	return -1
}

// Slice is a copy of the impacts of the list, which the caller may edit.
func (l Impacts) Slice() []ChangeImpact {
	if l.n == 0 {
		return nil
	}
	out := make([]ChangeImpact, 0, l.n)
	for _, b := range l.blocks {
		out = append(out, b...)
	}
	return out
}

// Append is the list with an impact more: its last block copied, the others shared.
func (l Impacts) Append(cn ChangeImpact) Impacts {
	blocks := make([][]ChangeImpact, len(l.blocks), len(l.blocks)+1)
	copy(blocks, l.blocks)
	if k := len(blocks) - 1; k >= 0 && len(blocks[k]) < ImpactBlock {
		b := make([]ChangeImpact, len(blocks[k]), len(blocks[k])+1)
		copy(b, blocks[k])
		blocks[k] = append(b, cn)
	} else {
		blocks = append(blocks, []ChangeImpact{cn})
	}
	return Impacts{blocks: blocks, n: l.n + 1}
}

// Set is the list with the impact at a position replaced: the block holding it copied, the others shared.
func (l Impacts) Set(i int, cn ChangeImpact) Impacts {
	blocks := slices.Clone(l.blocks)
	k := i / ImpactBlock
	blocks[k] = slices.Clone(blocks[k])
	blocks[k][i%ImpactBlock] = cn
	return Impacts{blocks: blocks, n: l.n}
}

// Impacts are the change impacts of the change as a list: the view of the change it was read from when it shares it
// (ShareImpacts), else its Nodes. Every reader of the impacts of a change that may share its view reads them so.
func (c Change) Impacts() Impacts {
	if c.Nodes != nil || c.shared.n == 0 {
		return ImpactsOf(c.Nodes)
	}
	return c.shared
}

// ShareImpacts gives the change the impacts of the view it was read from, shared (Nodes is left empty): what a reader
// that never edits them and never hands the change over reads, with no copy (Impacts).
func (c *Change) ShareImpacts(l Impacts) {
	c.Nodes, c.shared = nil, l
}
