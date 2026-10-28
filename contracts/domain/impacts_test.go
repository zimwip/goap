package domain

import (
	"fmt"
	"slices"
	"testing"
)

func impactsN(n int) []ChangeImpact {
	out := make([]ChangeImpact, n)
	for i := range out {
		out[i] = ChangeImpact{ID: ChangeImpactID(fmt.Sprint("i", i)), Key: fmt.Sprint("k", i)}
	}
	return out
}

// A list reads its impacts in their order, and a list made from it by Append or Set leaves it as it was.
func TestImpactsShareTheirBlocks(t *testing.T) {
	src := impactsN(ImpactBlock*2 + 3)
	l := ImpactsOf(src)
	if l.Len() != len(src) || !slices.Equal(ids(l.Slice()), ids(src)) {
		t.Fatalf("list %v, want %v", ids(l.Slice()), ids(src))
	}
	for i, cn := range l.All() {
		if l.At(i).ID != cn.ID || cn.ID != src[i].ID {
			t.Fatalf("at %d: %s / %s, want %s", i, l.At(i).ID, cn.ID, src[i].ID)
		}
	}
	if got := l.Index("i17"); got != 17 {
		t.Fatalf("index of i17 = %d", got)
	}
	a := l.Append(ChangeImpact{ID: "new"})
	b := l.Append(ChangeImpact{ID: "other"})
	s := l.Set(5, ChangeImpact{ID: "set"})
	if l.Len() != len(src) || l.At(5).ID != "i5" || l.Index("new") >= 0 {
		t.Fatalf("the list changed: %v", ids(l.Slice()))
	}
	if a.At(a.Len()-1).ID != "new" || b.At(b.Len()-1).ID != "other" || s.At(5).ID != "set" || src[5].ID != "i5" {
		t.Fatalf("append / set: %s %s %s", a.At(a.Len()-1).ID, b.At(b.Len()-1).ID, s.At(5).ID)
	}
	var empty Impacts
	if empty.Len() != 0 || empty.Slice() != nil || empty.Append(ChangeImpact{ID: "x"}).At(0).ID != "x" {
		t.Fatal("the empty list")
	}
}

// A change reads the impacts it shares with a view until it is given its own.
func TestChangeImpacts(t *testing.T) {
	var c Change
	c.ShareImpacts(ImpactsOf(impactsN(3)))
	if c.Nodes != nil || c.Impacts().Len() != 3 {
		t.Fatalf("shared: %d", c.Impacts().Len())
	}
	c.Nodes = impactsN(1)
	if c.Impacts().Len() != 1 {
		t.Fatalf("own: %d", c.Impacts().Len())
	}
	c.ShareImpacts(Impacts{})
	if c.Impacts().Len() != 0 {
		t.Fatal("cleared")
	}
}

func ids(l []ChangeImpact) []ChangeImpactID {
	out := make([]ChangeImpactID, len(l))
	for i, cn := range l {
		out[i] = cn.ID
	}
	return out
}
