package pbconv

import (
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestBaselineRoundTrip(t *testing.T) {
	b := domain.Baseline{ID: "B1", Name: "b", Branch: "main", Namespace: "alm", Nodes: map[domain.NodeID]domain.Version{"N1": 2}}
	got := BaselineFromPB(BaselineToPB(b))
	if got.Namespace != "alm" || got.Branch != "main" || got.Nodes["N1"] != 2 {
		t.Fatalf("round trip = %+v", got)
	}
}
