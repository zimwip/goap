// Package selfimprove registers the builtins of the self-observation methodology (ADR 0011, ADR 0062): they
// analyze a finished run, propose improvements and draft a new methodology version. They live outside the engine,
// which knows no observer; the composition roots register them.
package selfimprove

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/builtins"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/journal"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/observe"
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
	SaveDraft(ctx context.Context, m methodology.Methodology) (def.Issues, error)
}

// Config configures the self-observation builtins.
type Config struct {
	Traces     TraceSource       // optional: slow spans of the observed run
	Drafts     MethodologyDrafts // required by methodology.draft
	Thresholds observe.Thresholds
	// TraceURL is the trace viewer prefix (e.g. http://localhost:16686/trace/).
	TraceURL string
}

// Register adds the self-observation builtins to b: they analyze a finished run of e from its journal and traces,
// propose improvements of its methodology and turn the accepted ones into a draft version. It panics on a name
// already registered.
func Register(b engine.BuiltinExecutor, e *engine.Engine, cfg Config) {
	b.Register(builtins.ObserveAnalyze, func(ctx context.Context, ac engine.ActionContext) (engine.ActionResult, error) {
		return observeAnalyze(ctx, e, ac, cfg)
	})
	b.Register(builtins.ObservePropose, func(ctx context.Context, ac engine.ActionContext) (engine.ActionResult, error) {
		return observePropose(ctx, e, ac)
	})
	b.Register(builtins.MethodologyDraft, func(ctx context.Context, ac engine.ActionContext) (engine.ActionResult, error) {
		return methodologyDraft(ctx, e, ac, cfg)
	})
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

func observeAnalyze(ctx context.Context, e *engine.Engine, ac engine.ActionContext, cfg Config) (engine.ActionResult, error) {
	id := observedProcess(ac.Process.Vars)
	if id == "" {
		return engine.ActionResult{}, errors.New("no process to observe (vars.process or a process event)")
	}
	obs, err := e.Store.Get(ctx, id)
	if err != nil {
		return engine.ActionResult{}, err
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
	recs, err := journal.Read(ctx, e.Graph, journal.Filter{ProcessIDs: ids})
	if err != nil {
		return engine.ActionResult{}, err
	}
	items := map[domain.ItemID]domain.ChangeItem{}
	nodes := map[domain.ChangeImpactID]domain.ChangeImpact{}
	if obs.ChangeID != "" {
		if bb, err := e.Graph.Blackboard(ctx, obs.ChangeID); err == nil {
			for _, it := range bb.Change.Items {
				items[it.ID] = it
			}
			for _, n := range bb.Change.Nodes {
				nodes[n.ID] = n
			}
		}
	}
	var logs []engine.LogLine
	var spans []observe.Span
	if cfg.Traces != nil && obs.TraceID != "" {
		if spans, err = cfg.Traces.Spans(ctx, obs.TraceID); err != nil {
			logs = append(logs, engine.LogLine{Time: time.Now().UTC(), Level: "warn", Message: "traces unavailable: " + err.Error()})
			spans = nil
		}
	}
	r := observe.Analyze(recs, items, nodes, spans, cfg.Thresholds)
	r.WithDisabled(obs.Agent, slices.Sorted(maps.Keys(obs.Disabled)))
	if r.Process == "" {
		r.Process, r.Methodology, r.Version, r.Agent, r.Status = obs.ID, obs.Methodology, obs.MethodologyVersion, obs.Agent, string(obs.Status)
	}
	data, err := toData(r)
	if err != nil {
		return engine.ActionResult{}, err
	}
	if cfg.TraceURL != "" && r.TraceID != "" {
		data["traceUrl"] = cfg.TraceURL + r.TraceID
	}
	out := fmt.Sprintf("%s/%s: %d findings (%d steps, %d tokens, %d spans)", r.Methodology, r.Agent, len(r.Findings), r.Steps, r.Tokens, len(spans))
	return engine.ActionResult{Items: []engine.ItemInput{{Kind: "artifact", Type: "cost_report", Data: data}}, Output: out, Logs: logs}, nil
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

func observePropose(ctx context.Context, e *engine.Engine, ac engine.ActionContext) (engine.ActionResult, error) {
	r, err := costReport(ac.Blackboard)
	if err != nil {
		return engine.ActionResult{}, err
	}
	cm, err := e.Methodologies.Methodology(ctx, r.Methodology)
	if err != nil {
		return engine.ActionResult{}, err
	}
	props, notes := observe.Propose(r, cm.Methodology)
	// an improvement is a change impact on an element of the methodology (ADR 0024): the rationale says what and
	// why, the version written on the change branch holds the proposed properties
	var ops []dsl.NodeOp
	for _, p := range props {
		why := p.Title
		if p.Rationale != "" {
			why += ": " + p.Rationale
		}
		if p.Op == "update_node" {
			ops = append(ops, dsl.NodeOp{Op: "declare", Intent: string(domain.IntentModified), Key: p.Key, Rationale: why})
		} else {
			ops = append(ops, dsl.NodeOp{Op: "declare", Intent: string(domain.IntentCreated), Key: p.Key, Type: p.Type, Rationale: why})
		}
		ops = append(ops, dsl.NodeOp{Op: "write", Node: p.Key, Props: p.Props})
	}
	if notes == nil {
		notes = []string{}
	}
	items := []engine.ItemInput{{Kind: "artifact", Type: "improvement_plan", Data: map[string]any{
		"methodology": r.Methodology, "observedVersion": r.Version, "currentVersion": cm.Version, "proposals": len(props), "notes": notes}}}
	return engine.ActionResult{Items: items, Nodes: ops, Output: fmt.Sprintf("%d proposals, %d notes", len(props), len(notes))}, nil
}

func methodologyDraft(ctx context.Context, e *engine.Engine, ac engine.ActionContext, cfg Config) (engine.ActionResult, error) {
	if cfg.Drafts == nil {
		return engine.ActionResult{}, errors.New("no methodology registry to save drafts")
	}
	r, err := costReport(ac.Blackboard)
	if err != nil {
		return engine.ActionResult{}, err
	}
	bb := ac.Blackboard
	var edits []observe.Edit
	for _, cn := range bb.Change.Nodes {
		if cn.Review != domain.ReviewAccepted || cn.Post == nil || len(cn.Items) > 0 {
			continue
		}
		post, ok := bb.Nodes[*cn.Post]
		if !ok {
			continue
		}
		ed := observe.Edit{Op: "create_node", Key: cn.Key, Type: cn.Type, Props: post.Properties}
		if cn.Pre != nil {
			pre, ok := bb.Nodes[*cn.Pre]
			if !ok {
				continue
			}
			ed.Op, ed.Props = "update_node", changedProps(pre.Properties, post.Properties)
		}
		edits = append(edits, ed)
	}
	if len(edits) == 0 {
		return engine.ActionResult{Items: []engine.ItemInput{{Kind: "artifact", Type: "methodology_draft",
			Data: map[string]any{"methodology": r.Methodology, "skipped": true, "reason": "no proposal accepted"}}}, Output: "nothing to merge"}, nil
	}
	// the draft is saved with the identity of the process initiator
	ctx = authz.With(ctx, ac.Process.Initiator)
	cur, ok, err := cfg.Drafts.Definition(ctx, r.Methodology, "")
	if err != nil || !ok {
		return engine.ActionResult{}, fmt.Errorf("methodology %s: %v", r.Methodology, cmpErr(err, "not published"))
	}
	d, err := observe.Apply(cur, edits)
	if err != nil {
		return engine.ActionResult{}, err
	}
	d.Methodology.Version = observe.NextVersion(cur.Version, func(v string) bool {
		_, exists, _ := cfg.Drafts.Definition(ctx, cur.Name, v)
		return exists
	})
	issues, err := cfg.Drafts.SaveDraft(ctx, d.Methodology)
	if err != nil {
		return engine.ActionResult{}, err
	}
	data, _ := toData(d)
	data["methodology"], data["version"], data["from"] = cur.Name, d.Methodology.Version, cur.Version
	var is []any
	for _, i := range issues {
		is = append(is, i.String())
	}
	data["issues"] = orEmptyAnyList(is)
	return engine.ActionResult{Items: []engine.ItemInput{{Kind: "artifact", Type: "methodology_draft", Data: data}},
		Output: fmt.Sprintf("draft %s@%s: %d changes, %d issues", cur.Name, d.Methodology.Version, len(d.Applied), len(issues))}, nil
}

// changedProps are the properties a version changes compared to the one it starts from.
func changedProps(pre, post map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range post {
		if old, ok := pre[k]; !ok || fmt.Sprint(old) != fmt.Sprint(v) {
			out[k] = v
		}
	}
	return out
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
