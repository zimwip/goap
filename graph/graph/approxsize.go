package graph

import (
	"github.com/zimwip/goap/contracts/domain"
)

// The sizes below are estimates of the memory a value holds, for the bounds of the caches (draftCache): the bytes of
// its texts and a fixed cost per value, map entry and struct, not an exact count. They err high rather than low.

const (
	valueBytes  = 16  // a scalar or an interface
	entryBytes  = 48  // a map entry or a slice element beyond its value
	structBytes = 160 // the fixed fields of an event, a draft or an impact
)

// approxSize estimates the memory a decoded JSON-like value holds.
func approxSize(v any) int64 {
	switch v := v.(type) {
	case nil:
		return valueBytes
	case string:
		return valueBytes + int64(len(v))
	case map[string]any:
		n := int64(valueBytes)
		for k, x := range v {
			n += entryBytes + int64(len(k)) + approxSize(x)
		}
		return n
	case []any:
		n := int64(valueBytes)
		for _, x := range v {
			n += entryBytes + approxSize(x)
		}
		return n
	case []string:
		n := int64(valueBytes)
		for _, x := range v {
			n += entryBytes + int64(len(x))
		}
		return n
	default:
		return valueBytes
	}
}

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
