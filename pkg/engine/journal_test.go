package engine

import (
	"context"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestJournalRecordsTicksAndActions(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	p, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", BaselineID: base, Intent: "Le PSP change d'API, qu'est-ce que ça casse ?"})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != StatusCompleted {
		t.Fatalf("run: %v %+v", err, p)
	}
	recs, err := g.Journal(ctx, domain.ExecutionFilter{ChangeID: p.ChangeID})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	byID := map[string]domain.ExecutionRecord{}
	for i, r := range recs {
		kinds = append(kinds, r.Kind)
		byID[r.ID] = r
		if r.Seq != i+1 || r.ProcessID != p.ID || r.MethodologyVersion == "" {
			t.Fatalf("record %d: %+v", i, r)
		}
	}
	want := []string{"process.started", "tick", "action", "tick", "action", "tick", "action", "tick", "process.ended"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds %v", kinds)
		}
	}
	act := recs[2]
	if act.Action != "identify_impacts" || act.ActionKind != "llm" || len(act.ModelCalls) != 1 || act.EffectsMet == nil || !*act.EffectsMet || len(act.Items) != 1 {
		t.Fatalf("action record: %+v", act)
	}
	// provenance: every item produced by an action points to its record
	c, _ := g.Change(ctx, p.ChangeID)
	for _, it := range c.Items {
		r, ok := byID[it.Execution]
		if !ok || r.Action != it.ProducedBy {
			t.Fatalf("item %s (%s) without journal provenance", it.ID, it.ProducedBy)
		}
	}
	if end := recs[len(recs)-1]; end.Status != string(StatusCompleted) || end.Data["steps"] != 3 {
		t.Fatalf("end record: %+v", end)
	}
	if recs[1].Plan[0] != "identify_impacts" || recs[1].Data["replanned"] != false {
		t.Fatalf("tick: %+v", recs[1])
	}
}
