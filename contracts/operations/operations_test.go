package operations

import (
	"slices"
	"testing"
)

func TestCatalogue(t *testing.T) {
	seen := map[string]bool{}
	for _, o := range All() {
		if seen[o.Code] {
			t.Fatalf("duplicate %s", o.Code)
		}
		seen[o.Code] = true
		if !slices.Contains(Kinds(), o.Kind) || o.Method == "" {
			t.Fatalf("operation %+v", o)
		}
	}
	if o, ok := Lookup(ImpactReview); !ok || o.Kind != Reviewer {
		t.Fatalf("impact.review: %+v %v", o, ok)
	}
	if !Known("reviewer") || !Known(ImpactUpdate) || Known("impact.nope") {
		t.Fatal("Known")
	}
	if got := Expand([]string{"reviewer"}); !slices.Equal(got, []string{ImpactReview}) {
		t.Fatalf("Expand(reviewer) = %v", got)
	}
	if !Allows([]string{"doer"}, ImpactWithdraw) || Allows([]string{"reviewer"}, ImpactWithdraw) || !Allows([]string{ImpactWithdraw}, ImpactWithdraw) {
		t.Fatal("Allows")
	}
}
