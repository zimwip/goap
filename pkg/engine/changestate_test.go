package engine

import (
	"context"
	"testing"

	"github.com/zimwip/goap/pkg/assess"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
)

// The current state of a change shows each process once, whatever the number of runs, and what a blocked step needs.
func TestChangeStateShowsOneStatePerProcess(t *testing.T) {
	ctx := context.Background()
	e := stagedEngine(t)
	m, err := methodology.Parse([]byte(describedYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(StaticMethodologies)["described"] = c
	ch, err := e.Graph.CreateChange(ctx, graph.NewChange{Title: "Note", Intent: "write a note", Methodology: "described", Namespace: "alm", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	rate := func(score int, typ string, fields map[string]domain.HeaderValue) {
		t.Helper()
		bb, _ := e.Graph.Blackboard(ctx, ch.ID)
		e.Accompany(ctx, TriggerEvent{Type: typ, Change: &bb.Change, Fields: fields})
		e.Drain()
		text, _ := assess.Text(bb, assess.SubjectDescription)
		p := companionOf(t, e, ch.ID)[0]
		if _, err := e.Submit(ctx, p.ID, []ItemInput{{Kind: string(assess.KindAssessment), Data: map[string]any{"key": "description", "score": score, "verdict": "unclear", "rated": text}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Run(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	rate(2, "change.created", nil)
	intent := "write the release note"
	if _, err := e.Graph.UpdateChange(ctx, ch.ID, graph.ChangePatch{Intent: &intent}); err != nil {
		t.Fatal(err)
	}
	rate(2, "change.edited", map[string]domain.HeaderValue{"intent": {From: "write a note", To: intent}})
	st, err := e.ChangeState(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Processes) != 2 {
		t.Fatalf("one state per process: %+v", st.Processes)
	}
	byName := map[string]ProcessState{}
	for _, p := range st.Processes {
		byName[p.Process] = p
	}
	review, delivery := byName["review"], byName["delivery"]
	if !review.Reactive || review.Runs != 2 || review.State != StepDone || len(review.Steps) != 1 || review.Steps[0].State != StepDone {
		t.Fatalf("review: reactive, run twice, done once: %+v", review)
	}
	note := delivery.Steps[0]
	if delivery.Runs != 0 || note.State != StepTodo || len(note.Needs) != 1 || note.Needs[0].Condition != "description_accepted" || !note.Needs[0].Want || note.Needs[0].Description == "" {
		t.Fatalf("delivery waits for an accepted description, and says so: %+v", note)
	}
}
