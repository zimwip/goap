package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/methodology"
)

// foreachKey prefixes the call key of the stream of an element in the children of the process running the step.
const foreachSuffix = "#step:"

// runForeach is process.step for a step with foreach (ADR 0050): the step is carried out once per element of the
// list its expression gives, in parallel streams. Each element is bound to `vars.item` and gets the most specific
// method that applies to it (a method's `when` reads `vars.item`) and its own agent instance, a sub-agent on the same
// change. The step waits for the first stream still at work and is retried when it ends, so every stream is in
// flight at once; it is done when every stream completed, and fails when one failed. Streams already started stay
// the streams of the step even when the list changes after they did their work.
func runForeach(ctx context.Context, ac ActionContext, expr string) (ActionResult, error) {
	h := ac.Host
	e := h.e
	step, _ := ac.Action.Params["step"].(string)
	capability, _ := ac.Action.Params["capability"].(string)
	methodologyName := ac.Process.Methodology
	list, err := condition.CompileList(expr)
	if err != nil {
		return ActionResult{}, fmt.Errorf("step %s: foreach: %w", step, err)
	}
	items, err := list.Eval(ac.Blackboard)
	if err != nil {
		return ActionResult{}, fmt.Errorf("step %s: foreach: %w", step, err)
	}
	if groupBy, _ := ac.Action.Params["groupBy"].(string); groupBy != "" {
		if items, err = groupItems(ac.Blackboard, items, groupBy); err != nil {
			return ActionResult{}, fmt.Errorf("step %s: groupBy: %w", step, err)
		}
	}
	type stream struct {
		key, id string
		item    any
	}
	var streams []stream
	seen := map[string]bool{}
	for i, it := range items {
		id := itemKey(it, i)
		for seen[id] {
			id += fmt.Sprintf("-%d", i)
		}
		seen[id] = true
		streams = append(streams, stream{key: ac.Action.Name + foreachSuffix + id, id: id, item: it})
	}
	// streams started before keep their element: the list may no longer give it
	for _, key := range slices.Sorted(maps.Keys(ac.Process.Children)) {
		id, ok := strings.CutPrefix(key, ac.Action.Name+foreachSuffix)
		if !ok || seen[id] {
			continue
		}
		child, err := e.Store.Get(ctx, ac.Process.Children[key])
		if err != nil {
			return ActionResult{}, fmt.Errorf("step %s: %w", step, err)
		}
		var item any
		if child.Step != nil {
			item = child.Step.Item
		}
		seen[id] = true
		streams = append(streams, stream{key: key, id: id, item: item})
	}
	actor := authz.With(ctx, e.actor(ac.Process))
	var waitFor string
	var methods []string
	var failed []string
	for _, st := range streams {
		bb := ac.Blackboard
		bb.Vars = maps.Clone(bb.Vars)
		if bb.Vars == nil {
			bb.Vars = map[string]any{}
		}
		bb.Vars["item"] = st.item
		var me methodology.MethodChoice
		if id, started := ac.Process.Children[st.key]; started {
			// the method it was started for stays
			child, err := e.Store.Get(ctx, id)
			if err != nil {
				return ActionResult{}, fmt.Errorf("step %s: %w", step, err)
			}
			m, err := e.Methodologies.Methodology(ctx, methodologyName)
			if err != nil {
				return ActionResult{}, err
			}
			if child.Step == nil || child.Step.Method == "" {
				return ActionResult{}, fmt.Errorf("step %s: stream %s has no method", step, st.id)
			}
			meth, ok := m.MethodByName(child.Step.Method)
			if !ok {
				return ActionResult{}, fmt.Errorf("step %s: stream %s: unknown method %q", step, st.id, child.Step.Method)
			}
			me = methodology.MethodChoice{Method: meth, AgentGoal: m.MethodGoal(meth.Name)}
		} else if me, err = e.chooseMethod(ctx, ac, capability, bb, false); err != nil {
			return ActionResult{}, fmt.Errorf("step %s [%s]: %w", step, st.id, err)
		}
		if !slices.Contains(methods, me.Name) {
			methods = append(methods, me.Name)
		}
		sc := ac.Step.withMethod(me.Method)
		sc.Item, sc.ItemKey = st.item, st.id
		intent := ac.Action.Description
		if intent == "" {
			intent = "step " + step
		}
		intent += " for " + st.id
		if ac.Blackboard.Change.Intent != "" {
			intent += "\n\n(change: " + ac.Blackboard.Change.Intent + ")"
		}
		res, err := e.runChildStep(actor, h, st.key, methodologyName, me.ActorAgent(), me.AgentGoal, intent, false, sc)
		switch {
		case errors.Is(err, dsl.ErrSuspended):
			if waitFor == "" {
				waitFor = res.ProcessID
			}
		case err != nil:
			return ActionResult{}, fmt.Errorf("step %s [%s]: %w", step, st.id, err)
		case res.Status != string(StatusCompleted):
			failed = append(failed, fmt.Sprintf("%s (%s, process %s)", st.id, res.Status, res.ProcessID))
		}
	}
	switch {
	case waitFor != "":
		return ActionResult{Suspended: true, Child: waitFor, Method: strings.Join(methods, ","),
			Output: fmt.Sprintf("step %s: %d stream(s) at work", step, len(streams))}, nil
	case len(failed) > 0:
		return ActionResult{}, fmt.Errorf("step %s: stream(s) did not complete: %s", step, strings.Join(failed, ", "))
	}
	ids := make([]any, len(streams))
	for i, st := range streams {
		ids[i] = st.id
	}
	return ActionResult{
		Items: []ItemInput{{Kind: string(domain.KindArtifact), Type: methodology.ArtifactStepDone,
			Data: map[string]any{"step": step, "items": ids, "method": strings.Join(methods, ",")}}},
		Method: strings.Join(methods, ","),
		Output: fmt.Sprintf("step %s done for %d element(s) with %s", step, len(streams), strings.Join(methods, ", ")),
	}, nil
}

// itemKey is the identity of an element of a foreach list: a string as is, a map by its key, id or name, else its position.
func itemKey(item any, i int) string {
	switch v := item.(type) {
	case string:
		if v != "" {
			return v
		}
	case map[string]any:
		for _, k := range []string{"key", "id", "name"} {
			if s, ok := v[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return fmt.Sprintf("#%d", i)
}

// groupItems groups the elements by the key the expression gives for each (the element is `vars.item`): one
// {key, items} per group, in the order the keys first appear.
func groupItems(bb domain.Blackboard, items []any, expr string) ([]any, error) {
	by, err := condition.CompileValue(expr)
	if err != nil {
		return nil, err
	}
	var keys []string
	groups := map[string][]any{}
	for _, it := range items {
		b := bb
		b.Vars = maps.Clone(bb.Vars)
		if b.Vars == nil {
			b.Vars = map[string]any{}
		}
		b.Vars["item"] = it
		v, err := by.Eval(b)
		if err != nil {
			return nil, err
		}
		k := fmt.Sprint(v)
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], it)
	}
	out := make([]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]any{"key": k, "items": groups[k]})
	}
	return out, nil
}
