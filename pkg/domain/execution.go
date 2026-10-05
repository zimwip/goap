package domain

import (
	"strings"
	"time"
)

// Execution record kinds: the journal of the agent processes working on a
// change (ADR 0011). Every tick of the observe / plan / act loop, every action
// execution (with its model and tool calls) and every human decision is
// recorded on the change axis, next to the items it produced.
const (
	ExecProcessStarted = "process.started"
	ExecTick           = "tick"     // observe + plan: world state, plan, chosen action
	ExecAction         = "action"   // one action execution
	ExecApproval       = "approval" // a human approval decision
	// ExecSchedule records that a run picked up a process made runnable (started, resumed after a human input, an
	// approval or a sub-agent, relaunched on a flow, fired by a trigger): why, by whom and what caused it; StartedAt
	// is when it was queued, EndedAt when the run picked it up.
	ExecSchedule     = "schedule"
	ExecProcessEnded = "process.ended"
	// ExecAttach records a process being bound to a change (ADR 0031): the eager
	// default at Start, or an agent's own explicit bind of a deferred process.
	ExecAttach = "attach"
	// ExecUnblock records a person's decision on a run waiting for conditions or stuck (ADR 0036 §3): waive
	// conditions, retry, abandon.
	ExecUnblock = "unblock"
)

// ExecutionRecord is one entry of the execution journal of a change.
type ExecutionRecord struct {
	ID        string   `json:"id"`
	ChangeID  ChangeID `json:"changeId"`
	ProcessID string   `json:"processId"`
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
	Items []ItemID `json:"items,omitempty"`
	// Nodes are the change impacts the action declared (they carry this record id as their execution).
	Nodes []ChangeImpactID `json:"nodes,omitempty"`
	// A step reads the blackboard and extends it: BoardBefore / BoardAfter are the
	// numbers of items on the change when the step starts / ends (the blackboard
	// state), Reads the existing node versions the step started from.
	Reads       []NodeRef `json:"reads,omitempty"`
	BoardBefore int       `json:"boardBefore"`
	BoardAfter  int       `json:"boardAfter"`
	// BoardLast is the last item of the flow before the step: where relaunching the step forks it.
	BoardLast    ItemID      `json:"boardLast,omitempty"`
	InputTokens  int64       `json:"inputTokens,omitempty"`
	OutputTokens int64       `json:"outputTokens,omitempty"`
	ModelCalls   []ModelCall `json:"modelCalls,omitempty"`
	ToolCalls    []ToolUse   `json:"toolCalls,omitempty"`
	Actor        string      `json:"actor,omitempty"` // principal of a human decision
	Output       string      `json:"output,omitempty"`
	Error        string      `json:"error,omitempty"`
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
	// Exchange is what was sent and answered. It is not part of the journal record: Graph.Record writes it as an
	// entry of the log (LogModel) next to the record and strips it, so reading a record never carries the prompts.
	Exchange *ModelExchange `json:"exchange,omitempty"`
}

// MaxExchangeText caps each text of a ModelExchange (bytes); a longer one is cut and the exchange flagged Truncated.
const MaxExchangeText = 256 << 10

// ModelExchange is the request and the answer of one LLM call: the payload of a LogModel entry.
type ModelExchange struct {
	// Step is the step of the process the action run is, Call the position of the call in the ModelCalls of its record.
	Step     int            `json:"step"`
	Call     int            `json:"call"`
	System   string         `json:"system,omitempty"`
	Messages []ModelMessage `json:"messages,omitempty"`
	Response string         `json:"response,omitempty"`
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

// ExecutionFilter selects journal records (empty fields match everything;
// at least one of ChangeID / ProcessIDs is required).
type ExecutionFilter struct {
	ChangeID   ChangeID `json:"changeId,omitempty"`
	ProcessIDs []string `json:"processIds,omitempty"`
}
