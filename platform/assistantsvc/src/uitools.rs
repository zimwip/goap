//! Screen tools (ADR 0092) (`uitools.go`). The screen the person is on offers, with each message, the tools it can run
//! (navigate, filter, edit a field...). The service never runs them: an `effect` tool (no data changes) is handed back
//! to the web as a requested action, a `write` tool as a proposal the person confirms (ConfirmAction) and the web then
//! runs and reports (ReportAction). The model calls them under the prefix `ui.`, so that one can never shadow a server
//! tool.

use std::collections::{BTreeMap, HashMap};
use std::sync::LazyLock;

use goap_authz::{principal_from, with_principal};
use goap_convsvc as convsvc;
use goap_ctx::Ctx;
use regex::Regex;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value, json};

use crate::agents::{STATUS_FAILED, STATUS_PROPOSED};
use crate::context::{MAX_ID_BYTES, clean};
use crate::service::{Error, MAX_ERROR_TEXT, PRINCIPAL, Service};
use crate::turn::{ToolCall, Turn};
use crate::util::{Args, clip, go_json, q, sv};

/// ActionUITool is the type of the actions of the screen tools; PrefixUI the prefix of their name in the model's
/// calls.
pub const ACTION_UI_TOOL: &str = "ui_tool";
pub const PREFIX_UI: &str = "ui.";

pub const LEVEL_EFFECT: &str = "effect";
pub const LEVEL_WRITE: &str = "write";

/// StatusRequested is an effect handed to the web; StatusAccepted a write the person accepted and the web is to run;
/// StatusDone and StatusFailed the outcome the web reported.
pub const STATUS_REQUESTED: &str = "requested";
pub const STATUS_ACCEPTED: &str = "accepted";
pub const STATUS_DONE: &str = "done";

// Caps of the descriptors and of what the model calls them with.
pub const MAX_UI_TOOLS: usize = 30;
/// MaxProposalsPerAnswer and MaxEffectsPerAnswer cap the screen tools of one answer.
pub const MAX_PROPOSALS_PER_ANSWER: usize = 3;
pub const MAX_EFFECTS_PER_ANSWER: usize = 5;

pub(crate) const MAX_TOOL_DESCRIPTION: usize = 300;
pub(crate) const MAX_TOOL_GUIDANCE: usize = 200;
pub(crate) const MAX_TOOL_SCHEMA_BYTES: usize = 2 << 10;
pub(crate) const MAX_TOOL_PROPS: usize = 12;
pub(crate) const MAX_ENUM_VALUES: usize = 30;
pub(crate) const MAX_ENUM_BYTES: usize = 100;
pub(crate) const MAX_PARAM_DESC: usize = 200;
pub(crate) const MAX_ARRAY_ITEMS: usize = 50;
pub(crate) const MAX_ARG_BYTES: usize = 4 << 10;
pub(crate) const MAX_STRING_ARG: usize = 2000;

const TOOL_NAME_PATTERN: &str = r"^[a-z][a-z0-9_.-]{0,48}$";
const PROP_NAME_PATTERN: &str = r"^[A-Za-z][A-Za-z0-9_]{0,39}$";
static TOOL_NAME_RE: LazyLock<Regex> = LazyLock::new(|| Regex::new(TOOL_NAME_PATTERN).unwrap());
static PROP_NAME_RE: LazyLock<Regex> = LazyLock::new(|| Regex::new(PROP_NAME_PATTERN).unwrap());

/// UITool describes a tool of the current screen.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct UITool {
    pub name: String,
    pub description: String,
    /// Guidance is one line: what is required to feed it.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub guidance: String,
    /// Level is effect or write.
    pub level: String,
    pub args: UIArgs,
    /// Target is the element id it acts on.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub target: String,
}

/// UIArgs is the subset of JSON Schema a tool takes: an object of scalar, enum or array properties.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct UIArgs {
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub properties: BTreeMap<String, UIParam>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub required: Vec<String>,
}

/// UIParam is one property: Type is string, number, boolean, enum (Enum lists its values) or array (Items is the
/// type of its elements, a scalar or an enum).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct UIParam {
    #[serde(rename = "type")]
    pub r#type: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(rename = "enum", skip_serializing_if = "Vec::is_empty")]
    pub r#enum: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub items: Option<Box<UIParam>>,
}

fn check_param(name: &str, p: &UIParam, item: bool) -> Result<UIParam, String> {
    let mut out = UIParam {
        r#type: p.r#type.clone(),
        description: clean(&p.description, MAX_PARAM_DESC, false),
        ..Default::default()
    };
    match p.r#type.as_str() {
        "string" | "number" | "boolean" => {
            if !p.r#enum.is_empty() || p.items.is_some() {
                return Err(format!(
                    "{}: a {} takes no enum or items",
                    q(name),
                    p.r#type
                ));
            }
        }
        "enum" => {
            if p.items.is_some() || p.r#enum.is_empty() || p.r#enum.len() > MAX_ENUM_VALUES {
                return Err(format!(
                    "{}: an enum lists 1 to {MAX_ENUM_VALUES} values",
                    q(name)
                ));
            }
            for v in &p.r#enum {
                let v = clean(v, MAX_ENUM_BYTES, false);
                if v.is_empty() || v.len() > MAX_ENUM_BYTES {
                    return Err(format!("{}: an enum value is empty or too long", q(name)));
                } else if out.r#enum.contains(&v) {
                    return Err(format!("{}: duplicate enum value {}", q(name), q(&v)));
                } else {
                    out.r#enum.push(v);
                }
            }
        }
        "array" => {
            let Some(items) = p.items.as_deref().filter(|_| !item && p.r#enum.is_empty()) else {
                return Err(format!("{}: an array (never nested) needs items", q(name)));
            };
            let it = check_param(&format!("{name}[]"), items, true)?;
            out.items = Some(Box::new(it));
        }
        t => {
            return Err(format!(
                "{}: type {} is not one of string, number, boolean, enum, array",
                q(name),
                q(t)
            ));
        }
    }
    Ok(out)
}

/// checkTools validates and cleans the descriptors of a request: an invalid one refuses the request (it is a bug of
/// the screen, not of the model).
pub(crate) fn check_tools(tools: &[UITool]) -> Result<Vec<UITool>, Error> {
    if tools.len() > MAX_UI_TOOLS {
        return Err(Error::Invalid(format!(
            "{} screen tools, at most {MAX_UI_TOOLS}",
            tools.len()
        )));
    }
    let bad =
        |t: &UITool, msg: String| Error::Invalid(format!("screen tool {}: {msg}", q(&t.name)));
    let mut out = Vec::new();
    let mut seen = std::collections::HashSet::new();
    for t in tools {
        if !TOOL_NAME_RE.is_match(&t.name) {
            return Err(bad(t, format!("the name must match {TOOL_NAME_PATTERN}")));
        }
        if !seen.insert(t.name.clone()) {
            return Err(bad(t, "duplicate name".into()));
        }
        if t.level != LEVEL_EFFECT && t.level != LEVEL_WRITE {
            return Err(bad(
                t,
                format!("the level is {LEVEL_EFFECT} or {LEVEL_WRITE}"),
            ));
        }
        if t.description.trim().is_empty()
            || t.description.len() > MAX_TOOL_DESCRIPTION
            || t.guidance.len() > MAX_TOOL_GUIDANCE
            || t.target.len() > MAX_ID_BYTES
        {
            return Err(bad(
                t,
                format!(
                    "a description (at most {MAX_TOOL_DESCRIPTION} bytes), a guidance ({MAX_TOOL_GUIDANCE}) and a target ({MAX_ID_BYTES}) are needed within their size"
                ),
            ));
        }
        let mut c = UITool {
            name: t.name.clone(),
            description: clean(&t.description, MAX_TOOL_DESCRIPTION, false),
            guidance: clean(&t.guidance, MAX_TOOL_GUIDANCE, false),
            level: t.level.clone(),
            target: clean(&t.target, MAX_ID_BYTES, false),
            args: UIArgs::default(),
        };
        if t.args.properties.len() > MAX_TOOL_PROPS || t.args.required.len() > MAX_TOOL_PROPS {
            return Err(bad(t, format!("at most {MAX_TOOL_PROPS} properties")));
        }
        for (name, p) in &t.args.properties {
            if !PROP_NAME_RE.is_match(name) {
                return Err(bad(
                    t,
                    format!("the property {} must match {PROP_NAME_PATTERN}", q(name)),
                ));
            }
            let cp = check_param(name, p, false).map_err(|e| bad(t, e))?;
            c.args.properties.insert(name.clone(), cp);
        }
        for r in &t.args.required {
            if !c.args.properties.contains_key(r) || c.args.required.contains(r) {
                return Err(bad(
                    t,
                    format!("required {} is not a (single) property", q(r)),
                ));
            }
            c.args.required.push(r.clone());
        }
        let n = go_json(&c.args).len();
        if n > MAX_TOOL_SCHEMA_BYTES {
            return Err(bad(
                t,
                format!("the schema is {n} bytes, at most {MAX_TOOL_SCHEMA_BYTES}"),
            ));
        }
        out.push(c);
    }
    Ok(out)
}

pub(crate) fn tool_index(tools: &[UITool]) -> HashMap<String, UITool> {
    tools.iter().map(|t| (t.name.clone(), t.clone())).collect()
}

/// checkValue validates one argument against its param.
fn check_value(name: &str, p: &UIParam, v: &Value) -> Result<(), String> {
    match p.r#type.as_str() {
        "string" => match v.as_str() {
            Some(s) if s.len() <= MAX_STRING_ARG => {}
            _ => {
                return Err(format!(
                    "{} must be a string of at most {MAX_STRING_ARG} bytes",
                    q(name)
                ));
            }
        },
        "number" => {
            if !v.is_number() {
                return Err(format!("{} must be a number", q(name)));
            }
        }
        "boolean" => {
            if !v.is_boolean() {
                return Err(format!("{} must be a boolean", q(name)));
            }
        }
        "enum" => {
            if !v.as_str().is_some_and(|s| p.r#enum.iter().any(|e| e == s)) {
                return Err(format!(
                    "{} must be one of {}",
                    q(name),
                    p.r#enum.join(", ")
                ));
            }
        }
        "array" => {
            let Some(l) = v.as_array().filter(|l| l.len() <= MAX_ARRAY_ITEMS) else {
                return Err(format!(
                    "{} must be an array of at most {MAX_ARRAY_ITEMS} elements",
                    q(name)
                ));
            };
            if let Some(items) = &p.items {
                for (i, e) in l.iter().enumerate() {
                    check_value(&format!("{name}[{i}]"), items, e)?;
                }
            }
        }
        _ => {}
    }
    Ok(())
}

/// checkArgs validates the arguments of a call against the schema of its descriptor: unknown properties, missing
/// required ones, types, enums and size are refused.
fn check_args(t: &UITool, a: &Args) -> Result<(), String> {
    let n = go_json(&a.0).len();
    if n > MAX_ARG_BYTES {
        return Err(format!(
            "the arguments are {n} bytes, at most {MAX_ARG_BYTES}"
        ));
    }
    let unknown: Vec<&str> =
        a.0.keys()
            .filter(|k| !t.args.properties.contains_key(*k))
            .map(String::as_str)
            .collect();
    if !unknown.is_empty() {
        return Err(format!(
            "unknown argument(s) {} for {PREFIX_UI}{}",
            unknown.join(", "),
            t.name
        ));
    }
    for r in &t.args.required {
        if !a.0.contains_key(r) {
            return Err(format!(
                "{} is required ({})",
                q(r),
                or_default(&t.guidance, "see the tool description")
            ));
        }
    }
    for (k, v) in &a.0 {
        check_value(k, &t.args.properties[k], v)?;
    }
    Ok(())
}

fn or_default<'a>(s: &'a str, d: &'a str) -> &'a str {
    if s.is_empty() { d } else { s }
}

/// shortArgs is a one-line view of arguments for labels and the history: k=v pairs in key order, cut.
pub(crate) fn short_args(m: &Map<String, Value>) -> String {
    let parts: Vec<String> = m
        .iter()
        .map(|(k, v)| format!("{k}={}", clip(&go_json(v), 60)))
        .collect();
    clip(&parts.join(", "), 160)
}

impl Turn {
    /// uiTool handles a call of a screen tool: it is validated against this turn's descriptors and recorded, never
    /// run.
    pub(crate) fn ui_tool(&mut self, c: &ToolCall, a: &Args) -> Result<Value, String> {
        let name = c.name.strip_prefix(PREFIX_UI).unwrap_or(&c.name);
        let Some(tool) = self.ui_tools.get(name).cloned() else {
            let mut names: Vec<String> = self
                .ui_tools
                .keys()
                .map(|n| format!("{PREFIX_UI}{n}"))
                .collect();
            names.sort();
            return Err(format!(
                "the screen offers no tool {} this turn (screen tools: {})",
                q(&c.name),
                or_default(&names.join(", "), "none")
            ));
        };
        check_args(&tool, a)?;
        let mut act = Map::new();
        act.insert("type".into(), json!(ACTION_UI_TOOL));
        act.insert("level".into(), json!(tool.level));
        act.insert("tool".into(), json!(tool.name));
        act.insert("args".into(), Value::Object(a.0.clone()));
        let mut label = tool.description.clone();
        let s = short_args(&a.0);
        if !s.is_empty() {
            label.push_str(&format!(" ({s})"));
        }
        act.insert("label".into(), json!(clip(&label, 200)));
        if !tool.target.is_empty() {
            act.insert("target".into(), json!(tool.target));
        }
        let r = clean(&c.rationale, 500, false);
        if !r.is_empty() {
            act.insert("rationale".into(), json!(r));
        }
        if tool.level == LEVEL_EFFECT {
            if self.effects >= MAX_EFFECTS_PER_ANSWER {
                return Err(format!(
                    "too many interface actions in one answer (at most {MAX_EFFECTS_PER_ANSWER})"
                ));
            }
            self.effects += 1;
            act.insert("status".into(), json!(STATUS_REQUESTED));
            self.actions.push(act);
            return Ok(
                json!({"status": STATUS_REQUESTED, "note": "requested on the client: no result is available, do not rely on its outcome"}),
            );
        }
        if self.proposals >= MAX_PROPOSALS_PER_ANSWER {
            return Err(format!(
                "too many proposals in one answer (at most {MAX_PROPOSALS_PER_ANSWER}): the person decides these first"
            ));
        }
        self.proposals += 1;
        act.insert("status".into(), json!(STATUS_PROPOSED));
        act.insert("project".into(), json!(self.project));
        self.actions.push(act);
        Ok(
            json!({"status": STATUS_PROPOSED, "note": "proposed, awaiting the person's confirmation in the interface: nothing was changed. Say what you propose and why, and do not say it was done."}),
        )
    }
}

/// ReportInput is the outcome the web reports of an action it ran.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ReportInput {
    pub conversation_id: String,
    pub message_id: String,
    pub action_index: i64,
    /// Status is done or failed; Error says what went wrong.
    pub status: String,
    pub error: String,
}

impl Service {
    /// Report records the outcome of a screen tool the web ran (ADR 0092), as the owner of the conversation: a write
    /// proposal that was accepted, or an effect that was requested, becomes done or failed. Any other state is
    /// State, an outcome already reported Decided.
    pub async fn report(&self, ctx: &Ctx, in_: ReportInput) -> Result<convsvc::Message, Error> {
        let p = principal_from(ctx);
        if p.anonymous() {
            return Err(convsvc::Error::Anonymous.into());
        }
        if p.is_system() {
            return Err(convsvc::Error::Forbidden.into());
        }
        if in_.status != STATUS_DONE && in_.status != STATUS_FAILED {
            return Err(Error::Invalid(format!(
                "the status is {STATUS_DONE} or {STATUS_FAILED}"
            )));
        }
        let (_, msgs) = self.convs.get(ctx, &p, &in_.conversation_id).await?; // the owner only
        let Some(msg) = msgs.iter().find(|m| m.id == in_.message_id) else {
            return Err(convsvc::Error::NotFound.into());
        };
        if msg.role != convsvc::ROLE_ASSISTANT
            || in_.action_index < 0
            || in_.action_index as usize >= msg.actions.len()
            || sv(&msg.actions[in_.action_index as usize], "type") != ACTION_UI_TOOL
        {
            return Err(Error::Invalid("that is not a screen tool action".into()));
        }
        let sys = with_principal(&ctx.without_cancel(), PRINCIPAL.clone());
        let at = self.now().format("%Y-%m-%dT%H:%M:%SZ").to_string();
        let by = p.subject.clone();
        let status = in_.status.clone();
        let err = clean(&in_.error, MAX_ERROR_TEXT, false);
        let m = self
            .convs
            .update_action(
                &sys,
                &PRINCIPAL,
                &in_.message_id,
                in_.action_index,
                Box::new(move |mut a| {
                    let st = a.get("status").cloned();
                    match sv(&a, "status") {
                        STATUS_DONE | STATUS_FAILED => {
                            return Err(convsvc::Error::other(Error::Decided));
                        }
                        STATUS_ACCEPTED => {
                            if sv(&a, "level") != LEVEL_WRITE {
                                return Err(convsvc::Error::other(Error::State(String::new())));
                            }
                        }
                        STATUS_REQUESTED => {
                            if sv(&a, "level") != LEVEL_EFFECT {
                                return Err(convsvc::Error::other(Error::State(String::new())));
                            }
                        }
                        _ => {
                            return Err(convsvc::Error::other(Error::State(format!(
                                "it is {}",
                                crate::util::go_v(st.as_ref())
                            ))));
                        }
                    }
                    a.insert("status".into(), json!(status));
                    a.insert("reported".into(), json!({"by": by, "at": at}));
                    if !err.is_empty() && status == STATUS_FAILED {
                        a.insert("error".into(), json!(err));
                    }
                    Ok(a)
                }),
            )
            .await?;
        Ok(m)
    }
}
