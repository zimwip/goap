package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/observe"
)

// Builtins of the self-observation methodology (ADR 0011).
const (
	BuiltinObserveAnalyze   = "observe.analyze"
	BuiltinObservePropose   = "observe.propose"
	BuiltinMethodologyDraft = "methodology.draft"
)

// TraceSource reads the spans of a trace from the OpenTelemetry backend.
type TraceSource interface {
	Spans(ctx context.Context, traceID string) ([]observe.Span, error)
}

// MethodologyDrafts reads methodology definitions and saves improved
// versions as drafts (the registry).
type MethodologyDrafts interface {
	// Definition returns a version (empty: latest published); ok is false when it does not exist.
	Definition(ctx context.Context, name, version string) (m methodology.Methodology, ok bool, err error)
	SaveDraft(ctx context.Context, m methodology.Methodology) (methodology.Issues, error)
}

// SelfImprovement configures the self-observation builtins.
type SelfImprovement struct {
	Traces     TraceSource       // optional: slow spans of the observed run
	Drafts     MethodologyDrafts // required by methodology.draft
	Thresholds observe.Thresholds
	// TraceURL is the trace viewer prefix (e.g. http://localhost:16686/trace/).
	TraceURL string
}

// SelfImprovementBuiltins returns the builtins that analyze a finished run
// from its journal and traces, propose improvements of its methodology and
// turn the accepted ones into a draft version.
func (e *Engine) SelfImprovementBuiltins(cfg SelfImprovement) BuiltinExecutor {
	return BuiltinExecutor{
		BuiltinObserveAnalyze: func(ctx context.Context, ac ActionContext) (ActionResult, error) {
			return e.observeAnalyze(ctx, ac, cfg)
		},
		BuiltinObservePropose: func(ctx context.Context, ac ActionContext) (ActionResult, error) { return e.observePropose(ctx, ac) },
		BuiltinMethodologyDraft: func(ctx context.Context, ac ActionContext) (ActionResult, error) {
			return e.methodologyDraft(ctx, ac, cfg)
		},
	}
}

// observedProcess is the process to analyze: vars.process, or the process of
// the trigger event (vars.event.process.id).
func observedProcess(vars map[string]any) string {
	if id, ok := vars["process"].(string); ok && id != "" {
		return id
	}
	ev, _ := vars["event"].(map[string]any)
	p, _ := ev["process"].(map[string]any)
	id, _ := p["id"].(string)
	return id
}

func (e *Engine) observeAnalyze(ctx context.Context, ac ActionContext, cfg SelfImprovement) (ActionResult, error) {
	id := observedProcess(ac.Process.Vars)
	if id == "" {
		return ActionResult{}, errors.New("no process to observe (vars.process or a process event)")
	}
	obs, err := e.Store.Get(ctx, id)
	if err != nil {
		return ActionResult{}, err
	}
	ids := []string{obs.ID}
	if all, err := e.Store.List(ctx); err == nil {
		for grew := true; grew; {
			grew = false
			for _, p := range all {
				if p.ParentID != "" && slices.Contains(ids, p.ParentID) && !slices.Contains(ids, p.ID) {
					ids, grew = append(ids, p.ID), true
				}
			}
		}
	}
	recs, err := e.Graph.Journal(ctx, domain.ExecutionFilter{ProcessIDs: ids})
	if err != nil {
		return ActionResult{}, err
	}
	items := map[domain.ItemID]domain.ChangeItem{}
	if obs.ChangeID != "" {
		if bb, err := e.Graph.Blackboard(ctx, obs.ChangeID); err == nil {
			for _, it := range bb.Change.Items {
				items[it.ID] = it
			}
		}
	}
	var logs []LogLine
	var spans []observe.Span
	if cfg.Traces != nil && obs.TraceID != "" {
		if spans, err = cfg.Traces.Spans(ctx, obs.TraceID); err != nil {
			logs = append(logs, LogLine{Time: e.clock(), Level: "warn", Message: "traces unavailable: " + err.Error()})
			spans = nil
		}
	}
	r := observe.Analyze(recs, items, spans, cfg.Thresholds)
	r.WithDisabled(obs.Agent, slices.Sorted(maps.Keys(obs.Disabled)))
	if r.Process == "" {
		r.Process, r.Methodology, r.Version, r.Agent, r.Status = obs.ID, obs.Methodology, obs.MethodologyVersion, obs.Agent, string(obs.Status)
	}
	data, err := toData(r)
	if err != nil {
		return ActionResult{}, err
	}
	if cfg.TraceURL != "" && r.TraceID != "" {
		data["traceUrl"] = cfg.TraceURL + r.TraceID
	}
	out := fmt.Sprintf("%s/%s: %d findings (%d steps, %d tokens, %d spans)", r.Methodology, r.Agent, len(r.Findings), r.Steps, r.Tokens, len(spans))
	return ActionResult{Items: []ItemInput{{Kind: "artifact", Type: "cost_report", Data: data}}, Output: out, Logs: logs}, nil
}

// toData converts a value into JSON-compatible data (lists never null).
func toData(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, x := range m {
		if x == nil && strings.HasSuffix(k, "s") {
			m[k] = []any{}
		}
	}
	return m, nil
}

// costReport returns the latest cost report of the blackboard.
func costReport(bb domain.Blackboard) (observe.Report, error) {
	var r observe.Report
	for i := len(bb.Change.Items) - 1; i >= 0; i-- {
		it := bb.Change.Items[i]
		if it.Kind == domain.KindArtifact && it.Type == "cost_report" && bb.Change.Active(it.ID) {
			b, err := json.Marshal(it.Data)
			if err != nil {
				return r, err
			}
			return r, json.Unmarshal(b, &r)
		}
	}
	return r, errors.New("no cost report on the blackboard")
}

func (e *Engine) observePropose(ctx context.Context, ac ActionContext) (ActionResult, error) {
	r, err := costReport(ac.Blackboard)
	if err != nil {
		return ActionResult{}, err
	}
	cm, err := e.Methodologies.Methodology(ctx, r.Methodology)
	if err != nil {
		return ActionResult{}, err
	}
	props, notes := observe.Propose(r, cm.Methodology)
	var items []ItemInput
	for i, p := range props {
		in := ItemInput{Ref: fmt.Sprintf("p%d", i), Kind: "proposal", Type: "improvement",
			Data:     map[string]any{"title": p.Title, "rationale": p.Rationale, "finding": r.Findings[p.Finding].Kind},
			Proposal: &ProposalInput{Op: p.Op}}
		in.Proposal.Node = &struct {
			Base  string         `json:"base,omitempty"`
			Key   string         `json:"key,omitempty"`
			Type  string         `json:"type,omitempty"`
			Props map[string]any `json:"props,omitempty"`
		}{Props: p.Props}
		if p.Op == "update_node" {
			in.Proposal.Node.Base = p.Key
		} else {
			in.Proposal.Node.Key, in.Proposal.Node.Type = p.Key, p.Type
		}
		items = append(items, in)
	}
	if notes == nil {
		notes = []string{}
	}
	items = append(items, ItemInput{Kind: "artifact", Type: "improvement_plan", Data: map[string]any{
		"methodology": r.Methodology, "observedVersion": r.Version, "currentVersion": cm.Version, "proposals": len(props), "notes": notes}})
	return ActionResult{Items: items, Output: fmt.Sprintf("%d proposals, %d notes", len(props), len(notes))}, nil
}

func (e *Engine) methodologyDraft(ctx context.Context, ac ActionContext, cfg SelfImprovement) (ActionResult, error) {
	if cfg.Drafts == nil {
		return ActionResult{}, errors.New("no methodology registry to save drafts")
	}
	r, err := costReport(ac.Blackboard)
	if err != nil {
		return ActionResult{}, err
	}
	bb := ac.Blackboard
	var edits []observe.Edit
	for _, it := range bb.Change.Items {
		if it.Kind != domain.KindProposal || it.Proposal == nil || it.Proposal.Node == nil || bb.Change.EffectiveStatus(it.ID) != domain.ItemAccepted {
			continue
		}
		n := it.Proposal.Node
		ed := observe.Edit{Op: string(it.Proposal.Op), Key: n.Key, Type: n.Type, Props: n.Properties}
		if n.Base != nil {
			v, ok := bb.Nodes[*n.Base]
			if !ok {
				continue
			}
			ed.Key, ed.Type = v.Key, v.Type
		}
		edits = append(edits, ed)
	}
	if len(edits) == 0 {
		return ActionResult{Items: []ItemInput{{Kind: "artifact", Type: "methodology_draft",
			Data: map[string]any{"methodology": r.Methodology, "skipped": true, "reason": "no proposal accepted"}}}, Output: "nothing to merge"}, nil
	}
	// the draft is saved with the identity of the process initiator
	ctx = authz.With(ctx, ac.Process.Initiator)
	cur, ok, err := cfg.Drafts.Definition(ctx, r.Methodology, "")
	if err != nil || !ok {
		return ActionResult{}, fmt.Errorf("methodology %s: %v", r.Methodology, cmpErr(err, "not published"))
	}
	d, err := observe.Apply(cur, edits)
	if err != nil {
		return ActionResult{}, err
	}
	d.Methodology.Version = observe.NextVersion(cur.Version, func(v string) bool {
		_, exists, _ := cfg.Drafts.Definition(ctx, cur.Name, v)
		return exists
	})
	issues, err := cfg.Drafts.SaveDraft(ctx, d.Methodology)
	if err != nil {
		return ActionResult{}, err
	}
	data, _ := toData(d)
	data["methodology"], data["version"], data["from"] = cur.Name, d.Methodology.Version, cur.Version
	var is []any
	for _, i := range issues {
		is = append(is, i.String())
	}
	data["issues"] = orEmptyAnyList(is)
	return ActionResult{Items: []ItemInput{{Kind: "artifact", Type: "methodology_draft", Data: data}},
		Output: fmt.Sprintf("draft %s@%s: %d changes, %d issues", cur.Name, d.Methodology.Version, len(d.Applied), len(issues))}, nil
}

func cmpErr(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

func orEmptyAnyList(l []any) []any {
	if l == nil {
		return []any{}
	}
	return l
}
