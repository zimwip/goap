package engine

import (
	"context"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/domain"
)

// SpanIDer is implemented by tracers that expose the current span id, so that
// journal records link to their OpenTelemetry span.
type SpanIDer interface {
	SpanID(ctx context.Context) string
}

func (e *Engine) spanID(ctx context.Context) string {
	if s, ok := e.tracer().(SpanIDer); ok {
		return s.SpanID(ctx)
	}
	return ""
}

// journal appends records to the execution journal of the change of p (ADR
// 0011). It is best effort: a journal failure is logged, never fails the process.
func (e *Engine) journal(ctx context.Context, p *Process, recs ...domain.ExecutionRecord) {
	if p.ChangeID == "" || len(recs) == 0 {
		return
	}
	for i := range recs {
		r := &recs[i]
		if r.ID == "" {
			r.ID = uuid.NewString()
		}
		p.JournalSeq++
		r.Seq = p.JournalSeq
		r.ChangeID, r.ProcessID, r.ParentProcessID = p.ChangeID, p.ID, p.ParentID
		r.Methodology, r.MethodologyVersion, r.Agent, r.Planner, r.Goal = p.Methodology, p.MethodologyVersion, p.Agent, p.Planner, p.Goal
		if r.Status == "" {
			r.Status = string(p.Status)
		}
		if r.TraceID == "" {
			r.TraceID = e.tracer().TraceID(ctx)
		}
		if r.StartedAt.IsZero() {
			r.StartedAt = e.clock()
		}
		if !r.EndedAt.IsZero() {
			r.DurationMs = r.EndedAt.Sub(r.StartedAt).Milliseconds()
		}
	}
	if err := e.Graph.Record(ctx, recs); err != nil {
		e.log().Warn("journal write failed", "process", p.ID, "err", err)
	}
}

// actionRecord describes the execution of step i.
func actionRecord(p *Process, i int, kind, id string) domain.ExecutionRecord {
	s := p.Steps[i]
	r := domain.ExecutionRecord{ID: id, Kind: domain.ExecAction, Step: i, Action: s.Action, ActionKind: kind, Specialization: s.Specialization,
		Plan: s.Plan, Before: maps.Clone(s.Before), After: maps.Clone(s.After), Items: slices.Clone(s.Items),
		InputTokens: s.Usage.InputTokens, OutputTokens: s.Usage.OutputTokens, Actor: s.ApprovedBy, Output: truncate(s.Output, 2000),
		Error: s.Error, SpanID: s.SpanID, StartedAt: s.StartedAt, EndedAt: s.EndedAt}
	if !s.EndedAt.IsZero() && s.Error == "" && p.Pending == nil {
		met := s.EffectsMet
		r.EffectsMet = &met
	}
	for _, c := range s.LLMCalls {
		r.ModelCalls = append(r.ModelCalls, domain.ModelCall{Provider: c.Provider, Model: c.Model, InputTokens: c.InputTokens,
			OutputTokens: c.OutputTokens, DurationMs: c.DurationMs, Error: c.Error})
	}
	for _, c := range s.ToolCalls {
		r.ToolCalls = append(r.ToolCalls, domain.ToolUse{Name: c.Name, DurationMs: c.DurationMs, Error: c.Error})
	}
	if p.Pending != nil && p.Pending.Step == i {
		r.Data = map[string]any{"waiting": p.Pending.Kind}
		if p.Pending.ChildProcessID != "" {
			r.Data["child"] = p.Pending.ChildProcessID
		}
	}
	if len(s.Children) > 0 {
		if r.Data == nil {
			r.Data = map[string]any{}
		}
		r.Data["children"] = slices.Clone(s.Children)
	}
	return r
}

// replanned reports whether the new plan departs from the continuation of
// the previous one (the previous first action was executed).
func replanned(prev, next []string) bool {
	if len(prev) == 0 {
		return false
	}
	return !slices.Equal(prev[1:], next)
}

// endRecord summarizes a finished process.
func endRecord(p *Process) domain.ExecutionRecord {
	disabled := slices.Sorted(maps.Keys(p.Disabled))
	return domain.ExecutionRecord{Kind: domain.ExecProcessEnded, Error: p.Error, Step: len(p.Steps),
		InputTokens: p.Usage.InputTokens, OutputTokens: p.Usage.OutputTokens,
		Data: map[string]any{"steps": len(p.Steps), "llmCalls": p.Usage.LLMCalls, "toolCalls": p.Usage.ToolCalls,
			"disabled": disabled, "trigger": p.Trigger},
		StartedAt: p.CreatedAt}
}
