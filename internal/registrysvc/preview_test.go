package registrysvc

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	registryv1 "github.com/zimwip/goap/gen/goap/registry/v1"
	"github.com/zimwip/goap/pkg/methodology"
)

// TestPreviewPlanHandler plans a trivial one-step process straight from the request's Methodology, the way a
// draft being edited (not yet saved) is graphed by GetProcessGraph.
func TestPreviewPlanHandler(t *testing.T) {
	m := methodology.Methodology{Name: "m", Version: "1", Namespace: "alm",
		Conditions: []methodology.Condition{{Name: "done", Expr: "false"}},
		Processes: []methodology.Process{{Name: "flow", Steps: []methodology.Step{
			{Name: "step1", Action: "work"},
		}}},
		Actions: []methodology.Action{{Name: "work", Kind: methodology.KindHuman, Instructions: "do it", Effects: map[string]bool{"done": true}}},
	}
	h := &Handler{}
	resp, err := h.PreviewPlan(context.Background(), connect.NewRequest(&registryv1.PreviewPlanRequest{
		Methodology: ToPB(Record{Methodology: m}), Agent: "flow", Goal: "flow",
	}))
	if err != nil {
		t.Fatal(err)
	}
	p := resp.Msg.Preview
	if p == nil || len(resp.Msg.Issues) != 0 {
		t.Fatalf("issues: %+v", resp.Msg.Issues)
	}
	if p.Reached || len(p.Actions) != 1 || p.Actions[0].Step != "flow/step1" || p.Actions[0].Kind != methodology.KindHuman {
		t.Fatalf("%+v", p)
	}
}

// TestPreviewPlanHandlerUnknownGoal: naming an agent or goal the methodology does not declare is a not-found RPC
// error, not a soft "issue" (the methodology itself compiles fine).
func TestPreviewPlanHandlerUnknownGoal(t *testing.T) {
	m := methodology.Methodology{Name: "m", Version: "1", Namespace: "alm",
		Conditions: []methodology.Condition{{Name: "done", Expr: "false"}},
		Processes:  []methodology.Process{{Name: "flow", Steps: []methodology.Step{{Name: "step1", Action: "work"}}}},
		Actions:    []methodology.Action{{Name: "work", Kind: methodology.KindHuman, Instructions: "do it", Effects: map[string]bool{"done": true}}},
	}
	h := &Handler{}
	_, err := h.PreviewPlan(context.Background(), connect.NewRequest(&registryv1.PreviewPlanRequest{
		Methodology: ToPB(Record{Methodology: m}), Agent: "nope", Goal: "nope",
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected CodeNotFound, got %v", err)
	}
}

// TestPreviewPlanHandlerLivePlanner: an llm/llm-scoring agent cannot be previewed; the methodology still compiles,
// so this is reported as an issue (like a validation problem the UI shows inline), not an RPC error.
func TestPreviewPlanHandlerLivePlanner(t *testing.T) {
	m := methodology.Methodology{Name: "m", Version: "1", Namespace: "alm",
		Conditions: []methodology.Condition{{Name: "done", Expr: "false"}},
		Goals:      []methodology.Goal{{Name: "g", Pre: map[string]bool{"done": true}}},
		Actions:    []methodology.Action{{Name: "work", Kind: methodology.KindHuman, Instructions: "do it", Effects: map[string]bool{"done": true}}},
		Agents:     []methodology.Agent{{Name: "a", Planner: methodology.PlannerLLM, Model: "fast", Actions: []string{"work"}, Goals: []string{"g"}}},
	}
	h := &Handler{}
	resp, err := h.PreviewPlan(context.Background(), connect.NewRequest(&registryv1.PreviewPlanRequest{
		Methodology: ToPB(Record{Methodology: m}), Agent: "a", Goal: "g",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.Preview != nil || len(resp.Msg.Issues) != 1 {
		t.Fatalf("%+v", resp.Msg)
	}
}
