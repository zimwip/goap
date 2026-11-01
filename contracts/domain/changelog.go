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
	LogImpact = "impact" // impact.<op>: impact.proposed, impact.transitioned…
	LogChange = "change" // change.updated: an edit of the header of the change (title, intent, goal, status, data)
	// LogModel is the stream of the exchanges with a model (model.call, ADR 0059), written by the execution journal
	// (journal.StreamModel); the graph only reads that an execution called a model (the provenance of an export).
	LogModel = "model"
)

// HeaderEdit is the payload of a change.updated entry: for each field of the header the edit set, the value it left
// and the one it wrote (Data: per key).
type HeaderEdit struct {
	Fields map[string]HeaderValue `json:"fields"`
}

// HeaderValue is one field of a HeaderEdit.
type HeaderValue struct {
	From any `json:"from,omitempty"`
	To   any `json:"to"`
}

// HeaderEntry is the log entry of an edit of the header of a change; Subject lists the fields it changed.
func HeaderEntry(id string, change ChangeID, by string, at time.Time, edit HeaderEdit) (LogEntry, error) {
	raw, err := json.Marshal(edit)
	fields := make([]string, 0, len(edit.Fields))
	for f := range edit.Fields {
		fields = append(fields, f)
	}
	slices.Sort(fields)
	return LogEntry{ID: id, Change: change, Type: LogChange + ".updated", Subject: strings.Join(fields, ","), By: by, At: at, Payload: raw}, err
}

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
	Process   string    `json:"process,omitempty"`
	Execution string    `json:"execution,omitempty"`
	Subject   string    `json:"subject,omitempty"`
	By        string    `json:"by,omitempty"`
	At        time.Time `json:"at"`
	// Causation is the ID of the entry this one follows from (ADR 0105): a decided review of a change impact names the
	// submitted review (fact.review) that decided it. Empty when the entry has no cause in the log.
	Causation string          `json:"causation,omitempty"`
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

// FactEntry is the log entry of a fact. A flow event belongs to the flow it opens, adopts or discards. The payload
// leaves to the columns what they hold of the item (ADR 0180): its id, type, time and execution, its author, flow and
// target when the columns hold them as is (ItemOf puts them back).
func FactEntry(change ChangeID, it ChangeItem) (LogEntry, error) {
	e := LogEntry{ID: string(it.ID), Change: change, Type: LogFact + "." + string(it.Type), Flow: it.Flow, Execution: it.Execution,
		Subject: it.Target, By: it.ProducedBy, At: it.CreatedAt}
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
	p := itemPayload{ChangeItem: it}
	if it.Flow != e.Flow {
		p.Flow = &it.Flow
	}
	if it.Target != e.Subject {
		p.Target = &it.Target
	}
	if it.ProducedBy != e.By {
		p.ProducedBy = &it.ProducedBy
	}
	var err error
	e.Payload, err = json.Marshal(p)
	return e, err
}

// itemPayload is a fact as its log entry stores it: what the columns hold left out, what differs from them written.
type itemPayload struct {
	ChangeItem
	ID         absent  `json:"id,omitempty"`
	Type       absent  `json:"type,omitempty"`
	CreatedAt  absent  `json:"createdAt,omitempty"`
	Execution  absent  `json:"execution,omitempty"`
	Flow       *string `json:"flow,omitempty"`
	Target     *string `json:"target,omitempty"`
	ProducedBy *string `json:"producedBy,omitempty"`
}

// itemIn reads an item payload: what it leaves to the columns told apart from what it writes empty.
type itemIn struct {
	ChangeItem
	Flow       *string `json:"flow"`
	Target     *string `json:"target"`
	ProducedBy *string `json:"producedBy"`
}

// ItemOf is the fact a log entry records (FactEntry): its payload with what it leaves to the columns.
func ItemOf(l LogEntry) (ChangeItem, error) {
	var in itemIn
	if err := json.Unmarshal(l.Payload, &in); err != nil {
		return in.ChangeItem, err
	}
	it := in.ChangeItem
	if it.ID == "" {
		it.ID = ItemID(l.ID)
	}
	if it.Type == "" {
		it.Type = ItemType(strings.TrimPrefix(l.Type, LogFact+"."))
	}
	if it.CreatedAt.IsZero() {
		it.CreatedAt = l.At
	}
	if it.Execution == "" {
		it.Execution = l.Execution
	}
	it.Flow, it.Target, it.ProducedBy = l.Flow, l.Subject, l.By
	if in.Flow != nil {
		it.Flow = *in.Flow
	}
	if in.Target != nil {
		it.Target = *in.Target
	}
	if in.ProducedBy != nil {
		it.ProducedBy = *in.ProducedBy
	}
	return it, nil
}

// ImpactEntry is the log entry of an impact event: its envelope in the columns, the rest in the payload (ADR 0142;
// ImpactEventOf reads it back).
func ImpactEntry(e ImpactEvent) (LogEntry, error) {
	if e.Op == ImpactCommitted {
		return CommitEntry(e.ID, []ImpactEvent{e})
	}
	raw, err := marshalImpact(e)
	return LogEntry{ID: e.ID, Change: e.Change, Type: LogImpact + "." + string(e.Op), Flow: e.Flow, Execution: e.Execution,
		Subject: string(e.Impact), By: e.By, At: e.At, Causation: e.Causation, Payload: raw}, err
}
