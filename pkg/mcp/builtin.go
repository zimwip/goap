package mcp

import "github.com/zimwip/goap/pkg/algo"

// The built-in MCPs (ADR 0028): the platform itself as tools, the way an agent harness ships its own
// tools (read, search, edit, run a task). They are split by concern; each has a connector of the
// same name served by the hub and a pass-through adapter definition, and the default organisation
// holds an instance of each, so that every unit can use them until it restricts them.
const (
	// BuiltinGraph reads the versioned graph (read, glob, grep): read-only.
	BuiltinGraph = "goap-graph"
	// BuiltinChange works on a change, the blackboard of every modification (write, edit, link, note).
	BuiltinChange = "goap-change"
	// BuiltinScheduler starts and follows processes and fires triggers. Its scope is agent: only the
	// agent level (agents[].mcps, llm actions) may start other agents.
	BuiltinScheduler = "goap-scheduler"
	// BuiltinAdmin describes the platform: units, users, MCPs, connectors, domains, methodologies.
	BuiltinAdmin = "goap-admin"
)

// BuiltinNames lists the built-in MCPs.
var BuiltinNames = []string{BuiltinGraph, BuiltinChange, BuiltinScheduler, BuiltinAdmin}

// IsBuiltin reports whether an MCP is built in.
func IsBuiltin(name string) bool {
	for _, n := range BuiltinNames {
		if n == name {
			return true
		}
	}
	return false
}

func schemaObj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		r := make([]any, len(required))
		for i, n := range required {
			r[i] = n
		}
		s["required"] = r
	}
	return s
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func object(desc string) map[string]any { return map[string]any{"type": "object", "description": desc} }
func number(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }
func list(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

// argument descriptions shared by several tools
var (
	argNamespace = str("namespace of the graph (alm, organisation, platform, ...)")
	argBaseline  = str("baseline to read (default: the head of the main branch of the namespace)")
	argLimit     = num("maximum number of results (default 50)")
	argChange    = str("change id (default: the change the calling process works on)")
	argRationale = str("why the change does this")
)

// BuiltinDefs returns the definitions of the built-in MCPs.
func BuiltinDefs() []Def {
	return []Def{
		{Name: BuiltinGraph, Description: "Read the versioned graph: nodes, their links and baselines (built in, read-only).", Tools: []Tool{
			{Name: "read", ReadOnly: true, Description: "Read a node by key: type, version, properties and links.",
				InputSchema: schemaObj(map[string]any{"namespace": argNamespace, "key": str("key of the node"), "baseline": argBaseline}, "namespace", "key")},
			{Name: "glob", ReadOnly: true, Description: "List the nodes whose key matches a glob pattern (REQ-*), optionally of one type.",
				InputSchema: schemaObj(map[string]any{"namespace": argNamespace, "pattern": str("glob on the key (default *)"),
					"type": str("node type, <namespace>@<Type>"), "baseline": argBaseline, "limit": argLimit}, "namespace")},
			{Name: "grep", ReadOnly: true, Description: "Search the nodes whose properties match a regular expression.",
				InputSchema: schemaObj(map[string]any{"namespace": argNamespace, "pattern": str("regular expression (RE2)"),
					"type": str("node type, <namespace>@<Type>"), "property": str("search this property only"),
					"ignoreCase": boolean("case-insensitive match"), "baseline": argBaseline, "limit": argLimit}, "namespace", "pattern")},
			{Name: "links", ReadOnly: true, Description: "List the links of a node, with the key and type of the other end.",
				InputSchema: schemaObj(map[string]any{"namespace": argNamespace, "key": str("key of the node"),
					"direction": str("in, out or both (default)"), "type": str("link type, <namespace>@<link>"), "baseline": argBaseline}, "namespace", "key")},
			{Name: "baselines", ReadOnly: true, Description: "List the baselines of a namespace, the head of main first.",
				InputSchema: schemaObj(map[string]any{"namespace": argNamespace, "limit": argLimit}, "namespace")},
		}},
		{Name: BuiltinChange, Description: "Work on a change, the blackboard every modification of the graph goes through (built in).", Tools: []Tool{
			{Name: "create", Description: "Open a change on the head of main of a namespace: its intent (why), unit (who) and methodology (how).",
				InputSchema: schemaObj(map[string]any{"title": str("title"), "intent": str("why the change is made"), "namespace": argNamespace,
					"methodology": str("methodology performing it"), "unit": str("unit holding the change (default: the unit of the calling change)")}, "title", "intent", "namespace")},
			{Name: "read", ReadOnly: true, Description: "Read a change: status, items, the nodes it acts on and their review.",
				InputSchema: schemaObj(map[string]any{"change": argChange})},
			{Name: "list", ReadOnly: true, Description: "List changes, the latest first: find the open change a request continues before opening a new one.",
				InputSchema: schemaObj(map[string]any{"namespace": str("only the changes of this namespace"),
					"unit":   str("only the changes held by this unit"),
					"status": str("draft, active, committed, applied or abandoned (default: every status)"), "limit": argLimit})},
			{Name: "reformulate", Description: "Revise the title/intent of a change: the previous definition is superseded, not erased, so the change keeps its full history.",
				InputSchema: schemaObj(map[string]any{"change": argChange, "title": str("new title (default: unchanged)"),
					"intent": str("new intent"), "rationale": str("why the definition changes")}, "intent", "rationale")},
			{Name: "write", Description: "Create a node in the change.",
				InputSchema: schemaObj(map[string]any{"change": argChange, "key": str("key of the new node"), "type": str("node type, <namespace>@<Type>"),
					"properties": object("properties of the node"), "rationale": argRationale}, "key", "type", "rationale")},
			{Name: "edit", Description: "Modify the properties of a node in the change (a null value clears one); expect guards against a concurrent edit.",
				InputSchema: schemaObj(map[string]any{"change": argChange, "key": str("key of the node"), "properties": object("properties to set"),
					"expect": object("current values the edit expects"), "state": str("lifecycle state to move to"), "rationale": argRationale}, "key", "rationale")},
			{Name: "link", Description: "Add a link from a node of the change to a node of the change or of its baseline.",
				InputSchema: schemaObj(map[string]any{"change": argChange, "from": str("key of the source node"), "type": str("link type, <namespace>@<link>"),
					"to": str("key of the target node"), "rationale": argRationale}, "from", "type", "to")},
			{Name: "retire", Description: "Retire (delete) a node in the change.",
				InputSchema: schemaObj(map[string]any{"change": argChange, "key": str("key of the node"), "rationale": argRationale}, "key", "rationale")},
			{Name: "note", Description: "Add a note (an artifact item) to the blackboard of the change.",
				InputSchema: schemaObj(map[string]any{"change": argChange, "type": str("kind of note (default note)"), "text": str("content"),
					"data": object("structured content")}, "text")},
			{Name: "signal", Description: "Emit a named notification other agents or a live parent may react to.",
				InputSchema: schemaObj(map[string]any{"change": argChange, "type": str("name of the signal"), "data": object("payload"),
					"target": str("process id to address it to (default: broadcast)")}, "type")},
			{Name: "validate", ReadOnly: true, Description: "Check the consistency of the change: the issues a review would raise.",
				InputSchema: schemaObj(map[string]any{"change": argChange})},
			{Name: "options", ReadOnly: true, Description: "List the options of the change (hypotheses explored on flows of their own) and the active one: the change works on it.",
				InputSchema: schemaObj(map[string]any{"change": argChange})},
			{Name: "option", Description: "Open an option of the change: a hypothesis explored on a flow of its own; activate it to work on it.",
				InputSchema: schemaObj(map[string]any{"change": argChange, "name": str("short name of the option"), "hypothesis": str("what the option assumes"),
					"activate": boolean("work on the option from now on")}, "name", "hypothesis")},
			{Name: "activate", Description: "Work on an option of the change: every call that names no flow goes to it (\"main\": back to the main flow).",
				InputSchema: schemaObj(map[string]any{"change": argChange, "option": str("id of the option, or main")}, "option")},
			{Name: "evaluate", Description: "Record the evaluation of an option (criteria, scores, rationale): it moves to evaluated.",
				InputSchema: schemaObj(map[string]any{"change": argChange, "option": str("id of the option"), "comment": str("the evaluation")}, "option", "comment")},
			{Name: "compare", ReadOnly: true, Description: "Compare the options of the change on the nodes they changed, each against the main flow.",
				InputSchema: schemaObj(map[string]any{"change": argChange, "level": str("written (default: every version not rejected) or accepted"),
					"all": boolean("include the decided options")})},
			{Name: "decisions", ReadOnly: true, Description: "List the decision points of the change (open, blocked by questions, ratifying, escalated, decided) and their questions.",
				InputSchema: schemaObj(map[string]any{"change": argChange})},
			{Name: "decision", Description: "Open a decision point: a question the change must settle, among the open options by default.",
				InputSchema: schemaObj(map[string]any{"change": argChange, "question": str("what to decide"), "options": list("option ids or names (default: the open options)"),
					"criteria": list("the criteria of the decision"), "decider": str("agent (default) or human"), "threshold": number("confidence under which a ruling waits for a person (default 0.7)"),
					"maxRounds": num("rulings that may fail to settle it before a person rules it (default 3)"), "maxDuration": str("duration after which a person rules it (48h)")}, "question")},
			{Name: "rule", Description: "Rule a decision point: decided (option, confidence 0-1, justification) or undecidable (why, and the questions to answer first). Below the threshold a person ratifies.",
				InputSchema: schemaObj(map[string]any{"change": argChange, "point": str("decision point id (default: the only pending one)"), "outcome": str("decided or undecidable"),
					"option": str("the option chosen (id or name)"), "confidence": number("0 to 1"), "justification": str("why"), "questions": list("what must be known first (undecidable)")}, "outcome", "justification")},
			{Name: "answer", Description: "Answer an open question of a decision point: the point can be ruled again once its questions are answered.",
				InputSchema: schemaObj(map[string]any{"change": argChange, "question": str("question id"), "answer": str("the answer")}, "question", "answer")},
			// ADR 0036: the change in few tokens, following an information through it, its risks and actions
			{Name: "brief", ReadOnly: true, Description: "The change in brief, one line per fact: intent, impacts, decisions, risks, actions, latest artifacts.",
				InputSchema: schemaObj(map[string]any{"change": argChange})},
			{Name: "trace", ReadOnly: true, Description: "Follow an information through the change (an item id, a risk or action key, a node key): what produced it, what it derives from, what it led to, what replaced it.",
				InputSchema: schemaObj(map[string]any{"change": argChange, "ref": str("item id, risk / action key, or node key")}, "ref")},
			{Name: "risks", ReadOnly: true, Description: "The risk register of the change (key, probability, impact, score, status, owner, actions) and its actions.",
				InputSchema: schemaObj(map[string]any{"change": argChange})},
			{Name: "risk", Description: "Raise a risk, or update one by its key (a new version keeps what it does not restate).",
				InputSchema: schemaObj(map[string]any{"change": argChange, "key": str("RSK-n to update (default: a new risk)"), "title": str("the risk"),
					"description": str("cause and consequence"), "probability": num("1 to 5"), "impact": num("1 to 5"),
					"status": str("open, mitigating, accepted, occurred or closed"), "owner": str("the role that owns it"), "actions": list("the keys of its mitigation actions"),
					"rationale": str("why this version")})},
			{Name: "action", Description: "Create an action, or update one by its key: what must be done, by which role, for which risk or decision.",
				InputSchema: schemaObj(map[string]any{"change": argChange, "key": str("ACT-n to update (default: a new action)"), "title": str("what to do"),
					"status": str("open, done or cancelled"), "owner": str("the role that does it"), "due": str("due date"), "for": str("the risk key or decision point it answers"),
					"result": str("what was done")})},
		}},
		// orchestration: only the agent level may start other agents (scope agent)
		{Name: BuiltinScheduler, Scope: ScopeAgent, Description: "Start and follow processes (agents running methodologies) and fire triggers (built in, agent level only).", Tools: []Tool{
			{Name: "start", Description: "Start a process for an intent: an agent of a methodology on a new change or an existing one.",
				InputSchema: schemaObj(map[string]any{"intent": str("what is wanted"), "methodology": str("methodology (default: identified from the intent)"),
					"agent": str("agent of the methodology"), "goal": str("goal (skips the intent loop)"), "title": str("title of the change"),
					"change": str("continue this change"), "namespace": str("namespace of the new change (default: the one of the methodology, else of the calling change)"),
					"unit": str("unit holding the new change (default: the unit of the calling change)"),
					"vars": object("process variables")}, "intent")},
			{Name: "attach", Description: "Bind a process (default: the caller's own) to a change, existing (change) or new (ADR 0031).",
				InputSchema: schemaObj(map[string]any{"process": str("process id (default: the calling process)"), "change": str("reuse this existing change"),
					"title": str("title of a new change"), "intent": str("intent of a new change"),
					"namespace": str("namespace of a new change (default: the one of the process's methodology)"),
					"unit":      str("unit holding a new change (default: the default organisation)"), "baseline": str("baseline of a new change (default: the latest of the namespace)")})},
			{Name: "list", ReadOnly: true, Description: "List processes, the latest first.",
				InputSchema: schemaObj(map[string]any{"mine": boolean("only the processes started by the caller"),
					"status": str("clarifying, running, waiting, completed, stuck, failed"), "limit": argLimit})},
			{Name: "get", ReadOnly: true, Description: "Read a process: status, plan, steps, pending question or task, error.",
				InputSchema: schemaObj(map[string]any{"id": str("process id")}, "id")},
			{Name: "triggers", ReadOnly: true, Description: "List the triggers of the published methodologies and their state.",
				InputSchema: schemaObj(map[string]any{})},
			{Name: "fire", Description: "Fire a trigger now.",
				InputSchema: schemaObj(map[string]any{"methodology": str("methodology"), "agent": str("agent"), "trigger": str("trigger")}, "methodology", "agent", "trigger")},
		}},
		{Name: BuiltinAdmin, Description: "Describe the platform: organisation, users, MCPs, connectors, domains and methodologies (built in, read-only).", Tools: []Tool{
			{Name: "units", ReadOnly: true, Description: "List the organisational units with their parent.", InputSchema: schemaObj(map[string]any{})},
			{Name: "users", ReadOnly: true, Description: "List the users with their roles and unit.",
				InputSchema: schemaObj(map[string]any{"unit": str("only the members of this unit")})},
			{Name: "mcps", ReadOnly: true, Description: "List the MCPs a unit can use: adapter, where it is defined, allowed tools.",
				InputSchema: schemaObj(map[string]any{"unit": str("unit (default: the unit of the calling change)")})},
			{Name: "connectors", ReadOnly: true, Description: "List the registered connectors, their operations and liveness.", InputSchema: schemaObj(map[string]any{})},
			{Name: "domains", ReadOnly: true, Description: "List the published domains and their node types.", InputSchema: schemaObj(map[string]any{})},
			{Name: "methodologies", ReadOnly: true, Description: "List the published methodologies and their agents.", InputSchema: schemaObj(map[string]any{})},
		}},
	}
}

// BuiltinAdapterDefs returns the adapter definitions of the built-in MCPs: each tool is the operation
// of the same name of the built-in connector of the same name.
func BuiltinAdapterDefs() []AdapterDef {
	out := make([]AdapterDef, 0, len(BuiltinNames))
	for _, n := range BuiltinNames {
		out = append(out, AdapterDef{Name: n, Description: "The built-in " + n + " MCP on the built-in connector of the same name",
			MCP: n, Connector: n, Language: algo.JavaScript, Code: "return ctx.call(ctx.tool(), ctx.args());\n"})
	}
	return out
}

// BuiltinAdapter is the instance of a built-in adapter held by a unit (the default organisation holds
// one of each).
func BuiltinAdapter(unit, name string) Adapter { return Adapter{Unit: unit, MCP: name, Adapter: name} }
