package condition

import (
	"math"
	"sync"
	"time"

	"github.com/zimwip/goap/pkg/criticality"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine/blackboard"
	"github.com/zimwip/goap/pkg/risk"
	"github.com/zimwip/goap/pkg/verify"
)

// Activation converts a blackboard into the CEL variables of a condition.
// Every value is made of maps, lists, strings, int64, float64 and bools.
// Absent optional values are null so expressions can test them with `== null`.
func Activation(bb domain.Blackboard) map[string]any {
	c := bb.Change
	h := hydrator{bb: bb}
	items := make([]any, 0, len(c.Items))
	byKind := map[domain.ItemKind][]any{}
	for _, it := range c.Items {
		if !c.Active(it.ID) {
			continue // superseded by a rebase or a merge
		}
		m := h.item(it)
		items = append(items, m)
		byKind[it.Kind] = append(byKind[it.Kind], m)
	}
	vars := bb.Vars
	if vars == nil {
		vars = map[string]any{}
	}
	// the variables of the decision and risk concepts are computed when an expression reads them (lazy bindings of
	// CEL), so a methodology that reads none of them pays nothing
	opts := lazy(func() []any { return h.options() })
	rsk := lazy(func() []any { return risks(&c) })
	act := lazy(func() []any { return actionItems(&c) })
	vrf := lazy(func() []any { return verifications(&c) })
	drg := lazy(func() []any { return derogations(&c, bb.At) })
	pol := lazy(func() map[string]any { return criticalityPolicy(bb) })
	dec := lazy(func() decisionView { return decisions(bb) })
	objs := lazy(func() map[string][]any { return objectsByType(bb) })
	// the state of the change in the lifecycle of its methodology is the engine's (ADR 0098): an execution@State object
	state, _ := blackboard.Of(bb).State()
	return map[string]any{
		"change": map[string]any{
			"id": string(c.ID), "title": c.Title, "intent": c.Intent, "status": string(c.Status),
			"criticality": string(criticality.Of(c.Data)), "lifecycle": state.Lifecycle, "state": state.State, "goal": c.Goal, "methodology": c.Methodology, "branch": domain.BranchOf(c.Branch), "baseline": string(c.BaselineID), "resultBaseline": string(c.ResultBaselineID), "data": orEmpty(c.Data),
		},
		"items":          items,
		"changeImpacts":  h.changeImpacts(),
		"decisions":      orEmptyList(byKind[domain.KindDecision]),
		"artifacts":      orEmptyList(byKind[domain.KindArtifact]),
		"merges":         orEmptyList(byKind[domain.KindMerge]),
		"vars":           vars,
		"options":        func() any { return opts() },
		"activeOption":   domain.ActiveOptionOf(bb),
		"decisionPoints": func() any { return dec().points },
		"questions":      func() any { return dec().questions },
		"risks":          func() any { return rsk() },
		"actions":        func() any { return act() },
		"verifications":  func() any { return vrf() },
		"derogations":    func() any { return drg() },
		// criticalityPolicy is what the organisation requires of the criticality of the change: the facet its provider
		// gave the blackboard, else the compiled-in table (ADR 0075 §3; `policy` is a field of the decision points)
		"criticalityPolicy": func() any { return pol() },
		"objects":           func() any { return objs() },
	}
}

// objectsByType are the change objects of the blackboard by type (ADR 0098), each as CEL reads it.
func objectsByType(bb domain.Blackboard) map[string][]any {
	out := map[string][]any{}
	for _, o := range domain.ObjectsOf(bb) {
		value := o.Value
		if value == nil {
			value = map[string]any{}
		}
		labels := map[string]any{}
		for k, v := range o.Labels {
			labels[k] = v
		}
		out[o.Type] = append(out[o.Type], map[string]any{"key": o.Key, "version": int64(o.Version), "state": o.State, "workspace": o.Workspace,
			"value": value, "labels": labels, "by": o.By})
	}
	return out
}

// Resolve returns the value of an activation entry, computing a lazy one.
func Resolve(v any) any {
	if f, ok := v.(func() any); ok {
		return f()
	}
	return v
}

// lazy memoizes a computation; CEL calls a `func() any` binding when the variable is read.
func lazy[T any](f func() T) func() T {
	var once sync.Once
	var v T
	return func() T {
		once.Do(func() { v = f() })
		return v
	}
}

type decisionView struct{ points, questions []any }

// risks is the risk register of the change (ADR 0036 §1).
func risks(c *domain.Change) []any {
	rs := risk.Risks(*c)
	out := make([]any, 0, len(rs))
	for _, r := range rs {
		acts := make([]any, len(r.Actions))
		for i, a := range r.Actions {
			acts[i] = a
		}
		out = append(out, map[string]any{"key": r.Key, "title": r.Title, "description": r.Description, "probability": int64(r.Probability),
			"impact": int64(r.Impact), "score": int64(r.Score()), "status": r.Status, "live": r.Live(), "owner": r.Owner, "actions": acts,
			"step": r.Step, "item": string(r.Item), "versions": int64(r.Versions)})
	}
	return out
}

// actionItems are the actions of the change (ADR 0036 §1).
func actionItems(c *domain.Change) []any {
	as := risk.Actions(*c)
	out := make([]any, 0, len(as))
	for _, a := range as {
		out = append(out, map[string]any{"key": a.Key, "title": a.Title, "status": a.Status, "owner": a.Owner, "due": a.Due, "for": a.For,
			"result": a.Result, "item": string(a.Item), "versions": int64(a.Versions)})
	}
	return out
}

// verifications are the effects of the action runs and their state of verification (ADR 0075).
func verifications(c *domain.Change) []any {
	var items []domain.ChangeItem
	for _, it := range c.Items {
		if it.Kind == verify.KindVerification && c.Active(it.ID) {
			items = append(items, it)
		}
	}
	out := []any{}
	for _, s := range verify.Subjects(items) {
		out = append(out, map[string]any{"execution": s.Execution, "action": s.Action, "impact": s.Impact, "oracle": s.Oracle,
			"independent": s.Independent, "state": s.State, "producer": s.Producer, "by": s.By, "open": s.Open()})
	}
	return out
}

// criticalityPolicy is the policy of the level of the change: the one the blackboard carries (domain.FacetCriticalityPolicy,
// a criticality.Policy filled by the provider that reads the organisation), else the compiled-in table.
func criticalityPolicy(bb domain.Blackboard) map[string]any {
	if p, ok := bb.Facets[domain.FacetCriticalityPolicy].(criticality.Policy); ok {
		return p.Map()
	}
	return criticality.Defaults()[criticality.Of(bb.Change.Data)].Map()
}

// derogations are the derogations of the change (ADR 0075 §2); expired is read at the instant of the blackboard (the
// clock when it has none).
func derogations(c *domain.Change, at time.Time) []any {
	if at.IsZero() {
		at = time.Now()
	}
	out := []any{}
	for _, d := range risk.Derogations(*c) {
		out = append(out, map[string]any{"key": d.Key, "rule": d.Rule, "target": d.Target, "reason": d.Reason, "signatory": d.Signatory,
			"expires": d.Expires.Format(time.RFC3339), "status": d.Status, "open": d.Open(), "expired": d.ExpiredAt(at),
			"item": string(d.Item), "versions": int64(d.Versions)})
	}
	return out
}

// options are the options of the change (ADR 0032 §6).
func (h hydrator) options() []any {
	out := make([]any, 0, len(domain.OptionsOf(h.bb)))
	for _, o := range domain.OptionsOf(h.bb) {
		name, hyp := "", ""
		if o.Option != nil {
			name, hyp = o.Option.Name, o.Option.Hypothesis
		}
		out = append(out, map[string]any{"id": o.ID, "name": name, "hypothesis": hyp, "status": o.OptionStatus(),
			"active": o.Active, "evaluation": o.Evaluation, "evaluated": o.Evaluated})
	}
	return out
}

// decisions are the decision points of the change and all their questions (ADR 0009 §4).
func decisions(bb domain.Blackboard) decisionView {
	points, questions := []any{}, []any{}
	for _, d := range domain.DecisionPointsOf(bb) {
		qs := make([]any, 0, len(d.Questions))
		for _, q := range d.Questions {
			m := map[string]any{"id": q.ID, "point": q.Point, "text": q.Text, "status": q.Status, "answer": q.Answer, "answeredBy": q.AnsweredBy}
			qs = append(qs, m)
			questions = append(questions, m)
		}
		var ruling any
		if r := d.Ruling; r != nil {
			ruling = map[string]any{"outcome": r.Outcome, "option": r.Option, "confidence": r.Confidence, "justification": r.Justification,
				"by": r.By, "human": r.Human}
		}
		options, criteria := make([]any, 0, len(d.Options)), make([]any, 0, len(d.Criteria))
		for _, o := range d.Options {
			options = append(options, o)
		}
		for _, c := range d.Criteria {
			criteria = append(criteria, c)
		}
		points = append(points, map[string]any{"id": d.ID, "question": d.Question, "status": d.Status, "options": options, "criteria": criteria,
			"policy": policyView(d.Policy), "humanOnly": d.HumanOnly, "escalation": d.Escalation,
			"openQuestions": int64(d.OpenQuestions()), "questions": qs, "ruling": ruling, "option": d.Option, "decidedBy": d.DecidedBy})
	}
	return decisionView{points, questions}
}

// policyView is the policy values of a decision point as an expression sees them: always a map, the values the
// policy keeps. The values travel as JSON, which has one number type: a whole number is an int in an expression, so
// that `d.policy.maxRounds - d.policy.rounds` is the same arithmetic wherever the point was replayed.
func policyView(p map[string]any) map[string]any {
	out := make(map[string]any, len(p))
	for k, v := range p {
		switch n := v.(type) {
		case float64:
			if n == math.Trunc(n) && math.Abs(n) < 1<<53 {
				v = int64(n)
			}
		case int:
			v = int64(n)
		}
		out[k] = v
	}
	return out
}

type hydrator struct{ bb domain.Blackboard }

func (h hydrator) item(it domain.ChangeItem) map[string]any {
	derived := make([]any, 0, len(it.DerivedFrom))
	for _, d := range it.DerivedFrom {
		derived = append(derived, string(d))
	}
	m := map[string]any{
		"id": string(it.ID), "kind": string(it.Kind), "type": it.Type,
		"status":     string(h.bb.Change.EffectiveStatus(it.ID)),
		"producedBy": it.ProducedBy, "derivedFrom": derived, "data": orEmpty(it.Data), "decision": nil,
		// when it was written, in milliseconds since the epoch: compared with vars.event.at (ADR 0036 §3)
		"at": it.CreatedAt.UnixMilli(),
	}
	if d := it.Decision; d != nil {
		m["decision"] = map[string]any{"item": string(d.Item), "accept": d.Accept, "comment": d.Comment}
	}
	return m
}

func (h hydrator) ref(r domain.NodeRef) map[string]any {
	m := map[string]any{"id": string(r.ID), "version": int64(r.Version), "key": "", "type": "", "types": []any{}, "props": map[string]any{},
		"deleted": false, "latest": int64(r.Version), "out": []any{}, "in": []any{}, "state": "", "landable": true}
	v, ok := h.bb.Nodes[r]
	if !ok {
		return m
	}
	m["key"], m["type"], m["props"], m["deleted"], m["latest"] = v.Key, v.Type, orEmpty(v.Properties), v.Deleted, int64(v.Latest)
	m["state"], m["landable"] = v.State, !v.NotLandable
	m["types"] = h.bb.TypesOf(v.Type)
	out := make([]any, 0, len(v.Out))
	for _, l := range v.Out {
		out = append(out, map[string]any{"id": string(l.ID), "type": l.Type, "to": h.summary(l.To)})
	}
	in := make([]any, 0, len(v.In))
	for _, l := range v.In {
		in = append(in, map[string]any{"id": string(l.ID), "type": l.Type, "from": h.summary(l.From)})
	}
	m["out"], m["in"] = out, in
	return m
}

func (h hydrator) summary(r domain.NodeRef) map[string]any {
	m := map[string]any{"id": string(r.ID), "version": int64(r.Version), "key": "", "type": ""}
	if n, ok := h.bb.Neighbors[r]; ok {
		m["key"], m["type"] = n.Key, n.Type
	} else if v, ok := h.bb.Nodes[r]; ok {
		m["key"], m["type"] = v.Key, v.Type
	}
	m["types"] = h.bb.TypesOf(m["type"].(string))
	return m
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func orEmptyList(l []any) []any {
	if l == nil {
		return []any{}
	}
	return l
}

// changeImpacts lists the change impacts of the change (ADR 0024): the stored ones
// and the ones derived from its items. pre, post and landed are node views, or
// null while absent; a planned change impact has no post yet.
func (h hydrator) changeImpacts() []any {
	out := make([]any, 0, len(h.bb.Change.Nodes))
	for _, cn := range h.bb.Change.Nodes {
		ref := func(r *domain.NodeRef) any {
			if r == nil {
				return nil
			}
			return h.ref(*r)
		}
		reviews := make([]any, 0, len(cn.Reviews))
		comment := ""
		for _, r := range cn.Reviews {
			reviews = append(reviews, map[string]any{"status": string(r.Status), "by": r.By, "comment": r.Comment})
			comment = r.Comment
		}
		items := make([]any, 0, len(cn.Items))
		for _, id := range cn.Items {
			items = append(items, string(id))
		}
		out = append(out, map[string]any{
			"id": string(cn.ID), "key": cn.Key, "type": cn.Type, "types": h.bb.TypesOf(cn.Type),
			"intent": string(cn.Intent), "rationale": cn.Rationale, "review": string(cn.Review), "reviews": reviews, "comment": comment,
			"pre": ref(cn.Pre), "post": ref(cn.Post), "landed": ref(cn.Landed),
			"planned": cn.Post == nil, "hasPost": cn.Post != nil, "drafted": cn.Drafted(), "recheck": cn.Recheck, "props": h.currentProps(cn),
			"via": string(cn.Via), "producedBy": cn.ProducedBy, "items": items,
		})
	}
	return out
}

// currentProps are the properties of the node as the change has it: its draft (or the version written), else the one it starts from.
func (h hydrator) currentProps(cn domain.ChangeImpact) map[string]any {
	switch {
	case cn.Post != nil:
		return orEmpty(h.bb.Nodes[*cn.Post].Properties)
	case cn.Pre != nil:
		return orEmpty(h.bb.Nodes[*cn.Pre].Properties)
	}
	return map[string]any{}
}
