package domain

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// The log of a change (ADR 0030): one append-only stream of everything that happens to it, in one order. The facts
// of its blackboard (ADR 0017), the records of its execution journal (ADR 0011, scheduling included) and the events of
// its change impacts (ADR 0029) are entries of the same log; what an entry is, the flow it belongs to, the process and
// the action run behind it are columns, so the log is filtered on them directly.

// Streams of the log the graph itself writes: the first part of an entry type. Any other stream is written by a use
// case through Graph.AppendLog (the execution journal and the prompts of the model calls, pkg/journal).
const (
	LogFact   = "fact"   // fact.<item kind>: fact.artifact, fact.decision, fact.flow…
	LogImpact = "impact" // impact.<op>: impact.declared, impact.written…
)

// LogEntry is one entry of a change's log. Payload is the whole fact, journal record or impact event (JSON).
type LogEntry struct {
	// Seq orders the log (one sequence for every change: the order of the entries of a change is total).
	Seq    int64    `json:"seq"`
	ID     string   `json:"id"`
	Change ChangeID `json:"change"`
	// Type is "<stream>.<kind>".
	Type string `json:"type"`
	// Flow is the flow branch the entry belongs to ("" = the main flow).
	Flow string `json:"flow,omitempty"`
	// Process is the process of a journal record; Execution the action run behind the entry (a journal record is
	// its own); Subject what it is about (the action, the fact type, the change impact); By the principal or
	// component.
	Process   string          `json:"process,omitempty"`
	Execution string          `json:"execution,omitempty"`
	Subject   string          `json:"subject,omitempty"`
	By        string          `json:"by,omitempty"`
	At        time.Time       `json:"at"`
	Payload   json.RawMessage `json:"payload"`
}

// Stream is the stream of the entry: fact, impact, or one a use case writes.
func (e LogEntry) Stream() string {
	s, _, _ := strings.Cut(e.Type, ".")
	return s
}

// LogFilter selects entries of the log. Empty fields select everything.
type LogFilter struct {
	Change ChangeID
	// Types are exact types ("journal.action") or streams ("journal.": a type prefix ending with a dot).
	Types []string
	// Flows are flow branches; "" is the main flow.
	Flows     []string
	Processes []string
	Execution string
	// AfterSeq keeps the entries after a position (to follow the log); Limit caps their number.
	AfterSeq int64
	Limit    int
}

// MatchType tells whether a type is selected by the filter's types.
func (f LogFilter) MatchType(t string) bool {
	if len(f.Types) == 0 {
		return true
	}
	for _, x := range f.Types {
		if x == t || (strings.HasSuffix(x, ".") && strings.HasPrefix(t, x)) {
			return true
		}
	}
	return false
}

// Match tells whether an entry is selected (the limit aside).
func (f LogFilter) Match(e LogEntry) bool {
	return (f.Change == "" || e.Change == f.Change) &&
		f.MatchType(e.Type) &&
		(f.Flows == nil || slices.Contains(f.Flows, e.Flow)) &&
		(len(f.Processes) == 0 || slices.Contains(f.Processes, e.Process)) &&
		(f.Execution == "" || e.Execution == f.Execution) &&
		e.Seq > f.AfterSeq
}

// FactEntry is the log entry of a fact. A flow event belongs to the flow it opens, adopts or discards.
func FactEntry(change ChangeID, it ChangeItem) (LogEntry, error) {
	raw, err := json.Marshal(it)
	e := LogEntry{ID: string(it.ID), Change: change, Type: LogFact + "." + string(it.Kind), Flow: it.Flow, Execution: it.Execution,
		Subject: it.Type, By: it.ProducedBy, At: it.CreatedAt, Payload: raw}
	if f := it.FlowEvent; f != nil {
		e.Flow, e.Subject = f.Flow, f.Op
		if f.By != "" {
			e.By = f.By
		}
	}
	if d := it.DecisionEvent; d != nil {
		e.Subject = "decision." + d.Op
		if d.By != "" {
			e.By = d.By
		}
	}
	return e, err
}

// ImpactEntry is the log entry of an impact event.
func ImpactEntry(e ImpactEvent) (LogEntry, error) {
	raw, err := json.Marshal(e)
	return LogEntry{ID: e.ID, Change: e.Change, Type: LogImpact + "." + string(e.Op), Flow: e.Flow, Execution: e.Execution,
		Subject: string(e.Impact), By: e.By, At: e.At, Payload: raw}, err
}
