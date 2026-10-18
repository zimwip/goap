//! The tools of a turn as the model gets them (ADR 0098, `toolspecs.go`): the server tools of its frame (frame.rs)
//! with the JSON schema of their arguments, then the screen tools of the turn (ui.<name>) with theirs. The gateway
//! passes them through the provider's tool API, or through its text protocol for a provider without one.

use std::sync::LazyLock;

use goap_llm as llm;
use serde_json::{Map, Value, json};

use crate::agents::{TOOL_LIST_AGENTS, TOOL_START_AGENT};
use crate::frame::Frame;
use crate::methodtool::TOOL_METHODOLOGY_QUERY;
use crate::relate::{TOOL_ATTACH_CHANGE, TOOL_DETACH_CHANGE};
use crate::searchtool::TOOL_SEARCH_CHANGES;
use crate::turn::{
    TOOL_CREATE_CHANGE, TOOL_LIST_METHODOLOGIES, TOOL_OPEN_CHANGE, TOOL_SELECT_PROJECT, Turn,
};
use crate::uitools::{LEVEL_WRITE, PREFIX_UI, UIParam, UITool};

fn str_(desc: &str) -> Value {
    json!({"type": "string", "description": desc})
}

fn obj(props: Map<String, Value>, required: &[&str]) -> Map<String, Value> {
    let mut s = Map::new();
    s.insert("type".into(), json!("object"));
    s.insert("properties".into(), Value::Object(props));
    s.insert("additionalProperties".into(), json!(false));
    if !required.is_empty() {
        s.insert("required".into(), json!(required));
    }
    s
}

fn props(v: Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m,
        _ => Map::new(),
    }
}

/// rationaleProp is the one-sentence reason a proposal carries (start_agent, the screen tools).
fn rationale_prop() -> Value {
    str_("why you propose it, one sentence (shown to the person)")
}

fn spec(name: &str, schema: Map<String, Value>, description: &str) -> llm::ToolSpec {
    llm::ToolSpec {
        name: name.into(),
        description: description.into(),
        input_schema: schema,
    }
}

/// serverTools are every tool the server runs, as the caller; a turn declares those of its frame.
pub(crate) static SERVER_TOOLS: LazyLock<Vec<llm::ToolSpec>> = LazyLock::new(|| {
    vec![
        spec(
            TOOL_LIST_METHODOLOGIES,
            obj(Map::new(), &[]),
            "Lists the methodologies applicable to the active project (name, description, goal examples). Use it before proposing a methodology.",
        ),
        spec(
            TOOL_METHODOLOGY_QUERY,
            obj(
                props(json!({
                    "methodology": str_("its name; default: every methodology of the active project"),
                    "kind": {"type": "string", "enum": ["agent", "action", "goal", "process", "step", "role", "method", "trigger", "condition", "library"]},
                    "name": str_("exact name, or a glob with * and ?"),
                    "q": str_("text searched in name, description, examples"),
                    "parent": str_("the process, step, method or agent an element belongs to"),
                    "fields": {"type": "array", "items": {"type": "string"}, "description": "only these fields"},
                    "detail": {"type": "string", "enum": ["names", "summary", "full"]},
                    "limit": {"type": "integer", "description": "default 20, at most 50"},
                    "offset": {"type": "integer", "description": "page start"},
                })),
                &[],
            ),
            "Reads what a methodology declares, read-only and as the person. The result is {total, returned, offset, truncated, next, items} (with \"counts\" by kind when no kind is given and one methodology is queried).",
        ),
        spec(
            TOOL_SELECT_PROJECT,
            obj(
                props(json!({"project": str_("the project key")})),
                &["project"],
            ),
            "Asks the interface to make that project the active one. It is refused when the person may not work on it.",
        ),
        spec(
            TOOL_CREATE_CHANGE,
            obj(
                props(
                    json!({"title": str_("short title"), "intent": str_("why the change is needed"),
            "methodology": str_("optional: one that list_methodologies returned")}),
                ),
                &["title", "intent"],
            ),
            "Creates a change, in the active project, for the person. Offered once search_changes has run in the conversation: only do it when no change found fits, or the person asked for a new change or agreed to it.",
        ),
        spec(
            TOOL_SEARCH_CHANGES,
            obj(
                props(json!({
                    "q": str_("words of the intent (title and intent of the changes are searched, words of 3 characters or more)"),
                    "status": {"type": "array", "items": {"type": "string", "enum": ["draft", "active", "committed", "applied", "abandoned"]},
                        "description": "default: the open ones (draft, active, committed)"},
                })),
                &["q"],
            ),
            "Searches the changes of the active project (and the projects below it) whose title or intent holds the words of q, as the person: at most 10, best first, with \"total\". Call it on the intent before creating a change, and show the person what was found.",
        ),
        spec(
            TOOL_OPEN_CHANGE,
            obj(
                props(json!({"changeId": str_("the change id")})),
                &["changeId"],
            ),
            "Asks the interface to open that change. It is refused when it does not exist or is not visible to the person.",
        ),
        spec(
            TOOL_LIST_AGENTS,
            obj(Map::new(), &[]),
            "Lists the agents the person may run HERE: when they are looking at a change, those of that change's methodology (with the change's status, state and the processes already running, \"running\": true marks an agent already at work on it), with \"possibleSteps\" (the steps possible NOW towards its goal: id, \"why\", what it produces, \"mayRun\") and \"waitingSteps\" (a count of the steps waiting for conditions, never proposable); otherwise the agents of the methodologies of the active project that can start a change. Only agents whose required roles the person holds are listed; an agent flagged \"ready\" would carry out one of the possible steps.",
        ),
        spec(
            TOOL_START_AGENT,
            obj(
                props(json!({
                    "step": str_("on a change: the id of a step of possibleSteps"),
                    "methodology": str_("with no change open: the methodology, from list_agents"),
                    "agent": str_("with no change open: the agent, from list_agents"),
                    "goal": str_("the goal of the agent, optional"),
                    "intent": str_("what the person wants, optional"),
                    "changeId": str_("the change they are looking at, optional"),
                    "newChange": Value::Object(obj(props(json!({"title": str_("short title"), "intent": str_("why")})), &["title", "intent"])),
                    "rationale": str_("why this step or agent, one sentence"),
                })),
                &[],
            ),
            "Starts NOTHING: records a proposal to start an agent that the person confirms or rejects in the interface. On a change give \"step\" only; otherwise \"methodology\" and \"agent\" from list_agents (and \"newChange\" only when no change is open).",
        ),
        spec(
            TOOL_ATTACH_CHANGE,
            obj(
                props(json!({
                    "parent": str_("id of the change whose methodology integrates this one (a release), from search_changes"),
                    "rationale": rationale_prop(),
                })),
                &["parent"],
            ),
            "Changes NOTHING: records a proposal, which the person confirms, to attach the change they are looking at to a change integrating it (a release taking a candidate): it becomes its sub-change and merges into it.",
        ),
        spec(
            TOOL_DETACH_CHANGE,
            obj(props(json!({"rationale": rationale_prop()})), &[]),
            "Changes NOTHING: records a proposal, which the person confirms, to detach the change they are looking at from the change integrating it (a candidate deferred or rejected): a root change again.",
        ),
    ]
});

/// uiToolSpec is a screen tool as the model gets it: its arguments' schema, plus the rationale of its proposal.
pub(crate) fn ui_tool_spec(t: &UITool) -> llm::ToolSpec {
    let mut p = Map::new();
    p.insert("rationale".into(), rationale_prop());
    for (name, param) in &t.args.properties {
        p.insert(name.clone(), Value::Object(param_schema(param)));
    }
    let required: Vec<&str> = t.args.required.iter().map(String::as_str).collect();
    let mut desc = t.description.clone();
    if !t.guidance.is_empty() {
        desc.push_str(" Required to feed it: ");
        desc.push_str(&t.guidance);
    }
    if t.level == LEVEL_WRITE {
        desc.push_str(" (write: only proposed; the person accepts it before it is applied)");
    } else {
        desc.push_str(" (effect: run by the interface at once; no result comes back)");
    }
    llm::ToolSpec {
        name: format!("{PREFIX_UI}{}", t.name),
        description: desc,
        input_schema: obj(p, &required),
    }
}

pub(crate) fn param_schema(p: &UIParam) -> Map<String, Value> {
    let mut s = Map::new();
    match p.r#type.as_str() {
        "enum" => {
            s.insert("type".into(), json!("string"));
            s.insert(
                "enum".into(),
                if p.r#enum.is_empty() {
                    Value::Null
                } else {
                    json!(p.r#enum)
                },
            );
        }
        "array" => {
            s.insert("type".into(), json!("array"));
            let items = p.items.as_deref().map(param_schema).unwrap_or_default();
            s.insert("items".into(), Value::Object(items));
        }
        t => {
            s.insert("type".into(), json!(t));
        }
    }
    if !p.description.is_empty() {
        s.insert("description".into(), json!(p.description));
    }
    s
}

/// startAgentNoNew is start_agent where no new change may be proposed: without the newChange argument.
pub(crate) static START_AGENT_NO_NEW: LazyLock<llm::ToolSpec> = LazyLock::new(|| {
    let mut s = SERVER_TOOLS
        .iter()
        .find(|s| s.name == TOOL_START_AGENT)
        .cloned()
        .unwrap_or_default();
    let mut p = match s.input_schema.get("properties") {
        Some(Value::Object(m)) => m.clone(),
        _ => Map::new(),
    };
    p.remove("newChange");
    s.input_schema = obj(p, &[]);
    s.description = "Starts NOTHING: records a proposal to start an agent that the person confirms or rejects in the interface. On a change give \"step\" only (from possibleSteps); with no change open a new change is needed, offered once the open changes were searched (search_changes).".into();
    s
});

impl Turn {
    /// toolSpecs are the tools of this turn in its frame: the server tools of the frame, in its order, then the
    /// screen tools.
    pub(crate) fn tool_specs(&self, f: &Frame) -> Vec<llm::ToolSpec> {
        let mut specs = Vec::new();
        for name in f.tools(self.searched) {
            let Some(s) = SERVER_TOOLS.iter().find(|s| s.name == name) else {
                continue;
            };
            if name == TOOL_START_AGENT && !f.may_create(self.searched) {
                specs.push(START_AGENT_NO_NEW.clone());
            } else {
                specs.push(s.clone());
            }
        }
        for u in &self.in_.ui_tools {
            specs.push(ui_tool_spec(u));
        }
        specs
    }
}
