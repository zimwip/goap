package domain

import "testing"

func TestFoldImpacts(t *testing.T) {
	ref := func(v Version) *NodeRef { return &NodeRef{ID: "N", Version: v} }
	a := ChangeImpact{ID: "A", Key: "K", Type: "T", Intent: IntentModified, Rationale: "r", Pre: ref(1), Review: ReviewProposed, Execution: "x1"}
	b := ChangeImpact{ID: "B", Key: "K2", Type: "T", Intent: IntentCreated, Rationale: "r", Review: ReviewProposed, Flow: "F", Execution: "y1"}
	log := []ImpactEvent{
		{Op: ImpactDeclared, Impact: "A", State: &a, Execution: "x1"},
		{Op: ImpactWritten, Impact: "A", Post: ref(2), Execution: "x2"},
		{Op: ImpactReviewed, Impact: "A", Review: &Review{Status: ReviewAccepted, Comment: "ok", Execution: "x2"}},
		// the flow F replaces the run x2: it writes A again on its branch and declares B
		{Op: ImpactWritten, Impact: "A", Post: ref(3), Flow: "F", Execution: "y1"},
		{Op: ImpactDeclared, Impact: "B", State: &b, Flow: "F", Execution: "y1"},
	}
	for _, e := range log {
		if err := e.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	stored := FoldImpacts(log)
	if len(stored) != 2 || *stored[0].Post != *ref(2) || stored[0].Review != ReviewAccepted {
		t.Fatalf("stored = %+v", stored)
	}

	// the main flow does not see the candidate; the flow sees its own post and no review (made by a stale run)
	if main := ImpactsSeenBy(stored, log, nil, nil); len(main) != 1 {
		t.Fatalf("main view = %+v", main)
	}
	seen := ImpactsSeenBy(stored, log, []string{"F"}, map[string]bool{"x2": true})
	if len(seen) != 2 || *seen[0].Post != *ref(3) || seen[0].Review != ReviewProposed {
		t.Fatalf("flow view = %+v", seen)
	}
	// a sibling flow sees the main flow minus its own stale runs, not F
	if sib := ImpactsSeenBy(stored, log, []string{"G"}, map[string]bool{"x2": true}); len(sib) != 1 || sib[0].Post != nil {
		t.Fatalf("sibling view = %+v", sib)
	}

	// adoption: B becomes the main flow's, the review of the stale run is superseded
	adopted := ApplyImpactEvent(stored, ImpactEvent{Op: ImpactAdopted, Flow: "F", Stale: []string{"x2"}})
	if adopted[1].Flow != "" || !adopted[0].Reviews[0].Superseded || adopted[0].Review != ReviewProposed {
		t.Fatalf("adopted = %+v", adopted)
	}
	if stored[0].Reviews[0].Superseded {
		t.Fatal("ApplyImpactEvent modified its input")
	}
}
