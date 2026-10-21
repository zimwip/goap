package graph

import (
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/cache"
)

// The sizes below are estimates of the memory a value holds, for the bounds of the caches (draftCache): the bytes of
// its texts and a fixed cost per value, map entry and struct, not an exact count. They err high rather than low.

const (
	valueBytes  = cache.WordBytes   // a scalar or an interface
	entryBytes  = cache.SlotBytes   // a map entry or a slice element beyond its value
	structBytes = cache.StructBytes // the fixed fields of an event, a draft or an impact
)

// approxSize estimates the memory a decoded JSON-like value holds.
func approxSize(v any) int64 { return cache.Of(v) }

func draftSize(d *domain.Draft) int64 {
	if d == nil {
		return 0
	}
	n := structBytes + int64(len(d.Key)+len(d.Type)+len(d.State)+len(d.Execution)) + approxSize(d.Properties)
	n += int64(len(d.Origins)) * entryBytes
	for _, l := range d.Links {
		n += structBytes + int64(len(l.Type)) + approxSize(l.Properties)
	}
	return n
}

func impactSize(c *domain.ChangeImpact) int64 {
	if c == nil {
		return 0
	}
	return structBytes + int64(len(c.Rationale)+len(c.Key)+len(c.Type)) + int64(len(c.Reviews))*structBytes
}

// eventSize estimates what folding an event adds to a view: the event kept in its order, and the draft and the impact
// it installs or patches.
func eventSize(e domain.ImpactEvent) int64 {
	n := structBytes + int64(len(e.ID)+len(e.By)+len(e.Execution)) + approxSize(e.Patch) + impactSize(e.State)
	// the draft is held twice: by the event and by the drafts of the view
	n += 2 * draftSize(e.Draft)
	if e.Review != nil {
		n += structBytes + int64(len(e.Review.Comment))
	}
	return n
}

// itemSize estimates what a fact adds to a view.
func itemSize(it domain.ChangeItem) int64 {
	return structBytes + int64(len(it.Target)) + approxSize(it.Data)
}
