package graph

import (
	"context"
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
	rec := func(kind, process, flow, action string) domain.ExecutionRecord {
		return domain.ExecutionRecord{ID: uuid.NewString(), ChangeID: c.ID, ProcessID: process, Kind: kind, Flow: flow, Action: action, StartedAt: g.now()}
	}
	onFlow := rec(domain.ExecAction, "p2", w.flow, "identify_scope")
	if err := g.Record(ctx, []domain.ExecutionRecord{rec(domain.ExecSchedule, "p1", "", ""), rec(domain.ExecAction, "p1", "", "identify_scope"), onFlow}); err != nil {
		t.Fatal(err)
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
	if streams[domain.LogImpact] == 0 || streams[domain.LogFact] == 0 || streams[domain.LogJournal] != 3 {
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
	if got := count(domain.LogFilter{Types: []string{"journal.action", "impact.written"}}); len(got) != 4 {
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
	if got := count(domain.LogFilter{Execution: "e1"}); len(got) != 3 { // declared, written, reviewed by run e1
		t.Fatalf("by action run: %d", len(got))
	}
	if got := count(domain.LogFilter{AfterSeq: all[2].Seq, Limit: 2}); len(got) != 2 || got[0].Seq != all[3].Seq {
		t.Fatalf("following the log: %+v", got)
	}
}
