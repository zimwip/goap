package engine

import (
	"context"
	"time"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/methodology"
)

// Internals reaches the unexported workings of an engine for the integration tests of the platform
// (internal/enginee2e): they run the engine on a real graph, which the engine's own tests do not import, so that the
// engine depends on the graph's contract only (ADR 0125 §5). Production code never calls it (pkg/layering).
type Internals struct{ e *Engine }

// InternalsOf gives the internals of e.
func InternalsOf(e *Engine) Internals { return Internals{e} }

// ForeachSuffix separates a step from the element of its foreach in the key of a child.
const ForeachSuffix = foreachSuffix

// Schedule runs process id the way the engine schedules a run.
func (i Internals) Schedule(id string) { i.e.schedule(id) }

// SetClock replaces the engine's clock; nil restores the real one.
func (i Internals) SetClock(now func() time.Time) { i.e.now = now }

// SetInflight sets the number of runs the engine counts as held (MaxRuns).
func (i Internals) SetInflight(n int64) { i.e.inflight.Store(n) }

// Observe reads the blackboard of the change of p as a run does.
func (i Internals) Observe(ctx context.Context, p *Process, m *methodology.Compiled) (domain.Blackboard, error) {
	return i.e.observe(ctx, p, m)
}

// Blackboard reads the blackboard of a change with its history.
func (i Internals) Blackboard(ctx context.Context, id domain.ChangeID, flow string) (domain.Blackboard, error) {
	return i.e.blackboard(ctx, id, flow)
}

// MethodologyOf gives the methodology version p runs.
func (i Internals) MethodologyOf(ctx context.Context, p *Process) (*methodology.Compiled, error) {
	return i.e.methodologyOf(ctx, p)
}

// ResolveChange gives the change an attach request names for p.
func (i Internals) ResolveChange(ctx context.Context, p *Process, m *methodology.Compiled, req AttachRequest) (domain.ChangeID, error) {
	return i.e.resolveChange(ctx, p, m, req)
}

// ApplyNodeOps applies the node operations of an action of p.
func (i Internals) ApplyNodeOps(ctx context.Context, p *Process, ops []dsl.NodeOp, producedBy, execution string) ([]domain.ChangeImpactID, error) {
	return i.e.applyNodeOps(ctx, p, ops, producedBy, execution)
}

// ApplyDecisionOps applies the decision operations of an action of p.
func (i Internals) ApplyDecisionOps(ctx context.Context, p *Process, ops []DecisionOp, human bool, by string) ([]string, error) {
	return i.e.applyDecisionOps(ctx, p, ops, human, by)
}

// NewHost gives the host an action of p runs in.
func (i Internals) NewHost(p *Process, action methodology.Action, agentMCPs []string) *Host {
	return i.e.newHost(p, action, agentMCPs)
}

// WithVerify marks ctx as running action a, for its verification.
func (Internals) WithVerify(ctx context.Context, a methodology.Action) context.Context {
	return withVerify(ctx, a)
}

// ExpireAllDerogations runs the builtin expiring the derogations.
func (Internals) ExpireAllDerogations(ctx context.Context, ac ActionContext) (ActionResult, error) {
	return expireAllDerogations(ctx, ac)
}

// MergeTaskOf gives the merge task of change c.
func (Internals) MergeTaskOf(ctx context.Context, g GraphPort, c domain.Change) (*MergeTask, error) {
	return mergeTaskOf(ctx, g, c)
}

// RunLoop runs the tool loop of an llm action.
func (Internals) RunLoop(x LLMExecutor, ctx context.Context, ac ActionContext, prompt, system string, tools []mcp.ToolInfo) (ActionResult, error) {
	return x.runLoop(ctx, ac, prompt, system, tools)
}

// CronFire fires the schedule trigger of key.
func (Internals) CronFire(tm *TriggerManager, key string) { tm.cronFire(key) }
