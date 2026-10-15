package registrysvc

import (
	"maps"
	"strconv"
)

// stepChildren takes the steps of every process and method element out of its properties and returns one defEl per
// step and sub-step (methodology@Step for a process, methodology@MethodStep for a method), keyed by its full path
// ("<process>/<step>/<sub-step>"), plus the sub_activity children each parent (a process, method or step element,
// named "<kind>/<name-or-path>") is composed of, in their order. A step is a node of its own and nothing else holds it
// (ADR 0119): the definition reads the steps of a process back from its sub_activity links (stepsOf), each step at
// its position among its siblings.
func stepChildren(els []defEl) (stepEls []defEl, children map[string][]string) {
	children = map[string][]string{}
	var walk func(parentRel, path, kind string, raw []any)
	walk = func(parentRel, path, kind string, raw []any) {
		seen := map[string]bool{}
		for i, item := range raw {
			sm, _ := item.(map[string]any)
			name, _ := sm["name"].(string)
			if sm == nil || name == "" {
				continue
			}
			seg := name
			if seen[seg] { // names that repeat are told apart by position, as the elements are (split)
				seg += "~" + strconv.Itoa(i)
			}
			seen[seg] = true
			p := path + "/" + seg
			rel := kind + "/" + p
			children[parentRel] = append(children[parentRel], rel)
			props := stepNodeProps(sm)
			props["position"] = float64(i)
			stepEls = append(stepEls, defEl{kind: kind, name: p, props: props})
			if sub, _ := sm["steps"].([]any); len(sub) > 0 {
				walk(rel, p, kind, sub)
			}
		}
	}
	for i, e := range els {
		var kind string
		switch e.kind {
		case kindProcess:
			kind = kindStep
		case kindMethod:
			kind = kindMethodStep
		default:
			continue
		}
		raw, _ := e.props["steps"].([]any)
		props := maps.Clone(e.props)
		delete(props, "steps")
		els[i].props = props
		if len(raw) > 0 {
			walk(e.kind+"/"+e.name, e.name, kind, raw)
		}
	}
	return stepEls, children
}

// stepNodeProps is a step's own node properties: its JSON, with pre/done renamed to the Activity shape's
// input/output (Process/Step/Method/MethodStep share one input/output definition), and its nested steps dropped (they
// are nodes of their own, reached through sub_activity).
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

// stepJSON is the JSON of a step in its process or method, from the properties of its node (references put back):
// input/output named pre/done again, its position and removed mark dropped.
func stepJSON(props map[string]any) map[string]any {
	p := maps.Clone(props)
	if v, ok := p["input"]; ok {
		p["pre"] = v
	}
	delete(p, "input")
	if v, ok := p["output"]; ok {
		p["done"] = v
	}
	delete(p, "output")
	delete(p, "position")
	delete(p, "removed")
	return p
}
