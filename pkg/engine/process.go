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
	StatusWaiting    Status = "waiting" // waiting for a human action
	StatusCompleted  Status = "completed"
	StatusStuck      Status = "stuck" // no plan reaches the goal
	StatusFailed     Status = "failed"
)

// Terminal reports whether the process can no longer progress by itself.
func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusStuck || s == StatusFailed
}

// Process is an agent process working on a change.
type Process struct {
	ID          string          `json:"id"`
	Methodology string          `json:"methodology"`
	ChangeID    domain.ChangeID `json:"changeId"`
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
	Title      string            `json:"title,omitempty"`
	Usage      Usage             `json:"usage"`
	TraceID    string            `json:"traceId,omitempty"`
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
}

// Task kinds.
const (
	TaskInput    = "input"    // a human action: submit items
	TaskApproval = "approval" // an action needing a permission the initiator lacks
	TaskAgent    = "agent"    // a script action waiting for a sub-agent
)

// HumanTask is a pending human action or approval.
type HumanTask struct {
	Kind         string `json:"kind"`
	Permission   string `json:"permission,omitempty"`
	Action       string `json:"action"`
	Description  string `json:"description"`
	Instructions string `json:"instructions,omitempty"`
	Step         int    `json:"step"`
	// ChildProcessID is the sub-agent a TaskAgent waits for.
	ChildProcessID string `json:"childProcessId,omitempty"`
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
	Index      int             `json:"index"`
	Action     string          `json:"action"`
	Plan       []string        `json:"plan"`
	Before     goap.WorldState `json:"before"`
	After      goap.WorldState `json:"after,omitempty"`
	Items      []domain.ItemID `json:"items,omitempty"`
	EffectsMet bool            `json:"effectsMet"`
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
}

// ErrNotFound is returned for unknown processes.
var ErrNotFound = fmt.Errorf("process not found")

// MemoryStore is an in-memory Store.
type MemoryStore struct {
	mu sync.RWMutex
	m  map[string]*Process
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{m: map[string]*Process{}} }

func clone(p *Process) *Process {
	c := *p
	c.Steps = append([]Step(nil), p.Steps...)
	c.Intent.Turns = append([]intent.Turn(nil), p.Intent.Turns...)
	c.Children = maps.Clone(p.Children)
	c.Disabled = maps.Clone(p.Disabled)
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
