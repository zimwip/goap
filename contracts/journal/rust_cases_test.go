package journal

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// rustHistoryCases is where the Rust graph reads the logs the history is folded from and the history Go folds from each
// (ADR 0163 addendum, the history of a change).
const rustHistoryCases = "../../rust/crates/graph-core/src/history_cases.gen.json"

type rustEntry struct {
	Seq       int64  `json:"seq"`
	ID        string `json:"id"`
	Type      string `json:"type"`
	Subject   string `json:"subject"`
	Execution string `json:"execution"`
	Payload   string `json:"payload"`
}

type rustHistoryCase struct {
	Name string      `json:"name"`
	Log  []rustEntry `json:"log"`
	// Relies are the steps each step of a methodology relies on, as the Rust fold takes them.
	Relies  map[string]map[string][]string `json:"relies"`
	History History                        `json:"history"`
}

// entriesOf lays the log of a case out as the Rust fold reads it.
func entriesOf(t *testing.T, l *logOf) []rustEntry {
	t.Helper()
	out := make([]rustEntry, 0, len(l.entries))
	for _, e := range l.entries {
		out = append(out, rustEntry{Seq: e.Seq, ID: e.ID, Type: e.Type, Subject: e.Subject, Execution: e.Execution,
			Payload: string(e.Payload)})
	}
	return out
}

// TestRustHistoryCasesAreCurrent writes (GOAP_UPDATE_RUST=1) or checks the history cases of the Rust graph: the same
// logs as the tests above, with the history Go folds from each.
func TestRustHistoryCasesAreCurrent(t *testing.T) {
	relies := map[string]map[string][]string{"m": {"analyse": {"collect"}, "report": {"analyse", "collect"}}}
	var cases []rustHistoryCase
	add := func(name string, l *logOf) {
		t.Helper()
		h, err := Fold(l.entries, chain)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, rustHistoryCase{Name: name, Log: entriesOf(t, l), Relies: relies, History: h})
	}
	outcomes := &logOf{t: t}
	outcomes.impact("I1", domain.ImpactCreated, "x0")
	outcomes.ended("collect", "x1", false)
	outcomes.ended("analyse", "x2", true)
	add("outcomes", outcomes)

	cascade := &logOf{t: t}
	cascade.impact("I1", domain.ImpactCreated, "x0")
	cascade.ended("collect", "x1", false)
	cascade.ended("analyse", "x2", false)
	cascade.ended("report", "x3", false)
	add("all stand", cascade)
	cascade.impact("I1", domain.ImpactRebased, "")
	add("a rebase makes them stale", cascade)
	cascade.ended("collect", "x4", false)
	add("collect stands again", cascade)
	cascade.ended("analyse", "x5", false)
	cascade.ended("report", "x6", false)
	add("all stand again", cascade)

	writes := &logOf{t: t}
	writes.impact("I1", domain.ImpactCreated, "x0")
	writes.ended("collect", "x1", false)
	writes.ended("analyse", "x2", false)
	writes.impact("I1", domain.ImpactUpdated, "x2")
	add("a successor's write does not count", writes)
	writes.impact("I2", domain.ImpactCreated, "")
	writes.impact("I2", domain.ImpactUpdated, "")
	add("a new impact moves nothing", writes)
	writes.impact("I1", domain.ImpactUpdated, "")
	add("an edit from outside makes them stale", writes)

	got, err := json.MarshalIndent(map[string]any{"cases": cases}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if os.Getenv("GOAP_UPDATE_RUST") == "1" {
		if err := os.WriteFile(rustHistoryCases, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	have, err := os.ReadFile(rustHistoryCases)
	if err != nil || string(have) != string(got) {
		t.Fatalf("%s is not current: run GOAP_UPDATE_RUST=1 go test ./contracts/journal/ -run TestRustHistoryCasesAreCurrent", rustHistoryCases)
	}
}
