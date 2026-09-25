package domain

import "time"

// Execution record kinds: the journal of the agent processes working on a
// change (ADR 0011). Every tick of the observe / plan / act loop, every action
// execution (with its model and tool calls) and every human decision is
// recorded on the change axis, next to the items it produced.
const (
	ExecProcessStarted = "process.started"
	ExecTick           = "tick"     // observe + plan: world state, plan, chosen action
	ExecAction         = "action"   // one action execution
	ExecApproval       = "approval" // a human approval decision
	ExecProcessEnded   = "process.ended"
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
	// A step reads the blackboard and extends it: BoardBefore / BoardAfter are the
	// numbers of items on the change when the step starts / ends (the blackboard
	// state), Reads the existing node versions the step started from.
	Reads        []NodeRef   `json:"reads,omitempty"`
	BoardBefore  int         `json:"boardBefore"`
	BoardAfter   int         `json:"boardAfter"`
	InputTokens  int64       `json:"inputTokens,omitempty"`
	OutputTokens int64       `json:"outputTokens,omitempty"`
	ModelCalls   []ModelCall `json:"modelCalls,omitempty"`
	ToolCalls    []ToolUse   `json:"toolCalls,omitempty"`
	Actor        string      `json:"actor,omitempty"` // principal of a human decision
	Output       string      `json:"output,omitempty"`
	Error        string      `json:"error,omitempty"`
	// TraceID / SpanID link the record to its OpenTelemetry span.
	TraceID    string         `json:"traceId,omitempty"`
	SpanID     string         `json:"spanId,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
	StartedAt  time.Time      `json:"startedAt"`
	EndedAt    time.Time      `json:"endedAt,omitempty"`
	DurationMs int64          `json:"durationMs,omitempty"`
}

// ModelCall is one LLM call of an action.
type ModelCall struct {
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	DurationMs   int64  `json:"durationMs"`
	Error        string `json:"error,omitempty"`
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
