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
	return map[string]any{
		"change": map[string]any{
			"id": string(c.ID), "title": c.Title, "intent": c.Intent, "status": string(c.Status),
			"goal": c.Goal, "methodology": c.Methodology, "branch": domain.BranchOf(c.Branch), "baseline": string(c.BaselineID), "resultBaseline": string(c.ResultBaselineID), "data": orEmpty(c.Data),
		},
		"items":     items,
		"impacts":   orEmptyList(byKind[domain.KindImpact]),
		"proposals": orEmptyList(byKind[domain.KindProposal]),
		"decisions": orEmptyList(byKind[domain.KindDecision]),
		"artifacts": orEmptyList(byKind[domain.KindArtifact]),
		"merges":    orEmptyList(byKind[domain.KindMerge]),
		"vars":      vars,
	}
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
		"producedBy": it.ProducedBy, "derivedFrom": derived, "data": orEmpty(it.Data),
		"target": nil, "post": nil, "op": "", "node": nil, "link": nil, "decision": nil,
	}
	if it.Target != nil {
		m["target"] = h.ref(*it.Target)
	}
	if e := it.Post; e != nil {
		if e.Node != nil {
			m["post"] = h.ref(*e.Node)
		} else {
			m["post"] = string(e.Item) // the proposal producing the post version
		}
	}
	if p := it.Proposal; p != nil {
		m["op"] = string(p.Op)
		if p.Node != nil {
			n := map[string]any{"key": p.Node.Key, "type": p.Node.Type, "props": orEmpty(p.Node.Properties), "base": nil}
			if p.Node.Base != nil {
				base := h.ref(*p.Node.Base)
				n["base"] = base
				if n["type"] == "" {
					n["type"] = base["type"]
				}
				if n["key"] == "" {
					n["key"] = base["key"]
				}
			}
			n["types"] = h.bb.TypesOf(n["type"].(string))
			n["state"] = p.Node.State // transition_node / create_node: the target state
			m["node"] = n
		}
		if p.Link != nil {
			m["link"] = map[string]any{
				"id": string(p.Link.LinkID), "type": p.Link.Type, "props": orEmpty(p.Link.Properties),
				"from": h.endpoint(p.Link.From), "to": h.endpoint(p.Link.To),
			}
		}
	}
	if d := it.Decision; d != nil {
		m["decision"] = map[string]any{"item": string(d.Item), "accept": d.Accept, "comment": d.Comment}
	}
	return m
}

// endpoint returns a node view for an existing node, or a summary of the
// proposed node for an item endpoint.
func (h hydrator) endpoint(e domain.Endpoint) map[string]any {
	if e.Node != nil {
		m := h.ref(*e.Node)
		m["item"] = ""
		return m
	}
	m := map[string]any{"item": string(e.Item), "id": "", "version": int64(0), "key": "", "type": "", "types": []any{}, "props": map[string]any{}}
	if it, ok := h.bb.Change.Item(e.Item); ok && it.Proposal != nil && it.Proposal.Node != nil {
		m["key"] = it.Proposal.Node.Key
		m["type"] = it.Proposal.Node.Type
		m["types"] = h.bb.TypesOf(it.Proposal.Node.Type)
		m["props"] = orEmpty(it.Proposal.Node.Properties)
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
