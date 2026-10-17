//! The built-in MCPs (ADR 0028): the platform itself as tools.

use goap_adapter as adapter;
use goap_algo as algo;
use goap_mcp as mcp;
use serde_json::{Map, Value, json};

// The built-in MCPs (ADR 0028): the platform itself as tools, the way an agent harness ships its own tools (read,
// search, edit, run a task). They are split by concern; each has a connector of the same name served by the hub and
// a pass-through adapter definition, and the default organisation holds an instance of each, so that every unit can
// use them until it restricts them.

/// Graph reads the versioned graph (read, glob, grep): read-only.
pub const GRAPH: &str = "goap-graph";
/// Change works on a change, the blackboard of every modification (write, edit, link, note).
pub const CHANGE: &str = "goap-change";
/// Scheduler starts and follows processes and fires triggers. Its scope is agent: only the agent level
/// (agents[].mcps, llm actions) may start other agents.
pub const SCHEDULER: &str = "goap-scheduler";
/// Admin describes the platform: units, users, MCPs, connectors, domains, methodologies.
pub const ADMIN: &str = "goap-admin";

/// Names lists the built-in MCPs.
pub const NAMES: &[&str] = &[GRAPH, CHANGE, SCHEDULER, ADMIN];

/// Is reports whether an MCP is built in.
pub fn is(name: &str) -> bool {
    NAMES.contains(&name)
}

/// The pass-through code of the built-in adapters: each tool is the operation of the same name of the connector.
pub(crate) const PASS_THROUGH: &str = "return ctx.call(ctx.tool(), ctx.args());\n";

pub(crate) fn schema_obj(props: Value, required: &[&str]) -> Value {
    let mut s = Map::new();
    s.insert("type".into(), json!("object"));
    s.insert("properties".into(), props);
    if !required.is_empty() {
        s.insert("required".into(), json!(required));
    }
    Value::Object(s)
}

pub(crate) fn string(desc: &str) -> Value {
    json!({"type": "string", "description": desc})
}

pub(crate) fn num(desc: &str) -> Value {
    json!({"type": "integer", "description": desc})
}

pub(crate) fn boolean(desc: &str) -> Value {
    json!({"type": "boolean", "description": desc})
}

pub(crate) fn object(desc: &str) -> Value {
    json!({"type": "object", "description": desc})
}

pub(crate) fn number(desc: &str) -> Value {
    json!({"type": "number", "description": desc})
}

pub(crate) fn list(desc: &str) -> Value {
    json!({"type": "array", "items": {"type": "string"}, "description": desc})
}

/// A tool of a built-in MCP (its input schema an object schema of [`schema_obj`]).
pub(crate) fn tool(
    name: &str,
    read_only: bool,
    description: impl Into<String>,
    input_schema: Value,
) -> mcp::Tool {
    mcp::Tool {
        name: name.into(),
        description: description.into(),
        input_schema: match input_schema {
            Value::Object(m) => m,
            _ => Map::new(),
        },
        read_only,
    }
}

// argument descriptions shared by several tools
fn arg_namespace() -> Value {
    string("namespace of the graph (alm, organisation, platform, ...)")
}

fn arg_baseline() -> Value {
    string("baseline to read (default: the head of the main branch of the namespace)")
}

fn arg_limit() -> Value {
    num("maximum number of results (default 50)")
}

fn arg_change() -> Value {
    string("change id (default: the change the calling process works on)")
}

fn arg_rationale() -> Value {
    string("why the change does this")
}

/// Defs returns the definitions of the built-in MCPs.
pub fn defs() -> Vec<mcp::Def> {
    vec![
        mcp::Def {
            name: GRAPH.into(),
            description: "Read the versioned graph: nodes, their links and baselines (built in, read-only).".into(),
            tools: vec![
            tool("read", true, "Read a node by key: type, version, properties and links.", schema_obj(json!({"namespace": arg_namespace(), "key": string("key of the node"), "baseline": arg_baseline()}), &["namespace", "key"])),
            tool("glob", true, "List the nodes whose key matches a glob pattern (REQ-*), optionally of one type.", schema_obj(json!({"namespace": arg_namespace(), "pattern": string("glob on the key (default *)"), "type": string("node type, <namespace>@<Type>"), "baseline": arg_baseline(), "limit": arg_limit()}), &["namespace"])),
            tool("grep", true, "Search the nodes whose properties match a regular expression.", schema_obj(json!({"namespace": arg_namespace(), "pattern": string("regular expression (RE2)"), "type": string("node type, <namespace>@<Type>"), "property": string("search this property only"), "ignoreCase": boolean("case-insensitive match"), "baseline": arg_baseline(), "limit": arg_limit()}), &["namespace", "pattern"])),
            tool("links", true, "List the links of a node, with the key and type of the other end.", schema_obj(json!({"namespace": arg_namespace(), "key": string("key of the node"), "direction": string("in, out or both (default)"), "type": string("link type, <namespace>@<link>"), "baseline": arg_baseline()}), &["namespace", "key"])),
            tool("baselines", true, "List the baselines of a namespace, the head of main first.", schema_obj(json!({"namespace": arg_namespace(), "limit": arg_limit()}), &["namespace"])),
        ],
            ..Default::default()
        },
        mcp::Def {
            name: CHANGE.into(),
            description: "Work on a change, the blackboard every modification of the graph goes through (built in).".into(),
            tools: vec![
            tool("create", false, "Open a change on the head of main of a namespace: its intent (why), unit (who) and methodology (how).", schema_obj(json!({"title": string("title"), "intent": string("why the change is made"), "namespace": arg_namespace(), "methodology": string("methodology performing it"), "unit": string("unit holding the change (default: the unit of the calling change)"), "project": string("project the change acts in (default: the active project of the caller)")}), &["title", "intent", "namespace"])),
            tool("move", false, "Move a root change, draft or active, with its sub-changes, to another project whose methodologies include the change's: the nodes it already acts on keep their project, the nodes it creates take the new one.", schema_obj(json!({"change": arg_change(), "project": string("key of the target project")}), &["project"])),
            tool("attach", false, "Attach a root change to a change whose methodology integrates its own (a release taking a candidate): it becomes its sub-change, what it committed rebased onto the parent's branch, and merges into it.", schema_obj(json!({"change": arg_change(), "parent": string("id of the change integrating it")}), &["parent"])),
            tool("detach", false, "Detach a sub-change from the change integrating it (a candidate deferred or rejected): a root change again, rebased onto the branch its former root ancestor merges into.", schema_obj(json!({"change": arg_change()}), &[])),
            tool("depend", false, "Add or remove changes this change depends on: their work must have reached the branch it merges into (main, or the change integrating it) before it merges. Never a cycle, an ancestor or a sub-change.", schema_obj(json!({"change": arg_change(), "add": list("ids of changes it now depends on"), "remove": list("ids of changes it no longer depends on")}), &[])),
            tool("upgrade", false, "Move an open change to another version of its methodology (by default the latest published one): a change otherwise keeps the version it was created with. Its lifecycle and state must exist in that version.", schema_obj(json!({"change": arg_change(), "version": string("version of the methodology (default: the latest published one)")}), &[])),
            tool("dependencies", true, "Read the dependencies of a change (satisfied or what each waits for), the open changes depending on it, and the order its open sub-changes merge in.", schema_obj(json!({"change": arg_change()}), &[])),
            tool("read", true, "Read a change: status, items, the nodes it acts on and their review.", schema_obj(json!({"change": arg_change()}), &[])),
            tool("list", true, "List changes, the latest first: find the open change a request continues before opening a new one.", schema_obj(json!({"namespace": string("only the changes of this namespace"), "unit": string("only the changes held by this unit"), "status": string("draft, active, committed, applied or abandoned (default: every status)"), "limit": arg_limit()}), &[])),
            tool("reformulate", false, "Revise the title/intent of a change: the previous definition is superseded, not erased, so the change keeps its full history.", schema_obj(json!({"change": arg_change(), "title": string("new title (default: unchanged)"), "intent": string("new intent"), "rationale": string("why the definition changes")}), &["intent", "rationale"])),
            tool("write", false, "Create a node in the change.", schema_obj(json!({"change": arg_change(), "key": string("key of the new node"), "type": string("node type, <namespace>@<Type>"), "properties": object("properties of the node"), "rationale": arg_rationale()}), &["key", "type", "rationale"])),
            tool("edit", false, "Modify the properties of a node in the change, on its draft (the node is checked out by the first edit; no version exists until the change lands; a null value clears one), or move it to a lifecycle state (a transition of the draft, the node checked out first when it has none); an edit never reviews; expect guards against a concurrent edit.", schema_obj(json!({"change": arg_change(), "key": string("key of the node"), "properties": object("properties to set"), "expect": object("current values the edit expects"), "state": string("lifecycle state to move to (without properties)"), "rationale": arg_rationale()}), &["key", "rationale"])),
            tool("link", false, "Add a link from a node of the change to a node of the change or of its baseline.", schema_obj(json!({"change": arg_change(), "from": string("key of the source node"), "type": string("link type, <namespace>@<link>"), "to": string("key of the target node"), "rationale": arg_rationale()}), &["from", "type", "to"])),
            tool("unlink", false, "Remove a link from a node of the change: removing a child is a modification of its parent (a node is never deleted).", schema_obj(json!({"change": arg_change(), "from": string("key of the source node"), "type": string("link type, <namespace>@<link>"), "to": string("key of the target node"), "rationale": arg_rationale()}), &["from", "type", "to"])),
            tool("merge", false, "Merge nodes into a new one, seen from their parents: the parents (nodes holding a composition link to the sources) lose the links to the sources and gain one to the new node; the sources stay as they are. Every source needs a parent.", schema_obj(json!({"change": arg_change(), "sources": list("keys of the nodes to merge (at least two)"), "key": string("key of the new node"), "type": string("node type of the new node, <namespace>@<Type>"), "properties": object("properties of the new node"), "rationale": arg_rationale()}), &["sources", "key", "type", "rationale"])),
            tool("split", false, "Split a node into new ones, seen from its parents: the parents lose the link to it and gain a link to each new node; the node stays as it is. The other links to it are returned as suspect.", schema_obj(json!({"change": arg_change(), "source": string("key of the node to split"), "into": json!({"type": "array", "description": "the new nodes, at least two: [{key, type, properties, rationale}]", "items": json!({"type": "object"})})}), &["source", "into"])),
            tool("cancel", false, "Cancel the checkout of a node of the change: its draft is dropped; a node the change created goes away.", schema_obj(json!({"change": arg_change(), "key": string("key of the node")}), &["key"])),
            tool("remove", false, "Take nodes out of the change: their drafts are dropped and they leave the list of change impacts (one key, or several keys at once: all or none).", schema_obj(json!({"change": arg_change(), "key": string("key of the node"), "keys": list("keys of several nodes, taken out together")}), &[])),
            tool("note", false, "Add a note (an artifact item) to the blackboard of the change.", schema_obj(json!({"change": arg_change(), "target": string("what the note is (default note)"), "text": string("content"), "data": object("structured content")}), &["text"])),
            tool("signal", false, "Emit a named notification other agents or a live parent may react to.", schema_obj(json!({"change": arg_change(), "target": string("name of the signal"), "data": object("payload"), "recipient": string("process id to address it to (default: broadcast)")}), &["target"])),
            tool("validate", true, "Check the consistency of the change: the issues a review would raise.", schema_obj(json!({"change": arg_change()}), &[])),
            tool("options", true, "List the options of the change (hypotheses explored on flows of their own) and the active one: the change works on it.", schema_obj(json!({"change": arg_change()}), &[])),
            tool("option", false, "Open an option of the change: a hypothesis explored on a flow of its own; activate it to work on it.", schema_obj(json!({"change": arg_change(), "name": string("short name of the option"), "hypothesis": string("what the option assumes"), "activate": boolean("work on the option from now on")}), &["name", "hypothesis"])),
            tool("activate", false, "Work on an option of the change: every call that names no flow goes to it (\"main\": back to the main flow).", schema_obj(json!({"change": arg_change(), "option": string("id of the option, or main")}), &["option"])),
            tool("evaluate", false, "Record the evaluation of an option (criteria, scores, rationale): it moves to evaluated.", schema_obj(json!({"change": arg_change(), "option": string("id of the option"), "comment": string("the evaluation")}), &["option", "comment"])),
            tool("compare", true, "Compare the options of the change on the nodes they changed, each against the main flow.", schema_obj(json!({"change": arg_change(), "level": string("written (default: every version not rejected) or accepted"), "all": boolean("include the decided options")}), &[])),
            tool("decisions", true, "List the decision points of the change (open, blocked by questions, ratifying, escalated, decided) and their questions.", schema_obj(json!({"change": arg_change()}), &[])),
            tool("decision", false, "Open a decision point: a question the change must settle, among the open options by default.", schema_obj(json!({"change": arg_change(), "question": string("what to decide"), "options": list("option ids or names (default: the open options)"), "criteria": list("the criteria of the decision"), "decider": string("agent (default) or human"), "threshold": number("confidence under which a ruling waits for a person (default 0.7)"), "maxRounds": num("rulings that may fail to settle it before a person rules it (default 3)"), "maxDuration": string("duration after which a person rules it (48h)")}), &["question"])),
            tool("rule", false, "Rule a decision point: decided (option, confidence 0-1, justification) or undecidable (why, and the questions to answer first). Below the threshold a person ratifies.", schema_obj(json!({"change": arg_change(), "point": string("decision point id (default: the only pending one)"), "outcome": string("decided or undecidable"), "option": string("the option chosen (id or name)"), "confidence": number("0 to 1"), "justification": string("why"), "questions": list("what must be known first (undecidable)")}), &["outcome", "justification"])),
            tool("answer", false, "Answer an open question of a decision point: the point can be ruled again once its questions are answered.", schema_obj(json!({"change": arg_change(), "question": string("question id"), "answer": string("the answer")}), &["question", "answer"])),
            // ADR 0036: the change in few tokens, following an information through it, its risks and actions
            tool("brief", true, "The change in brief, one line per fact: intent, impacts, decisions, risks, actions, latest artifacts.", schema_obj(json!({"change": arg_change()}), &[])),
            tool("trace", true, "Follow an information through the change (an item id, a risk or action key, a node key): what produced it, what it derives from, what it led to, what replaced it.", schema_obj(json!({"change": arg_change(), "ref": string("item id, risk / action key, or node key")}), &["ref"])),
            tool("risks", true, "The risk register of the change (key, probability, impact, score, status, owner, actions) and its actions.", schema_obj(json!({"change": arg_change()}), &[])),
            tool("risk", false, "Raise a risk, or update one by its key (a new version keeps what it does not restate).", schema_obj(json!({"change": arg_change(), "key": string("RSK-n to update (default: a new risk)"), "title": string("the risk"), "description": string("cause and consequence"), "probability": num("1 to 5"), "impact": num("1 to 5"), "status": string("open, mitigating, accepted, occurred or closed"), "owner": string("the role that owns it"), "actions": list("the keys of its mitigation actions"), "rationale": string("why this version")}), &[])),
            tool("derogations", true, "The derogations of the change (rule, target, reason, signatory, expires, status).", schema_obj(json!({"change": arg_change()}), &[])),
            tool("derogation", false, "Sign a derogation (a bounded acceptance of a gap), or update / close one by its key. Needs derogation:sign; the signatory is the caller.", schema_obj(json!({"change": arg_change(), "key": string("DRG-n to update (default: a new derogation)"), "rule": string("what is waived"), "target": string("the change impact (id or key), action or gate it applies to"), "reason": string("why"), "expires": string("a date (YYYY-MM-DD) or RFC 3339"), "status": string("open or closed")}), &[])),
            tool("reviews", true, "The reviews of the change (global comment, status open / submitted / discarded, one entry per impact with its comment and outcome) and the impacts that await a review.", schema_obj(json!({"change": arg_change(), "flow": string("flow (default: the active option, else main)")}), &[])),
            tool("review_open", false, "Open a review the reviewer builds up, then submits: a global comment and, optionally, the impacts it takes.", schema_obj(json!({"change": arg_change(), "flow": string("flow (default: the active option, else main)"), "comment": string("the global comment"), "impacts": list("keys (or ids) of the impacts that await review")}), &[])),
            tool("review_update", false, "Change an open review (only its author or an administrator): comment, impacts to add or remove, and per impact its comment and outcome.", schema_obj(json!({"change": arg_change(), "review": string("review key"), "comment": string("the global comment"), "add": list("impacts to add (keys or ids)"), "remove": list("impacts to remove"), "entries": json!({"type": "array", "description": "[{impact, comment, outcome: accept | reject}]", "items": json!({"type": "object"})})}), &["review"])),
            tool("review_submit", false, "Submit an open review: every entry is reviewed, with its comment and the global one, in one transaction (all or none). A refusal names the entry and leaves the review open; a submitted review is final.", schema_obj(json!({"change": arg_change(), "review": string("review key")}), &["review"])),
            tool("review_discard", false, "Discard an open review that was never submitted (final).", schema_obj(json!({"change": arg_change(), "review": string("review key")}), &["review"])),
            tool("action", false, "Create an action, or update one by its key: what must be done, by which role, for which risk or decision.", schema_obj(json!({"change": arg_change(), "key": string("ACT-n to update (default: a new action)"), "title": string("what to do"), "status": string("open, done or cancelled"), "owner": string("the role that does it"), "due": string("due date"), "for": string("the risk key or decision point it answers"), "result": string("what was done")}), &[])),
        ],
            ..Default::default()
        },
        // orchestration: only the agent level may start other agents (scope agent)
        mcp::Def {
            name: SCHEDULER.into(),
            description: "Start and follow processes (agents running methodologies) and fire triggers (built in, agent level only).".into(),
            scope: mcp::SCOPE_AGENT.into(),
            tools: vec![
            tool("start", false, "Start a process for an intent: an agent of a methodology on a new change or an existing one.", schema_obj(json!({"intent": string("what is wanted"), "methodology": string("methodology (default: identified from the intent)"), "agent": string("agent of the methodology"), "goal": string("goal (skips the intent loop)"), "title": string("title of the change"), "change": string("continue this change"), "namespace": string("namespace of the new change (default: the one of the methodology, else of the calling change)"), "unit": string("unit holding the new change (default: the unit of the calling change)"), "vars": object("process variables")}), &["intent"])),
            tool("attach", false, "Bind a process (default: the caller's own) to a change, existing (change) or new (ADR 0031).", schema_obj(json!({"process": string("process id (default: the calling process)"), "change": string("reuse this existing change"), "title": string("title of a new change"), "intent": string("intent of a new change"), "namespace": string("namespace of a new change (default: the one of the process's methodology)"), "unit": string("unit holding a new change (default: the default organisation)"), "baseline": string("baseline of a new change (default: the latest of the namespace)")}), &[])),
            tool("list", true, "List processes, the latest first.", schema_obj(json!({"mine": boolean("only the processes started by the caller"), "status": string("clarifying, running, waiting, completed, stuck, failed"), "limit": arg_limit()}), &[])),
            tool("get", true, "Read a process: status, plan, steps, pending question or task, error.", schema_obj(json!({"id": string("process id")}), &["id"])),
            tool("starting_points", true, "The steps of the methodology of a change that are possible now towards its goal (the end point of the process): every entry condition holds and nothing carries them out. Each has what makes it possible, what it produces, the roles and how to start it; the steps that wait are only counted. Propose only these (ADR 0097).", schema_obj(json!({"changeId": string("change (default: the calling change)"), "methodology": string("methodology (default: the one of the change)")}), &[])),
            tool("triggers", true, "List the triggers of the published methodologies and their state.", schema_obj(json!({}), &[])),
            tool("fire", false, "Fire a trigger now.", schema_obj(json!({"methodology": string("methodology"), "agent": string("agent"), "trigger": string("trigger")}), &["methodology", "agent", "trigger"])),
        ],
        },
        mcp::Def {
            name: ADMIN.into(),
            description: "Describe the platform: organisation, users, MCPs, connectors, domains and methodologies (built in, read-only).".into(),
            tools: vec![
            tool("units", true, "List the organisational units with their parent.", schema_obj(json!({}), &[])),
            tool("users", true, "List the users with their roles and unit.", schema_obj(json!({"unit": string("only the members of this unit")}), &[])),
            tool("mcps", true, "List the MCPs a unit can use: adapter, where it is defined, allowed tools.", schema_obj(json!({"unit": string("unit (default: the unit of the calling change)")}), &[])),
            tool("connectors", true, "List the registered connectors, their operations and liveness.", schema_obj(json!({}), &[])),
            tool("domains", true, "List the published domains and their node types.", schema_obj(json!({}), &[])),
            tool("methodologies", true, "List the published methodologies and their agents.", schema_obj(json!({}), &[])),
        ],
            ..Default::default()
        },
    ]
}

/// AdapterDefs returns the adapter definitions of the built-in MCPs: each tool is the operation of the same name of
/// the built-in connector of the same name.
pub fn adapter_defs() -> Vec<adapter::Def> {
    NAMES
        .iter()
        .map(|n| adapter::Def {
            name: n.to_string(),
            description: format!("The built-in {n} MCP on the built-in connector of the same name"),
            mcp: n.to_string(),
            connector: n.to_string(),
            language: algo::JAVASCRIPT.into(),
            code: PASS_THROUGH.into(),
            ..Default::default()
        })
        .collect()
}

/// Adapter is the instance of a built-in adapter held by a unit (the default organisation holds one of each).
pub fn adapter(unit: &str, name: &str) -> adapter::Instance {
    adapter::Instance {
        unit: unit.into(),
        mcp: name.into(),
        adapter: name.into(),
        ..Default::default()
    }
}
