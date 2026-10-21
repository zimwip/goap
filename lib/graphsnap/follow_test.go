package graphsnap_test

import (
	"context"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/graphsnap"
)

// TestHeads: a head is asked once, then again only once a branch moved.
func TestHeads(t *testing.T) {
	ctx := context.Background()
	asked, head := 0, domain.BaselineID("b1")
	ask := func(context.Context, string, string) (domain.Baseline, error) {
		asked++
		return domain.Baseline{ID: head}, nil
	}
	var h graphsnap.Heads
	for range 3 {
		if b, err := h.Get(ctx, "organisation", "main", ask); err != nil || b.ID != "b1" {
			t.Fatalf("head = %v, %v", b, err)
		}
	}
	if asked != 1 {
		t.Fatalf("asked %d times, want once", asked)
	}
	head = "b2"
	if b, _ := h.Get(ctx, "organisation", "main", ask); b.ID != "b1" {
		t.Fatalf("no event, no new head: %v", b)
	}
	h.Moved()
	if b, _ := h.Get(ctx, "organisation", "main", ask); b.ID != "b2" || asked != 2 {
		t.Fatalf("after an event: %v, asked %d", b, asked)
	}
}
