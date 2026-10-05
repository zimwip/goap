package pbconv

import (
	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/pkg/journal"
)

// ExecutionToPB converts a journal record (the prompts of its model calls are entries of the log of their own, never part
// of it).
func ExecutionToPB(r journal.Record) *graphv1.ExecutionRecord {
	out := &graphv1.ExecutionRecord{Id: r.ID, ChangeId: string(r.ChangeID), ProcessId: r.ProcessID, ParentProcessId: r.ParentProcessID,
		Seq: int32(r.Seq), Kind: r.Kind, Methodology: r.Methodology, MethodologyVersion: r.MethodologyVersion, Agent: r.Agent,
		Planner: r.Planner, Goal: r.Goal, Status: r.Status, Step: int32(r.Step), Action: r.Action, ActionKind: r.ActionKind,
		Specialization: r.Specialization, Plan: r.Plan, Before: r.Before, After: r.After, EffectsMet: r.EffectsMet,
		InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, Actor: r.Actor, Output: r.Output, Error: r.Error,
		TraceId: r.TraceID, SpanId: r.SpanID, Data: Struct(r.Data), StartedAt: Time(r.StartedAt), EndedAt: Time(r.EndedAt), DurationMs: r.DurationMs}
	for _, it := range r.Items {
		out.Items = append(out.Items, string(it))
	}
	out.BoardBefore, out.BoardAfter, out.BoardLast = int32(r.BoardBefore), int32(r.BoardAfter), string(r.BoardLast)
	out.Flow = r.Flow
	for _, n := range r.Reads {
		out.Reads = append(out.Reads, RefToPB(n))
	}
	for _, c := range r.ModelCalls {
		out.ModelCalls = append(out.ModelCalls, &graphv1.ModelCall{Provider: c.Provider, Model: c.Model, InputTokens: c.InputTokens,
			OutputTokens: c.OutputTokens, DurationMs: c.DurationMs, Error: c.Error})
	}
	for _, c := range r.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, &graphv1.ToolUse{Name: c.Name, DurationMs: c.DurationMs, Error: c.Error})
	}
	return out
}

func ExecutionsToPB(rs []journal.Record) []*graphv1.ExecutionRecord {
	out := make([]*graphv1.ExecutionRecord, len(rs))
	for i, r := range rs {
		out[i] = ExecutionToPB(r)
	}
	return out
}
