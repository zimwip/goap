//! Improvement proposals and their application to a methodology draft (`improve.go`).

use std::collections::{BTreeMap, HashMap, HashSet};

use goap_methodology::{Action, Agent, KIND_LLM, KIND_SCRIPT, LANG_JAVASCRIPT, Methodology};
use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value, json};

use crate::analyze::*;
use crate::util::{null_default, null_if_empty, number, quote};

/// The errors of the package.
#[derive(Debug, Clone, thiserror::Error)]
pub enum Error {
    /// A JSON encoding or decoding failed.
    #[error("{0}")]
    Json(String),
}

impl From<serde_json::Error> for Error {
    fn from(e: serde_json::Error) -> Self {
        Error::Json(e.to_string())
    }
}

/// Proposal is an improvement of the methodology model, expressed on its definition nodes (so that it is reviewed
/// like any other change), in the terms of the definition: the self-observation writes the names of its elements as
/// links (ADR 0120, methodology.EncodeRefs). A proposed MCP tool (TYPE_TOOL_REQUEST) is not part of the definition:
/// it is requested from the platform team and referenced by tool actions once available.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Proposal {
    /// Finding is the index of the finding in the report.
    pub finding: i64,
    /// Op is create_node or update_node.
    pub op: String,
    /// Node to update, or key of the node to create.
    pub key: String,
    #[serde(rename = "type", skip_serializing_if = "String::is_empty")]
    pub r#type: String,
    #[serde(deserialize_with = "null_default")]
    pub props: Map<String, Value>,
    pub title: String,
    pub rationale: String,
}

fn props(v: Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m,
        _ => Map::new(),
    }
}

/// Propose derives improvement proposals from the findings of a report for the methodology that ran (rules; an LLM
/// action may refine them).
pub fn propose(r: &Report, m: &Methodology) -> (Vec<Proposal>, Vec<String>) {
    let mut out = Vec::new();
    let mut notes = Vec::new();
    let actions: HashMap<&str, &Action> = m.actions.iter().map(|a| (a.name.as_str(), a)).collect();
    let mut done: HashSet<String> = HashSet::new();
    let stats: HashMap<&str, &ActionStats> =
        r.actions.iter().map(|s| (s.action.as_str(), s)).collect();
    let empty = ActionStats::default();
    for (i, f) in r.findings.iter().enumerate() {
        let i = i as i64;
        let a = actions.get(f.action.as_str()).copied();
        match f.kind.as_str() {
            FIND_SYSTEMATIZABLE => {
                let Some(a) = a else { continue };
                if !done.insert(format!("spec:{}", a.name)) {
                    continue;
                }
                let name = format!("{}_script", a.name);
                let st = stats.get(a.name.as_str()).copied().unwrap_or(&empty);
                out.push(Proposal {
                    finding: i,
                    op: "create_node".into(),
                    r#type: TYPE_ACTION.into(),
                    key: element_key(&m.name, &m.version, "action", &name),
                    props: props(json!({
                        "name": name, "kind": KIND_SCRIPT, "language": LANG_JAVASCRIPT,
                        "specializes": a.name, "priority": 10,
                        "description": format!("Systematized version of {} (no LLM call).", a.name),
                        "code": script_template(a, st),
                    })),
                    title: format!("Specialize {} with a script", a.name),
                    rationale: format!(
                        "{}. The script takes priority; add a `when` guard to keep the LLM for atypical cases.",
                        f.evidence
                    ),
                });
            }
            FIND_LLM_HEAVY => {
                let Some(a) = a else { continue };
                if a.kind != KIND_LLM
                    || a.model == "fast"
                    || !done.insert(format!("model:{}", a.name))
                {
                    continue;
                }
                out.push(Proposal {
                    finding: i,
                    op: "update_node".into(),
                    key: element_key(&m.name, &m.version, "action", &a.name),
                    props: props(json!({"model": "fast"})),
                    title: format!("Fast model for {}", a.name),
                    rationale: format!(
                        "{}. A lighter model (fast alias) reduces cost and latency; check the quality of its outputs.",
                        f.evidence
                    ),
                    ..Default::default()
                });
            }
            FIND_LOOP | FIND_FAILURE => {
                let Some(a) = a else { continue };
                if !done.insert(format!("cost:{}", a.name)) {
                    continue;
                }
                let cost = if a.cost < 1.0 { 1.0 } else { a.cost };
                let mut p = Map::new();
                p.insert("cost".into(), number(cost * 2.0));
                out.push(Proposal {
                    finding: i,
                    op: "update_node".into(),
                    key: element_key(&m.name, &m.version, "action", &a.name),
                    props: p,
                    title: format!("Raise the cost of {}", a.name),
                    rationale: format!(
                        "{}. Doubling its cost steers the planner toward other paths; also review its preconditions / effects.",
                        f.evidence
                    ),
                    ..Default::default()
                });
            }
            FIND_DISABLED => {
                let ag = agent_of(m, &f.agent);
                let (Some(a), Some(ag)) = (a, ag) else {
                    continue;
                };
                if !done.insert(format!("agent:{}:{}", ag.name, a.name)) {
                    continue;
                }
                let keep: Vec<String> = admissible(m, ag)
                    .into_iter()
                    .filter(|x| *x != a.name)
                    .collect();
                let mut p = Map::new();
                // A Go nil slice encodes as null.
                p.insert(
                    "actions".into(),
                    if keep.is_empty() {
                        Value::Null
                    } else {
                        json!(keep)
                    },
                );
                out.push(Proposal {
                    finding: i,
                    op: "update_node".into(),
                    key: element_key(&m.name, &m.version, "agent", &ag.name),
                    props: p,
                    title: format!("Remove {} from agent {}", a.name, ag.name),
                    rationale: format!(
                        "{}. The action does not produce its effects for this agent: removing it avoids wasted cycles.",
                        f.evidence
                    ),
                    ..Default::default()
                });
            }
            FIND_SLOW_SPAN | FIND_SLOW_ACTION => {
                let tool = if f.action.is_empty() {
                    f.span.as_str()
                } else {
                    f.action.as_str()
                };
                let name = tool_name(tool);
                if !done.insert(format!("tool:{name}")) {
                    continue;
                }
                out.push(Proposal {
                    finding: i,
                    op: "create_node".into(),
                    r#type: TYPE_TOOL_REQUEST.into(),
                    key: element_key(&m.name, &m.version, "tool", &name),
                    props: props(json!({
                        "name": name, "description": format!("Dedicated MCP tool for {tool}"),
                        "motivation": f.evidence, "span": f.span,
                    })),
                    title: format!("MCP tool {name}"),
                    rationale: format!(
                        "{}. A dedicated MCP tool (caching, batch processing) would take this hot spot off the critical path.",
                        f.evidence
                    ),
                });
            }
            FIND_REPLANNING => notes.push(f.evidence.clone()),
            _ => {}
        }
    }
    (out, notes)
}

fn agent_of<'a>(m: &'a Methodology, name: &str) -> Option<&'a Agent> {
    m.agents.iter().find(|a| a.name == name)
}

fn admissible(m: &Methodology, ag: &Agent) -> Vec<String> {
    if !ag.actions.is_empty() {
        return ag.actions.clone();
    }
    m.actions
        .iter()
        .filter(|a| !a.is_specialization())
        .map(|a| a.name.clone())
        .collect()
}

fn tool_name(s: &str) -> String {
    let b: String = s
        .trim()
        .to_lowercase()
        .chars()
        .map(|r| {
            if r.is_ascii_lowercase() || r.is_ascii_digit() {
                r
            } else {
                '_'
            }
        })
        .collect();
    b.trim_matches('_').to_string()
}

/// script_template is the starting point of a systematized action: the shape of what the LLM produced, to be
/// completed by the methodologist.
fn script_template(a: &Action, s: &ActionStats) -> String {
    let outputs: Vec<&str> = s.outputs.keys().map(String::as_str).collect();
    let mut b = String::new();
    b.push_str(&format!(
        "// Systematized version of {} (proposed from execution observation).\n",
        a.name
    ));
    b.push_str(&format!(
        "// Observed LLM outputs: {}\n",
        outputs.join(", ")
    ));
    if !a.prompt.is_empty() {
        b.push_str("// Original instructions:\n");
        for l in a.prompt.trim().split('\n') {
            b.push_str(&format!("//   {l}\n"));
        }
    }
    b.push_str("function run(ctx) {\n");
    for k in outputs {
        if k == "changeImpact/modified" {
            b.push_str("for (const n of ctx.nodes(\"\")) {\n  // TODO rule to select the impacted nodes\n  // ctx.impactNode(n.key, \"rule\");\n}\n");
        } else if k == "changeImpact/created" {
            b.push_str("for (const n of ctx.changeImpacts()) {\n  // TODO nodes to create\n  // const c = ctx.impactNodeCreate(\"Type\", \"KEY\", \"why\");\n  // ctx.writeNode(c, { props: {}, links: [{ type: \"type\", to: n.key }] });\n}\n");
        } else if k.starts_with("artifact") {
            let target = k.strip_prefix("artifact/").unwrap_or(k);
            b.push_str(&format!(
                "ctx.addArtifact(\"{target}\", {{ /* TODO */ }});\n"
            ));
        }
    }
    b.push_str("}\n");
    b
}

/// Edit is an accepted change of the methodology model: a node update, creation or deletion on the key of a
/// definition node.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct Edit {
    /// create_node | update_node | delete_node
    pub op: String,
    pub key: String,
    pub r#type: String,
    #[serde(deserialize_with = "null_default")]
    pub props: Map<String, Value>,
}

/// Draft is the result of applying edits to a methodology.
#[derive(Debug, Clone, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Draft {
    #[serde(skip)]
    pub methodology: Methodology,
    #[serde(serialize_with = "null_if_empty")]
    pub applied: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub skipped: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub tool_requests: Vec<Map<String, Value>>,
}

/// Apply returns a copy of m with the edits applied (unknown elements and edits on other methodologies are skipped).
pub fn apply(m: &Methodology, edits: &[Edit]) -> Result<Draft, Error> {
    let mut d = Draft::default();
    let mut cp: Methodology = remarshal(m)?;
    for e in edits {
        let Some((_, kind, name)) = parse_key(&e.key).filter(|(meth, _, _)| *meth == m.name) else {
            d.skipped.push(format!(
                "{}: other methodology or node outside the model",
                e.key
            ));
            continue;
        };
        if e.op == "create_node" && e.r#type == TYPE_TOOL_REQUEST {
            let mut tr = e.props.clone();
            tr.insert("key".into(), Value::String(e.key.clone()));
            d.tool_requests.push(tr);
            d.applied.push(format!("MCP tool request {name}"));
            continue;
        }
        let res: Result<(), String> = match kind {
            "action" => edit(&mut cp.actions, e, name, |a| &a.name).map(|()| {
                if e.op == "delete_node" {
                    for ag in &mut cp.agents {
                        ag.actions.retain(|x| x != name);
                    }
                }
            }),
            "agent" => edit(&mut cp.agents, e, name, |a| &a.name),
            "goal" => edit(&mut cp.goals, e, name, |g| &g.name),
            "condition" => edit(&mut cp.conditions, e, name, |c| &c.name),
            "methodology" => match e.props.get("description").and_then(Value::as_str) {
                Some(desc) if e.op == "update_node" => {
                    cp.description = desc.to_string();
                    Ok(())
                }
                _ => Err("only the description of a methodology can be edited here".into()),
            },
            _ => Err(format!("unsupported element kind {}", quote(kind))),
        };
        if let Err(err) = res {
            d.skipped.push(format!("{}: {err}", e.key));
            continue;
        }
        let op = e.op.strip_suffix("_node").unwrap_or(&e.op);
        d.applied.push(format!("{op} {kind} {name}"));
    }
    d.methodology = cp;
    Ok(d)
}

/// parse_key reads the key of a definition node (see element_key): the methodology, the kind and the name of the
/// element.
fn parse_key(key: &str) -> Option<(&str, &str, &str)> {
    let rest = key.strip_prefix("MV:")?;
    let parts: Vec<&str> = rest.splitn(3, '/').collect();
    let meth = parts[0].split_once('@').map_or(parts[0], |(m, _)| m);
    match parts.len() {
        1 => Some((meth, "methodology", meth)),
        3 => Some((meth, parts[1], parts[2])),
        _ => None,
    }
}

/// edit applies one edit to a list of definitions (JSON patch semantics: a null property removes the field).
fn edit<T, F>(list: &mut Vec<T>, e: &Edit, name: &str, name_of: F) -> Result<(), String>
where
    T: Serialize + DeserializeOwned,
    F: Fn(&T) -> &String,
{
    let i = list.iter().position(|x| name_of(x) == name);
    match e.op.as_str() {
        "delete_node" => {
            let i = i.ok_or("unknown element")?;
            list.remove(i);
            Ok(())
        }
        "create_node" => {
            if i.is_some() {
                return Err("already exists".into());
            }
            let mut props = e.props.clone();
            props
                .entry("name")
                .or_insert_with(|| Value::String(name.to_string()));
            let x: T = remarshal(&props).map_err(|e| e.to_string())?;
            list.push(x);
            Ok(())
        }
        "update_node" => {
            let i = i.ok_or("unknown element")?;
            let mut cur: BTreeMap<String, Value> =
                remarshal(&list[i]).map_err(|e| e.to_string())?;
            for (k, v) in &e.props {
                if v.is_null() {
                    cur.remove(k);
                } else {
                    cur.insert(k.clone(), v.clone());
                }
            }
            list[i] = remarshal(&cur).map_err(|e| e.to_string())?;
            Ok(())
        }
        op => Err(format!("unsupported op {op}")),
    }
}

fn remarshal<F: Serialize + ?Sized, T: DeserializeOwned>(from: &F) -> Result<T, Error> {
    Ok(serde_json::from_value(serde_json::to_value(from)?)?)
}

/// NextVersion bumps the patch number of a semantic version ("1.2.3" → "1.2.4"; other versions get ".1"), skipping
/// taken versions.
pub fn next_version(v: &str, taken: impl Fn(&str) -> bool) -> String {
    let next = |v: &str| -> String {
        let mut parts: Vec<String> = v.split('.').map(String::from).collect();
        if let (Ok(n), 3) = (parts[parts.len() - 1].parse::<i64>(), parts.len()) {
            let last = parts.len() - 1;
            parts[last] = (n + 1).to_string();
            return parts.join(".");
        }
        format!("{v}.1")
    };
    let mut out = next(v);
    let mut i = 0;
    while i < 1000 && taken(&out) {
        out = next(&out);
        i += 1;
    }
    out
}
