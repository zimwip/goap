// Package journal is the execution journal of a change (ADR 0011, 0030, 0059): the records of what the agent processes
// do on it (every tick of the observe / plan / act loop, every action run with its model and tool calls, every human
// decision) and the prompts of the LLM calls. It is a use case of the log of a change: the graph holds a generic typed
// log (domain.LogEntry, Graph.AppendLog / ChangeLog) and knows nothing of what is in it; this package turns a record into
// the entries of the log and back, caps the prompts and says what a valid record is. pkg/graph and pkg/domain import
// nothing of it (pkg/layering).
package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/domain"
)

// ErrInvalid is wrapped by the errors about a record or a filter that is not valid.
var ErrInvalid = errors.New("invalid journal")

// Streams of the log of a change this package writes (the first part of an entry type, domain.LogEntry.Stream).
const (
	StreamJournal = "journal" // journal.<record kind>: journal.schedule, journal.tick, journal.action…
	StreamModel   = "model"   // model.call: the request and the answer of one LLM call of an action run
)

// Record kinds: every tick of the observe / plan / act loop, every action execution (with its model and tool calls)
// and every human decision is recorded on the change axis, next to the items it produced.
const (
	KindProcessStarted = "process.started"
	KindTick           = "tick"     // observe + plan: world state, plan, chosen action
	KindAction         = "action"   // one action execution
	KindApproval       = "approval" // a human approval decision
	// KindSchedule records that a run picked up a process made runnable (started, resumed after a human input, an
	// approval or a sub-agent, relaunched on a flow, fired by a trigger): why, by whom and what caused it; StartedAt
	// is when it was queued, EndedAt when the run picked it up.
	KindSchedule     = "schedule"
	KindProcessEnded = "process.ended"
	// KindAttach records a process being bound to a change (ADR 0031): the eager default at Start, or an agent's own
	// explicit bind of a deferred process.
	KindAttach = "attach"
	// KindUnblock records a person's decision on a run waiting for conditions or stuck (ADR 0036 §3): waive
	// conditions, retry, abandon.
	KindUnblock = "unblock"
)

// Record is one entry of the execution journal of a change.
type Record struct {
	ID        string          `json:"id"`
	ChangeID  domain.ChangeID `json:"changeId"`
	ProcessID string          `json:"processId"`
	// ParentProcessID is set for sub-agent processes.
	ParentProcessID string `json:"parentProcessId,omitempty"`
	// Seq orders the records of a process.
	Seq         int    `json:"seq"`
	Kind        string `json:"kind"`
	Methodology string `json:"methodology,omitempty"`
	// MethodologyVersion is the published version the process ran.
	MethodologyVersion string `json:"methodologyVersion,omitempty"`
	Agent              string `json:"agent,omitempty"`
	Planner            string `json:"planner,omitempty"`
	Goal               string `json:"goal,omitempty"`
	// Status of the process after the record.
	Status string `json:"status,omitempty"`
	Step   int    `json:"step"`
	Action string `json:"action,omitempty"`
	// ActivityRef is the Activity (methodology@Process/Step/Method/MethodStep node key) this record's action
	// executes - the Activity Run this record is (architecture plan "Activity concept"), resolved from the
	// compiled step's Action.Step at run-start. Empty for records of an action with no step (not generated from
	// a process/method), or before this is wired up by a caller.
	ActivityRef string `json:"activityRef,omitempty"`
	// ActionKind is the kind of the executed action; Specialization is the
	// specialized action actually run for an abstract one.
	ActionKind     string          `json:"actionKind,omitempty"`
	Specialization string          `json:"specialization,omitempty"`
	Plan           []string        `json:"plan,omitempty"`
	Before         map[string]bool `json:"before,omitempty"`
	After          map[string]bool `json:"after,omitempty"`
	EffectsMet     *bool           `json:"effectsMet,omitempty"`
	// Items produced by the action (they carry this record id as provenance).
	Items []domain.ItemID `json:"items,omitempty"`
	// Nodes are the change impacts the action declared (they carry this record id as their execution).
	Nodes []domain.ChangeImpactID `json:"nodes,omitempty"`
	// A step reads the blackboard and extends it: BoardBefore / BoardAfter are the
	// numbers of items on the change when the step starts / ends (the blackboard
	// state), Reads the existing node versions the step started from.
	Reads       []domain.NodeRef `json:"reads,omitempty"`
	BoardBefore int              `json:"boardBefore"`
	BoardAfter  int              `json:"boardAfter"`
	// BoardLast is the last item of the flow before the step: where relaunching the step forks it.
	BoardLast    domain.ItemID `json:"boardLast,omitempty"`
	InputTokens  int64         `json:"inputTokens,omitempty"`
	OutputTokens int64         `json:"outputTokens,omitempty"`
	ModelCalls   []ModelCall   `json:"modelCalls,omitempty"`
	ToolCalls    []ToolUse     `json:"toolCalls,omitempty"`
	Actor        string        `json:"actor,omitempty"` // principal of a human decision
	Output       string        `json:"output,omitempty"`
	Error        string        `json:"error,omitempty"`
	// TraceID / SpanID link the record to its OpenTelemetry span.
	TraceID string         `json:"traceId,omitempty"`
	SpanID  string         `json:"spanId,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
	// Flow is the flow branch the process runs on ("" = the main flow, ADR 0017).
	Flow       string    `json:"flow,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	EndedAt    time.Time `json:"endedAt,omitempty"`
	DurationMs int64     `json:"durationMs,omitempty"`
}

// ModelCall is one LLM call of an action.
type ModelCall struct {
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	DurationMs   int64  `json:"durationMs"`
	Error        string `json:"error,omitempty"`
	// Exchange is what was sent and answered. It is not part of the journal record: Entries writes it as an entry of
	// the log (StreamModel) next to the record and strips it, so reading a record never carries the prompts.
	Exchange *ModelExchange `json:"exchange,omitempty"`
}

// MaxExchangeText caps each text of a ModelExchange (bytes); a longer one is cut and the exchange flagged Truncated.
const MaxExchangeText = 256 << 10

// ModelExchange is the request and the answer of one LLM call: the payload of a StreamModel entry.
type ModelExchange struct {
	// Step is the step of the process the action run is, Call the position of the call in the ModelCalls of its record.
	Step     int            `json:"step"`
	Call     int            `json:"call"`
	System   string         `json:"system,omitempty"`
	Messages []ModelMessage `json:"messages,omitempty"`
	Response string         `json:"response,omitempty"`
	// Behaviors names the global behaviours the gateway added to System on its way to the model (ADR 0093; "!name": dropped
	// by the cap), BehaviorTokens the tokens they added (an estimate). System is what the engine built: the gateway's
	// addition is not repeated in the log, the ledger row of the call holds the same names.
	Behaviors      []string `json:"behaviors,omitempty"`
	BehaviorTokens int      `json:"behaviorTokens,omitempty"`
	// Truncated says a text was cut to MaxExchangeText.
	Truncated bool `json:"truncated,omitempty"`
}

// ModelMessage is a turn of the conversation sent to the model.
type ModelMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Cap cuts the texts of the exchange to MaxExchangeText.
func (x *ModelExchange) Cap() {
	cut := func(s string) string {
		if len(s) <= MaxExchangeText {
			return s
		}
		x.Truncated = true
		return strings.ToValidUTF8(s[:MaxExchangeText], "")
	}
	x.System, x.Response = cut(x.System), cut(x.Response)
	for i := range x.Messages {
		x.Messages[i].Content = cut(x.Messages[i].Content)
	}
}

// ToolUse is one tool call of an action.
type ToolUse struct {
	Name       string `json:"name"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
}

// Filter selects journal records (empty fields match everything; at least one of ChangeID / ProcessIDs is required).
type Filter struct {
	ChangeID   domain.ChangeID `json:"changeId,omitempty"`
	ProcessIDs []string        `json:"processIds,omitempty"`
}

// LogFilter is the filter of the log of a change selecting the journal records.
func (f Filter) LogFilter() (domain.LogFilter, error) {
	if f.ChangeID == "" && len(f.ProcessIDs) == 0 {
		return domain.LogFilter{}, fmt.Errorf("journal filter needs a change or processes: %w", ErrInvalid)
	}
	return domain.LogFilter{Change: f.ChangeID, Types: []string{StreamJournal + "."}, Processes: f.ProcessIDs}, nil
}

// Log is the log of the changes the journal is written to and read from (*graph.Graph and the graph service client).
type Log interface {
	// AppendLog appends the entries as one write.
	AppendLog(ctx context.Context, entries []domain.LogEntry) error
	// ChangeLog returns the entries matching the filter.
	ChangeLog(ctx context.Context, f domain.LogFilter) ([]domain.LogEntry, map[string]int, error)
}

// Append writes the records to the log, as one write (ids are assigned and the start time is set when empty).
func Append(ctx context.Context, l Log, recs []Record) error {
	var all []domain.LogEntry
	for _, r := range recs {
		es, err := Entries(r)
		if err != nil {
			return err
		}
		all = append(all, es...)
	}
	if len(all) == 0 {
		return nil
	}
	return l.AppendLog(ctx, all)
}

// Read returns the records matching the filter, in the order of the log.
func Read(ctx context.Context, l Log, f Filter) ([]Record, error) {
	lf, err := f.LogFilter()
	if err != nil {
		return nil, err
	}
	entries, _, err := l.ChangeLog(ctx, lf)
	if err != nil {
		return nil, err
	}
	return Decode(entries)
}

// Entries are the log entries of a record: its own, then the exchange of each model call that carries one (the record
// keeps the summary of its calls, the prompts are entries of their own, capped to MaxExchangeText). The record is not
// changed.
func Entries(r Record) ([]domain.LogEntry, error) {
	if r.ChangeID == "" || r.ProcessID == "" || r.Kind == "" {
		return nil, fmt.Errorf("execution record needs a change, a process and a kind: %w", ErrInvalid)
	}
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	calls := r.ModelCalls
	r.ModelCalls = slices.Clone(calls)
	for i := range r.ModelCalls {
		r.ModelCalls[i].Exchange = nil
	}
	e, err := entry(r)
	if err != nil {
		return nil, err
	}
	out := []domain.LogEntry{e}
	for i, c := range calls {
		if c.Exchange == nil {
			continue
		}
		ex := *c.Exchange
		ex.Step, ex.Call = r.Step, i
		ex.Cap()
		me, err := modelEntry(r, ex)
		if err != nil {
			return nil, err
		}
		out = append(out, me)
	}
	return out, nil
}

// entry is the log entry of a record: its own action run.
func entry(r Record) (domain.LogEntry, error) {
	raw, err := json.Marshal(r)
	by := r.Actor
	if by == "" {
		by = r.Agent
	}
	return domain.LogEntry{ID: r.ID, Change: r.ChangeID, Type: StreamJournal + "." + r.Kind, Flow: r.Flow, Process: r.ProcessID, Execution: r.ID,
		Subject: r.Action, By: by, At: r.StartedAt, Payload: raw}, err
}

// modelEntry is the log entry of the exchange of an LLM call. It belongs to the action run (journal record) that made
// the call, whose process and flow it takes; the subject is the position of the call in the record's ModelCalls.
func modelEntry(r Record, ex ModelExchange) (domain.LogEntry, error) {
	raw, err := json.Marshal(ex)
	return domain.LogEntry{ID: r.ID + "/" + strconv.Itoa(ex.Call), Change: r.ChangeID, Type: StreamModel + ".call", Flow: r.Flow, Process: r.ProcessID,
		Execution: r.ID, Subject: strconv.Itoa(ex.Call), By: r.Agent, At: r.StartedAt, Payload: raw}, err
}

// Decode returns the records held by the journal entries of a log (the other entries are skipped).
func Decode(entries []domain.LogEntry) ([]Record, error) {
	var out []Record // nil when empty
	for _, e := range entries {
		if e.Stream() != StreamJournal {
			continue
		}
		var r Record
		if err := json.Unmarshal(e.Payload, &r); err != nil {
			return nil, fmt.Errorf("log entry %d (%s): %w", e.Seq, e.Type, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// DecodeExchange is the exchange of a model entry.
func DecodeExchange(e domain.LogEntry) (ModelExchange, error) {
	var ex ModelExchange
	if err := json.Unmarshal(e.Payload, &ex); err != nil {
		return ex, fmt.Errorf("log entry %d (%s): %w", e.Seq, e.Type, err)
	}
	return ex, nil
}
