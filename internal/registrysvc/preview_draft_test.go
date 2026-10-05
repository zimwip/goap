package registrysvc

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	registryv1 "github.com/zimwip/goap/gen/goap/registry/v1"
	"github.com/zimwip/goap/pkg/methodology"
)

// A draft with a broken rule elsewhere is still previewed (like its graph), the issues coming with the plan.
func TestPreviewPlanHandlerOfADraftWithIssues(t *testing.T) {
	m := methodology.Methodology{Name: "m", Version: "1", Namespace: "alm",
		Conditions: []methodology.Condition{{Name: "done", Expr: "false"}},
		Processes:  []methodology.Process{{Name: "flow", Steps: []methodology.Step{{Name: "step1", Action: "work"}}}},
		Actions: []methodology.Action{
			{Name: "work", Kind: methodology.KindHuman, Instructions: "do it", Effects: map[string]bool{"done": true}},
			{Name: "broken", Kind: methodology.KindHuman, Instructions: "x", Pre: map[string]bool{"nope": true}, Effects: map[string]bool{"done": true}},
		},
	}
	h := &Handler{}
	resp, err := h.PreviewPlan(context.Background(), connect.NewRequest(&registryv1.PreviewPlanRequest{
		Methodology: ToPB(Record{Methodology: m}), Agent: "flow", Goal: "flow",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.Preview == nil || len(resp.Msg.Issues) == 0 {
		t.Fatalf("want a plan with issues: %+v", resp.Msg)
	}
}
