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
	// flat is the list of a slice (ImpactsOf); else blocks, of pointers to impacts never edited, so that a copy of a
	// block copies pointers, not impacts
	flat   []ChangeImpact
	blocks [][]*ChangeImpact
	n      int
	// look, when set, finds the impacts of the first looked positions without a walk (IndexedImpacts); the positions
	// after them (Append) are walked
	look   ImpactLookup
	looked int
}

// ImpactLookup finds the impacts of a list by what they name, as a walk of the list would: the view of a change keeps
// its impacts indexed and gives its lists this, so that finding one impact of a change of N impacts does not cost N.
// Positions it answers past the list are ignored; the impacts it answers are checked by the caller as a walk would.
type ImpactLookup interface {
	// Position is the position of the impact of an id.
	Position(id ChangeImpactID) (int, bool)
	// OfNode are the positions of the impacts naming a node (its Pre or its Post), in order.
	OfNode(id NodeID) []int
	// OfKey are the positions of the impacts of a key, in order.
	OfKey(key string) []int
	// Odd is the number of impacts superseded or of a flow.
	Odd() int
}

// ImpactsOf is the list of the impacts of a slice, which it shares: the slice must not be edited while the list is read.
func ImpactsOf(s []ChangeImpact) Impacts {
	if len(s) == 0 {
		return Impacts{}
	}
	return Impacts{flat: s, n: len(s)}
}

// ImpactsOfBlocks is the list of blocks of ImpactBlock impacts each but the last, which it shares: the holder of the
// blocks keeps them as they are while the list is read (a write of the view copies the block first), and never edits
// an impact they point to.
func ImpactsOfBlocks(blocks [][]*ChangeImpact, n int) Impacts { return Impacts{blocks: blocks, n: n} }

// IndexedImpacts is ImpactsOfBlocks with the lookup of the holder of the blocks, which answers for these n impacts.
func IndexedImpacts(blocks [][]*ChangeImpact, n int, look ImpactLookup) Impacts {
	return Impacts{blocks: blocks, n: n, look: look, looked: n}
}

// Len is the number of impacts of the list.
func (l Impacts) Len() int { return l.n }

// At is the impact at a position of the list.
func (l Impacts) At(i int) ChangeImpact {
	if l.flat != nil {
		return l.flat[i]
	}
	return *l.blocks[i/ImpactBlock][i%ImpactBlock]
}

// All iterates over the impacts of the list in their order, with their positions.
func (l Impacts) All() iter.Seq2[int, ChangeImpact] {
	return func(yield func(int, ChangeImpact) bool) {
		if l.flat != nil {
			for i, cn := range l.flat {
				if !yield(i, cn) {
					return
				}
			}
			return
		}
		i := 0
		for _, b := range l.blocks {
			for _, cn := range b {
				if !yield(i, *cn) {
					return
				}
				i++
			}
		}
	}
}

// Index is the position of the impact of an id, -1 when the list holds none.
func (l Impacts) Index(id ChangeImpactID) int {
	if l.look != nil {
		if i, ok := l.look.Position(id); ok && i < l.looked && l.At(i).ID == id {
			return i
		}
		for i := l.looked; i < l.n; i++ {
			if l.At(i).ID == id {
				return i
			}
		}
		return -1
	}
	for i, cn := range l.All() {
		if cn.ID == id {
			return i
		}
	}
	return -1
}

// OfNode iterates in order over the impacts that may name a node (its Pre or its Post): those the lookup answers, else
// every impact; the caller checks each as a walk of the list would.
func (l Impacts) OfNode(id NodeID) iter.Seq2[int, ChangeImpact] {
	if l.look == nil {
		return l.All()
	}
	return l.near(l.look.OfNode(id))
}

// OfKey iterates in order over the impacts that may have a key, as OfNode does.
func (l Impacts) OfKey(key string) iter.Seq2[int, ChangeImpact] {
	if l.look == nil {
		return l.All()
	}
	return l.near(l.look.OfKey(key))
}

// near iterates over the positions a lookup answers within the list, then over the impacts appended after them.
func (l Impacts) near(at []int) iter.Seq2[int, ChangeImpact] {
	return func(yield func(int, ChangeImpact) bool) {
		for _, i := range at {
			if i < l.looked && !yield(i, l.At(i)) {
				return
			}
		}
		for i := l.looked; i < l.n; i++ {
			if !yield(i, l.At(i)) {
				return
			}
		}
	}
}

// Plain tells whether no impact of the list is superseded or of a flow (the main flow sees such a list as it is).
func (l Impacts) Plain() bool {
	if l.look != nil {
		if l.look.Odd() > 0 {
			return false
		}
		for i := l.looked; i < l.n; i++ {
			if cn := l.At(i); cn.Superseded || cn.Flow != "" {
				return false
			}
		}
		return true
	}
	return l.Find(func(cn ChangeImpact) bool { return cn.Superseded || cn.Flow != "" }) < 0
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
	if l.flat != nil {
		return slices.Clone(l.flat)
	}
	out := make([]ChangeImpact, 0, l.n)
	for _, b := range l.blocks {
		for _, cn := range b {
			out = append(out, *cn)
		}
	}
	return out
}

// pointed is the list in blocks of pointers (a list of a slice points into it).
func (l Impacts) pointed() [][]*ChangeImpact {
	if l.flat == nil {
		return l.blocks
	}
	blocks := make([][]*ChangeImpact, 0, (l.n+ImpactBlock-1)/ImpactBlock)
	for i := 0; i < l.n; i += ImpactBlock {
		b := make([]*ChangeImpact, 0, ImpactBlock)
		for j := i; j < min(i+ImpactBlock, l.n); j++ {
			b = append(b, &l.flat[j])
		}
		blocks = append(blocks, b)
	}
	return blocks
}

// Append is the list with an impact more: its last block copied, the others shared.
func (l Impacts) Append(cn ChangeImpact) Impacts {
	own := l.pointed()
	blocks := make([][]*ChangeImpact, len(own), len(own)+1)
	copy(blocks, own)
	if k := len(blocks) - 1; k >= 0 && len(blocks[k]) < ImpactBlock {
		b := make([]*ChangeImpact, len(blocks[k]), len(blocks[k])+1)
		copy(b, blocks[k])
		blocks[k] = append(b, &cn)
	} else {
		blocks = append(blocks, []*ChangeImpact{&cn})
	}
	return Impacts{blocks: blocks, n: l.n + 1, look: l.look, looked: l.looked}
}

// Set is the list with the impact at a position replaced: the block holding it copied, the others shared.
func (l Impacts) Set(i int, cn ChangeImpact) Impacts {
	blocks := slices.Clone(l.pointed())
	k := i / ImpactBlock
	blocks[k] = slices.Clone(blocks[k])
	blocks[k][i%ImpactBlock] = &cn
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
