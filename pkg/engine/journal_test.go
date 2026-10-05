package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/journal"
	"github.com/zimwip/goap/pkg/methodology"
)

func TestJournalRecordsTicksAndActions(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	// the intent text deliberately echoes the "assess_impact" goal example
	// in methodologies/examples/impact-analysis.yaml.
	p, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", BaselineID: base, Intent: "The PSP changes its API, what does this break?", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != StatusCompleted {
		t.Fatalf("run: %v %+v", err, p)
	}
	recs, err := journal.Read(ctx, g, journal.Filter{ChangeID: p.ChangeID})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	byID := map[string]journal.Record{}
	for i, r := range recs {
		kinds = append(kinds, r.Kind)
		byID[r.ID] = r
		if r.Seq != i+1 || r.ProcessID != p.ID || r.MethodologyVersion == "" {
			t.Fatalf("record %d: %+v", i, r)
		}
	}
	// attach (ADR 0031, gap 5) precedes process.started: the change is bound
	// eagerly inside Start, before Run's own first cycle ever runs.
	want := []string{"attach", "process.started", "schedule", "tick", "action", "tick", "action", "tick", "action", "tick", "process.ended"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds %v", kinds)
		}
	}
	// the run says why it happens: the process was started, queued, then picked up
	if sch := recs[2]; sch.Data["reason"] != "started" || sch.EndedAt.Before(sch.StartedAt) {
		t.Fatalf("schedule record: %+v", sch)
	}
	act := recs[4]
	if act.Action != "identify_impacts" || act.ActionKind != "llm" || len(act.ModelCalls) != 1 || act.EffectsMet == nil || !*act.EffectsMet || len(act.Nodes) != 1 {
		t.Fatalf("action record: %+v", act)
	}
	// the prompt of the call is an entry of the log of the change, linked to the action run; the record and the
	// process keep the counts only
	entries, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: p.ChangeID, Types: []string{journal.StreamModel + ".call"}, Execution: act.ID})
	if err != nil || len(entries) != 1 || entries[0].Subject != "0" || entries[0].Process != p.ID {
		t.Fatalf("model entries: %+v %v", entries, err)
	}
	var ex journal.ModelExchange
	if err := json.Unmarshal(entries[0].Payload, &ex); err != nil || ex.System == "" || len(ex.Messages) == 0 || ex.Response == "" {
		t.Fatalf("exchange: %+v %v", ex, err)
	}
	if act.ModelCalls[0].Exchange != nil {
		t.Fatalf("the record holds the prompt")
	}
	for _, st := range p.Steps {
		for _, c := range st.LLMCalls {
			if c.Exchange != nil {
				t.Fatalf("the process holds a prompt: step %d", st.Index)
			}
		}
	}
	// provenance: every item produced by an action points to its record
	c, _ := g.Change(ctx, p.ChangeID)
	for _, it := range c.Items {
		r, ok := byID[it.Execution]
		if !ok || r.Action != it.ProducedBy {
			t.Fatalf("item %s (%s) without journal provenance", it.ID, it.ProducedBy)
		}
	}
	for _, n := range c.Nodes {
		r, ok := byID[n.Execution]
		if !ok || r.Action != n.ProducedBy {
			t.Fatalf("change impact %s (%s) without journal provenance", n.Key, n.ProducedBy)
		}
	}
	if end := recs[len(recs)-1]; end.Status != string(StatusCompleted) || end.Data["steps"] != float64(3) { // JSON numbers, as every store returns them
		t.Fatalf("end record: %+v", end)
	}
	if recs[3].Plan[0] != "identify_impacts" || recs[3].Data["replanned"] != false {
		t.Fatalf("tick: %+v", recs[3])
	}
}

// A step starts from a blackboard state, reads existing nodes and produces the
// next state: the marks of consecutive steps chain, and the items produced by a
// step account for the growth of the board.
func TestJournalStepsChainBlackboardStates(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	p, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", BaselineID: base, Intent: "The PSP changes its API, what does this break?", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != StatusCompleted {
		t.Fatalf("run: %v %+v", err, p)
	}
	recs, err := journal.Read(ctx, g, journal.Filter{ChangeID: p.ChangeID})
	if err != nil {
		t.Fatal(err)
	}
	var acts []journal.Record
	for _, r := range recs {
		if r.Kind == journal.KindAction {
			acts = append(acts, r)
		}
	}
	if len(acts) < 2 {
		t.Fatalf("expected several actions, got %d", len(acts))
	}
	sawReads := false
	for i, a := range acts {
		if a.BoardAfter-a.BoardBefore != len(a.Items) {
			t.Fatalf("action %s: board %d -> %d but %d items", a.Action, a.BoardBefore, a.BoardAfter, len(a.Items))
		}
		if i > 0 && a.BoardBefore != acts[i-1].BoardAfter {
			t.Fatalf("action %s starts from board %d, previous ended at %d", a.Action, a.BoardBefore, acts[i-1].BoardAfter)
		}
		sawReads = sawReads || len(a.Reads) > 0
	}
	if !sawReads {
		t.Fatal("no step recorded the node versions it read")
	}
}

// An action generated for a process step journals its Activity Run against that step's path (architecture plan
// "Activity concept": ExecutionRecord.ActivityRef), not just its own action name.
func TestJournalRecordsActivityRef(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	m, err := methodology.Parse([]byte(stagedYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(StaticMethodologies)["staged"] = c
	p, err := e.Start(ctx, StartRequest{Methodology: "staged", Goal: "delivery", Intent: "deliver the note", ProjectID: testProject, BaselineID: base})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusWaiting || p.Pending.Action != "delivery/prepare/note" {
		t.Fatalf("expected the note step, got %s %+v", p.Status, p.Pending)
	}
	writer := authz.With(ctx, authz.Principal{Subject: "w", Roles: []string{"writer@ORG-DEFAULT"}})
	if _, err := e.Submit(writer, p.ID, []ItemInput{{Kind: "artifact", Type: "note"}}); err != nil {
		t.Fatal(err)
	}
	recs, err := journal.Read(ctx, g, journal.Filter{ChangeID: p.ChangeID})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range recs {
		if r.Kind == journal.KindAction && r.Action == "delivery/prepare/note" {
			found = true
			if r.ActivityRef != "delivery/prepare/note" {
				t.Fatalf("activityRef = %q, want the step path", r.ActivityRef)
			}
		}
	}
	if !found {
		t.Fatal("no action record for the note step")
	}
}
