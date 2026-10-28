package graph

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/zimwip/goap/contracts/domain"
)

// TestCommitScale measures a commit of a few edits as the namespace grows (GOAP_SCALE_NODES nodes, 0: skipped).
func TestCommitScale(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("GOAP_SCALE_NODES"))
	if n == 0 {
		t.Skip("GOAP_SCALE_NODES not set")
	}
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		g := New(repo)
		const batch = 50
		var keys []string
		var seed time.Duration
		seeds := 0
		commit := func(edits []NodeEdit) time.Duration {
			start := time.Now()
			if _, err := g.CommitEdits(ctx, Edits{ProjectID: "PROJ-ROOT", Title: "c", By: "bench", Edits: edits}); err != nil {
				t.Fatal(err)
			}
			return time.Since(start)
		}
		for len(keys) < n {
			var edits []NodeEdit
			for i := 0; i < batch && len(keys) < n; i++ {
				k := fmt.Sprintf("N-%d", len(keys))
				keys = append(keys, k)
				edits = append(edits, NodeEdit{Key: k, Type: "Need", Props: map[string]any{"title": k}})
			}
			seed += commit(edits)
			seeds++
		}
		t.Logf("%d nodes: seed commit of 50 edits %v", n, seed/time.Duration(seeds))
		var total time.Duration
		const rounds = 20
		for r := 0; r < rounds; r++ {
			var edits []NodeEdit
			for i := 0; i < 10; i++ {
				node, err := g.NodeByKey(ctx, domain.DefaultNamespace, keys[(r*10+i)%len(keys)])
				if err != nil {
					t.Fatal(err)
				}
				ref := node.Ref()
				edits = append(edits, NodeEdit{Pre: &ref, Key: node.Key, Type: node.Type, Props: map[string]any{"title": fmt.Sprintf("r%d", r)}})
			}
			total += commit(edits)
		}
		t.Logf("%d nodes: commit of 10 edits %v", n, total/rounds)
	})
}
