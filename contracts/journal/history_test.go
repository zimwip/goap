package journal

import (
	"reflect"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// logOf builds a log from records and impact events, numbering the entries in order.
type logOf struct {
	t       *testing.T
	entries []domain.LogEntry
}

func (l *logOf) add(e domain.LogEntry, err error) {
	l.t.Helper()
	if err != nil {
		l.t.Fatal(err)
	}
	e.Seq = int64(len(l.entries) + 1)
	l.entries = append(l.entries, e)
}

// ended records a step run that ended: its step.ended record then its action record.
func (l *logOf) ended(path, exec string, failed bool) {
	l.t.Helper()
	l.add(entry(Record{ID: exec + "/ended", Kind: KindStepEnded, ProcessID: "p", Methodology: "m", ActivityRef: path, Action: path,
		Data: map[string]any{"execution": exec}}))
	met := !failed
	l.add(entry(Record{ID: exec, Kind: KindAction, ProcessID: "p", Methodology: "m", ActivityRef: path, Action: path, EffectsMet: &met}))
}

func (l *logOf) impact(id string, op domain.ImpactOp, exec string) {
	l.t.Helper()
	l.add(domain.ImpactEntry(domain.ImpactEvent{ID: id + string(op) + exec, Impact: domain.ChangeImpactID(id), Op: op, Execution: exec}))
}

// analyse relies on collect, report on analyse.
func chain(_, later, earlier string) bool {
	deps := map[string][]string{"analyse": {"collect"}, "report": {"analyse", "collect"}}
	for _, d := range deps[later] {
		if d == earlier {
			return true
		}
	}
	return false
}

func fold(t *testing.T, l *logOf) map[string]StepRun {
	t.Helper()
	h, err := Fold(l.entries, chain)
	if err != nil {
		t.Fatal(err)
	}
	return h.Last()
}

func TestHistoryOutcomes(t *testing.T) {
	l := &logOf{t: t}
	l.impact("I1", domain.ImpactCreated, "x0")
	l.ended("collect", "x1", false)
	l.ended("analyse", "x2", true)
	last := fold(t, l)
	if c := last["collect"]; c.Outcome != OutcomeSucceeded || !c.Current || c.Runs != 1 {
		t.Fatalf("collect %+v", c)
	}
	if a := last["analyse"]; a.Outcome != OutcomeFailed || a.Current {
		t.Fatalf("a failed run never stands: %+v", a)
	}
}

// A rebase moving what a step started from makes it stale, and the steps relying on it; running it again does not
// bring back the steps relying on it, whose success is now older (ADR 0109 §5).
func TestHistoryInvalidatesInCascade(t *testing.T) {
	l := &logOf{t: t}
	l.impact("I1", domain.ImpactCreated, "x0")
	l.ended("collect", "x1", false)
	l.ended("analyse", "x2", false)
	l.ended("report", "x3", false)
	if last := fold(t, l); !last["collect"].Current || !last["analyse"].Current || !last["report"].Current {
		t.Fatalf("all stand: %+v", last)
	}
	l.impact("I1", domain.ImpactRebased, "")
	last := fold(t, l)
	for _, p := range []string{"collect", "analyse", "report"} {
		if last[p].Current {
			t.Fatalf("%s stands after the rebase: %+v", p, last[p])
		}
	}
	l.ended("collect", "x4", false)
	last = fold(t, l)
	if !last["collect"].Current || last["collect"].Runs != 2 || last["analyse"].Current || last["report"].Current {
		t.Fatalf("collect stands again, analyse and report are to redo: %+v", last)
	}
	l.ended("analyse", "x5", false)
	l.ended("report", "x6", false)
	if last := fold(t, l); !last["analyse"].Current || !last["report"].Current {
		t.Fatalf("all stand again: %+v", last)
	}
}

// What a step relying on another writes never makes the other stale; what any other writes does.
func TestHistoryWritesOfSuccessorsDoNotCount(t *testing.T) {
	l := &logOf{t: t}
	l.impact("I1", domain.ImpactCreated, "x0")
	l.ended("collect", "x1", false)
	l.ended("analyse", "x2", false)
	l.impact("I1", domain.ImpactUpdated, "x2") // analyse edits what collect read, after collect
	if last := fold(t, l); !last["collect"].Current {
		t.Fatalf("collect stands: %+v", last["collect"])
	}
	l.impact("I2", domain.ImpactCreated, "")
	l.impact("I2", domain.ImpactUpdated, "") // a new impact: nothing a step saw
	if last := fold(t, l); !last["collect"].Current || !last["analyse"].Current {
		t.Fatalf("a new impact moves nothing: %+v", last)
	}
	l.impact("I1", domain.ImpactUpdated, "") // a person's edit
	if last := fold(t, l); last["collect"].Current || last["analyse"].Current {
		t.Fatalf("an edit from outside makes them stale: %+v", last)
	}
}

// A folder fed the log a few entries at a time, through copies, gives the history of the whole log at once, and a copy
// left behind keeps the history of what it was fed (ADR 0153).
func TestFolderEqualsFold(t *testing.T) {
	l := &logOf{t: t}
	l.impact("I1", domain.ImpactCreated, "x0")
	l.ended("collect", "x1", false)
	l.ended("analyse", "x2", true)
	l.ended("analyse", "x3", false)
	l.impact("I1", domain.ImpactUpdated, "x3")
	l.ended("report", "x4", false)
	l.impact("I1", domain.ImpactRebased, "")
	l.ended("collect", "x5", false)
	for step := 1; step <= len(l.entries); step++ {
		f := NewFolder()
		var kept []*Folder
		for i := 0; i < len(l.entries); i += step {
			kept = append(kept, f)
			f = f.Clone()
			if err := f.Add(l.entries[i:min(i+step, len(l.entries))]); err != nil {
				t.Fatal(err)
			}
		}
		want, err := Fold(l.entries, chain)
		if err != nil {
			t.Fatal(err)
		}
		if got := f.History(chain); !reflect.DeepEqual(got, want) {
			t.Fatalf("by %d: %+v\nwant %+v", step, got, want)
		}
		// each copy left behind still gives the history of the entries it was fed
		for k, kf := range kept {
			prefix, err := Fold(l.entries[:min(k*step, len(l.entries))], chain)
			if err != nil {
				t.Fatal(err)
			}
			if got := kf.History(chain); !reflect.DeepEqual(got, prefix) {
				t.Fatalf("by %d, copy %d: %+v\nwant %+v", step, k, got, prefix)
			}
		}
	}
}
