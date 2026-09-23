package engine

import (
	"context"
	"testing"
	"time"
)

func TestEventTriggerStartsReviewerOnSameChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, base := agentsSetup(t)
	broker := NewBroker()
	e.Events = broker
	tm := &TriggerManager{Engine: e}
	tm.Start(ctx)
	go tm.WatchProcesses(ctx, broker)
	time.Sleep(20 * time.Millisecond) // subscription ready

	states := tm.States()
	if len(states) != 2 {
		t.Fatalf("expected 2 triggers, got %+v", states)
	}
	d, _ := e.Start(ctx, StartRequest{Methodology: "test-design", Agent: "test-designer", BaselineID: base, Intent: "concevoir"})
	d, _ = e.Run(ctx, d.ID)
	if d.Status != StatusCompleted {
		t.Fatalf("designer %s", d.Status)
	}
	var reviewer *Process
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && reviewer == nil {
		e.Drain()
		ps, _ := e.Store.List(ctx)
		for _, p := range ps {
			if p.Agent == "reviewer" && p.Status == StatusCompleted {
				reviewer = p
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if reviewer == nil {
		t.Fatalf("trigger did not start the reviewer: %+v", tm.States())
	}
	if reviewer.ChangeID != d.ChangeID || reviewer.Trigger != "test-design/reviewer/after_design" ||
		reviewer.Initiator.Subject != "system:trigger:test-design/reviewer/after_design" {
		t.Fatalf("unexpected triggered process %+v", reviewer)
	}
	// the reviewer completion does not match the filter: no loop
	e.Drain()
	time.Sleep(50 * time.Millisecond)
	n := 0
	ps, _ := e.Store.List(ctx)
	for _, p := range ps {
		if p.Trigger != "" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one triggered process, got %d", n)
	}
}

func TestManualFireOfScheduleTrigger(t *testing.T) {
	ctx := context.Background()
	e, _ := agentsSetup(t)
	tm := &TriggerManager{Engine: e}
	if err := tm.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := tm.Fire(ctx, "test-design", "test-designer", "nightly")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusRunning || p.Goal != "designed" || p.Trigger == "" {
		t.Fatalf("unexpected process %+v", p)
	}
	e.Drain()
	p, _ = e.Store.Get(ctx, p.ID)
	if p.Status != StatusCompleted {
		t.Fatalf("scheduled run %s %s", p.Status, p.Error)
	}
	bb, _ := e.Graph.Blackboard(ctx, p.ChangeID)
	if bb.Change.Data["trigger"] != "test-design/test-designer/nightly" {
		t.Fatalf("change not marked with its trigger: %v", bb.Change.Data)
	}
	if s := tm.States(); s[1].Fires != 1 || s[1].LastProcessID != p.ID {
		t.Fatalf("state %+v", s)
	}
	if _, err := tm.Fire(ctx, "test-design", "test-designer", "nope"); err == nil {
		t.Fatal("unknown trigger must fail")
	}
}
