package engine

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/llm"
)

func TestCompact(t *testing.T) {
	big := strings.Repeat("x", 5000)
	msgs := []llm.Message{{Role: "user", Content: "the task " + big}}
	for range 20 {
		msgs = append(msgs, llm.Message{Role: "assistant", Content: "calling"},
			llm.Message{Role: "user", ToolResults: []llm.ToolResult{{CallID: "c", Content: big}}})
	}
	if got := compact(msgs, 1<<20); len(got[2].ToolResults[0].Content) != len(big) {
		t.Fatal("a conversation under the limit is compacted")
	}
	got := compact(msgs, 50_000)
	if len(got) != len(msgs) || got[0].Content != msgs[0].Content {
		t.Fatal("the task or the structure changed")
	}
	if c := got[2].ToolResults[0].Content; !strings.HasSuffix(c, elided) || len(c) > 300 {
		t.Fatalf("old result: %d bytes", len(c))
	}
	if c := got[len(got)-1].ToolResults[0].Content; c != big {
		t.Fatal("a latest result was elided")
	}
	if msgs[2].ToolResults[0].Content != big {
		t.Fatal("compact changed its input")
	}
	// compacting again changes nothing
	again := compact(got, 50_000)
	if again[2].ToolResults[0].Content != got[2].ToolResults[0].Content {
		t.Fatal("compaction is not stable")
	}
}

func TestCapResultKeepsTheTail(t *testing.T) {
	s := "BEGIN" + strings.Repeat("a", 1000) + "the build failed here"
	got := capResult(s, 300)
	if !strings.HasPrefix(got, "BEGIN") || !strings.HasSuffix(got, "the build failed here") || !strings.Contains(got, "bytes cut") || len(got) > 350 {
		t.Fatalf("cap: %q", got)
	}
}
