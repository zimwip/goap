//! The goap-change connector (`change.go`).

use std::collections::{BTreeSet, HashMap};
use std::sync::Arc;

use goap_access as access;
use goap_authz::{self as authz, Principal};
use goap_connectorkit::{Connector, InvokeError};
use goap_ctx::Ctx;
use goap_decision as decision;
use goap_domain::{
    self as domain, Blackboard, ChangeID, ChangeImpact, ChangeImpactID, ChangeItem, ChangeStatus,
    Flow, ItemID, Node, NodeRef,
};
use goap_gen::connector::v1::ConnectorInfo;
use goap_graphapi as graphapi;
use goap_mcpbuiltin as mcpbuiltin;
use goap_risk as risk;
use serde_json::{Map, Value};

use crate::builtin::{
    Args, Error, Op, Ports, boxed, caller, info, json, nullable, obj, op, schema, unknown,
};

/// Change is the goap-change connector: it works on a change, the blackboard every modification of the graph goes
/// through (CLAUDE.md, rule 2). A node is declared on the change (a change impact, ADR 0024) the first time a tool
/// touches it, then written on the change branch; nothing lands on main until the change is applied, which stays a
/// decision of the methodology or of a reviewer. Without a change argument the tools work on the change of the
/// calling process.
#[derive(Clone)]
pub struct Change {
    pub(crate) p: Ports,
}

const S: &str = "string";

fn change_ops() -> Vec<Op> {
    vec![
        op(
            "create",
            "Open a change on the head of main of a namespace: {change}",
            schema(
                &[
                    ("title", S),
                    ("intent", S),
                    ("namespace", S),
                    ("methodology", S),
                    ("unit", S),
                    ("project", S),
                ],
                &["title", "intent", "namespace"],
            ),
        ),
        op(
            "move",
            "Move a root change, draft or active, with its sub-changes, to another project: {change}",
            schema(&[("change", S), ("project", S)], &["project"]),
        ),
        op(
            "attach",
            "Attach a root change to a change whose methodology integrates its own, as its sub-change: {change}",
            schema(&[("change", S), ("parent", S)], &["parent"]),
        ),
        op(
            "detach",
            "Detach a sub-change from the change integrating it, a root change again: {change}",
            schema(&[("change", S)], &[]),
        ),
        op(
            "depend",
            "Add or remove changes this change depends on: {change, add: [ids], remove: [ids]}",
            schema(&[("change", S), ("add", "array"), ("remove", "array")], &[]),
        ),
        op(
            "upgrade",
            "Move an open change to another version of its methodology (default: the latest): {change}",
            schema(&[("change", S), ("version", S)], &[]),
        ),
        op(
            "dependencies",
            "Read the dependencies of a change, its dependents and the merge order of its sub-changes: {change}",
            schema(&[("change", S)], &[]),
        ),
        op(
            "read",
            "Read a change: {change, nodes, items}",
            schema(&[("change", S)], &[]),
        ),
        op(
            "list",
            "List changes, the latest first: {changes, truncated}",
            schema(
                &[
                    ("namespace", S),
                    ("unit", S),
                    ("status", S),
                    ("limit", "integer"),
                ],
                &[],
            ),
        ),
        op(
            "reformulate",
            "Revise the title/intent of a change, superseding the previous definition (history kept): {change, item}",
            schema(
                &[("change", S), ("title", S), ("intent", S), ("rationale", S)],
                &["intent", "rationale"],
            ),
        ),
        op(
            "write",
            "Create a node in the change: {node}",
            schema(
                &[
                    ("change", S),
                    ("key", S),
                    ("type", S),
                    ("properties", "object"),
                    ("rationale", S),
                ],
                &["key", "type", "rationale"],
            ),
        ),
        op(
            "edit",
            "Modify the properties of a node in the change (its draft, the node being checked out on the first edit), or move it to a lifecycle state (a transition of the draft, the node checked out first when it has none): {node}",
            schema(
                &[
                    ("change", S),
                    ("key", S),
                    ("properties", "object"),
                    ("expect", "object"),
                    ("state", S),
                    ("rationale", S),
                ],
                &["key", "rationale"],
            ),
        ),
        op(
            "link",
            "Add a link from a node of the change: {node}",
            schema(
                &[
                    ("change", S),
                    ("from", S),
                    ("type", S),
                    ("to", S),
                    ("rationale", S),
                ],
                &["from", "type", "to"],
            ),
        ),
        op(
            "unlink",
            "Remove a link from a node of the change (removing a child is a modification of its parent): {node}",
            schema(
                &[
                    ("change", S),
                    ("from", S),
                    ("type", S),
                    ("to", S),
                    ("rationale", S),
                ],
                &["from", "type", "to"],
            ),
        ),
        op(
            "merge",
            "Merge nodes into a new one, seen from their parents (the sources stay as they are): {successors, sources, parents, suspect}",
            schema(
                &[
                    ("change", S),
                    ("sources", "array"),
                    ("key", S),
                    ("type", S),
                    ("properties", "object"),
                    ("rationale", S),
                ],
                &["sources", "key", "type", "rationale"],
            ),
        ),
        op(
            "split",
            "Split a node into new ones, seen from its parents (the node stays as it is): {successors, sources, parents, suspect}",
            schema(
                &[("change", S), ("source", S), ("into", "array")],
                &["source", "into"],
            ),
        ),
        op(
            "cancel",
            "Cancel the checkout of a node of the change: its draft is dropped (a node created by the change goes away): {node}",
            schema(&[("change", S), ("key", S)], &["key"]),
        ),
        op(
            "remove",
            "Take a node out of the change (its draft is dropped), or several at once with keys (all or none): {removed}",
            schema(&[("change", S), ("key", S), ("keys", "array")], &[]),
        ),
        op(
            "note",
            "Add an artifact item to the blackboard (target: what the artifact is, default note): {item}",
            schema(
                &[
                    ("change", S),
                    ("target", S),
                    ("text", S),
                    ("data", "object"),
                ],
                &["text"],
            ),
        ),
        op(
            "signal",
            "Emit a named notification other agents or a live parent may react to (target: the signal's name, recipient: a process id, empty for all): {item}",
            schema(
                &[
                    ("change", S),
                    ("target", S),
                    ("data", "object"),
                    ("recipient", S),
                ],
                &["target"],
            ),
        ),
        op(
            "validate",
            "Check the consistency of the change: {issues}",
            schema(&[("change", S)], &[]),
        ),
        op(
            "options",
            "List the options of the change and the active one: {active, options}",
            schema(&[("change", S)], &[]),
        ),
        op(
            "option",
            "Open an option of the change, a hypothesis explored on its own flow: {option}",
            schema(
                &[
                    ("change", S),
                    ("name", S),
                    ("hypothesis", S),
                    ("activate", "boolean"),
                ],
                &["name", "hypothesis"],
            ),
        ),
        op(
            "activate",
            "Work on an option of the change (\"main\": the main flow): {active}",
            schema(&[("change", S), ("option", S)], &["option"]),
        ),
        op(
            "evaluate",
            "Record the evaluation of an option: {option}",
            schema(
                &[("change", S), ("option", S), ("comment", S)],
                &["option", "comment"],
            ),
        ),
        op(
            "compare",
            "Compare the options of the change on the nodes they changed: {level, options, nodes}",
            schema(&[("change", S), ("level", S), ("all", "boolean")], &[]),
        ),
        op(
            "decisions",
            "List the decision points of the change and their questions: {points}",
            schema(&[("change", S)], &[]),
        ),
        op(
            "decision",
            "Open a decision point on a question, among the open options by default: {point}",
            schema(
                &[
                    ("change", S),
                    ("question", S),
                    ("options", "array"),
                    ("criteria", "array"),
                    ("decider", S),
                    ("threshold", "number"),
                    ("maxRounds", "integer"),
                    ("maxDuration", S),
                ],
                &["question"],
            ),
        ),
        op(
            "rule",
            "Rule a decision point: decided (option, confidence, justification) or undecidable (justification, questions): {point}",
            schema(
                &[
                    ("change", S),
                    ("point", S),
                    ("outcome", S),
                    ("option", S),
                    ("confidence", "number"),
                    ("justification", S),
                    ("questions", "array"),
                ],
                &["outcome", "justification"],
            ),
        ),
        op(
            "answer",
            "Answer an open question of a decision point: {point}",
            schema(
                &[("change", S), ("question", S), ("answer", S)],
                &["question", "answer"],
            ),
        ),
        op(
            "brief",
            "The change in brief, one line per fact (intent, impacts, decisions, risks, actions, latest artifacts): {brief}",
            schema(&[("change", S)], &[]),
        ),
        op(
            "trace",
            "Follow an information through the change — an item id, a risk or action key, or a node key: what produced it, what it derives from, what it led to, what replaced it: {trace}",
            schema(&[("change", S), ("ref", S)], &["ref"]),
        ),
        op(
            "risks",
            "The risk register and the actions of the change: {risks, actions}",
            schema(&[("change", S)], &[]),
        ),
        op(
            "risk",
            "Raise a risk, or update one by its key (a new version keeps what it does not restate); probability and impact 1-5, status open|mitigating|accepted|occurred|closed: {risk}",
            schema(
                &[
                    ("change", S),
                    ("key", S),
                    ("title", S),
                    ("description", S),
                    ("probability", "integer"),
                    ("impact", "integer"),
                    ("status", S),
                    ("owner", S),
                    ("actions", "array"),
                    ("rationale", S),
                ],
                &[],
            ),
        ),
        op(
            "derogations",
            "The derogations of the change (rule, target, reason, signatory, expires, status): {derogations}",
            schema(&[("change", S)], &[]),
        ),
        op(
            "derogation",
            "Sign a derogation, or update / close one by its key (needs derogation:sign; the signatory is the caller); expires is a date or RFC 3339, status open|closed: {derogation}",
            schema(
                &[
                    ("change", S),
                    ("key", S),
                    ("rule", S),
                    ("target", S),
                    ("reason", S),
                    ("expires", S),
                    ("status", S),
                ],
                &[],
            ),
        ),
        op(
            "reviews",
            "The reviews of the change (comment, status open|submitted|discarded, entries: impact, comment, outcome accept|reject) and the impacts that await review: {reviews, awaiting}",
            schema(&[("change", S), ("flow", S)], &[]),
        ),
        op(
            "review_open",
            "Open a review: a global comment and, optionally, the impacts (ids or node keys) it takes: {review}",
            schema(
                &[
                    ("change", S),
                    ("flow", S),
                    ("comment", S),
                    ("impacts", "array"),
                ],
                &[],
            ),
        ),
        op(
            "review_update",
            "Change an open review (its author or an administrator): the comment, impacts to add / remove, entries [{impact, comment, outcome accept|reject}]: {review}",
            schema(
                &[
                    ("change", S),
                    ("review", S),
                    ("comment", S),
                    ("add", "array"),
                    ("remove", "array"),
                    ("entries", "array"),
                ],
                &["review"],
            ),
        ),
        op(
            "review_submit",
            "Submit an open review: every entry is reviewed with its comment and the global one, all or none; a refusal names the entry and leaves the review open: {review}",
            schema(&[("change", S), ("review", S)], &["review"]),
        ),
        op(
            "review_discard",
            "Discard an open review that was never submitted: {review}",
            schema(&[("change", S), ("review", S)], &["review"]),
        ),
        op(
            "action",
            "Create an action, or update one by its key; status open|done|cancelled, for: the risk key or decision point it answers: {action}",
            schema(
                &[
                    ("change", S),
                    ("key", S),
                    ("title", S),
                    ("status", S),
                    ("owner", S),
                    ("due", S),
                    ("for", S),
                    ("result", S),
                ],
                &[],
            ),
        ),
    ]
}

/// changeID is the change a call names, else the change of the calling process.
pub(crate) fn change_id(ctx: &Ctx, a: Args<'_>) -> Result<ChangeID, Error> {
    let id = a.str("change");
    if !id.is_empty() {
        return Ok(ChangeID::from(id));
    }
    let id = goap_mcp::call_from(ctx).change;
    if !id.is_empty() {
        return Ok(ChangeID::from(id));
    }
    Err(Error::msg(
        r#"argument "change" is required outside a process"#,
    ))
}

/// The first item the graph wrote.
fn first(items: Vec<ChangeItem>) -> Result<ChangeItem, Error> {
    items
        .into_iter()
        .next()
        .ok_or_else(|| Error::msg("the change wrote no item"))
}

/// intentItemType is the item type reformulate uses to keep the history of a change's definition: each revision
/// supersedes the previous one (never overwritten), the header (title, intent) always holding the current one.
pub const INTENT_ITEM_TYPE: &str = "intent";

/// latestIntentItem returns the current "intent" item of a change, if any has been recorded yet (none: the change is
/// still on the definition it was created with).
pub fn latest_intent_item(c: &domain::Change) -> Option<&ChangeItem> {
    c.items
        .iter()
        .rev()
        .find(|it| it.r#type == domain::TYPE_ARTIFACT && it.target == INTENT_ITEM_TYPE)
}

/// producer names what produced an item or a write: the calling process when there is one.
pub(crate) fn producer(ctx: &Ctx) -> String {
    let p = goap_mcp::call_from(ctx).process;
    if !p.is_empty() {
        return p;
    }
    mcpbuiltin::CHANGE.into()
}

pub(crate) fn change_summary(c: &domain::Change) -> Value {
    Value::Object(obj([
        ("id", json(&c.id)),
        ("title", json(&c.title)),
        ("intent", json(&c.intent)),
        ("methodology", json(&c.methodology)),
        ("methodologyVersion", json(&c.methodology_version)),
        ("namespace", json(&c.namespace)),
        ("unit", json(&c.owner_org)),
        ("status", json(&c.status)),
        ("baselineId", json(&c.baseline_id)),
        ("branch", json(&c.branch)),
        ("parentId", json(&c.parent_id)),
        ("project", json(&c.project_id)),
        ("guardians", nullable(&c.guardians)),
        ("dependsOn", nullable(&c.depends_on)),
    ]))
}

pub(crate) fn as_strings(v: Option<&Value>) -> Vec<String> {
    match v {
        Some(Value::Array(l)) => l
            .iter()
            .filter_map(|x| x.as_str().map(|s| s.trim().to_string()))
            .collect(),
        _ => Vec::new(),
    }
}

fn node_result(out: &ChangeImpact) -> Result<Map<String, Value>, Error> {
    Ok(obj([(
        "node",
        Value::Object(obj([
            ("key", json(&out.key)),
            ("type", json(&out.r#type)),
            ("intent", json(&out.intent)),
            ("review", json(&out.review)),
            ("post", json(&out.post)),
        ])),
    )]))
}

/// Go's `%v` of a JSON value, for messages.
fn go_v(v: Option<&Value>) -> String {
    match v {
        None | Some(Value::Null) => "<nil>".into(),
        Some(Value::String(s)) => s.clone(),
        Some(v) => v.to_string(),
    }
}

/// Go's `fmt.Sprint` of a JSON value.
fn sprint(v: &Value) -> String {
    match v {
        Value::Null => "<nil>".into(),
        Value::String(s) => s.clone(),
        Value::Number(n) => n.as_f64().map_or_else(|| n.to_string(), |f| f.to_string()),
        v => v.to_string(),
    }
}

/// `fmt.Sscanf(s, prefix+"%d", &i)`: the number after the prefix (leading spaces and a sign allowed).
fn scan_num(s: &str, prefix: &str) -> Option<i64> {
    let rest = s.strip_prefix(prefix)?.trim_start();
    let (sign, digits) = match rest.as_bytes().first() {
        Some(b'+') => (1, &rest[1..]),
        Some(b'-') => (-1, &rest[1..]),
        _ => (1, rest),
    };
    let end = digits
        .find(|c: char| !c.is_ascii_digit())
        .unwrap_or(digits.len());
    digits[..end].parse::<i64>().ok().map(|n| sign * n)
}

/// Why a caller may not change the access nodes, for the gate a merge or a split hands the graph.
#[derive(Clone)]
enum GateRefusal {
    NoAuthorizer,
    Forbidden(String),
    Message(String),
}

impl GateRefusal {
    fn error(&self, typ: &str) -> graphapi::Error {
        match self {
            GateRefusal::NoAuthorizer => graphapi::Error::other(authz::Error::Forbidden(format!(
                "{typ} nodes cannot be changed without an authorizer"
            ))),
            GateRefusal::Forbidden(d) => graphapi::Error::other(authz::Error::Forbidden(d.clone())),
            GateRefusal::Message(m) => graphapi::Error::msg(m.clone()),
        }
    }
}

impl Change {
    pub fn new(p: Ports) -> Self {
        Change { p }
    }

    /// Invoke without the boxing of the connector's error: the operation's result or its typed error.
    pub async fn call(
        &self,
        ctx: &Ctx,
        op: &str,
        raw: &Map<String, Value>,
    ) -> Result<Map<String, Value>, Error> {
        let who = caller(ctx)?;
        let a = Args(raw);
        let g = self.p.graph()?;
        match op {
            "create" => return self.create(ctx, &who, a).await,
            "list" => return self.list(ctx, a).await,
            _ => {}
        }
        let id = change_id(ctx, a)?;
        match op {
            "move" => {
                let moved = g.move_change(ctx, &id, &a.str("project")).await?;
                return Ok(obj([("change", change_summary(&moved))]));
            }
            "attach" => {
                let attached = g
                    .attach_change(ctx, &id, &ChangeID::from(a.str("parent")))
                    .await?;
                return Ok(obj([("change", change_summary(&attached))]));
            }
            "detach" => {
                let detached = g.detach_change(ctx, &id).await?;
                return Ok(obj([("change", change_summary(&detached))]));
            }
            "depend" => {
                let cur = g.change(ctx, &id).await?;
                let mut deps = cur.depends_on.clone();
                for d in as_strings(a.get("add")) {
                    let d = ChangeID::from(d);
                    if !d.is_empty() && !deps.contains(&d) {
                        deps.push(d);
                    }
                }
                for d in as_strings(a.get("remove")) {
                    deps.retain(|x| x.as_str() != d);
                }
                let set = g.set_change_dependencies(ctx, &id, deps).await?;
                return Ok(obj([("change", change_summary(&set))]));
            }
            "upgrade" => {
                let up = g.upgrade_change(ctx, &id, &a.str("version")).await?;
                return Ok(obj([("change", change_summary(&up))]));
            }
            "dependencies" => {
                let d = g.change_dependencies(ctx, &id).await?;
                return Ok(obj([
                    ("dependsOn", nullable(&d.depends_on)),
                    ("dependents", nullable(&d.dependents)),
                    ("mergeOrder", nullable(&d.order)),
                ]));
            }
            "options" | "option" | "activate" | "evaluate" | "compare" => {
                return self.options(ctx, &who, &id, op, a).await;
            }
            "decisions" | "decision" | "rule" | "answer" => {
                return self.decisions(ctx, &who, &id, op, a).await;
            }
            _ => {}
        }
        let bb = g.blackboard(ctx, &id).await?;
        let mut w = Working {
            c: self,
            ctx,
            who: &who,
            bb,
            base_nodes: None,
        };
        match op {
            "read" => Ok(obj([
                ("change", change_summary(&w.bb.change)),
                ("nodes", Value::Array(w.impacts())),
                ("items", nullable(&w.bb.change.items)),
            ])),
            "reformulate" => self.reformulate(ctx, &w.bb, a).await,
            "validate" => {
                let issues = g.validate_board(ctx, &id, "").await?;
                Ok(obj([("issues", nullable(&issues))]))
            }
            "brief" => Ok(obj([("brief", json(goap_brief::of(&w.bb, None)))])),
            "trace" => {
                let r = a.required("ref")?;
                let t = goap_brief::trace(&w.bb.change, &r)?;
                Ok(obj([("trace", json(t))]))
            }
            "risks" => Ok(obj([
                ("risks", nullable(&risk::risks(&w.bb.change))),
                ("actions", nullable(&risk::actions(&w.bb.change))),
            ])),
            "risk" | "action" => self.record(ctx, &w.bb, op, a).await,
            "reviews" => self.reviews(ctx, &id, a).await,
            "review_open" | "review_update" | "review_submit" | "review_discard" => {
                self.review(ctx, &w.bb, op, a).await
            }
            "derogations" => Ok(obj([(
                "derogations",
                nullable(&risk::derogations(&w.bb.change)),
            )])),
            "derogation" => self.derogate(ctx, &w.bb, a).await,
            "note" => {
                let text = a.required("text")?;
                let mut typ = a.str("target");
                if typ.is_empty() {
                    typ = "note".into();
                }
                let mut data = a.object("data");
                data.insert("text".into(), Value::String(text));
                let items = g
                    .add_items(
                        ctx,
                        &id,
                        vec![ChangeItem {
                            r#type: domain::TYPE_ARTIFACT,
                            target: typ,
                            status: domain::ITEM_PROPOSED,
                            data,
                            produced_by: producer(ctx),
                            ..Default::default()
                        }],
                    )
                    .await?;
                Ok(obj([("item", json(first(items)?))]))
            }
            "signal" => {
                let typ = a.required("target")?;
                let items = g
                    .add_items(
                        ctx,
                        &id,
                        vec![ChangeItem {
                            r#type: domain::TYPE_SIGNAL,
                            target: typ,
                            status: domain::ITEM_PROPOSED,
                            data: a.object("data"),
                            recipient: a.str("recipient"),
                            produced_by: producer(ctx),
                            ..Default::default()
                        }],
                    )
                    .await?;
                Ok(obj([("item", json(first(items)?))]))
            }
            "write" => {
                let (key, typ) = (a.str("key"), a.str("type"));
                if key.is_empty() || typ.is_empty() {
                    return Err(Error::msg(r#"arguments "key" and "type" are required"#));
                }
                self.gate(ctx, &who, &typ).await?;
                if w.impact(&key).is_some() {
                    return Err(Error::msg(format!(
                        "{key} is already in the change: edit it"
                    )));
                }
                if w.base(&key).await?.is_some() {
                    return Err(Error::msg(format!(
                        "{key} exists in the baseline of the change: edit it"
                    )));
                }
                let out = g
                    .impact_node_create(
                        ctx,
                        &id,
                        graphapi::NodeCreate {
                            key,
                            r#type: typ,
                            properties: a.object("properties"),
                            rationale: a.str("rationale"),
                            produced_by: producer(ctx),
                            ..Default::default()
                        },
                    )
                    .await?;
                node_result(&out)
            }
            "edit" => {
                let imp = w.touch(&a.str("key"), &a.str("rationale")).await?;
                w.expect(&imp, &a.object("expect")).await?;
                let (props, state) = (a.object("properties"), a.str("state"));
                if props.is_empty() && state.is_empty() {
                    return Err(Error::msg(
                        r#"edit "properties", or a "state" to move the node to"#,
                    ));
                }
                if !props.is_empty() {
                    if !state.is_empty() {
                        return Err(Error::msg(
                            "edit the properties and move the node to a state in two calls",
                        ));
                    }
                    return w.edit(imp, props, Vec::new()).await;
                }
                let out = g
                    .impact_node_transition(
                        ctx,
                        &id,
                        graphapi::NodeTransition {
                            checkout: graphapi::NodeCheckout {
                                impact: imp.id.clone(),
                                ..Default::default()
                            },
                            to: state,
                        },
                    )
                    .await?;
                node_result(&out)
            }
            "cancel" => {
                let key = a.str("key");
                let Some(imp) = w.impact(&key) else {
                    return Err(Error::msg(format!("{key} is not in the change")));
                };
                let out = g.impact_node_cancel(ctx, &id, &imp.id, "", "").await?;
                node_result(&out)
            }
            "remove" => {
                let mut keys = as_strings(a.get("keys"));
                let k = a.str("key");
                if !k.is_empty() {
                    keys.insert(0, k);
                }
                if keys.is_empty() {
                    return Err(Error::msg(r#"argument "key" or "keys" is required"#));
                }
                let mut ids: Vec<ChangeImpactID> = Vec::with_capacity(keys.len());
                let mut removed: Vec<String> = Vec::with_capacity(keys.len());
                for k in &keys {
                    let Some(imp) = w.impact(k) else {
                        return Err(Error::msg(format!("{k} is not in the change")));
                    };
                    if ids.contains(&imp.id) {
                        continue;
                    }
                    ids.push(imp.id);
                    removed.push(imp.key);
                }
                if ids.len() == 1 {
                    g.withdraw_impact(ctx, &id, &ids[0], "", "").await?;
                    return Ok(obj([("removed", json(&removed[0]))]));
                }
                // several at once: all of them or none (a5a144d)
                g.withdraw_impacts(ctx, &id, ids, "", "").await?;
                Ok(obj([("removed", json(&removed))]))
            }
            "merge" | "split" => self.restructure(ctx, &w, op, a).await,
            "unlink" => {
                let (typ, to) = (a.str("type"), a.str("to"));
                if typ.is_empty() || to.is_empty() {
                    return Err(Error::msg(r#"arguments "type" and "to" are required"#));
                }
                let mut rationale = a.str("rationale");
                if rationale.is_empty() {
                    rationale = format!("unlink {typ} to {to}");
                }
                let imp = w.touch(&a.str("from"), &rationale).await?;
                let imp = w.checkout(imp).await?;
                let base = w.base(&to).await?;
                let mut target = base.map(|n| n.id);
                if let Some(t) = w.impact(&to)
                    && let Some(post) = &t.post
                {
                    target = Some(post.id.clone());
                }
                let Some(target) = target else {
                    return Err(Error::msg(format!("no node {to}")));
                };
                let bb = g.blackboard_in(ctx, &id, "").await?;
                let out = imp
                    .post
                    .as_ref()
                    .and_then(|p| bb.nodes.get(p))
                    .map(|n| n.out.clone())
                    .unwrap_or_default();
                for l in out {
                    if l.r#type == typ && l.to.id == target {
                        g.impact_link_delete(ctx, &id, &l.id, "", "").await?;
                        return node_result(&imp);
                    }
                }
                Err(Error::msg(format!("{} has no {typ} link to {to}", imp.key)))
            }
            "link" => {
                let (typ, to) = (a.str("type"), a.str("to"));
                if typ.is_empty() || to.is_empty() {
                    return Err(Error::msg(r#"arguments "type" and "to" are required"#));
                }
                let mut rationale = a.str("rationale");
                if rationale.is_empty() {
                    rationale = format!("link {typ} to {to}");
                }
                let imp = w.touch(&a.str("from"), &rationale).await?;
                let r = w.node_ref(&to).await?;
                w.edit(
                    imp,
                    Map::new(),
                    vec![graphapi::LinkWrite {
                        r#type: typ,
                        to: r,
                        ..Default::default()
                    }],
                )
                .await
            }
            _ => Err(unknown(op)),
        }
    }

    /// options works on the options of a change (ADR 0009 §3, ADR 0032 §6); selecting or rejecting one is a decision
    /// made through the graph service, not a tool.
    async fn options(
        &self,
        ctx: &Ctx,
        who: &Principal,
        id: &ChangeID,
        op: &str,
        a: Args<'_>,
    ) -> Result<Map<String, Value>, Error> {
        fn summary(f: &Flow) -> Value {
            let mut out = obj([
                ("id", json(&f.id)),
                ("status", json(f.option_status())),
                ("active", json(f.active)),
            ]);
            if let Some(o) = &f.option {
                out.insert("name".into(), json(&o.name));
                out.insert("hypothesis".into(), json(&o.hypothesis));
            }
            if !f.evaluation.is_empty() {
                out.insert("evaluation".into(), json(&f.evaluation));
            }
            Value::Object(out)
        }
        let g = self.p.graph()?;
        match op {
            "options" => {
                let os = g.options(ctx, id).await?;
                let mut active = domain::MAIN_FLOW.to_string();
                let mut list = Vec::new();
                for f in &os {
                    list.push(summary(f));
                    if f.active {
                        active = f.id.clone();
                    }
                }
                Ok(obj([
                    ("active", json(active)),
                    ("options", Value::Array(list)),
                ]))
            }
            "option" => {
                let name = a.required("name")?;
                let f = g
                    .open_option(
                        ctx,
                        id,
                        graphapi::OpenOptionRequest {
                            name,
                            hypothesis: a.str("hypothesis"),
                            activate: a.boolean("activate"),
                            by: who.subject.clone(),
                            ..Default::default()
                        },
                    )
                    .await?;
                Ok(obj([("option", summary(&f))]))
            }
            "activate" => {
                let mut active = g
                    .activate_option(ctx, id, &a.str("option"), &who.subject)
                    .await?;
                if active.is_empty() {
                    active = domain::MAIN_FLOW.into();
                }
                Ok(obj([("active", json(active))]))
            }
            "evaluate" => {
                let option = a.required("option")?;
                let f = g
                    .evaluate_option(ctx, id, &option, &who.subject, &a.str("comment"))
                    .await?;
                Ok(obj([("option", summary(&f))]))
            }
            _ => {
                let cmp = g
                    .compare_options(ctx, id, &a.str("level"), a.boolean("all"))
                    .await?;
                let opts: Vec<Value> = cmp.options.iter().map(summary).collect();
                Ok(obj([
                    ("level", json(&cmp.level)),
                    ("options", Value::Array(opts)),
                    ("nodes", nullable(&cmp.nodes)),
                ]))
            }
        }
    }

    /// decisions works on the decision points of a change (ADR 0009 §4). A ruling made through a tool is an agent's:
    /// the policy of its point may leave to a person's ratification, which is not a tool.
    async fn decisions(
        &self,
        ctx: &Ctx,
        who: &Principal,
        id: &ChangeID,
        op: &str,
        a: Args<'_>,
    ) -> Result<Map<String, Value>, Error> {
        let strs = |name: &str| -> Vec<String> {
            match a.get(name) {
                Some(Value::Array(l)) => l.iter().map(sprint).collect(),
                _ => Vec::new(),
            }
        };
        let num = |name: &str| a.number(name).unwrap_or(0.0);
        let g = self.p.graph()?;
        let d = match op {
            "decisions" => {
                let ps = g.decision_points(ctx, id).await?;
                return Ok(obj([("points", json(ps))]));
            }
            "decision" => {
                let mut input = graphapi::OpenDecisionRequest {
                    question: a.str("question"),
                    criteria: strs("criteria"),
                    by: who.subject.clone(),
                    ..Default::default()
                };
                if a.has("options") {
                    input.options = Some(strs("options"));
                }
                // the policy values the tool names are handed over as they come, the graph's policy reads them
                for k in [
                    decision::KEY_DECIDER,
                    decision::KEY_THRESHOLD,
                    decision::KEY_MAX_ROUNDS,
                    decision::KEY_MAX_DURATION,
                ] {
                    if let Some(v) = a.get(k) {
                        let empty = match v {
                            Value::String(s) => s.is_empty(),
                            Value::Number(n) => n.as_f64() == Some(0.0),
                            _ => false,
                        };
                        if !empty {
                            input.policy.insert(k.into(), v.clone());
                        }
                    }
                }
                g.open_decision(ctx, id, input).await?
            }
            "rule" => {
                g.rule_decision(
                    ctx,
                    id,
                    graphapi::RuleRequest {
                        point: a.str("point"),
                        outcome: a.str("outcome"),
                        option: a.str("option"),
                        confidence: num("confidence"),
                        justification: a.str("justification"),
                        questions: strs("questions"),
                        by: who.subject.clone(),
                        ..Default::default()
                    },
                )
                .await?
            }
            _ => {
                // answer
                g.answer_question(
                    ctx,
                    id,
                    &a.str("question"),
                    &a.str("answer"),
                    &goap_mcp::call_from(ctx).process,
                    &who.subject,
                )
                .await?
            }
        };
        Ok(obj([("point", json(d))]))
    }

    /// create opens a change: intent (why), unit (who), methodology (how) and namespace (what).
    async fn create(
        &self,
        ctx: &Ctx,
        who: &Principal,
        a: Args<'_>,
    ) -> Result<Map<String, Value>, Error> {
        let (title, intent, ns) = (a.str("title"), a.str("intent"), a.str("namespace"));
        if title.is_empty() || intent.is_empty() || ns.is_empty() {
            return Err(Error::msg(
                r#"arguments "title", "intent" and "namespace" are required"#,
            ));
        }
        let g = self.p.graph()?;
        let head = g
            .branch_head(ctx, &ns, domain::MAIN_BRANCH)
            .await
            .map_err(|e| e.context(format!("head of main of {ns}")))?;
        // the change acts in the project the call names, else the caller's active project, else the root (ADR 0091)
        let mut project = a.str("project");
        if project.is_empty() {
            project = who.project.clone();
        }
        if project.is_empty() {
            project = g.structures(ctx).await?.project().root;
        }
        let mut data = Map::new();
        data.insert("createdBy".into(), json(&who.subject));
        data.insert("via".into(), json(mcpbuiltin::CHANGE));
        let ch = g
            .create_change(
                ctx,
                graphapi::NewChange {
                    title,
                    intent,
                    methodology: a.str("methodology"),
                    namespace: ns,
                    baseline_id: head.id,
                    owner_org: a.unit(ctx, who),
                    project_id: project,
                    data,
                    ..Default::default()
                },
            )
            .await?;
        Ok(obj([("change", change_summary(&ch))]))
    }

    /// reformulate revises the definition (title, intent) of a change: the current one is superseded as an "intent"
    /// item, never erased, so the change keeps its full history; the header is then patched to the new definition,
    /// the one prompts and reads see.
    async fn reformulate(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        a: Args<'_>,
    ) -> Result<Map<String, Value>, Error> {
        let new_intent = a.required("intent")?;
        let rationale = a.required("rationale")?;
        let mut title = a.str("title");
        if title.is_empty() {
            title = bb.change.title.clone();
        }
        let id = &bb.change.id;
        let g = self.p.graph()?;
        let prev: ItemID = match latest_intent_item(&bb.change) {
            Some(it) => it.id.clone(),
            None => {
                // the definition set at creation has no item of its own yet: snapshot it first, so the history stays
                // complete in the items once the header is overwritten below.
                let snap = g
                    .add_items(
                        ctx,
                        id,
                        vec![ChangeItem {
                            r#type: domain::TYPE_ARTIFACT,
                            target: INTENT_ITEM_TYPE.into(),
                            status: domain::ITEM_ACCEPTED,
                            data: obj([
                                ("title", json(&bb.change.title)),
                                ("intent", json(&bb.change.intent)),
                            ]),
                            produced_by: "create".into(),
                            ..Default::default()
                        }],
                    )
                    .await?;
                first(snap)?.id
            }
        };
        let items = g
            .add_items(
                ctx,
                id,
                vec![ChangeItem {
                    r#type: domain::TYPE_ARTIFACT,
                    target: INTENT_ITEM_TYPE.into(),
                    status: domain::ITEM_ACCEPTED,
                    data: obj([
                        ("title", json(&title)),
                        ("intent", json(&new_intent)),
                        ("rationale", json(&rationale)),
                    ]),
                    produced_by: producer(ctx),
                    supersedes: vec![prev],
                    ..Default::default()
                }],
            )
            .await?;
        let ch = g
            .update_change(
                ctx,
                id,
                graphapi::ChangePatch {
                    title: Some(title),
                    intent: Some(new_intent),
                    ..Default::default()
                },
            )
            .await?;
        Ok(obj([
            ("change", change_summary(&ch)),
            ("item", json(first(items)?)),
        ]))
    }

    /// list lists changes matching a filter, the latest first: the caller checks whether a request continues one of
    /// them before opening a new one (goap-change.create).
    async fn list(&self, ctx: &Ctx, a: Args<'_>) -> Result<Map<String, Value>, Error> {
        let mut f = graphapi::ChangesFilter {
            namespace: a.str("namespace"),
            owner_org: a.str("unit"),
            ..Default::default()
        };
        let st = a.str("status");
        if !st.is_empty() {
            f.status = vec![ChangeStatus::from(st)];
        }
        let mut cs = self.p.graph()?.list_changes(ctx, f).await?;
        cs.sort_by_key(|c| std::cmp::Reverse(c.created_at));
        let truncated = cs.len() > a.limit();
        if truncated {
            cs.truncate(a.limit());
        }
        let list: Vec<Value> = cs.iter().map(change_summary).collect();
        Ok(obj([
            ("changes", Value::Array(list)),
            ("truncated", Value::Bool(truncated)),
        ]))
    }

    /// The authorizer of the access nodes: Floor, else Authz.
    fn floor(&self) -> Option<&dyn authz::Authorizer> {
        self.p.floor.as_deref().or(self.p.authz.as_deref())
    }

    fn write_access_request(who: &Principal) -> authz::Request {
        authz::Request {
            subject: who.clone(),
            action: "write".into(),
            resource: authz::Resource {
                r#type: access::RESOURCE_POLICY.into(),
                org: who.org.clone(),
                ..Default::default()
            },
        }
    }

    /// gate applies the access gate of the platform to the access nodes (`adminOnly` node types, ADR 0068 and 0020):
    /// in process the connector writes through the graph directly, so the handler's gate does not stand in front of
    /// it.
    pub(crate) async fn gate(&self, ctx: &Ctx, who: &Principal, typ: &str) -> Result<(), Error> {
        if !self.p.graph()?.admin_only_type(ctx, typ).await? {
            return Ok(());
        }
        let Some(gate) = self.floor() else {
            return Err(Error::Forbidden(format!(
                "{typ} nodes cannot be changed without an authorizer: {}",
                authz::ERR_FORBIDDEN
            )));
        };
        authz::check(ctx, Some(gate), &Self::write_access_request(who)).await?;
        Ok(())
    }

    /// The gate of a merge or a split, handed to the graph (graphapi::Gate is synchronous): whether the caller may
    /// write the access nodes is asked first, and when it may not, whether each type the change can see is admin-only;
    /// a type it cannot see is refused (Go asked both for each type the graph names, as it came).
    async fn sync_gate(&self, ctx: &Ctx, w: &Working<'_>) -> Result<graphapi::Gate, Error> {
        let refusal = match self.floor() {
            None => Some(GateRefusal::NoAuthorizer),
            Some(a) => match authz::check(ctx, Some(a), &Self::write_access_request(w.who)).await {
                Ok(()) => None,
                Err(authz::Error::Forbidden(d)) => Some(GateRefusal::Forbidden(d)),
                Err(e) => Some(GateRefusal::Message(e.to_string())),
            },
        };
        let Some(refusal) = refusal else {
            return Ok(Arc::new(|_: &str| Ok(())));
        };
        let g = self.p.graph()?;
        let mut types: BTreeSet<String> = BTreeSet::new();
        let (nodes, _) = g.change_graph(ctx, &w.bb.change.id, "").await?;
        types.extend(nodes.into_iter().map(|n| n.r#type));
        types.extend(w.bb.nodes.values().map(|n| n.r#type.clone()));
        types.extend(w.bb.change.nodes.iter().map(|n| n.r#type.clone()));
        let mut admin_only: HashMap<String, bool> = HashMap::new();
        for t in types {
            let a = g.admin_only_type(ctx, &t).await?;
            admin_only.insert(t, a);
        }
        Ok(Arc::new(move |typ: &str| match admin_only.get(typ) {
            Some(false) => Ok(()),
            _ => Err(refusal.error(typ)),
        }))
    }

    /// restructure merges or splits nodes from the side of their parents (ADR 0077): the sources are named by key, the
    /// graph finds their parents; the gate of the access nodes stands in front of every node the call modifies.
    async fn restructure(
        &self,
        ctx: &Ctx,
        w: &Working<'_>,
        op: &str,
        a: Args<'_>,
    ) -> Result<Map<String, Value>, Error> {
        let id = &w.bb.change.id;
        let gate = self.sync_gate(ctx, w).await?;
        let create = |m: &Map<String, Value>| -> graphapi::NodeCreate {
            let s = Args(m);
            graphapi::NodeCreate {
                key: s.str("key"),
                r#type: s.str("type"),
                properties: s.object("properties"),
                rationale: s.str("rationale"),
                produced_by: producer(ctx),
                ..Default::default()
            }
        };
        let g = self.p.graph()?;
        let out = if op == "merge" {
            let sources = as_strings(a.get("sources"))
                .into_iter()
                .map(|x| graphapi::NodeName {
                    key: x,
                    ..Default::default()
                })
                .collect();
            let into = obj([
                ("key", json(a.str("key"))),
                ("type", json(a.str("type"))),
                ("properties", Value::Object(a.object("properties"))),
                ("rationale", json(a.str("rationale"))),
            ]);
            g.impact_node_merge(
                ctx,
                id,
                graphapi::MergeInput {
                    sources,
                    gate: Some(gate),
                    into: create(&into),
                    ..Default::default()
                },
            )
            .await?
        } else {
            let into = match a.get("into") {
                Some(Value::Array(l)) => {
                    l.iter().filter_map(Value::as_object).map(create).collect()
                }
                _ => Vec::new(),
            };
            g.impact_node_split(
                ctx,
                id,
                graphapi::SplitInput {
                    source: graphapi::NodeName {
                        key: a.str("source"),
                        ..Default::default()
                    },
                    into,
                    gate: Some(gate),
                    rationale: a.str("rationale"),
                    ..Default::default()
                },
            )
            .await?
        };
        fn summary(list: &[ChangeImpact]) -> Value {
            if list.is_empty() {
                return Value::Null;
            }
            Value::Array(
                list.iter()
                    .map(|cn| {
                        Value::Object(obj([
                            ("key", json(&cn.key)),
                            ("type", json(&cn.r#type)),
                            ("intent", json(&cn.intent)),
                            ("review", json(&cn.review)),
                            ("post", json(&cn.post)),
                            ("via", json(&cn.via)),
                        ]))
                    })
                    .collect(),
            )
        }
        let suspect = if out.suspect.is_empty() {
            Value::Null
        } else {
            Value::Array(
                out.suspect
                    .iter()
                    .map(|s| {
                        // SuspectLink has no JSON tags in Go: its field names as they are
                        Value::Object(obj([
                            ("From", json(&s.from)),
                            ("FromKey", json(&s.from_key)),
                            ("Type", json(&s.r#type)),
                            ("To", json(&s.to)),
                            ("ToKey", json(&s.to_key)),
                        ]))
                    })
                    .collect(),
            )
        };
        Ok(obj([
            ("successors", summary(&out.successors)),
            ("sources", summary(&out.sources)),
            ("parents", summary(&out.parents)),
            ("suspect", suspect),
        ]))
    }

    /// record raises or updates a risk or an action of the change (ADR 0036 §1): a new version of the record of its
    /// key, keeping what it does not restate. Without a key, a new record gets the next free one (RSK-n, ACT-n).
    async fn record(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        op: &str,
        a: Args<'_>,
    ) -> Result<Map<String, Value>, Error> {
        let (kind, prefix, fields): (_, _, &[&str]) = if op == "action" {
            (
                risk::TYPE_ACTION,
                "ACT-",
                &["title", "status", "owner", "due", "for", "result"],
            )
        } else {
            (
                risk::TYPE_RISK,
                "RSK-",
                &["title", "description", "status", "owner"],
            )
        };
        let mut key = a.str("key");
        let mut existing = false;
        let mut n = 0;
        for it in &bb.change.items {
            if it.r#type != kind {
                continue;
            }
            let k = it.data.get("key").and_then(Value::as_str).unwrap_or("");
            existing = existing || (!key.is_empty() && k == key);
            if let Some(i) = scan_num(k, prefix)
                && i > n
            {
                n = i;
            }
        }
        if key.is_empty() {
            key = format!("{prefix}{}", n + 1);
        }
        let mut data = obj([("key", json(&key))]);
        for f in fields {
            let v = a.str(f);
            if !v.is_empty() {
                data.insert((*f).into(), json(v));
            }
        }
        if kind == risk::TYPE_RISK {
            for f in ["probability", "impact"] {
                if let Some(v) = a.get(f).filter(|v| v.is_number()) {
                    data.insert(f.into(), v.clone());
                }
            }
            if let Some(acts @ Value::Array(_)) = a.get("actions") {
                data.insert("actions".into(), acts.clone());
            }
            let r = a.str("rationale");
            if !r.is_empty() {
                data.insert("rationale".into(), json(r));
            }
        }
        if !existing && a.str("title").is_empty() {
            return Err(Error::msg(format!("a new {op} needs a title")));
        }
        if existing {
            // the title is required on every version: keep the current one
            if let Some(it) = bb.change.items.iter().rev().find(|it| {
                it.r#type == kind && it.data.get("key").and_then(Value::as_str) == Some(&key)
            }) && !data.contains_key("title")
            {
                data.insert(
                    "title".into(),
                    it.data.get("title").cloned().unwrap_or(Value::Null),
                );
            }
        }
        let items = self
            .p
            .graph()?
            .add_items(
                ctx,
                &bb.change.id,
                vec![ChangeItem {
                    r#type: kind,
                    target: op.into(),
                    status: domain::ITEM_PROPOSED,
                    data,
                    produced_by: producer(ctx),
                    ..Default::default()
                }],
            )
            .await?;
        let mut out = Map::new();
        out.insert(op.into(), json(first(items)?));
        Ok(out)
    }

    /// derogate signs a derogation of the change, or writes the next version of one (ADR 0075 §2): the signatory is
    /// the caller, the graph checks derogation:sign (Graph.ItemAuthorizer); a version restates what the previous one
    /// said.
    async fn derogate(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        a: Args<'_>,
    ) -> Result<Map<String, Value>, Error> {
        let mut key = a.str("key");
        let ds = risk::derogations(&bb.change);
        let n = ds
            .iter()
            .filter_map(|d| scan_num(&d.key, "DRG-"))
            .fold(0, i64::max);
        let mut data = Map::new();
        if key.is_empty() {
            key = format!("DRG-{}", n + 1);
        } else {
            for d in ds.iter().filter(|d| d.key == key) {
                data = obj([
                    ("rule", json(&d.rule)),
                    ("target", json(&d.target)),
                    ("reason", json(&d.reason)),
                    (
                        "expires",
                        json(d.expires.to_rfc3339_opts(chrono::SecondsFormat::Secs, true)),
                    ),
                    ("status", json(&d.status)),
                ]);
            }
        }
        data.insert("key".into(), json(&key));
        data.insert("signatory".into(), json(authz::principal_from(ctx).subject));
        for f in ["rule", "target", "reason", "expires", "status"] {
            let v = a.str(f);
            if !v.is_empty() {
                data.insert(f.into(), json(v));
            }
        }
        let items = self
            .p
            .graph()?
            .add_items(
                ctx,
                &bb.change.id,
                vec![ChangeItem {
                    r#type: risk::TYPE_DEROGATION,
                    target: "derogation".into(),
                    status: domain::ITEM_PROPOSED,
                    data,
                    produced_by: producer(ctx),
                    ..Default::default()
                }],
            )
            .await?;
        Ok(obj([("derogation", json(first(items)?))]))
    }
}

/// working is one call on the blackboard of a change.
pub(crate) struct Working<'a> {
    c: &'a Change,
    ctx: &'a Ctx,
    pub(crate) who: &'a Principal,
    pub(crate) bb: Blackboard,
    base_nodes: Option<Vec<Node>>,
}

impl Working<'_> {
    /// impact returns the change impact of a node as the flow the call works on sees it (the main flow, or the
    /// active option: the blackboard is the one of that flow).
    fn impact(&self, key: &str) -> Option<ChangeImpact> {
        self.bb
            .change
            .nodes
            .iter()
            .find(|n| n.key == key && !n.superseded)
            .cloned()
    }

    fn impacts(&self) -> Vec<Value> {
        let mut out = Vec::new();
        for n in &self.bb.change.nodes {
            if n.superseded {
                continue;
            }
            let mut v = obj([
                ("key", json(&n.key)),
                ("type", json(&n.r#type)),
                ("intent", json(&n.intent)),
                ("rationale", json(&n.rationale)),
                ("review", json(&n.review)),
                ("planned", json(n.planned())),
            ]);
            if !n.flow.is_empty() {
                v.insert("flow".into(), json(&n.flow));
            }
            if let Some(nv) = n.post.as_ref().and_then(|p| self.bb.nodes.get(p)) {
                v.insert("properties".into(), Value::Object(nv.properties.clone()));
                v.insert("state".into(), json(&nv.state));
                v.insert("retired".into(), json(nv.deleted));
            }
            out.push(Value::Object(v));
        }
        out
    }

    /// base returns a node of the reference baseline of the change.
    async fn base(&mut self, key: &str) -> Result<Option<Node>, Error> {
        if self.base_nodes.is_none() {
            let (nodes, _) = self
                .c
                .p
                .graph()?
                .baseline_graph(self.ctx, &self.bb.change.baseline_id)
                .await?;
            self.base_nodes = Some(nodes);
        }
        let ns = &self.bb.change.namespace;
        Ok(self.base_nodes.as_ref().and_then(|nodes| {
            nodes
                .iter()
                .find(|n| n.key == key && (ns.is_empty() || n.namespace == *ns) && !n.deleted)
                .cloned()
        }))
    }

    async fn declare(&mut self, mut imp: ChangeImpact) -> Result<ChangeImpact, Error> {
        if imp.rationale.is_empty() {
            return Err(Error::msg(format!("{}: a rationale is required", imp.key)));
        }
        imp.review = domain::REVIEW_PROPOSED;
        imp.produced_by = producer(self.ctx);
        let key = imp.key.clone();
        let out = self
            .c
            .p
            .graph()?
            .propose_impact(self.ctx, &self.bb.change.id, vec![imp])
            .await?;
        let Some(first) = out.into_iter().next() else {
            return Err(Error::msg(format!(
                "{key}: the change did not declare the node"
            )));
        };
        self.bb.change.nodes.push(first.clone());
        Ok(first)
    }

    /// touch returns the change impact of a node, declaring the modification of a node of the baseline the first time
    /// the change touches it.
    async fn touch(&mut self, key: &str, rationale: &str) -> Result<ChangeImpact, Error> {
        if key.is_empty() {
            return Err(Error::msg("the key of the node is required"));
        }
        if let Some(imp) = self.impact(key) {
            self.c.gate(self.ctx, self.who, &imp.r#type).await?;
            return Ok(imp);
        }
        let Some(n) = self.base(key).await? else {
            return Err(Error::msg(format!(
                "no node {key} in the change nor in its baseline"
            )));
        };
        self.c.gate(self.ctx, self.who, &n.r#type).await?;
        let pre = n.r#ref();
        self.declare(ChangeImpact {
            key: key.into(),
            r#type: n.r#type.clone(),
            intent: domain::INTENT_MODIFIED,
            rationale: rationale.into(),
            pre: Some(pre),
            ..Default::default()
        })
        .await
    }

    /// current returns the properties of the node as the change sees it.
    async fn current(&mut self, imp: &ChangeImpact) -> Option<Map<String, Value>> {
        for (r, is_pre) in [(&imp.post, false), (&imp.pre, true)] {
            let Some(r) = r else {
                continue;
            };
            if let Some(nv) = self.bb.nodes.get(r) {
                return Some(nv.properties.clone());
            }
            if is_pre && let Ok(Some(n)) = self.base(&imp.key).await {
                return Some(n.properties);
            }
        }
        None
    }

    /// expect checks the values an edit expects, so that an edit does not overwrite a concurrent one.
    async fn expect(&mut self, imp: &ChangeImpact, want: &Map<String, Value>) -> Result<(), Error> {
        if want.is_empty() {
            return Ok(());
        }
        let cur = self.current(imp).await.unwrap_or_default();
        for (k, v) in want {
            let got = cur.get(k);
            if !domain::json_equal(got.unwrap_or(&Value::Null), v) {
                return Err(Error::msg(format!(
                    "{}: {k} is {}, the edit expects {}",
                    imp.key,
                    go_v(got),
                    go_v(Some(v))
                )));
            }
        }
        Ok(())
    }

    /// ref returns the version a link points to: the one the change writes, else the one of the baseline.
    async fn node_ref(&mut self, key: &str) -> Result<NodeRef, Error> {
        if let Some(post) = self.impact(key).and_then(|imp| imp.post) {
            return Ok(post);
        }
        if let Some(n) = self.base(key).await? {
            return Ok(n.r#ref());
        }
        Err(Error::msg(format!(
            "no node {key} to link to in the change nor in its baseline"
        )))
    }

    /// checkout returns the change impact with a draft of its own on the flow the call works on, the node checked out
    /// when it has none (ADR 0076, 0079).
    async fn checkout(&self, imp: ChangeImpact) -> Result<ChangeImpact, Error> {
        if let Some(nv) = imp.post.as_ref().and_then(|p| self.bb.nodes.get(p))
            && graphapi::is_working(
                &self.bb.change,
                &domain::active_option_of(&self.bb),
                &nv.node,
            )
        {
            return Ok(imp);
        }
        Ok(self
            .c
            .p
            .graph()?
            .impact_node_checkout(
                self.ctx,
                &self.bb.change.id,
                graphapi::NodeCheckout {
                    impact: imp.id,
                    ..Default::default()
                },
            )
            .await?)
    }

    /// edit edits the draft of a change impact: its properties and outgoing links.
    async fn edit(
        &self,
        imp: ChangeImpact,
        props: Map<String, Value>,
        links: Vec<graphapi::LinkWrite>,
    ) -> Result<Map<String, Value>, Error> {
        let g = self.c.p.graph()?;
        let mut imp = self.checkout(imp).await?;
        if !props.is_empty() {
            imp = g
                .impact_node_update(
                    self.ctx,
                    &self.bb.change.id,
                    &imp.id,
                    graphapi::NodeUpdate {
                        properties: props,
                        ..Default::default()
                    },
                )
                .await?;
        }
        for l in links {
            g.impact_link_create(self.ctx, &self.bb.change.id, &imp.id, l, "", "")
                .await?;
        }
        node_result(&imp)
    }
}

#[async_trait::async_trait]
impl Connector for Change {
    fn info(&self) -> ConnectorInfo {
        info(
            mcpbuiltin::CHANGE,
            "Changes of the platform, the blackboard of every modification, worked on for the caller (built in).",
            &change_ops(),
        )
    }

    async fn invoke(
        &self,
        ctx: &Ctx,
        op: &str,
        args: Map<String, Value>,
        _config: Map<String, Value>,
        _secrets: HashMap<String, String>,
    ) -> Result<Map<String, Value>, InvokeError> {
        self.call(ctx, op, &args).await.map_err(boxed)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scan_num_reads_like_sscanf() {
        assert_eq!(scan_num("RSK-3", "RSK-"), Some(3));
        assert_eq!(scan_num("RSK-12x", "RSK-"), Some(12));
        assert_eq!(scan_num("RSK-", "RSK-"), None);
        assert_eq!(scan_num("ACT-2", "RSK-"), None);
        assert_eq!(scan_num("DRG- 4", "DRG-"), Some(4));
    }

    #[test]
    fn sprint_and_go_v() {
        assert_eq!(sprint(&json("a")), "a");
        assert_eq!(sprint(&json(3)), "3");
        assert_eq!(sprint(&json(1.5)), "1.5");
        assert_eq!(sprint(&json(true)), "true");
        assert_eq!(go_v(None), "<nil>");
        assert_eq!(go_v(Some(&json("x"))), "x");
    }

    #[test]
    fn latest_intent_item_is_the_last_one() {
        let mut c = domain::Change::default();
        assert!(latest_intent_item(&c).is_none());
        for id in ["I1", "N1", "I2"] {
            c.items.push(ChangeItem {
                id: ItemID::from(id),
                r#type: domain::TYPE_ARTIFACT,
                target: if id.starts_with('I') {
                    INTENT_ITEM_TYPE.into()
                } else {
                    "note".into()
                },
                ..Default::default()
            });
        }
        assert_eq!(latest_intent_item(&c).unwrap().id.as_str(), "I2");
    }
}
