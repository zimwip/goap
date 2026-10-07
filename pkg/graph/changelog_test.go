package graph

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/domain"
)

func TestChangeLog(t *testing.T) { forEachRepo(t, testChangeLog) }

// Facts, journal records and impact events are entries of one log, in one order, filtered on their columns (ADR 0030).
func testChangeLog(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newFlowWorld(t, repo) // impact events of runs e1, e2 on the main flow, then a flow opened (a fact)
	g, c := w.g, w.change
	// the graph knows no use case: the entries of the execution journal (pkg/journal) are entries of a stream of its own
	rec := func(kind, process, flow, action string) domain.LogEntry {
		id := uuid.NewString()
		return domain.LogEntry{ID: id, Change: c.ID, Type: "journal." + kind, Process: process, Flow: flow, Execution: id, Subject: action,
			At: g.now(), Payload: []byte(`{}`)}
	}
	onFlow := rec("action", "p2", w.flow, "identify_scope")
	if err := g.AppendLog(ctx, []domain.LogEntry{rec("schedule", "p1", "", ""), rec("action", "p1", "", "identify_scope"), onFlow}); err != nil {
		t.Fatal(err)
	}
	// the streams of the graph itself are written through its own operations only, and a change must exist
	if err := g.AppendLog(ctx, []domain.LogEntry{{Change: c.ID, Type: domain.LogFact + ".artifact", Payload: []byte(`{}`)}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("forged fact: %v", err)
	}
	if err := g.AppendLog(ctx, []domain.LogEntry{{Change: c.ID, Type: domain.LogImpact + ".written", Payload: []byte(`{}`)}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("forged impact event: %v", err)
	}
	if err := g.AppendLog(ctx, []domain.LogEntry{{Change: "unknown", Type: "journal.tick", Payload: []byte(`{}`)}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown change: %v", err)
	}
	if err := g.AppendLog(ctx, []domain.LogEntry{{Change: c.ID, Type: "journal", Payload: []byte(`{}`)}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no kind: %v", err)
	}
	if _, _, err := g.ChangeLog(ctx, domain.LogFilter{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a log read needs a change or processes: %v", err)
	}
	if got, _, err := g.ChangeLog(ctx, domain.LogFilter{Processes: []string{"p2"}}); err != nil || len(got) != 1 {
		t.Fatalf("by process alone: %v %v", got, err)
	}
	all, counts, err := g.ChangeLog(ctx, domain.LogFilter{Change: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	streams := map[string]int{}
	for i, e := range all {
		streams[e.Stream()]++
		if i > 0 && e.Seq <= all[i-1].Seq {
			t.Fatalf("the log is not in one order: %d after %d", e.Seq, all[i-1].Seq)
		}
	}
	if streams[domain.LogImpact] == 0 || streams[domain.LogFact] == 0 || streams["journal"] != 3 {
		t.Fatalf("streams = %v", streams)
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	if total != len(all) || counts["journal.action"] != 2 {
		t.Fatalf("counts = %v for %d entries", counts, len(all))
	}
	count := func(f domain.LogFilter) []domain.LogEntry {
		t.Helper()
		f.Change = c.ID
		out, _, err := g.ChangeLog(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range out {
			if !f.Match(e) {
				t.Fatalf("entry %d (%s) does not match %+v", e.Seq, e.Type, f)
			}
		}
		return out
	}
	if n := len(count(domain.LogFilter{Types: []string{"journal."}})); n != 3 {
		t.Fatalf("journal stream: %d", n)
	}
	if got := count(domain.LogFilter{Types: []string{"journal.action", "impact.checkedOut"}}); len(got) != 3 {
		t.Fatalf("two types: %d entries", len(got))
	}
	if got := count(domain.LogFilter{Flows: []string{w.flow}}); len(got) != 2 || !slices.ContainsFunc(got, func(e domain.LogEntry) bool { return e.Type == "fact.flow" }) {
		t.Fatalf("flow entries: %+v", got) // its opening and its action
	}
	if got := count(domain.LogFilter{Flows: []string{""}, Types: []string{"journal."}}); len(got) != 2 {
		t.Fatalf("main flow journal: %d", len(got))
	}
	if got := count(domain.LogFilter{Processes: []string{"p2"}}); len(got) != 1 || got[0].ID != onFlow.ID {
		t.Fatalf("by process: %+v", got)
	}
	if got := count(domain.LogFilter{Execution: "e1"}); len(got) != 4 { // proposed, checked out, updated, reviewed by run e1
		t.Fatalf("by action run: %d", len(got))
	}
	if got := count(domain.LogFilter{AfterSeq: all[2].Seq, Limit: 2}); len(got) != 2 || got[0].Seq != all[3].Seq {
		t.Fatalf("following the log: %+v", got)
	}
}

func TestUpdateChangeIsLogged(t *testing.T) { forEachRepo(t, testUpdateChangeIsLogged) }

// An edit of the header of a change (title, intent, goal, status, data) is an entry of its log, in the same
// transaction; an edit that changes nothing writes none, and the stream is the graph's own.
func testUpdateChangeIsLogged(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newFlowWorld(t, repo)
	g, c := w.g, w.change
	headers := func() []domain.LogEntry {
		out, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: c.ID, Types: []string{domain.LogChange + "."}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := len(headers())
	title := "A new title"
	if _, err := g.UpdateChange(ctx, c.ID, ChangePatch{Title: &title, Data: map[string]any{"criticality": "high"}}); err != nil {
		t.Fatal(err)
	}
	got := headers()
	if len(got) != before+1 || got[len(got)-1].Type != "change.updated" || got[len(got)-1].Subject != "data.criticality,title" {
		t.Fatalf("header entries = %+v", got)
	}
	if _, err := g.UpdateChange(ctx, c.ID, ChangePatch{Title: &title}); err != nil {
		t.Fatal(err)
	}
	if len(headers()) != before+1 {
		t.Fatal("an edit that changes nothing is logged")
	}
	if err := g.AppendLog(ctx, []domain.LogEntry{{Change: c.ID, Type: domain.LogChange + ".updated", Payload: []byte(`{}`)}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("forged header entry: %v", err)
	}
}
