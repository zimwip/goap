package condition

import (
	"github.com/zimwip/goap/pkg/domain"
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
	points, questions := decisions(bb)
	return map[string]any{
		"change": map[string]any{
			"id": string(c.ID), "title": c.Title, "intent": c.Intent, "status": string(c.Status),
			"goal": c.Goal, "methodology": c.Methodology, "branch": domain.BranchOf(c.Branch), "baseline": string(c.BaselineID), "resultBaseline": string(c.ResultBaselineID), "data": orEmpty(c.Data),
		},
		"items":          items,
		"changeImpacts":  h.changeImpacts(),
		"decisions":      orEmptyList(byKind[domain.KindDecision]),
		"artifacts":      orEmptyList(byKind[domain.KindArtifact]),
		"merges":         orEmptyList(byKind[domain.KindMerge]),
		"vars":           vars,
		"options":        h.options(),
		"activeOption":   bb.ActiveOption,
		"decisionPoints": points,
		"questions":      questions,
		"risks":          risks(&c),
		"actions":        actionItems(&c),
	}
}

// risks is the risk register of the change (ADR 0036 §1).
func risks(c *domain.Change) []any {
	rs := c.Risks()
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
	as := c.ActionItems()
	out := make([]any, 0, len(as))
	for _, a := range as {
		out = append(out, map[string]any{"key": a.Key, "title": a.Title, "status": a.Status, "owner": a.Owner, "due": a.Due, "for": a.For,
			"result": a.Result, "item": string(a.Item), "versions": int64(a.Versions)})
	}
	return out
}

// options are the options of the change (ADR 0032 §6).
func (h hydrator) options() []any {
	out := make([]any, 0, len(h.bb.Options))
	for _, o := range h.bb.Options {
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
func decisions(bb domain.Blackboard) (points, questions []any) {
	points, questions = []any{}, []any{}
	for _, d := range bb.DecisionPoints {
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
			"decider": d.Decider, "threshold": d.Threshold, "rounds": int64(d.Rounds), "maxRounds": int64(d.MaxRounds), "escalation": d.Escalation,
			"openQuestions": int64(d.OpenQuestions()), "questions": qs, "ruling": ruling, "option": d.Option, "decidedBy": d.DecidedBy})
	}
	return points, questions
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
	}
	if d := it.Decision; d != nil {
		m["decision"] = map[string]any{"item": string(d.Item), "accept": d.Accept, "comment": d.Comment}
	}
	return m
}

func (h hydrator) ref(r domain.NodeRef) map[string]any {
	m := map[string]any{"id": string(r.ID), "version": int64(r.Version), "key": "", "type": "", "types": []any{}, "props": map[string]any{},
		"deleted": false, "latest": int64(r.Version), "out": []any{}, "in": []any{}, "state": "", "editable": true}
	v, ok := h.bb.Nodes[r]
	if !ok {
		return m
	}
	m["key"], m["type"], m["props"], m["deleted"], m["latest"] = v.Key, v.Type, orEmpty(v.Properties), v.Deleted, int64(v.Latest)
	m["state"], m["editable"] = v.State, !v.Frozen
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
			"planned": cn.Post == nil, "hasPost": cn.Post != nil, "recheck": cn.Recheck, "props": h.currentProps(cn),
			"via": string(cn.Via), "producedBy": cn.ProducedBy, "items": items,
		})
	}
	return out
}

// currentProps are the properties of the node as the change has it: the version written, else the one it starts from.
func (h hydrator) currentProps(cn domain.ChangeImpact) map[string]any {
	switch {
	case cn.Post != nil:
		return orEmpty(h.bb.Nodes[*cn.Post].Properties)
	case cn.Pre != nil:
		return orEmpty(h.bb.Nodes[*cn.Pre].Properties)
	}
	return map[string]any{}
}
