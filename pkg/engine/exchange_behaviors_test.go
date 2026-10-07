package engine

import (
	"testing"

	"github.com/zimwip/goap/pkg/llm"
)

// The log of the change records the system text the engine built and the behaviours the gateway added to it (ADR 0093).
func TestExchangeRecordsGatewayBehaviors(t *testing.T) {
	x := exchangeOf(llm.Request{System: "S", Messages: []llm.Message{{Role: "user", Content: "q"}}}, llm.Response{Text: "a", Behaviors: []string{"terse", "!big"}, BehaviorTokens: 40})
	if x.System != "S" || len(x.Behaviors) != 2 || x.Behaviors[0] != "terse" || x.BehaviorTokens != 40 {
		t.Fatalf("%+v", x)
	}
}
