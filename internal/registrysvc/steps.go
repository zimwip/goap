package registrysvc

import "maps"

// stepChildren walks the steps of every process and method element recursively, one step and sub-step deep, and
// returns one defEl per step (methodology@Step for a process, methodology@MethodStep for a method), keyed by its
// full path ("<process>/<step>/<sub-step>"), plus the sub_activity children each parent (a process, method or step
// element, named "<kind>/<name-or-path>") needs linked. Steps stay authored inline in the owning process/method's own
// JSON for decode fidelity (encodeMethodology/decodeInto never look at these); this additionally gives each step a
// real, versioned graph identity of its own (architecture plan "Activity concept").
func stepChildren(els []defEl) (stepEls []defEl, children map[string][]string) {
	children = map[string][]string{}
	var walk func(parentRel, path, kind string, raw []any)
	walk = func(parentRel, path, kind string, raw []any) {
		for _, item := range raw {
			sm, _ := item.(map[string]any)
			name, _ := sm["name"].(string)
			if sm == nil || name == "" {
				continue
			}
			p := path + "/" + name
			rel := kind + "/" + p
			children[parentRel] = append(children[parentRel], rel)
			stepEls = append(stepEls, defEl{kind: kind, name: p, props: stepNodeProps(sm)})
			if sub, _ := sm["steps"].([]any); len(sub) > 0 {
				walk(rel, p, kind, sub)
			}
		}
	}
	for _, e := range els {
		var kind string
		switch e.kind {
		case kindProcess:
			kind = kindStep
		case kindMethod:
			kind = kindMethodStep
		default:
			continue
		}
		if raw, _ := e.props["steps"].([]any); len(raw) > 0 {
			walk(e.kind+"/"+e.name, e.name, kind, raw)
		}
	}
	return stepEls, children
}

// stepNodeProps is a step's own node properties: its JSON, with pre/done renamed to the Activity shape's
// input/output (architecture plan: Process/Step/Method/MethodStep share one input/goals/output definition), and its
// nested steps dropped (they are separate nodes of their own, reached through sub_activity, not duplicated here).
func stepNodeProps(raw map[string]any) map[string]any {
	p := maps.Clone(raw)
	if v, ok := p["pre"]; ok {
		p["input"] = v
	}
	delete(p, "pre")
	if v, ok := p["done"]; ok {
		p["output"] = v
	}
	delete(p, "done")
	delete(p, "steps")
	return p
}
