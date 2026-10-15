package journal

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
)

// The history of a change (ADR 0109 §4): what the runs on the change did, step by step, with the outcome of each step
// and whether its success still stands. It is a pure fold of the log of the change, a part of the context the
// conditions are derived from (the blackboard facet domain.FacetHistory).

// KindStepEnded records that the action of a step of a process ended without error (ADR 0109): the step ran to its end
// on the context the log had then. It is written before the run's world is observed again, so the conditions of that
// observation see it; the action record follows with the effects that observation found.
const KindStepEnded = "step.ended"

// Outcomes of a step run.
const (
	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"
	OutcomeRunning   = "running"
)

// HistoryTypes are the entry types the fold reads: the journal records of the runs and the steps, and the writes that
// move what a step started from (impact events, facts).
var HistoryTypes = []string{
	StreamJournal + "." + KindProcessStarted, StreamJournal + "." + KindProcessEnded, StreamJournal + "." + KindAction,
	StreamJournal + "." + KindStepEnded, domain.LogImpact + ".", domain.LogFact + ".",
}

// Run is a process that ran on the change.
type Run struct {
	Process     string `json:"process"`
	Parent      string `json:"parent,omitempty"`
	Methodology string `json:"methodology,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Goal        string `json:"goal,omitempty"`
	Status      string `json:"status,omitempty"`
	Seq         int64  `json:"seq"`
}

// StepRun is one run of a step of a process.
type StepRun struct {
	// Path is the path of the step (process/step...), Methodology the methodology of the process that ran it.
	Path        string `json:"path"`
	Methodology string `json:"methodology,omitempty"`
	Process     string `json:"process"`
	Action      string `json:"action,omitempty"`
	Execution   string `json:"execution,omitempty"`
	Outcome     string `json:"outcome"`
	Error       string `json:"error,omitempty"`
	// Subject is the subject a scoped run was for (ADR 0109 §3.5); its inputs are that subject alone (SubjectRef, what
	// the log names it by).
	Subject    string `json:"subject,omitempty"`
	SubjectRef string `json:"subjectRef,omitempty"`
	// Seq is the position in the log where the run ended (succeeded or failed); Runs counts the runs of the path up to
	// this one.
	Seq  int64 `json:"seq"`
	Runs int   `json:"runs"`
	// Current: the run succeeded and its success still stands (ADR 0109 §5); StaleBy says why not when it succeeded:
	// the log entry that moved what it started from, or the step it relied on that ran again.
	Current bool   `json:"current"`
	StaleBy string `json:"staleBy,omitempty"`
	// children are the processes the step started (sub-agents), whose writes are the step's.
	children []string
}

// History is the history of a change.
type History struct {
	Runs  []Run     `json:"runs"`
	Steps []StepRun `json:"steps"`
}

// Last is the latest run of each step path.
func (h History) Last() map[string]StepRun {
	out := make(map[string]StepRun, len(h.Steps))
	for _, s := range h.Steps {
		out[s.Path] = s
	}
	return out
}

// Relies tells whether the step later relies on the step earlier of the same methodology (its entry needs the exit
// of earlier, directly or through other steps): what the methodology says, given to the fold by its caller. Nil: no
// step relies on another.
type Relies func(methodology, later, earlier string) bool

// writeOf is a write that may move what a step started from: an event on an impact, or a version of a keyed item.
type writeOf struct {
	seq       int64
	subject   string // impact id, or "<kind>:<key>"
	execution string
	id        string
}

// impactMoves are the operations on an impact that change what a step reading it saw: an edit of the draft, a move of
// its lifecycle, its cancellation or withdrawal, the rebase of the version it starts from (ADR 0109 §5). A review, a
// commit or a landing does not change the content.
var impactMoves = []domain.ImpactOp{domain.ImpactUpdated, domain.ImpactTransitioned, domain.ImpactCancelled,
	domain.ImpactWithdrawn, domain.ImpactRebased}

// Fold is the history of the change whose log entries are given (in the order of the log; the other entries are
// skipped). relies says which steps rely on which (the writes of a step relying on another never make the other stale).
func Fold(entries []domain.LogEntry, relies Relies) (History, error) {
	var h History
	parent := map[string]string{} // process -> parent process
	procOf := map[string]string{} // execution -> process
	born := map[string]int64{}    // subject -> seq of its first appearance
	var writes []writeOf
	ended := map[string]int{}  // execution -> index of its step run
	runIdx := map[string]int{} // process -> index of its run
	for _, e := range entries {
		switch e.Stream() {
		case StreamJournal:
			var r Record
			if err := json.Unmarshal(e.Payload, &r); err != nil {
				return h, err
			}
			if r.ParentProcessID != "" {
				parent[r.ProcessID] = r.ParentProcessID
			}
			switch r.Kind {
			case KindProcessStarted:
				runIdx[r.ProcessID] = len(h.Runs)
				h.Runs = append(h.Runs, Run{Process: r.ProcessID, Parent: r.ParentProcessID, Methodology: r.Methodology, Agent: r.Agent,
					Goal: r.Goal, Status: r.Status, Seq: e.Seq})
			case KindProcessEnded:
				if i, ok := runIdx[r.ProcessID]; ok {
					h.Runs[i].Status = r.Status
				}
			case KindStepEnded:
				exec, _ := r.Data["execution"].(string)
				h.dropRunning(r.ActivityRef, r.ProcessID, ended)
				ended[exec] = len(h.Steps)
				procOf[exec] = r.ProcessID
				h.Steps = append(h.Steps, StepRun{Path: r.ActivityRef, Methodology: r.Methodology, Process: r.ProcessID, Action: r.Action,
					Execution: exec, Outcome: OutcomeSucceeded, Seq: e.Seq, Subject: r.Subject, SubjectRef: r.SubjectRef, children: stringList(r.Data["children"])})
			case KindAction:
				procOf[r.ID] = r.ProcessID
				if r.ActivityRef == "" {
					continue
				}
				failed := r.Error != "" || (r.EffectsMet != nil && !*r.EffectsMet && r.Data["progress"] != true)
				if i, ok := ended[r.ID]; ok {
					if failed {
						h.Steps[i].Outcome, h.Steps[i].Error, h.Steps[i].Seq = OutcomeFailed, r.Error, e.Seq
					}
					continue
				}
				outcome := OutcomeRunning
				if failed {
					outcome = OutcomeFailed
				}
				h.dropRunning(r.ActivityRef, r.ProcessID, ended)
				h.Steps = append(h.Steps, StepRun{Path: r.ActivityRef, Methodology: r.Methodology, Process: r.ProcessID, Action: r.Action,
					Execution: r.ID, Outcome: outcome, Error: r.Error, Seq: e.Seq, Subject: r.Subject, SubjectRef: r.SubjectRef})
			}
		case domain.LogImpact:
			if _, ok := born[e.Subject]; !ok {
				born[e.Subject] = e.Seq
			}
			if slices.Contains(impactMoves, domain.ImpactOp(e.Type[len(domain.LogImpact)+1:])) {
				writes = append(writes, writeOf{seq: e.Seq, subject: e.Subject, execution: e.Execution, id: e.ID})
			}
		case domain.LogFact:
			var it struct {
				Type string         `json:"type"`
				Data map[string]any `json:"data"`
			}
			if json.Unmarshal(e.Payload, &it) != nil {
				continue
			}
			key, _ := it.Data["key"].(string)
			if key == "" {
				continue // an item without key is never rewritten: a new one moves nothing a step saw
			}
			subject := it.Type + ":" + key
			if _, ok := born[subject]; !ok {
				born[subject] = e.Seq
				continue
			}
			writes = append(writes, writeOf{seq: e.Seq, subject: subject, execution: e.Execution, id: e.ID})
		}
	}
	// a running step whose action is not over yet stays running; count the runs of each path
	count := map[string]int{}
	for i := range h.Steps {
		count[h.Steps[i].Path]++
		h.Steps[i].Runs = count[h.Steps[i].Path]
	}
	h.current(parent, procOf, born, writes, relies)
	return h, nil
}

// dropRunning removes the running entry of a step a process runs again: a step waiting for its sub-agent is run again
// once the sub-agent ends, its first run never ended.
func (h *History) dropRunning(path, process string, ended map[string]int) {
	for i := len(h.Steps) - 1; i >= 0; i-- {
		s := h.Steps[i]
		if s.Path != path || s.Process != process {
			continue
		}
		if s.Subject != "" && s.Outcome != OutcomeRunning {
			return // a scoped step runs once per subject: an earlier subject's run is not run again
		}
		if s.Outcome == OutcomeRunning {
			h.Steps = slices.Delete(h.Steps, i, i+1)
			for x, j := range ended {
				if j > i {
					ended[x] = j - 1
				}
			}
		}
		return
	}
}

// current sets Current and StaleBy on every step run (ADR 0109 §5): a success stands while (1) nothing it started from
// moved after it, but by a step relying on it, and (2) every step it relies on has a success standing, older than its own.
func (h *History) current(parent, procOf map[string]string, born map[string]int64, writes []writeOf, relies Relies) {
	// the step run a process belongs to: the step whose sub-agents it descends from
	owner := map[string]int{}
	for i, s := range h.Steps {
		for _, c := range s.children {
			owner[c] = i
		}
	}
	stepOf := func(execution string) (int, bool) {
		for p := procOf[execution]; p != ""; p = parent[p] {
			if i, ok := owner[p]; ok {
				return i, true
			}
		}
		for i, s := range h.Steps {
			if s.Execution == execution {
				return i, true
			}
		}
		return 0, false
	}
	rel := func(later, earlier StepRun) bool {
		return relies != nil && later.Methodology == earlier.Methodology && relies(later.Methodology, later.Path, earlier.Path)
	}
	latest := map[string]int{} // path -> index of its latest run, in log order
	for i := range h.Steps {
		s := &h.Steps[i]
		latest[s.Path] = i
		if s.Outcome != OutcomeSucceeded {
			continue
		}
		s.Current = true
		// (1) a write after it, on what existed before it ended, by nothing relying on it
		for _, w := range writes {
			if w.seq <= s.Seq || born[w.subject] >= s.Seq || (s.SubjectRef != "" && w.subject != s.SubjectRef) {
				continue
			}
			if j, ok := stepOf(w.execution); ok && (j == i || rel(h.Steps[j], *s)) {
				continue
			}
			s.Current, s.StaleBy = false, "log entry "+w.id
			break
		}
	}
	// (2) in log order: what it relies on stands and is older; a later run of a step it relies on makes it stale
	for i := range h.Steps {
		s := &h.Steps[i]
		if !s.Current {
			continue
		}
		for path, j := range latest {
			p := h.Steps[j]
			if path == s.Path || !rel(*s, p) {
				continue
			}
			if p.Seq > s.Seq || (p.Outcome == OutcomeSucceeded && !p.Current) || p.Outcome == OutcomeFailed {
				s.Current, s.StaleBy = false, "step "+path
				break
			}
		}
	}
	// a stale step makes the steps relying on it stale: repeat until nothing moves (the chain is finite)
	for changed := true; changed; {
		changed = false
		for i := range h.Steps {
			s := &h.Steps[i]
			if !s.Current {
				continue
			}
			for path, j := range latest {
				if p := h.Steps[j]; path != s.Path && rel(*s, p) && !p.Current {
					s.Current, s.StaleBy, changed = false, "step "+path, true
					break
				}
			}
		}
	}
}

func stringList(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	if s, ok := v.([]string); ok {
		return s
	}
	return out
}

// StepDependencies gives the steps of a methodology each step relies on (methodology.Compiled.StepDependencies), nil
// when the methodology is not known. The methodology is a name or a reference at a version (domain.MethodologyRef).
type StepDependencies func(ctx context.Context, methodology string) map[string]map[string]bool

// FoldWith is the fold of the history of a change whose steps rely on one another as deps says (nil: no step relies on
// another), each methodology asked once per fold: what the graph plugs as the history facet of the blackboard
// (graph.LogFacet with HistoryTypes) and the engine runs over the log it reads.
func FoldWith(deps StepDependencies) func(ctx context.Context, c domain.Change, entries []domain.LogEntry) (any, error) {
	return func(ctx context.Context, c domain.Change, entries []domain.LogEntry) (any, error) {
		var relies Relies
		if deps != nil {
			known := map[string]map[string]map[string]bool{}
			relies = func(methodology, later, earlier string) bool {
				d, ok := known[methodology]
				if !ok {
					ask := methodology
					if methodology == c.Methodology {
						ask = c.MethodologyRef() // the version the change follows (ADR 0113)
					}
					d = deps(ctx, ask)
					known[methodology] = d
				}
				return d[later][earlier]
			}
		}
		return Fold(entries, relies)
	}
}
