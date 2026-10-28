package pbconv

import (
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

func TestCommitRoundTrip(t *testing.T) {
	b := domain.Commit{ID: "B1", Name: "b", Branch: "main", Namespace: "alm", Nodes: map[domain.NodeID]domain.Version{"N1": 2}}
	got := CommitFromPB(CommitToPB(b))
	if got.Namespace != "alm" || got.Branch != "main" || got.Nodes["N1"] != 2 {
		t.Fatalf("round trip = %+v", got)
	}
}
