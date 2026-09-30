// Package engine runs agent processes: intent loop, then observe / plan /
// act cycles over the blackboard of a change until the goal holds.
package engine

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"sync"
	"time"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/goap"
	"github.com/zimwip/goap/pkg/intent"
)

// Status is the lifecycle state of a process.
type Status string

const (
	StatusClarifying Status = "clarifying" // waiting for the user to answer an intent question
	StatusRunning    Status = "running"
	StatusWaiting    Status = "waiting" // waiting for a person, a sub-agent, or conditions established outside the process
	StatusCompleted  Status = "completed"
	// StatusStuck: no plan reaches the goal, whatever is established outside the process; it waits for a person to
	// unblock it (TaskUnblock), and is tried again when the change moves.
	StatusStuck  Status = "stuck"
	StatusFailed Status = "failed"
	// StatusSuperseded: the run was replaced by a relaunched flow that was adopted,
	// or it is the relaunched flow itself that was discarded.
	StatusSuperseded Status = "superseded"
)

// Terminal reports whether the process has ended. A stuck process has not: a person can always unblock it.
func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusSuperseded
}

// Process is an agent process working on a change.
type Process struct {
	ID          string          `json:"id"`
	Methodology string          `json:"methodology"`
	ChangeID    domain.ChangeID `json:"changeId"`
	// Org is the organisation holding the change: its owner unit, or the default one (a cache:
	// the change is the source). Its adapters decide which MCPs the actions can use.
	Org string `json:"org,omitempty"`
	// Project is the project the change belongs to: its ProjectID, or the root project (a cache: the
	// change is the source, ADR 0039). Assignment nodes on its chain grant the roles step checks see.
	Project string `json:"project,omitempty"`
	// Queued says why the process was last made runnable; the run that picks it up journals it as a schedule
	// record (ADR 0011) and clears it.
	Queued *Queued `json:"queued,omitempty"`
	// Initiator is the principal who started the process; automatic actions
	// run with its permissions.
	Initiator authz.Principal `json:"initiator"`
	// Agent of the methodology run by the process and its planner.
	Agent   string `json:"agent,omitempty"`
	Planner string `json:"planner,omitempty"`
	// ParentID is the process that called this one as a sub-agent.
	ParentID string `json:"parentId,omitempty"`
	// Trigger ("<methodology>/<agent>/<trigger>") started the process.
	Trigger string `json:"trigger,omitempty"`
	// Children maps sub-agent calls ("action#index:agent") to their process,
	// so that a retried action finds the sub-agent it started.
	Children   map[string]string `json:"children,omitempty"`
	BaselineID domain.BaselineID `json:"baselineId,omitempty"`
	Namespace  string            `json:"namespace,omitempty"`
	OwnBranch  bool              `json:"ownBranch,omitempty"`
	// Flow is the flow branch the process works on ("" = the main flow). A relaunched
	// step starts a new process on a new branch: RelaunchOf is the process it
	// replaces if the branch is adopted, FromStep the step it restarts.
	Flow       string `json:"flow,omitempty"`
	RelaunchOf string `json:"relaunchOf,omitempty"`
	FromStep   int    `json:"fromStep,omitempty"`
	// Dismissed lists the sets of blackboard issues a human chose to ignore (see issuesKey).
	Dismissed map[string]bool `json:"dismissed,omitempty"`
	Title     string          `json:"title,omitempty"`
	Usage     Usage           `json:"usage"`
	TraceID   string          `json:"traceId,omitempty"`
	// TraceParent (W3C) of the process root span: later runs continue the trace.
	TraceParent string             `json:"traceParent,omitempty"`
	Status      Status             `json:"status"`
	Goal        string             `json:"goal,omitempty"`
	Intent      intent.Session     `json:"intent"`
	Question    string             `json:"question,omitempty"`
	Candidates  []intent.Candidate `json:"candidates,omitempty"`
	Pending     *HumanTask         `json:"pending,omitempty"`
	Plan        []string           `json:"plan,omitempty"`
	World       goap.WorldState    `json:"world,omitempty"`
	Unknown     map[string]string  `json:"unknown,omitempty"`
	Steps       []Step             `json:"steps"`
	Vars        map[string]any     `json:"vars,omitempty"`
	// Step is the step of a process of the parent that this process carries out (a sub-agent started by
	// process.step, ADR 0034): its guidance reaches the actions of this process.
	Step *StepContext `json:"step,omitempty"`
	// Inbox holds the events a companion run received while it was at work, handled one after the other (ADR 0036 §3).
	Inbox []map[string]any `json:"inbox,omitempty"`
	// Disabled lists actions excluded from planning after repeatedly failing
	// to deliver their effects.
	Disabled  map[string]bool `json:"disabled,omitempty"`
	Error     string          `json:"error,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
	// MethodologyVersion is the published version run by the process.
	MethodologyVersion string `json:"methodologyVersion,omitempty"`
	// JournalSeq numbers the execution journal records of the process.
	JournalSeq int `json:"journalSeq,omitempty"`
	// Started marks that the process.started journal record was written (Run
	// no longer uses JournalSeq==0 for this, since a deferred process (ADR
	// 0031, gap 5) may journal an attach record before it ever gets to Run).
	Started bool `json:"started,omitempty"`
}

// Task kinds.
const (
	TaskInput    = "input"    // a human action: submit items
	TaskApproval = "approval" // an action needing a permission the initiator lacks
	TaskAgent    = "agent"    // a script action waiting for a sub-agent
	TaskFlow     = "flow"     // a relaunched flow is ready: a human adopts or discards it
	// TaskBoard: the blackboard is inconsistent; a human relaunches the step that produced the
	// faulty content (Proposal) or ignores the issues. TaskRelaunched: the process waits for the
	// decision of the flow relaunched to fix it.
	TaskBoard      = "board"
	TaskRelaunched = "relaunched"
	// TaskCondition: no plan reaches the goal only because of conditions none of the agent's actions establish
	// (another process, a person, the state of the change will): the process waits for them, and is tried again
	// when the change moves. A process that no such condition would unblock is stuck.
	TaskCondition = "condition"
	// TaskUnblock: the process is stuck; whoever answers for it declares conditions established (a waiver on the
	// change), retries the actions it gave up on, or abandons it (Engine.Unblock). The same decision answers a
	// TaskCondition: a person may always unblock a run instead of waiting.
	TaskUnblock = "unblock"
)

// HumanTask is a pending human action or approval.
type HumanTask struct {
	Kind         string `json:"kind"`
	Permission   string `json:"permission,omitempty"`
	Action       string `json:"action"`
	Description  string `json:"description"`
	Instructions string `json:"instructions,omitempty"`
	// NodeTypes restricts which qualified node types (<namespace>@<NodeType>) a
	// TaskInput may create or pick to edit; empty: every type of the change's
	// namespace (methodology.Action.NodeTypes).
	NodeTypes []string `json:"nodeTypes,omitempty"`
	Step      int      `json:"step"`
	// ChildProcessID is the sub-agent a TaskAgent waits for.
	ChildProcessID string `json:"childProcessId,omitempty"`
	// WakeOn lists signal names (TaskAgent) that, if emitted by the child and
	// addressed to this process before it terminates, wake this process early.
	WakeOn []string `json:"wakeOn,omitempty"`
	// TaskBoard: what is wrong, and the earliest step to restart from (nil when none can be).
	Issues   []domain.BoardIssue `json:"issues,omitempty"`
	Proposal *RelaunchProposal   `json:"proposal,omitempty"`
	// FlowID is the flow a TaskRelaunched waits for.
	FlowID string `json:"flowId,omitempty"`
	// Context is the step of a process the task belongs to (ADR 0034, ADR 0035 §2): what to do and how.
	Context *StepContext `json:"context,omitempty"`
	// Conditions a TaskCondition waits for, or that would unblock a TaskUnblock ("name" when expected true, "!name"
	// when expected false).
	Conditions []string `json:"conditions,omitempty"`
}

// WaitsForConditions reports whether the process waits for conditions established outside it (TaskCondition).
func (p *Process) WaitsForConditions() bool {
	return p.Status == StatusWaiting && p.Pending != nil && p.Pending.Kind == TaskCondition
}

// Usage accounts LLM tokens and calls.
type Usage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	LLMCalls     int   `json:"llmCalls"`
	ToolCalls    int   `json:"toolCalls"`
}

// Add accumulates u2.
func (u *Usage) Add(u2 Usage) {
	u.InputTokens += u2.InputTokens
	u.OutputTokens += u2.OutputTokens
	u.LLMCalls += u2.LLMCalls
	u.ToolCalls += u2.ToolCalls
}

// LLMCall records one model call.
type LLMCall struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	DurationMs   int64  `json:"durationMs"`
	Error        string `json:"error,omitempty"`
}

// ToolCall records one tool call.
type ToolCall struct {
	Name       string `json:"name"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
}

// LogLine is a process log line.
type LogLine struct {
	Time      time.Time `json:"time"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	ProcessID string    `json:"processId,omitempty"`
	Action    string    `json:"action,omitempty"`
	Step      int       `json:"step"`
}

// Step records one executed action.
type Step struct {
	Index  int             `json:"index"`
	Action string          `json:"action"`
	Plan   []string        `json:"plan"`
	Before goap.WorldState `json:"before"`
	After  goap.WorldState `json:"after,omitempty"`
	Items  []domain.ItemID `json:"items,omitempty"`
	// Nodes are the change impacts the step declared (ADR 0024).
	Nodes []domain.ChangeImpactID `json:"nodes,omitempty"`
	// Decisions are the decision points the step opened, ruled or answered a question of (ADR 0009 §4).
	Decisions []string `json:"decisions,omitempty"`
	// Reads are the node versions referenced on the blackboard when the step
	// started; BoardBefore / BoardAfter the item count of the change around it.
	Reads       []domain.NodeRef `json:"reads,omitempty"`
	BoardBefore int              `json:"boardBefore,omitempty"`
	BoardAfter  int              `json:"boardAfter,omitempty"`
	// LastItem is the last item of the flow when the step started (the fork point of a relaunch).
	LastItem   domain.ItemID `json:"lastItem,omitempty"`
	EffectsMet bool          `json:"effectsMet"`
	// Progress: an incremental action produced items without reaching its effects yet.
	Progress   bool       `json:"progress,omitempty"`
	ApprovedBy string     `json:"approvedBy,omitempty"`
	Usage      Usage      `json:"usage"`
	LLMCalls   []LLMCall  `json:"llmCalls,omitempty"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	Logs       []LogLine  `json:"logs,omitempty"`
	Children   []string   `json:"children,omitempty"`
	Sandbox    string     `json:"sandbox,omitempty"`
	// Specialization is the action actually run for an abstract action.
	Specialization string `json:"specialization,omitempty"`
	// Execution is the journal record of the step; SpanID its OpenTelemetry span.
	Execution string    `json:"execution,omitempty"`
	SpanID    string    `json:"spanId,omitempty"`
	Output    string    `json:"output,omitempty"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitempty"`
}

// Pending reports whether the step has not ended yet (waiting for a human).
func (s *Step) Pending() bool { return s.EndedAt.IsZero() }

// Store persists processes.
type Store interface {
	Get(ctx context.Context, id string) (*Process, error)
	Put(ctx context.Context, p *Process) error
	List(ctx context.Context) ([]*Process, error)
	// AppendProcessLog records entries of a process's own log (ADR 0031):
	// turn-by-turn state that would otherwise force a full rewrite of the
	// process row (e.g. the intent/clarification dialogue), independent of
	// any change.
	AppendProcessLog(ctx context.Context, processID string, entries ...ProcessLogEntry) error
	ListProcessLog(ctx context.Context, processID string) ([]ProcessLogEntry, error)
}

// ProcessLogEntry is one append-only entry of a process's own log.
type ProcessLogEntry struct {
	Seq       int64          `json:"seq"`
	ProcessID string         `json:"processId"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload,omitempty"`
	At        time.Time      `json:"at"`
}

// ErrNotFound is returned for unknown processes.
var ErrNotFound = fmt.Errorf("process not found")

// MemoryStore is an in-memory Store.
type MemoryStore struct {
	mu  sync.RWMutex
	m   map[string]*Process
	log map[string][]ProcessLogEntry
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{m: map[string]*Process{}, log: map[string][]ProcessLogEntry{}}
}

// AppendProcessLog implements Store.
func (s *MemoryStore) AppendProcessLog(_ context.Context, processID string, entries ...ProcessLogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		e.ProcessID = processID
		e.Seq = int64(len(s.log[processID])) + 1
		s.log[processID] = append(s.log[processID], e)
	}
	return nil
}

// ListProcessLog implements Store.
func (s *MemoryStore) ListProcessLog(_ context.Context, processID string) ([]ProcessLogEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]ProcessLogEntry(nil), s.log[processID]...), nil
}

func clone(p *Process) *Process {
	c := *p
	c.Steps = append([]Step(nil), p.Steps...)
	c.Intent.Turns = append([]intent.Turn(nil), p.Intent.Turns...)
	c.Children = maps.Clone(p.Children)
	c.Disabled = maps.Clone(p.Disabled)
	c.Vars = maps.Clone(p.Vars)
	return &c
}

// Get implements Store.
func (s *MemoryStore) Get(_ context.Context, id string) (*Process, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.m[id]
	if !ok {
		return nil, fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	return clone(p), nil
}

// Put implements Store.
func (s *MemoryStore) Put(_ context.Context, p *Process) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[p.ID] = clone(p)
	return nil
}

// List implements Store.
func (s *MemoryStore) List(_ context.Context) ([]*Process, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Process, 0, len(s.m))
	for _, p := range s.m {
		out = append(out, clone(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// RelaunchProposal is the step to restart from to fix an inconsistent blackboard: the earliest
// step, over every run of the change, that produced faulty content.
type RelaunchProposal struct {
	Process string `json:"process"`
	Step    int    `json:"step"`
	Action  string `json:"action"`
	Reason  string `json:"reason"`
	// Culprits are the faulty items the step produced.
	Culprits []domain.ItemID `json:"culprits"`
}

// Queued is why and by whom a process was made runnable, and what caused it.
type Queued struct {
	// Reason: started, sub-agent, trigger, answered, input, approved, rejected, relaunched, sub-agent-ended,
	// board-ignored, flow-adopted, flow-discarded.
	Reason string         `json:"reason"`
	By     string         `json:"by,omitempty"`
	At     time.Time      `json:"at"`
	Cause  map[string]any `json:"cause,omitempty"`
}
