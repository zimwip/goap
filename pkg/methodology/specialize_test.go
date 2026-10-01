package methodology

import "testing"

// RankByPriority is the generic Activity-specialization mechanism: filters to what's applicable, then orders
// by priority descending, ties keeping declaration order (a stable sort) - the shape both Action specialization
// (pkg/engine) and Method capability-matching (MethodsFor) share.
func TestRankByPriority(t *testing.T) {
	type item struct {
		name     string
		ok       bool
		priority int
	}
	items := []item{
		{"low", true, 1},
		{"excluded", false, 100},
		{"high", true, 10},
		{"tie-a", true, 5},
		{"tie-b", true, 5},
	}
	got := RankByPriority(items, func(i item) bool { return i.ok }, func(i item) int { return i.priority })
	want := []string{"high", "tie-a", "tie-b", "low"}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, w := range want {
		if got[i].name != w {
			t.Fatalf("position %d: got %q, want %q (full: %+v)", i, got[i].name, w, got)
		}
	}
}

func TestRankByPriorityEmpty(t *testing.T) {
	got := RankByPriority([]int{1, 2, 3}, func(int) bool { return false }, func(i int) int { return i })
	if len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
