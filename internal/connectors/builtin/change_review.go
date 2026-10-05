package builtin

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/review"
)

// reviews lists the reviews of the change (ADR 0080) and the impacts that await a review on the flow.
func (c Change) reviews(ctx context.Context, id domain.ChangeID, a args) (map[string]any, error) {
	full, err := c.p.Graph.Change(ctx, id) // the blackboard is the view of one flow: the reviews of every flow are in the change
	if err != nil {
		return nil, err
	}
	rs := review.Reviews(full.Items)
	flow := full.ResolveFlow(a.str("flow"))
	awaiting := []map[string]any{}
	for _, cn := range review.Awaiting(full, flow, rs, "") {
		awaiting = append(awaiting, map[string]any{"id": cn.ID, "key": cn.Key, "type": cn.Type, "intent": cn.Intent})
	}
	return result(map[string]any{"reviews": rs, "awaiting": awaiting})
}

// impactID resolves an impact named by its id or by the key of its node.
func impactID(bb domain.Blackboard, name string) (string, error) {
	for _, n := range bb.Change.Nodes {
		if !n.Superseded && (string(n.ID) == name || n.Key == name) {
			return string(n.ID), nil
		}
	}
	return "", fmt.Errorf("%s is not in the change", name)
}

func impactIDs(bb domain.Blackboard, v any) ([]string, error) {
	var out []string
	list, _ := v.([]any)
	for _, x := range list {
		s, _ := x.(string)
		id, err := impactID(bb, s)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// review runs the operations of the review object (ADR 0080) as the caller.
func (c Change) review(ctx context.Context, bb domain.Blackboard, op string, a args) (map[string]any, error) {
	svc := review.Service{Port: c.p.Graph}
	who := review.ActorOf(ctx)
	id := bb.Change.ID
	var (
		r   review.Review
		err error
	)
	switch op {
	case "review_open":
		if r, err = svc.Open(ctx, id, a.str("flow"), a.str("comment"), who); err != nil {
			return nil, err
		}
		if add, aerr := impactIDs(bb, a["impacts"]); aerr != nil || len(add) > 0 {
			if aerr != nil {
				return nil, aerr
			}
			r, err = svc.Update(ctx, id, r.Key, review.Edit{Add: add}, who)
		}
	case "review_update":
		key, kerr := a.required("review")
		if kerr != nil {
			return nil, kerr
		}
		var e review.Edit
		if v, ok := a["comment"].(string); ok {
			e.Comment = &v
		}
		if e.Add, err = impactIDs(bb, a["add"]); err != nil {
			return nil, err
		}
		if e.Remove, err = impactIDs(bb, a["remove"]); err != nil {
			return nil, err
		}
		entries, _ := a["entries"].([]any)
		for _, x := range entries {
			m, _ := x.(map[string]any)
			imp, ierr := impactID(bb, args(m).str("impact"))
			if ierr != nil {
				return nil, ierr
			}
			ee := review.EntryEdit{Impact: imp}
			if v, ok := m["comment"].(string); ok {
				ee.Comment = &v
			}
			if v, ok := m["outcome"].(string); ok {
				ee.Outcome = &v
			}
			e.Entries = append(e.Entries, ee)
		}
		r, err = svc.Update(ctx, id, key, e, who)
	case "review_submit", "review_discard":
		key, kerr := a.required("review")
		if kerr != nil {
			return nil, kerr
		}
		if op == "review_submit" {
			r, err = svc.Submit(ctx, id, key, who)
		} else {
			r, err = svc.Discard(ctx, id, key, who)
		}
	}
	if err != nil {
		return nil, err
	}
	return result(map[string]any{"review": r})
}
