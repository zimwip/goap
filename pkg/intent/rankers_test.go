package intent

import (
	"context"
	"testing"

	"github.com/zimwip/goap/pkg/llm"
)

// The ranker declares itself to the ledger of the gateway (ADR 0089).
func TestLLMRankerStampsItsSource(t *testing.T) {
	var got llm.CallMeta
	r := LLMRanker{Client: llm.ClientFunc(func(ctx context.Context, _ llm.Request) (llm.Response, error) {
		got = llm.MetaFrom(ctx)
		return llm.Response{Text: `{"candidates":[]}`}, nil
	})}
	if _, err := r.Rank(context.Background(), []Turn{{Role: "user", Text: "x"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got.Source != llm.SourceIntent {
		t.Fatalf("%+v", got)
	}
}
