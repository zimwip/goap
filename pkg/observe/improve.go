package observe

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/zimwip/goap/pkg/methodology"
)

// TypeToolRequest is the node type of a proposed MCP tool: tools are not
// part of a methodology definition, they are requested from the platform
// team (the MCP connector) and referenced by tool actions once available.
const TypeToolRequest = "ToolRequest"

// Proposal is an improvement of the methodology model, expressed on its
// metamodel nodes (so that it is reviewed like any other change).
type Proposal struct {
	// Finding is the index of the finding in the report.
	Finding int `json:"finding"`
	// Op is create_node or update_node.
	Op        string         `json:"op"`
	Key       string         `json:"key"` // node to update, or key of the node to create
	Type      string         `json:"type,omitempty"`
	Props     map[string]any `json:"props"`
	Title     string         `json:"title"`
	Rationale string         `json:"rationale"`
	// LinkType / LinkTo link a created node to an existing element.
	LinkType string `json:"linkType,omitempty"`
	LinkTo   string `json:"linkTo,omitempty"`
}

// Propose derives improvement proposals from the findings of a report for
// the methodology that ran (rules; an LLM action may refine them).
func Propose(r Report, m *methodology.Methodology) (props []Proposal, notes []string) {
	actions := map[string]methodology.Action{}
	for _, a := range m.Actions {
		actions[a.Name] = a
	}
	done := map[string]bool{}
	once := func(k string) bool {
		if done[k] {
			return false
		}
		done[k] = true
		return true
	}
	stats := map[string]ActionStats{}
	for _, s := range r.Actions {
		stats[s.Action] = s
	}
	for i, f := range r.Findings {
		a, known := actions[f.Action]
		switch f.Kind {
		case FindSystematizable:
			if !known || !once("spec:"+a.Name) {
				continue
			}
			name := a.Name + "_script"
			props = append(props, Proposal{Finding: i, Op: "create_node", Type: "Action", Key: ElementKey(m.Name, "action", name),
				Props: map[string]any{"name": name, "kind": methodology.KindScript, "language": methodology.LangJavaScript,
					"specializes": a.Name, "priority": 10, "description": "Systematized version of " + a.Name + " (no LLM call).",
					"code": scriptTemplate(a, stats[a.Name])},
				Title:     "Specialize " + a.Name + " with a script",
				Rationale: f.Evidence + ". The script takes priority; add a `when` guard to keep the LLM for atypical cases.",
				LinkType:  "specializes", LinkTo: ElementKey(m.Name, "action", a.Name)})
		case FindLLMHeavy:
			if !known || a.Kind != methodology.KindLLM || a.Model == "fast" || !once("model:"+a.Name) {
				continue
			}
			props = append(props, Proposal{Finding: i, Op: "update_node", Key: ElementKey(m.Name, "action", a.Name),
				Props: map[string]any{"model": "fast"}, Title: "Fast model for " + a.Name,
				Rationale: f.Evidence + ". A lighter model (fast alias) reduces cost and latency; check the quality of its outputs."})
		case FindLoop, FindFailure:
			if !known || !once("cost:"+a.Name) {
				continue
			}
			cost := a.Cost
			if cost < 1 {
				cost = 1
			}
			props = append(props, Proposal{Finding: i, Op: "update_node", Key: ElementKey(m.Name, "action", a.Name),
				Props: map[string]any{"cost": cost * 2}, Title: "Raise the cost of " + a.Name,
				Rationale: f.Evidence + ". Doubling its cost steers the planner toward other paths; also review its preconditions / effects."})
		case FindDisabled:
			ag := agentOf(m, f.Agent)
			if !known || ag == nil || !once("agent:"+ag.Name+":"+a.Name) {
				continue
			}
			var keep []string
			for _, x := range admissible(m, *ag) {
				if x != a.Name {
					keep = append(keep, x)
				}
			}
			props = append(props, Proposal{Finding: i, Op: "update_node", Key: ElementKey(m.Name, "agent", ag.Name),
				Props: map[string]any{"actions": keep}, Title: "Remove " + a.Name + " from agent " + ag.Name,
				Rationale: f.Evidence + ". The action does not produce its effects for this agent: removing it avoids wasted cycles."})
		case FindSlowSpan, FindSlowAction:
			tool := f.Span
			if f.Action != "" {
				tool = f.Action
			}
			name := toolName(tool)
			if !once("tool:" + name) {
				continue
			}
			props = append(props, Proposal{Finding: i, Op: "create_node", Type: TypeToolRequest, Key: ElementKey(m.Name, "tool", name),
				Props: map[string]any{"name": name, "description": "Dedicated MCP tool for " + tool, "motivation": f.Evidence, "span": f.Span},
				Title: "MCP tool " + name, Rationale: f.Evidence + ". A dedicated MCP tool (caching, batch processing) would take this hot spot off the critical path."})
		case FindReplanning:
			notes = append(notes, f.Evidence)
		}
	}
	return props, notes
}

func agentOf(m *methodology.Methodology, name string) *methodology.Agent {
	for i := range m.Agents {
		if m.Agents[i].Name == name {
			return &m.Agents[i]
		}
	}
	return nil
}

func admissible(m *methodology.Methodology, ag methodology.Agent) []string {
	if len(ag.Actions) > 0 {
		return ag.Actions
	}
	var out []string
	for _, a := range m.Actions {
		if !a.IsSpecialization() {
			out = append(out, a.Name)
		}
	}
	return out
}

func toolName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// scriptTemplate is the starting point of a systematized action: the shape
// of what the LLM produced, to be completed by the methodologist.
func scriptTemplate(a methodology.Action, s ActionStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "// Systematized version of %s (proposed from execution observation).\n", a.Name)
	fmt.Fprintf(&b, "// Observed LLM outputs: %s\n", strings.Join(slices.Sorted(maps.Keys(s.Outputs)), ", "))
	if a.Prompt != "" {
		fmt.Fprintf(&b, "// Original instructions:\n")
		for _, l := range strings.Split(strings.TrimSpace(a.Prompt), "\n") {
			fmt.Fprintf(&b, "//   %s\n", l)
		}
	}
	b.WriteString("function run(ctx) {\n")
	for _, k := range slices.Sorted(maps.Keys(s.Outputs)) {
		switch {
		case strings.HasPrefix(k, "impact"):
			b.WriteString("for (const n of ctx.nodes(\"\")) {\n  // TODO rule to select the impacted nodes\n  // ctx.addImpact(n.key, \"rule\");\n}\n")
		case strings.HasPrefix(k, "proposal/update_node"):
			b.WriteString("for (const i of ctx.impacts()) {\n  // TODO update rule\n  // ctx.proposeUpdate(i.target.key, {});\n}\n")
		case strings.HasPrefix(k, "proposal/create_node"), strings.HasPrefix(k, "proposal/add_link"):
			b.WriteString("for (const i of ctx.impacts()) {\n  // TODO nodes / links to create\n  // const p = ctx.proposeNode(\"Type\", \"KEY\", {});\n  // ctx.proposeLink(p, \"type\", i.target.key);\n}\n")
		case strings.HasPrefix(k, "artifact"):
			b.WriteString("ctx.addArtifact(\"" + strings.TrimPrefix(k, "artifact/") + "\", { /* TODO */ });\n")
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// Edit is an accepted change of the methodology model: a node update,
// creation or deletion on a metamodel key.
type Edit struct {
	Op    string // create_node | update_node | delete_node
	Key   string
	Type  string
	Props map[string]any
}

// Draft is the result of applying edits to a methodology.
type Draft struct {
	Methodology  methodology.Methodology `json:"-"`
	Applied      []string                `json:"applied"`
	Skipped      []string                `json:"skipped,omitempty"`
	ToolRequests []map[string]any        `json:"toolRequests,omitempty"`
}

// Apply returns a copy of m with the edits applied (unknown elements and
// edits on other methodologies are skipped).
func Apply(m methodology.Methodology, edits []Edit) (Draft, error) {
	var d Draft
	cp, err := clone(m)
	if err != nil {
		return d, err
	}
	for _, e := range edits {
		meth, kind, name, ok := parseKey(e.Key)
		if !ok || meth != m.Name {
			d.Skipped = append(d.Skipped, e.Key+": other methodology or node outside the model")
			continue
		}
		if e.Op == "create_node" && e.Type == TypeToolRequest {
			tr := maps.Clone(e.Props)
			if tr == nil {
				tr = map[string]any{}
			}
			tr["key"] = e.Key
			d.ToolRequests = append(d.ToolRequests, tr)
			d.Applied = append(d.Applied, "MCP tool request "+name)
			continue
		}
		var err error
		switch kind {
		case "action":
			cp.Actions, err = edit(cp.Actions, e, name, func(a methodology.Action) string { return a.Name })
			if err == nil && e.Op == "delete_node" {
				for i := range cp.Agents {
					cp.Agents[i].Actions = slices.DeleteFunc(cp.Agents[i].Actions, func(x string) bool { return x == name })
				}
			}
		case "agent":
			cp.Agents, err = edit(cp.Agents, e, name, func(a methodology.Agent) string { return a.Name })
		case "goal":
			cp.Goals, err = edit(cp.Goals, e, name, func(g methodology.Goal) string { return g.Name })
		case "condition":
			cp.Conditions, err = edit(cp.Conditions, e, name, func(c methodology.Condition) string { return c.Name })
		case "methodology":
			if desc, ok := e.Props["description"].(string); ok && e.Op == "update_node" {
				cp.Description = desc
			} else {
				err = fmt.Errorf("only the description of a methodology can be edited here")
			}
		default:
			err = fmt.Errorf("unsupported element kind %q", kind)
		}
		if err != nil {
			d.Skipped = append(d.Skipped, e.Key+": "+err.Error())
			continue
		}
		d.Applied = append(d.Applied, strings.TrimSuffix(e.Op, "_node")+" "+kind+" "+name)
	}
	d.Methodology = cp
	return d, nil
}

func clone(m methodology.Methodology) (methodology.Methodology, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return m, err
	}
	var out methodology.Methodology
	return out, json.Unmarshal(b, &out)
}

func parseKey(key string) (meth, kind, name string, ok bool) {
	rest, ok := strings.CutPrefix(key, "M:")
	if !ok {
		return "", "", "", false
	}
	parts := strings.SplitN(rest, "/", 3)
	switch len(parts) {
	case 1:
		return parts[0], "methodology", parts[0], true
	case 3:
		return parts[0], parts[1], parts[2], true
	}
	return "", "", "", false
}

// edit applies one edit to a list of definitions (JSON patch semantics: a
// null property removes the field).
func edit[T any](list []T, e Edit, name string, nameOf func(T) string) ([]T, error) {
	i := slices.IndexFunc(list, func(x T) bool { return nameOf(x) == name })
	switch e.Op {
	case "delete_node":
		if i < 0 {
			return list, fmt.Errorf("unknown element")
		}
		return slices.Delete(list, i, i+1), nil
	case "create_node":
		if i >= 0 {
			return list, fmt.Errorf("already exists")
		}
		props := maps.Clone(e.Props)
		if props == nil {
			props = map[string]any{}
		}
		if _, ok := props["name"]; !ok {
			props["name"] = name
		}
		var x T
		if err := remarshal(props, &x); err != nil {
			return list, err
		}
		return append(list, x), nil
	case "update_node":
		if i < 0 {
			return list, fmt.Errorf("unknown element")
		}
		var cur map[string]any
		if err := remarshal(list[i], &cur); err != nil {
			return list, err
		}
		for k, v := range e.Props {
			if v == nil {
				delete(cur, k)
			} else {
				cur[k] = v
			}
		}
		var x T
		if err := remarshal(cur, &x); err != nil {
			return list, err
		}
		list[i] = x
		return list, nil
	}
	return list, fmt.Errorf("unsupported op %s", e.Op)
}

func remarshal(from, to any) error {
	b, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, to)
}

// NextVersion bumps the patch number of a semantic version ("1.2.3" →
// "1.2.4"; other versions get ".1"), skipping taken versions.
func NextVersion(v string, taken func(string) bool) string {
	next := func(v string) string {
		parts := strings.Split(v, ".")
		if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil && len(parts) == 3 {
			parts[len(parts)-1] = strconv.Itoa(n + 1)
			return strings.Join(parts, ".")
		}
		return v + ".1"
	}
	out := next(v)
	for i := 0; i < 1000 && taken(out); i++ {
		out = next(out)
	}
	return out
}
