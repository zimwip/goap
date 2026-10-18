//! The CEL variables of a condition, read from a blackboard.
use std::{collections::BTreeMap, sync::OnceLock};

use cel::{
    common::value::{CowVal, Val},
    context::VariableResolver,
};
use chrono::{SecondsFormat, Utc};
use goap_domain::{
    Blackboard, Change, ChangeImpact, ChangeItem, Dependency, FACET_CRITICALITY_POLICY,
    FACET_DEPENDENCIES, FACET_HISTORY, NodeRef, TYPE_ARTIFACT, TYPE_DECISION,
};
use goap_journal::{History, OUTCOME_SUCCEEDED};
use serde_json::{Map, Value, json};

use crate::value::to_val;

/// A JSON object, Go's `map[string]any`.
pub type Object = Map<String, Value>;

/// The variables of the decision and risk concepts: computed when an expression reads them (lazy bindings), so a
/// methodology that reads none of them pays nothing.
pub const LAZY_VARIABLES: [&str; 11] = [
    "options",
    "decisionPoints",
    "questions",
    "risks",
    "actions",
    "verifications",
    "derogations",
    "criticalityPolicy",
    "dependencies",
    "history",
    "assessments",
];

#[derive(Debug, Clone, Copy)]
enum Lazy {
    Options,
    DecisionPoints,
    Questions,
    Risks,
    Actions,
    Verifications,
    Derogations,
    CriticalityPolicy,
    Dependencies,
    History,
    Assessments,
}

impl Lazy {
    fn of(name: &str) -> Option<Lazy> {
        Some(match name {
            "options" => Lazy::Options,
            "decisionPoints" => Lazy::DecisionPoints,
            "questions" => Lazy::Questions,
            "risks" => Lazy::Risks,
            "actions" => Lazy::Actions,
            "verifications" => Lazy::Verifications,
            "derogations" => Lazy::Derogations,
            "criticalityPolicy" => Lazy::CriticalityPolicy,
            "dependencies" => Lazy::Dependencies,
            "history" => Lazy::History,
            "assessments" => Lazy::Assessments,
            _ => return None,
        })
    }
}

struct Slot {
    json: OnceLock<Value>,
    /// The value as CEL sees it, converted once while the JSON value does not change.
    cel: OnceLock<Box<dyn Val>>,
    lazy: Option<Lazy>,
}

impl Slot {
    fn plain(v: Value) -> Slot {
        let json = OnceLock::new();
        let _ = json.set(v);
        Slot {
            json,
            cel: OnceLock::new(),
            lazy: None,
        }
    }
}

/// Activation converts a blackboard into the CEL variables of a condition. Every value is made of maps, lists,
/// strings, numbers and bools; absent optional values are null so expressions can test them with `== null`. The
/// variables of [`LAZY_VARIABLES`] are computed when first read (by an expression, or [`Activation::get`]).
pub struct Activation<'a> {
    bb: &'a Blackboard,
    slots: BTreeMap<String, Slot>,
    decisions: OnceLock<(Value, Value)>,
}

/// Activation converts a blackboard into the CEL variables of a condition (Go's `Activation`).
pub fn activation(bb: &Blackboard) -> Activation<'_> {
    Activation::new(bb)
}

impl<'a> Activation<'a> {
    /// The variables of a blackboard.
    pub fn new(bb: &'a Blackboard) -> Activation<'a> {
        let c = &bb.change;
        let h = Hydrator { bb };
        let mut items = Vec::with_capacity(c.items.len());
        let mut decisions = Vec::new();
        let mut artifacts = Vec::new();
        for it in &c.items {
            if !c.active(&it.id) {
                continue; // superseded by a rebase or a merge
            }
            let m = h.item(it);
            if it.r#type == TYPE_DECISION {
                decisions.push(m.clone());
            } else if it.r#type == TYPE_ARTIFACT {
                artifacts.push(m.clone());
            }
            items.push(m);
        }
        let mut slots = BTreeMap::new();
        slots.insert(
            "change".to_string(),
            Slot::plain(Value::Object(change_map(c))),
        );
        slots.insert("items".to_string(), Slot::plain(Value::Array(items)));
        slots.insert(
            "changeImpacts".to_string(),
            Slot::plain(Value::Array(h.change_impacts())),
        );
        slots.insert(
            "decisions".to_string(),
            Slot::plain(Value::Array(decisions)),
        );
        slots.insert(
            "artifacts".to_string(),
            Slot::plain(Value::Array(artifacts)),
        );
        slots.insert(
            "vars".to_string(),
            Slot::plain(Value::Object(bb.vars.clone())),
        );
        slots.insert(
            "activeOption".to_string(),
            Slot::plain(Value::String(goap_domain::active_option_of(bb))),
        );
        for name in LAZY_VARIABLES {
            slots.insert(
                name.to_string(),
                Slot {
                    json: OnceLock::new(),
                    cel: OnceLock::new(),
                    lazy: Lazy::of(name),
                },
            );
        }
        Activation {
            bb,
            slots,
            decisions: OnceLock::new(),
        }
    }

    /// The blackboard the variables are read from.
    pub fn blackboard(&self) -> &'a Blackboard {
        self.bb
    }

    /// The names of the variables, sorted.
    pub fn names(&self) -> Vec<&str> {
        self.slots.keys().map(|k| k.as_str()).collect()
    }

    /// Whether the variable is computed when first read (and not replaced by a plain value since).
    pub fn is_lazy(&self, name: &str) -> bool {
        self.slots.get(name).is_some_and(|s| s.lazy.is_some())
    }

    /// Whether the value of the variable is known (a plain one, or a lazy one read already).
    pub fn is_computed(&self, name: &str) -> bool {
        self.slots.get(name).is_some_and(|s| s.json.get().is_some())
    }

    /// The value of a variable, computing a lazy one (Go's `Resolve(act[name])`).
    pub fn get(&self, name: &str) -> Option<&Value> {
        let slot = self.slots.get(name)?;
        Some(slot.json.get_or_init(|| match slot.lazy {
            Some(l) => self.compute(l),
            None => Value::Null,
        }))
    }

    /// The value of a variable to change in place (computed first when lazy): it is a plain value from then on.
    pub fn get_mut(&mut self, name: &str) -> Option<&mut Value> {
        self.get(name)?;
        let slot = self.slots.get_mut(name)?;
        slot.lazy = None;
        slot.cel = OnceLock::new();
        slot.json.get_mut()
    }

    /// Sets a variable to a plain value.
    pub fn set(&mut self, name: &str, v: Value) {
        self.slots.insert(name.to_string(), Slot::plain(v));
    }

    fn compute(&self, l: Lazy) -> Value {
        let bb = self.bb;
        let c = &bb.change;
        match l {
            Lazy::Options => Value::Array(Hydrator { bb }.options()),
            Lazy::DecisionPoints => self.decisions.get_or_init(|| decisions(bb)).0.clone(),
            Lazy::Questions => self.decisions.get_or_init(|| decisions(bb)).1.clone(),
            Lazy::Risks => Value::Array(risks(c)),
            Lazy::Actions => Value::Array(action_items(c)),
            Lazy::Verifications => Value::Array(verifications(c)),
            Lazy::Derogations => Value::Array(derogations(c, bb.at)),
            Lazy::CriticalityPolicy => Value::Object(criticality_policy(bb)),
            Lazy::Dependencies => Value::Array(dependencies(bb)),
            Lazy::History => Value::Object(history(bb)),
            Lazy::Assessments => Value::Array(assessments(bb)),
        }
    }

    fn cel_value(&self, name: &str) -> Option<&dyn Val> {
        let slot = self.slots.get(name)?;
        let json = self.get(name)?;
        Some(slot.cel.get_or_init(|| to_val(json)).as_ref())
    }
}

impl VariableResolver for Activation<'_> {
    fn resolve<'b>(&'b self, variable: &str) -> Option<CowVal<'b, 'b>> {
        self.cel_value(variable).map(CowVal::Borrowed)
    }
}

/// An activation with variables of its own on top (`self`, `assessment`, ...), which win.
pub(crate) struct Overlay<'x, 'a> {
    pub(crate) base: &'x Activation<'a>,
    pub(crate) extra: BTreeMap<String, Box<dyn Val>>,
}

impl<'x, 'a> Overlay<'x, 'a> {
    pub(crate) fn new(base: &'x Activation<'a>) -> Self {
        Overlay {
            base,
            extra: BTreeMap::new(),
        }
    }

    pub(crate) fn with(mut self, name: &str, v: &Value) -> Self {
        self.extra.insert(name.to_string(), to_val(v));
        self
    }
}

impl VariableResolver for Overlay<'_, '_> {
    fn resolve<'b>(&'b self, variable: &str) -> Option<CowVal<'b, 'b>> {
        if let Some(v) = self.extra.get(variable) {
            return Some(CowVal::Borrowed(v.as_ref()));
        }
        self.base.cel_value(variable).map(CowVal::Borrowed)
    }
}

fn change_map(c: &Change) -> Object {
    let v = json!({
        "id": c.id.as_str(), "title": c.title, "intent": c.intent, "status": c.status.as_str(),
        "criticality": goap_criticality::of(&c.data).as_str(), "lifecycle": c.lifecycle, "state": c.state,
        "goal": c.goal, "methodology": c.methodology, "branch": goap_domain::branch_of(&c.branch),
        "baseline": c.baseline_id.as_str(), "resultBaseline": c.result_baseline_id.as_str(),
        "data": Value::Object(c.data.clone()),
        "dependsOn": c.depends_on.iter().map(|id| Value::from(id.as_str())).collect::<Vec<_>>(),
    });
    into_object(v)
}

pub(crate) fn into_object(v: Value) -> Object {
    match v {
        Value::Object(m) => m,
        _ => Map::new(),
    }
}

/// risks is the risk register of the change (ADR 0036 §1).
fn risks(c: &Change) -> Vec<Value> {
    goap_risk::risks(c)
        .into_iter()
        .map(|r| {
            json!({"key": r.key, "title": r.title, "description": r.description, "probability": r.probability,
                "impact": r.impact, "score": r.score(), "status": r.status, "live": r.live(), "owner": r.owner,
                "actions": r.actions, "step": r.step, "item": r.item.as_str(), "versions": r.versions})
        })
        .collect()
}

/// assessments are the ratings of the change (ADR 0110).
fn assessments(bb: &Blackboard) -> Vec<Value> {
    goap_assess::assessments(bb)
        .into_iter()
        .map(|a| {
            let fs: Vec<Value> = a
                .findings
                .iter()
                .map(|f| json!({"text": f.text, "severity": f.severity}))
                .collect();
            json!({"key": a.key, "score": a.score, "verdict": a.verdict, "findings": fs, "suggestion": a.suggestion,
                "rated": a.rated, "current": a.current, "answered": a.answered, "item": a.item.as_str(),
                "versions": a.versions})
        })
        .collect()
}

/// actionItems are the actions of the change (ADR 0036 §1).
fn action_items(c: &Change) -> Vec<Value> {
    goap_risk::actions(c)
        .into_iter()
        .map(|a| {
            json!({"key": a.key, "title": a.title, "status": a.status, "owner": a.owner, "due": a.due,
                "for": a.r#for, "result": a.result, "item": a.item.as_str(), "versions": a.versions})
        })
        .collect()
}

/// verifications are the effects of the action runs and their state of verification (ADR 0075).
fn verifications(c: &Change) -> Vec<Value> {
    let items: Vec<ChangeItem> = c
        .items
        .iter()
        .filter(|it| it.r#type == goap_verify::TYPE_VERIFICATION && c.active(&it.id))
        .cloned()
        .collect();
    goap_verify::subjects(&items)
        .into_iter()
        .map(|s| {
            let open = s.open();
            json!({"execution": s.execution, "action": s.action, "impact": s.impact, "oracle": s.oracle,
                "independent": s.independent, "state": s.state, "producer": s.producer, "by": s.by, "open": open})
        })
        .collect()
}

/// criticalityPolicy is the policy of the level of the change: the one the blackboard carries
/// (FACET_CRITICALITY_POLICY, a criticality Policy filled by the provider that reads the organisation), else the
/// compiled-in table.
fn criticality_policy(bb: &Blackboard) -> Object {
    if let Some(p) = bb.facet_ref::<goap_criticality::Policy>(FACET_CRITICALITY_POLICY) {
        return p.map();
    }
    goap_criticality::defaults()
        .get(&goap_criticality::of(&bb.change.data))
        .cloned()
        .unwrap_or_default()
        .map()
}

/// derogations are the derogations of the change (ADR 0075 §2); expired is read at the instant of the blackboard
/// (the clock when it has none).
fn derogations(c: &Change, at: Option<chrono::DateTime<Utc>>) -> Vec<Value> {
    let at = at.unwrap_or_else(Utc::now);
    goap_risk::derogations(c)
        .into_iter()
        .map(|d| {
            json!({"key": d.key, "rule": d.rule, "target": d.target, "reason": d.reason, "signatory": d.signatory,
                "expires": go_rfc3339(d.expires), "status": d.status, "open": d.open(),
                "expired": d.expired_at(at), "item": d.item.as_str(), "versions": d.versions})
        })
        .collect()
}

/// Go's `t.Format(time.RFC3339)`.
fn go_rfc3339(t: chrono::DateTime<Utc>) -> String {
    t.to_rfc3339_opts(SecondsFormat::Secs, true)
}

/// decisions are the decision points of the change and all their questions (ADR 0009 §4).
fn decisions(bb: &Blackboard) -> (Value, Value) {
    let mut points = Vec::new();
    let mut questions = Vec::new();
    for d in goap_domain::decision_points_of(bb) {
        let mut qs = Vec::with_capacity(d.questions.len());
        for q in &d.questions {
            let m = json!({"id": q.id, "point": q.point, "text": q.text, "status": q.status, "answer": q.answer,
                "answeredBy": q.answered_by});
            qs.push(m.clone());
            questions.push(m);
        }
        let ruling = match &d.ruling {
            Some(r) => json!({"outcome": r.outcome, "option": r.option, "confidence": r.confidence,
                "justification": r.justification, "by": r.by, "human": r.human}),
            None => Value::Null,
        };
        points.push(json!({"id": d.id, "question": d.question, "status": d.status, "options": d.options,
            "criteria": d.criteria, "policy": Value::Object(policy_view(&d.policy)), "humanOnly": d.human_only,
            "escalation": d.escalation, "openQuestions": d.open_questions() as i64, "questions": qs,
            "ruling": ruling, "option": d.option, "decidedBy": d.decided_by}));
    }
    (Value::Array(points), Value::Array(questions))
}

/// policyView is the policy values of a decision point as an expression sees them: always a map, the values the
/// policy keeps. The values travel as JSON, which has one number type: a whole number is an int in an expression, so
/// that `d.policy.maxRounds - d.policy.rounds` is the same arithmetic wherever the point was replayed.
fn policy_view(p: &Object) -> Object {
    p.iter()
        .map(|(k, v)| {
            let v = match v {
                Value::Number(n) if n.as_i64().is_none() && n.as_u64().is_none() => {
                    let f = n.as_f64().unwrap_or(f64::NAN);
                    if f == f.trunc() && f.abs() < (1u64 << 53) as f64 {
                        Value::from(f as i64)
                    } else {
                        v.clone()
                    }
                }
                _ => v.clone(),
            };
            (k.clone(), v)
        })
        .collect()
}

struct Hydrator<'a> {
    bb: &'a Blackboard,
}

impl Hydrator<'_> {
    /// options are the options of the change (ADR 0032 §6).
    fn options(&self) -> Vec<Value> {
        goap_domain::options_of(self.bb)
            .iter()
            .map(|o| {
                let (name, hyp) = match &o.option {
                    Some(s) => (s.name.as_str(), s.hypothesis.as_str()),
                    None => ("", ""),
                };
                json!({"id": o.id, "name": name, "hypothesis": hyp, "status": o.option_status(),
                    "active": o.active, "evaluation": o.evaluation, "evaluated": o.evaluated})
            })
            .collect()
    }

    fn item(&self, it: &ChangeItem) -> Value {
        let derived: Vec<Value> = it
            .derived_from
            .iter()
            .map(|d| Value::from(d.as_str()))
            .collect();
        let decision = match &it.decision {
            Some(d) => json!({"item": d.item.as_str(), "accept": d.accept, "comment": d.comment}),
            None => Value::Null,
        };
        json!({
            "id": it.id.as_str(), "type": it.r#type.as_str(), "target": it.target,
            "status": self.bb.change.effective_status(&it.id).as_str(),
            "producedBy": it.produced_by, "derivedFrom": derived, "data": Value::Object(it.data.clone()),
            "decision": decision,
            // when it was written, in milliseconds since the epoch: compared with vars.event.at (ADR 0036 §3)
            "at": it.created_at.timestamp_millis(),
        })
    }

    fn node_ref(&self, r: &NodeRef) -> Value {
        let Some(v) = self.bb.nodes.get(r) else {
            return json!({"id": r.id.as_str(), "version": r.version, "key": "", "type": "", "types": [],
                "props": {}, "deleted": false, "latest": r.version, "out": [], "in": [], "state": "",
                "landable": true});
        };
        let out: Vec<Value> = v
            .out
            .iter()
            .map(|l| json!({"id": l.id.as_str(), "type": l.r#type, "to": self.summary(&l.to)}))
            .collect();
        let inn: Vec<Value> = v
            .r#in
            .iter()
            .map(|l| json!({"id": l.id.as_str(), "type": l.r#type, "from": self.summary(&l.from)}))
            .collect();
        json!({"id": r.id.as_str(), "version": r.version, "key": v.node.key, "type": v.node.r#type,
            "types": self.bb.types_of(&v.node.r#type), "props": Value::Object(v.node.properties.clone()),
            "deleted": v.node.deleted, "latest": v.latest, "out": out, "in": inn, "state": v.node.state,
            "landable": !v.not_landable})
    }

    fn summary(&self, r: &NodeRef) -> Value {
        let (key, typ) = if let Some(n) = self.bb.neighbors.get(r) {
            (n.key.as_str(), n.r#type.as_str())
        } else if let Some(v) = self.bb.nodes.get(r) {
            (v.node.key.as_str(), v.node.r#type.as_str())
        } else {
            ("", "")
        };
        json!({"id": r.id.as_str(), "version": r.version, "key": key, "type": typ, "types": self.bb.types_of(typ)})
    }

    /// changeImpacts lists the change impacts of the change (ADR 0024): the stored ones and the ones derived from
    /// its items. pre, post and landed are node views, or null while absent; a planned change impact has no post
    /// yet.
    fn change_impacts(&self) -> Vec<Value> {
        self.bb
            .change
            .nodes
            .iter()
            .map(|cn| {
                let rf = |r: &Option<NodeRef>| match r {
                    Some(r) => self.node_ref(r),
                    None => Value::Null,
                };
                let mut comment = "";
                let reviews: Vec<Value> = cn
                    .reviews
                    .iter()
                    .map(|r| {
                        comment = r.comment.as_str();
                        json!({"status": r.status.as_str(), "by": r.by, "comment": r.comment})
                    })
                    .collect();
                let items: Vec<Value> = cn.items.iter().map(|id| Value::from(id.as_str())).collect();
                json!({
                    "id": cn.id.as_str(), "key": cn.key, "type": cn.r#type, "types": self.bb.types_of(&cn.r#type),
                    "intent": cn.intent.as_str(), "rationale": cn.rationale, "review": cn.review.as_str(),
                    "reviews": reviews, "comment": comment,
                    "pre": rf(&cn.pre), "post": rf(&cn.post), "landed": rf(&cn.landed),
                    "planned": cn.post.is_none(), "hasPost": cn.post.is_some(), "drafted": cn.drafted(),
                    "recheck": cn.recheck, "props": Value::Object(self.current_props(cn)),
                    "via": cn.via.as_str(), "producedBy": cn.produced_by, "items": items,
                })
            })
            .collect()
    }

    /// currentProps are the properties of the node as the change has it: its draft (or the version written), else
    /// the one it starts from.
    fn current_props(&self, cn: &ChangeImpact) -> Object {
        let r = cn.post.as_ref().or(cn.pre.as_ref());
        match r {
            Some(r) => self
                .bb
                .nodes
                .get(r)
                .map(|v| v.node.properties.clone())
                .unwrap_or_default(),
            None => Map::new(),
        }
    }
}

/// dependencies are the changes the change depends on (ADR 0108): {id, title, methodology, status, state, parent,
/// satisfied, reason}.
fn dependencies(bb: &Blackboard) -> Vec<Value> {
    goap_domain::facet::<Vec<Dependency>>(bb, FACET_DEPENDENCIES)
        .into_iter()
        .map(|d| {
            json!({"id": d.id.as_str(), "title": d.title, "methodology": d.methodology,
                "status": d.status.as_str(), "state": d.state, "parent": d.parent_id.as_str(),
                "satisfied": d.satisfied, "reason": d.reason})
        })
        .collect()
}

/// history is the history of the change (ADR 0109 §4): {runs, steps, last}, last mapping each step path to its
/// latest run.
fn history(bb: &Blackboard) -> Object {
    let h = goap_domain::facet::<History>(bb, FACET_HISTORY);
    let runs: Vec<Value> = h
        .runs
        .iter()
        .map(|r| {
            json!({"process": r.process, "parent": r.parent, "methodology": r.methodology, "agent": r.agent,
                "goal": r.goal, "status": r.status, "seq": r.seq})
        })
        .collect();
    let mut steps = Vec::with_capacity(h.steps.len());
    let mut last = Map::new();
    for s in &h.steps {
        let m = json!({"path": s.path, "methodology": s.methodology, "process": s.process, "action": s.action,
            "execution": s.execution, "outcome": s.outcome, "succeeded": s.outcome == OUTCOME_SUCCEEDED,
            "error": s.error, "seq": s.seq, "runs": s.runs, "current": s.current, "staleBy": s.stale_by,
            "subject": s.subject});
        steps.push(m.clone());
        last.insert(s.path.clone(), m);
    }
    let mut out = Map::new();
    out.insert("runs".into(), Value::Array(runs));
    out.insert("steps".into(), Value::Array(steps));
    out.insert("last".into(), Value::Object(last));
    out
}
