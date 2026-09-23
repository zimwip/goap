// Package observe analyzes the execution journal of agent processes (and
// their OpenTelemetry spans) to find what makes a methodology costly, and
// turns the findings into improvement proposals on the methodology model
// (ADR 0011): specializations, agent definitions, new actions, MCP tools.
package observe

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// Finding kinds.
const (
	FindLoop           = "loop"               // an action executed again and again
	FindFailure        = "failure"            // executions without the promised effects
	FindDisabled       = "disabled"           // actions disabled after repeated failures
	FindLLMHeavy       = "llm_heavy"          // an action concentrating model tokens / calls
	FindSystematizable = "llm_systematizable" // model calls producing regular, systematizable output
	FindReplanning     = "replanning"         // the plan changed course often
	FindSlowAction     = "slow_action"        // long executions
	FindSlowSpan       = "slow_span"          // slow operations seen in the traces (tools, model calls)
)

// Thresholds tune the analysis.
type Thresholds struct {
	LoopExecutions  int     // executions of one action in a run (default 3)
	Failures        int     // executions without effects (default 2)
	LLMTokenShare   float64 // share of the run tokens (default 0.4)
	LLMCalls        int     // model calls of one action (default 3)
	Replans         int     // plan changes in a run (default 3)
	SlowActionMs    int64   // default 30 s
	SlowSpanMs      int64   // default 10 s
	MinSystematized int     // items needed to judge an output regular (default 2)
}

func (t Thresholds) withDefaults() Thresholds {
	def := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	def(&t.LoopExecutions, 3)
	def(&t.Failures, 2)
	def(&t.LLMCalls, 3)
	def(&t.Replans, 3)
	def(&t.MinSystematized, 2)
	if t.LLMTokenShare == 0 {
		t.LLMTokenShare = 0.4
	}
	if t.SlowActionMs == 0 {
		t.SlowActionMs = 30_000
	}
	if t.SlowSpanMs == 0 {
		t.SlowSpanMs = 10_000
	}
	return t
}

// Span is an OpenTelemetry span of the observed run (from the trace backend).
type Span struct {
	Name       string            `json:"name"`
	DurationMs int64             `json:"durationMs"`
	Error      bool              `json:"error,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// ActionStats aggregates the executions of one action.
type ActionStats struct {
	Action          string         `json:"action"`
	Kind            string         `json:"kind"`
	Agent           string         `json:"agent"`
	Executions      int            `json:"executions"`
	Failures        int            `json:"failures"`
	ModelCalls      int            `json:"modelCalls"`
	ToolCalls       int            `json:"toolCalls"`
	Tokens          int64          `json:"tokens"`
	DurationMs      int64          `json:"durationMs"`
	MaxDurationMs   int64          `json:"maxDurationMs"`
	Outputs         map[string]int `json:"outputs,omitempty"` // produced items by kind[/type|op]
	Specializations map[string]int `json:"specializations,omitempty"`
	Records         []string       `json:"records,omitempty"`
}

// SpanStats aggregates spans by name.
type SpanStats struct {
	Name          string `json:"name"`
	Count         int    `json:"count"`
	Errors        int    `json:"errors"`
	DurationMs    int64  `json:"durationMs"`
	MaxDurationMs int64  `json:"maxDurationMs"`
	Action        string `json:"action,omitempty"`
	Tool          string `json:"tool,omitempty"`
}

// Finding is a costly element of the methodology.
type Finding struct {
	Kind        string `json:"kind"`
	Methodology string `json:"methodology"`
	Agent       string `json:"agent,omitempty"`
	Action      string `json:"action,omitempty"`
	// Element is the metamodel key of the element the finding is about.
	Element  string   `json:"element"`
	Count    int      `json:"count,omitempty"`
	Tokens   int64    `json:"tokens,omitempty"`
	Duration int64    `json:"durationMs,omitempty"`
	Share    float64  `json:"share,omitempty"`
	Evidence string   `json:"evidence"`
	Records  []string `json:"records,omitempty"`
	Span     string   `json:"span,omitempty"`
}

// Report is the cost analysis of a run (a process and its sub-agents).
type Report struct {
	Process     string        `json:"process"`
	Change      string        `json:"change"`
	Methodology string        `json:"methodology"`
	Version     string        `json:"version"`
	Agent       string        `json:"agent"`
	Status      string        `json:"status"`
	Processes   int           `json:"processes"`
	Ticks       int           `json:"ticks"`
	Replans     int           `json:"replans"`
	Steps       int           `json:"steps"`
	Tokens      int64         `json:"tokens"`
	ModelCalls  int           `json:"modelCalls"`
	ToolCalls   int           `json:"toolCalls"`
	DurationMs  int64         `json:"durationMs"`
	TraceID     string        `json:"traceId,omitempty"`
	Actions     []ActionStats `json:"actions"`
	Spans       []SpanStats   `json:"spans,omitempty"`
	Findings    []Finding     `json:"findings"`
}

// ElementKey returns the metamodel key of an element (see pkg/metamodel).
func ElementKey(meth, kind, name string) string {
	if kind == "methodology" {
		return "M:" + meth
	}
	return "M:" + meth + "/" + kind + "/" + name
}

func outputKey(it domain.ChangeItem) string {
	switch {
	case it.Proposal != nil:
		return string(it.Kind) + "/" + string(it.Proposal.Op)
	case it.Type != "":
		return string(it.Kind) + "/" + it.Type
	}
	return string(it.Kind)
}

// Analyze computes the cost report of a run from its journal records, the
// items of the observed change and the spans of its trace (optional).
func Analyze(recs []domain.ExecutionRecord, items map[domain.ItemID]domain.ChangeItem, spans []Span, th Thresholds) Report {
	th = th.withDefaults()
	var r Report
	stats := map[string]*ActionStats{}
	agents := map[string]string{}
	procs := map[string]bool{}
	var order []string
	for _, rec := range recs {
		procs[rec.ProcessID] = true
		if r.Process == "" || rec.ParentProcessID == "" && rec.Kind == domain.ExecProcessStarted {
			r.Process, r.Change, r.Methodology, r.Version, r.Agent = rec.ProcessID, string(rec.ChangeID), rec.Methodology, rec.MethodologyVersion, rec.Agent
		}
		if r.TraceID == "" && rec.ParentProcessID == "" {
			r.TraceID = rec.TraceID
		}
		switch rec.Kind {
		case domain.ExecTick:
			r.Ticks++
			if rep, _ := rec.Data["replanned"].(bool); rep {
				r.Replans++
			}
		case domain.ExecProcessEnded:
			if rec.ParentProcessID == "" {
				r.Status, r.DurationMs = rec.Status, rec.DurationMs
			}
		case domain.ExecAction:
			key := rec.Agent + "/" + rec.Action
			s, ok := stats[key]
			if !ok {
				s = &ActionStats{Action: rec.Action, Kind: rec.ActionKind, Agent: rec.Agent, Outputs: map[string]int{}, Specializations: map[string]int{}}
				stats[key] = s
				order = append(order, key)
			}
			agents[rec.Action] = rec.Agent
			if waiting, _ := rec.Data["waiting"].(string); waiting != "" {
				continue // the execution goes on after a human / sub-agent
			}
			r.Steps++
			s.Executions++
			if rec.Error != "" || (rec.EffectsMet != nil && !*rec.EffectsMet) {
				s.Failures++
			}
			s.ModelCalls += len(rec.ModelCalls)
			s.ToolCalls += len(rec.ToolCalls)
			s.Tokens += rec.InputTokens + rec.OutputTokens
			s.DurationMs += rec.DurationMs
			s.MaxDurationMs = max(s.MaxDurationMs, rec.DurationMs)
			if rec.Specialization != "" {
				s.Specializations[rec.Specialization]++
			}
			for _, id := range rec.Items {
				if it, ok := items[id]; ok {
					s.Outputs[outputKey(it)]++
				}
			}
			s.Records = append(s.Records, rec.ID)
			r.Tokens += rec.InputTokens + rec.OutputTokens
			r.ModelCalls += len(rec.ModelCalls)
			r.ToolCalls += len(rec.ToolCalls)
		}
	}
	r.Processes = len(procs)
	for _, k := range order {
		r.Actions = append(r.Actions, *stats[k])
	}
	r.Spans = aggregateSpans(spans)
	r.Findings = findings(r, th)
	return r
}

func aggregateSpans(spans []Span) []SpanStats {
	by := map[string]*SpanStats{}
	for _, sp := range spans {
		s, ok := by[sp.Name]
		if !ok {
			s = &SpanStats{Name: sp.Name, Action: sp.Attributes["goap.action"], Tool: sp.Attributes["gen_ai.tool.name"]}
			by[sp.Name] = s
		}
		s.Count++
		s.DurationMs += sp.DurationMs
		s.MaxDurationMs = max(s.MaxDurationMs, sp.DurationMs)
		if sp.Error {
			s.Errors++
		}
	}
	out := make([]SpanStats, 0, len(by))
	for _, s := range by {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DurationMs > out[j].DurationMs })
	return out
}

func findings(r Report, th Thresholds) []Finding {
	var out []Finding
	el := func(kind, name string) string { return ElementKey(r.Methodology, kind, name) }
	for _, s := range r.Actions {
		base := Finding{Methodology: r.Methodology, Agent: s.Agent, Action: s.Action, Element: el("action", s.Action), Records: s.Records}
		if s.Executions >= th.LoopExecutions {
			f := base
			f.Kind, f.Count = FindLoop, s.Executions
			f.Evidence = fmt.Sprintf("%s exécutée %d fois dans le run (seuil %d)", s.Action, s.Executions, th.LoopExecutions)
			out = append(out, f)
		}
		if s.Failures >= th.Failures {
			f := base
			f.Kind, f.Count = FindFailure, s.Failures
			f.Evidence = fmt.Sprintf("%s : %d exécutions sans les effets promis sur %d", s.Action, s.Failures, s.Executions)
			out = append(out, f)
		}
		if s.ModelCalls > 0 {
			share := 0.0
			if r.Tokens > 0 {
				share = float64(s.Tokens) / float64(r.Tokens)
			}
			if regular(s, th) {
				f := base
				f.Kind, f.Tokens, f.Share, f.Count = FindSystematizable, s.Tokens, share, s.ModelCalls
				f.Evidence = fmt.Sprintf("%s : %d appels LLM (%d tokens) pour une sortie régulière (%s) : systématisable par un script",
					s.Action, s.ModelCalls, s.Tokens, strings.Join(slices.Sorted(maps.Keys(s.Outputs)), ", "))
				out = append(out, f)
			} else if s.ModelCalls >= th.LLMCalls || (r.Tokens > 0 && share >= th.LLMTokenShare && s.Tokens > 0) {
				f := base
				f.Kind, f.Tokens, f.Share, f.Count = FindLLMHeavy, s.Tokens, share, s.ModelCalls
				f.Evidence = fmt.Sprintf("%s : %d appels LLM, %d tokens (%.0f %% du run)", s.Action, s.ModelCalls, s.Tokens, 100*share)
				out = append(out, f)
			}
		}
		if s.MaxDurationMs >= th.SlowActionMs {
			f := base
			f.Kind, f.Duration = FindSlowAction, s.MaxDurationMs
			f.Evidence = fmt.Sprintf("%s : jusqu'à %d ms par exécution", s.Action, s.MaxDurationMs)
			out = append(out, f)
		}
	}
	if r.Replans >= th.Replans {
		out = append(out, Finding{Kind: FindReplanning, Methodology: r.Methodology, Agent: r.Agent, Element: el("agent", r.Agent), Count: r.Replans,
			Evidence: fmt.Sprintf("le plan a changé %d fois sur %d cycles : préconditions / effets à revoir", r.Replans, r.Ticks)})
	}
	for _, sp := range r.Spans {
		if sp.MaxDurationMs < th.SlowSpanMs {
			continue
		}
		f := Finding{Kind: FindSlowSpan, Methodology: r.Methodology, Agent: r.Agent, Action: sp.Action, Span: sp.Name, Duration: sp.MaxDurationMs, Count: sp.Count,
			Element: el("methodology", ""), Evidence: fmt.Sprintf("span %q : %d appels, jusqu'à %d ms", sp.Name, sp.Count, sp.MaxDurationMs)}
		if sp.Action != "" {
			f.Element = el("action", sp.Action)
		}
		out = append(out, f)
	}
	return out
}

// regular reports whether the output of an LLM action looks systematizable:
// one single shape of produced items.
func regular(s ActionStats, th Thresholds) bool {
	if s.Kind != "llm" || len(s.Outputs) != 1 {
		return false
	}
	n := 0
	for _, c := range s.Outputs {
		n += c
	}
	return n >= th.MinSystematized
}

// WithDisabled adds findings for actions disabled in the run.
func (r *Report) WithDisabled(agent string, disabled []string) {
	for _, a := range disabled {
		r.Findings = append(r.Findings, Finding{Kind: FindDisabled, Methodology: r.Methodology, Agent: agent, Action: a,
			Element: ElementKey(r.Methodology, "action", a), Evidence: a + " désactivée après des échecs répétés"})
	}
}
