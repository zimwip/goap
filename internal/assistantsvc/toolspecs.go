package assistantsvc

import (
	"github.com/zimwip/goap/pkg/llm"
)

// The tools of a turn as the model gets them (ADR 0098): the seven server tools with the JSON schema of their
// arguments, then the screen tools of the turn (ui.<name>) with theirs. The gateway passes them through the provider's
// tool API, or through its text protocol for a provider without one.

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// rationaleProp is the one-sentence reason a proposal carries (start_agent, the screen tools).
var rationaleProp = str("why you propose it, one sentence (shown to the person)")

// serverTools are the seven tools the server runs, as the caller.
var serverTools = []llm.ToolSpec{
	{Name: ToolListMethodologies, InputSchema: obj(map[string]any{}),
		Description: "Lists the methodologies applicable to the active project (name, description, goal examples). Use it before proposing a methodology."},
	{Name: ToolMethodologyQuery, InputSchema: obj(map[string]any{
		"methodology": str("its name; default: every methodology of the active project"),
		"kind":        map[string]any{"type": "string", "enum": []string{"agent", "action", "goal", "process", "step", "role", "method", "trigger", "condition", "library"}},
		"name":        str("exact name, or a glob with * and ?"),
		"q":           str("text searched in name, description, examples"),
		"parent":      str("the process, step, method or agent an element belongs to"),
		"fields":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "only these fields"},
		"detail":      map[string]any{"type": "string", "enum": []string{"names", "summary", "full"}},
		"limit":       map[string]any{"type": "integer", "description": "default 20, at most 50"},
		"offset":      map[string]any{"type": "integer", "description": "page start"},
	}), Description: "Reads what a methodology declares, read-only and as the person. The result is {total, returned, offset, truncated, next, items} (with \"counts\" by kind when no kind is given and one methodology is queried)."},
	{Name: ToolSelectProject, InputSchema: obj(map[string]any{"project": str("the project key")}, "project"),
		Description: "Asks the interface to make that project the active one. It is refused when the person may not work on it."},
	{Name: ToolCreateChange, InputSchema: obj(map[string]any{"title": str("short title"), "intent": str("why the change is needed"),
		"methodology": str("optional: one that list_methodologies returned")}, "title", "intent"),
		Description: "Creates a change, in the active project, for the person. Only do it when they asked to start a change or agreed to it."},
	{Name: ToolOpenChange, InputSchema: obj(map[string]any{"changeId": str("the change id")}, "changeId"),
		Description: "Asks the interface to open that change. It is refused when it does not exist or is not visible to the person."},
	{Name: ToolListAgents, InputSchema: obj(map[string]any{}),
		Description: "Lists the agents the person may run HERE: when they are looking at a change, those of that change's methodology (with the change's status, state and the processes already running, \"running\": true marks an agent already at work on it), with \"possibleSteps\" (the steps possible NOW towards its goal: id, \"why\", what it produces, \"mayRun\") and \"waitingSteps\" (a count of the steps waiting for conditions, never proposable); otherwise the agents of the methodologies of the active project that can start a change. Only agents whose required roles the person holds are listed; an agent flagged \"ready\" would carry out one of the possible steps."},
	{Name: ToolStartAgent, InputSchema: obj(map[string]any{
		"step":        str("on a change: the id of a step of possibleSteps"),
		"methodology": str("with no change open: the methodology, from list_agents"),
		"agent":       str("with no change open: the agent, from list_agents"),
		"goal":        str("the goal of the agent, optional"),
		"intent":      str("what the person wants, optional"),
		"changeId":    str("the change they are looking at, optional"),
		"newChange":   obj(map[string]any{"title": str("short title"), "intent": str("why")}, "title", "intent"),
		"rationale":   str("why this step or agent, one sentence"),
	}), Description: "Starts NOTHING: records a proposal to start an agent that the person confirms or rejects in the interface. On a change give \"step\" only; otherwise \"methodology\" and \"agent\" from list_agents (and \"newChange\" only when no change is open)."},
}

// uiToolSpec is a screen tool as the model gets it: its arguments' schema, plus the rationale of its proposal.
func uiToolSpec(t UITool) llm.ToolSpec {
	props := map[string]any{"rationale": rationaleProp}
	for name, p := range t.Args.Properties {
		props[name] = paramSchema(p)
	}
	desc := t.Description
	if t.Guidance != "" {
		desc += " Required to feed it: " + t.Guidance
	}
	switch t.Level {
	case LevelWrite:
		desc += " (write: only proposed; the person accepts it before it is applied)"
	default:
		desc += " (effect: run by the interface at once; no result comes back)"
	}
	return llm.ToolSpec{Name: PrefixUI + t.Name, Description: desc, InputSchema: obj(props, t.Args.Required...)}
}

func paramSchema(p UIParam) map[string]any {
	s := map[string]any{}
	switch p.Type {
	case "enum":
		s["type"], s["enum"] = "string", p.Enum
	case "array":
		s["type"], s["items"] = "array", paramSchema(*p.Items)
	default:
		s["type"] = p.Type
	}
	if p.Description != "" {
		s["description"] = p.Description
	}
	return s
}

// toolSpecs are the tools of this turn.
func (t *turn) toolSpecs() []llm.ToolSpec {
	specs := append([]llm.ToolSpec(nil), serverTools...)
	for _, u := range t.in.UITools {
		specs = append(specs, uiToolSpec(u))
	}
	return specs
}
