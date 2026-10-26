//! The variables of the conditions of a methodology (Go's `condition.Activation`): what an expression over a change
//! sees, from the blackboard the graph answers `GetBlackboard` with (its protocol JSON) and the facets the graph adds.
//! Every value is made of maps, lists, strings, integers, doubles and booleans, as Go's: an integer of the change's own
//! records (a score, a count) is an integer, a number written by a producer (`data`, `props`, `vars`) a double, as
//! Go reads one from JSON.
use std::collections::HashMap;

use chrono::{DateTime, Utc};
use serde_json::{Map, Value, json};

use crate::{
    criticality::{self, Policy},
    flow::{FLOW_ADOPTED, FLOW_DISCARDED, FLOW_OPEN, Flows, Item},
    jsontext,
};

const ITEM_PROPOSED: &str = "proposed";
const ITEM_ACCEPTED: &str = "accepted";
const ITEM_REJECTED: &str = "rejected";
const ITEM_SUPERSEDED: &str = "superseded";
const ITEM_CANDIDATE: &str = "candidate";
const ITEM_STALE: &str = "stale";
/// The Unix milliseconds of Go's zero time, what an item with no creation time is at.
const GO_ZERO_MILLIS: i64 = -62_135_596_800_000;

const TYPE_ARTIFACT: &str = "artifact";
const TYPE_DECISION: &str = "decision";
const TYPE_TRANSITION: &str = "transition";
const TYPE_RISK: &str = "risk";
const TYPE_ACTION: &str = "action";
const TYPE_DEROGATION: &str = "derogation";
const TYPE_VERIFICATION: &str = "verification";
const TYPE_ASSESSMENT: &str = "assessment";
const TYPE_COMMENT: &str = "comment";
/// The start of the target of the artifact of an attestation (`condition.AttestationPrefix`).
pub const ATTESTATION_PREFIX: &str = "attestation:";

/// What the graph adds to a blackboard besides its wire (Go's facets and `Blackboard.Vars` / `Supertypes`).
#[derive(Debug, Clone, Default)]
pub struct Facets {
    /// the ancestors of each node type (`Blackboard.Supertypes`)
    pub types: HashMap<String, Vec<String>>,
    pub vars: Map<String, Value>,
    /// the changes the change depends on, as the conditions see them (`dependencies`)
    pub dependencies: Vec<Value>,
    /// the history of the change, as the conditions see it (`history`); none: nothing ran
    pub history: Option<Value>,
    /// what the organisation requires of the criticality of the change (none: the compiled-in table)
    pub criticality_policy: Option<Policy>,
}

fn s<'a>(v: &'a Value, k: &str) -> &'a str {
    v.get(k).and_then(Value::as_str).unwrap_or_default()
}

fn b(v: &Value, k: &str) -> bool {
    v.get(k).and_then(Value::as_bool).unwrap_or_default()
}

fn list<'a>(v: &'a Value, k: &str) -> &'a [Value] {
    v.get(k)
        .and_then(Value::as_array)
        .map(Vec::as_slice)
        .unwrap_or_default()
}

fn obj(v: Option<&Value>) -> Value {
    match v {
        Some(Value::Object(m)) => jsontext::floats(Value::Object(m.clone())),
        _ => Value::Object(Map::new()),
    }
}

fn strings(v: &Value, k: &str) -> Vec<Value> {
    list(v, k).iter().filter(|x| x.is_string()).cloned().collect()
}

/// An int64 of the protocol (protojson writes it as a string).
fn int(v: &Value, k: &str) -> i64 {
    match v.get(k) {
        Some(Value::Number(n)) => n.as_i64().unwrap_or_default(),
        Some(Value::String(t)) => t.parse().unwrap_or_default(),
        _ => 0,
    }
}

/// A number of a record's data as an int (Go's `dataInt` / `num`: an int, or a float truncated).
fn data_int(d: &Value, k: &str) -> i64 {
    match d.get(k) {
        Some(Value::Number(n)) => n.as_i64().or_else(|| n.as_f64().map(|f| f as i64)).unwrap_or_default(),
        _ => 0,
    }
}

fn time(v: &Value, k: &str) -> Option<DateTime<Utc>> {
    DateTime::parse_from_rfc3339(s(v, k))
        .ok()
        .map(|t| t.with_timezone(&Utc))
}

/// The items of a change and the flows they replay: the effective status of an item (`Change.EffectiveStatus`).
struct Items<'a> {
    items: &'a [Value],
    flows: Flows,
}

impl<'a> Items<'a> {
    fn of(items: &'a [Value]) -> Items<'a> {
        let replayed: Vec<Item> = items.iter().filter_map(|it| Item::of(&it.to_string()).ok()).collect();
        Items {
            items,
            flows: Flows::replay(&replayed),
        }
    }

    fn effective_status(&self, id: &str) -> String {
        let mut st = ITEM_PROPOSED.to_string();
        let mut own = "";
        for it in self.items {
            if s(it, "id") == id {
                st = s(it, "status").to_string();
                own = s(it, "flow");
            }
            if list(it, "supersedes").iter().any(|x| x.as_str() == Some(id)) {
                return ITEM_SUPERSEDED.into();
            }
        }
        for it in self.items {
            if s(it, "type") == TYPE_DECISION
                && let Some(d) = it.get("decision")
                && s(d, "item") == id
            {
                st = if b(d, "accept") { ITEM_ACCEPTED } else { ITEM_REJECTED }.into();
            }
        }
        if !own.is_empty() {
            match self.flows.effective(own) {
                FLOW_OPEN => return ITEM_CANDIDATE.into(),
                FLOW_DISCARDED => return ITEM_REJECTED.into(),
                _ => {}
            }
        }
        for f in &self.flows.list {
            if !f.stale.iter().any(|x| x == id) {
                continue;
            }
            match self.flows.effective(&f.id) {
                FLOW_ADOPTED => return ITEM_SUPERSEDED.into(),
                FLOW_OPEN if st == ITEM_PROPOSED || st == ITEM_ACCEPTED => st = ITEM_STALE.into(),
                _ => {}
            }
        }
        st
    }

    /// Whether an item is not superseded (`Change.Active`).
    fn active(&self, id: &str) -> bool {
        let st = self.effective_status(id);
        st != ITEM_SUPERSEDED && st != ITEM_CANDIDATE
    }

    /// The items in effect of a type, not rejected (the register's `registerOf`), each with its position.
    fn live<'s>(&'s self, typ: &'s str) -> impl Iterator<Item = (usize, &'a Value)> + 's {
        self.items
            .iter()
            .enumerate()
            .filter(move |(_, it)| s(it, "type") == typ && s(it, "status") != ITEM_REJECTED && self.active(s(it, "id")))
    }
}

/// A record of the register: the last version of each key, merged over the earlier ones (`registerOf`).
struct Record<'a> {
    key: String,
    item: &'a Value,
    data: Value,
    versions: i64,
}

fn register<'a>(items: &Items<'a>, typ: &str) -> Vec<Record<'a>> {
    let mut out: Vec<Record<'a>> = Vec::new();
    for (_, it) in items.live(typ) {
        let key = s(it.get("data").unwrap_or(&Value::Null), "key").trim().to_string();
        if key.is_empty() {
            continue;
        }
        let data = it.get("data").and_then(Value::as_object).cloned().unwrap_or_default();
        match out.iter_mut().find(|r| r.key == key) {
            Some(r) => {
                if let Value::Object(m) = &mut r.data {
                    m.extend(data);
                }
                r.item = it;
                r.versions += 1;
            }
            None => out.push(Record {
                key,
                item: it,
                data: Value::Object(data),
                versions: 1,
            }),
        }
    }
    out
}

/// A string of a record's data, trimmed (`dataString`).
fn ds(d: &Value, k: &str) -> String {
    s(d, k).trim().to_string()
}

fn risks(items: &Items) -> Vec<Value> {
    register(items, TYPE_RISK)
        .into_iter()
        .map(|r| {
            let d = &r.data;
            let mut status = ds(d, "status");
            if status.is_empty() {
                status = "open".into();
            }
            let (p, i) = (data_int(d, "probability"), data_int(d, "impact"));
            json!({"key": r.key, "title": ds(d, "title"), "description": ds(d, "description"), "probability": p,
                "impact": i, "score": p * i, "live": status == "open" || status == "mitigating", "status": status,
                "owner": ds(d, "owner"), "actions": strings(d, "actions"), "step": ds(d, "step"), "item": s(r.item, "id"),
                "versions": r.versions})
        })
        .collect()
}

fn action_items(items: &Items) -> Vec<Value> {
    register(items, TYPE_ACTION)
        .into_iter()
        .map(|r| {
            let d = &r.data;
            let or = |k: &str, dflt: &str| Some(ds(d, k)).filter(|x| !x.is_empty()).unwrap_or(dflt.into());
            let (status, kind) = (or("status", "open"), or("kind", "mitigation"));
            let mut purpose = ds(d, "purpose");
            if kind == "poc" && purpose.is_empty() {
                purpose = "derisk".into();
            }
            let (for_, risks) = (ds(d, "for"), strings(d, "risks"));
            // the risks it mitigates now (`ActionItem.Covers`, ADR 0131)
            let mitigates = status != "cancelled" && !(kind == "poc" && status == "done");
            let mut covers: Vec<Value> = Vec::new();
            for k in std::iter::once(Value::String(for_.clone())).chain(risks.iter().cloned()) {
                if k.as_str().is_some_and(|k| !k.is_empty()) && mitigates && !covers.contains(&k) {
                    covers.push(k);
                }
            }
            json!({"key": r.key, "title": ds(d, "title"), "status": status, "owner": ds(d, "owner"), "due": ds(d, "due"),
                "for": for_, "result": ds(d, "result"), "kind": kind, "purpose": purpose, "risks": risks,
                "hypotheses": strings(d, "hypotheses"), "change": ds(d, "change"), "covers": covers,
                "item": s(r.item, "id"), "versions": r.versions})
        })
        .collect()
}

fn derogations(items: &Items, at: DateTime<Utc>) -> Vec<Value> {
    register(items, TYPE_DEROGATION)
        .into_iter()
        .map(|r| {
            let d = &r.data;
            // validated on write; an unreadable expiry is the zero time: expired
            let exp = criticality::parse_expiry(&ds(d, "expires")).unwrap_or_else(|_| jsontext::zero_time());
            let mut status = ds(d, "status");
            if status.is_empty() {
                status = "open".into();
            }
            let open = status != "closed";
            json!({"key": r.key, "rule": ds(d, "rule"), "target": ds(d, "target"), "reason": ds(d, "reason"),
                "signatory": ds(d, "signatory"), "expires": criticality::rfc3339(exp), "status": status, "open": open,
                "expired": open && exp <= at, "item": s(r.item, "id"), "versions": r.versions})
        })
        .collect()
}

/// The effects of the runs and their state of verification (`verify.Subjects`).
fn verifications(items: &Items) -> Vec<Value> {
    struct Subject {
        execution: String,
        action: String,
        impact: String,
        oracle: String,
        independent: bool,
        state: String,
        producer: String,
        by: String,
    }
    let valid = |st: &str| ["produced", "verified", "accepted", "accepted_with_reserve", "rejected"].contains(&st);
    let mut out: Vec<Subject> = Vec::new();
    let mut by_impact: HashMap<String, usize> = HashMap::new();
    for it in items.items {
        if s(it, "type") != TYPE_VERIFICATION || !items.active(s(it, "id")) {
            continue;
        }
        let d = it.get("data").unwrap_or(&Value::Null);
        let state = s(d, "state");
        if state == "produced" {
            let base = || Subject {
                execution: s(it, "execution").into(),
                action: s(d, "action").into(),
                impact: String::new(),
                oracle: s(d, "oracle").into(),
                independent: b(d, "independent"),
                state: state.into(),
                producer: s(it, "producedBy").into(),
                by: String::new(),
            };
            let impacts: Vec<&str> = list(d, "impacts").iter().filter_map(Value::as_str).collect();
            if impacts.is_empty() {
                out.push(base());
                continue;
            }
            for id in impacts {
                let mut sub = base();
                sub.impact = id.into();
                match by_impact.get(id) {
                    Some(&i) => out[i] = sub, // produced again (a relaunch): starts over
                    None => {
                        by_impact.insert(id.into(), out.len());
                        out.push(sub);
                    }
                }
            }
            continue;
        }
        if let Some(&i) = by_impact.get(s(d, "impact"))
            && valid(state)
        {
            out[i].state = state.into();
            if !s(d, "by").is_empty() {
                out[i].by = s(d, "by").into();
            }
        }
    }
    out.into_iter()
        .map(|v| {
            let open = v.state == "produced" || v.state == "verified";
            json!({"execution": v.execution, "action": v.action, "impact": v.impact, "oracle": v.oracle,
                "independent": v.independent, "state": v.state, "producer": v.producer, "by": v.by, "open": open})
        })
        .collect()
}

/// A JSON text as Go's `encoding/json` writes a value: keys sorted, `<`, `>`, `&` and the line separators escaped.
fn go_json(v: &Value) -> String {
    let mut out = String::new();
    for c in serde_json::to_string(v).unwrap_or_default().chars() {
        match c {
            '<' => out.push_str("\\u003c"),
            '>' => out.push_str("\\u003e"),
            '&' => out.push_str("\\u0026"),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            c => out.push(c),
        }
    }
    out
}

/// The fingerprint of a text (`assess.Fingerprint`, FNV-1a 32 bits).
pub fn fingerprint(text: &str) -> String {
    let mut h: u32 = 0x811c9dc5;
    for b in text.as_bytes() {
        h ^= u32::from(*b);
        h = h.wrapping_mul(0x01000193);
    }
    format!("{h:08x}")
}

/// What the blackboard reads of a node view of its wire.
struct Board<'a> {
    wire: &'a Value,
    facets: &'a Facets,
    items: Items<'a>,
}

impl<'a> Board<'a> {
    fn change(&self) -> &'a Value {
        self.wire.get("change").unwrap_or(&Value::Null)
    }

    fn types_of(&self, t: &str) -> Value {
        let mut out = vec![Value::String(t.into())];
        out.extend(
            self.facets
                .types
                .get(t)
                .into_iter()
                .flatten()
                .map(|s| Value::String(s.clone())),
        );
        Value::Array(out)
    }

    /// The node view of a reference (`Blackboard.Nodes`).
    fn view(&self, r: &Value) -> Option<&'a Value> {
        let (id, version) = (s(r, "id"), int(r, "version"));
        list(self.wire, "nodes").iter().find(|v| {
            let n = v.get("node").unwrap_or(&Value::Null);
            s(n, "id") == id && int(n, "version") == version
        })
    }

    fn neighbour(&self, r: &Value) -> Option<&'a Value> {
        let (id, version) = (s(r, "id"), int(r, "version"));
        list(self.wire, "neighbors")
            .iter()
            .find(|n| s(n, "id") == id && int(n, "version") == version)
    }

    fn summary(&self, r: &Value) -> Value {
        let (key, typ) = match (self.neighbour(r), self.view(r).and_then(|v| v.get("node"))) {
            (Some(n), _) | (None, Some(n)) => (s(n, "key"), s(n, "type")),
            _ => ("", ""),
        };
        json!({"id": s(r, "id"), "version": int(r, "version"), "key": key, "type": typ, "types": self.types_of(typ)})
    }

    fn reference(&self, r: &Value) -> Value {
        let mut m = json!({"id": s(r, "id"), "version": int(r, "version"), "key": "", "type": "", "types": [],
            "props": {}, "deleted": false, "latest": int(r, "version"), "out": [], "in": [], "state": "",
            "committable": true});
        let Some(v) = self.view(r) else { return m };
        let n = v.get("node").unwrap_or(&Value::Null);
        let out: Vec<Value> = list(v, "out")
            .iter()
            .map(|l| json!({"id": s(l, "id"), "type": s(l, "type"), "to": self.summary(l.get("to").unwrap_or(&Value::Null))}))
            .collect();
        let r#in: Vec<Value> = list(v, "in")
            .iter()
            .map(|l| json!({"id": s(l, "id"), "type": s(l, "type"), "from": self.summary(l.get("from").unwrap_or(&Value::Null))}))
            .collect();
        let o = m.as_object_mut().expect("an object");
        o.insert("key".into(), s(n, "key").into());
        o.insert("type".into(), s(n, "type").into());
        o.insert("props".into(), obj(n.get("props")));
        o.insert("deleted".into(), b(n, "deleted").into());
        o.insert("latest".into(), int(v, "latest").into());
        o.insert("state".into(), s(n, "state").into());
        o.insert("committable".into(), (!b(v, "notCommittable")).into());
        o.insert("types".into(), self.types_of(s(n, "type")));
        o.insert("out".into(), out.into());
        o.insert("in".into(), r#in.into());
        m
    }

    fn item(&self, it: &Value) -> Value {
        let at = time(it, "createdAt").map_or(GO_ZERO_MILLIS, |t| t.timestamp_millis());
        let decision = it.get("decision").map_or(
            Value::Null,
            |d| json!({"item": s(d, "item"), "accept": b(d, "accept"), "comment": s(d, "comment")}),
        );
        json!({"id": s(it, "id"), "type": s(it, "type"), "target": s(it, "target"),
            "status": self.items.effective_status(s(it, "id")), "producedBy": s(it, "producedBy"),
            "derivedFrom": strings(it, "derivedFrom"), "data": obj(it.get("data")), "decision": decision, "at": at})
    }

    fn impacts(&self) -> Vec<Value> {
        list(self.change(), "nodes")
            .iter()
            .map(|cn| {
                let r = |k: &str| cn.get(k).map_or(Value::Null, |r| self.reference(r));
                let reviews: Vec<Value> = list(cn, "reviews")
                    .iter()
                    .map(|r| json!({"status": s(r, "status"), "by": s(r, "by"), "comment": s(r, "comment")}))
                    .collect();
                let comment = list(cn, "reviews").last().map_or("", |r| s(r, "comment"));
                let post = cn.get("post");
                let props = match post.or(cn.get("pre")) {
                    Some(r) => obj(self.view(r).and_then(|v| v.get("node")).and_then(|n| n.get("props"))),
                    None => json!({}),
                };
                json!({"id": s(cn, "id"), "key": s(cn, "key"), "type": s(cn, "type"), "types": self.types_of(s(cn, "type")),
                    "intent": s(cn, "intent"), "rationale": s(cn, "rationale"), "review": s(cn, "review"),
                    "reviews": reviews, "comment": comment, "pre": r("pre"), "post": r("post"),
                    "committed": r("committed"), "planned": post.is_none(), "hasPost": post.is_some(),
                    "drafted": post.is_some_and(|p| int(p, "version") == 0), "recheck": b(cn, "recheck"),
                    "props": props, "via": s(cn, "via"), "producedBy": s(cn, "producedBy"), "items": strings(cn, "items")})
            })
            .collect()
    }

    /// The text a subject of an assessment stands for (`assess.Text`).
    fn text(&self, key: &str) -> Option<String> {
        let c = self.change();
        let (title, intent) = (s(c, "title").trim(), s(c, "intent").trim());
        match key {
            "description" => return Some(format!("{title}\n\n{intent}")),
            "title" => return Some(title.into()),
            "intent" => return Some(intent.into()),
            _ => {}
        }
        if let Some(node_key) = key.strip_prefix("impact:") {
            let n = list(c, "nodes")
                .iter()
                .rev()
                .find(|n| s(n, "key") == node_key && !b(n, "superseded"))?;
            let mut doc = json!({"key": s(n, "key"), "type": s(n, "type"), "intent": s(n, "intent")});
            if let Some(r) = n.get("post").or(n.get("pre")) {
                let v = self.view(r);
                let node = v.and_then(|v| v.get("node")).unwrap_or(&Value::Null);
                let mut links: Vec<String> = v
                    .map(|v| list(v, "out"))
                    .unwrap_or_default()
                    .iter()
                    .map(|l| format!("{}>{}", s(l, "type"), s(l.get("to").unwrap_or(&Value::Null), "id")))
                    .collect();
                links.sort();
                let o = doc.as_object_mut().expect("an object");
                o.insert("state".into(), s(node, "state").into());
                o.insert(
                    "props".into(),
                    node.get("props").map_or(Value::Null, |p| jsontext::floats(p.clone())),
                );
                o.insert("links".into(), json!(links));
            }
            return Some(go_json(&doc));
        }
        if let Some(r) = key.strip_prefix("item:") {
            for it in self.items.items.iter().rev() {
                let t = s(it, "type");
                if t == TYPE_ASSESSMENT || t == TYPE_COMMENT || !self.items.active(s(it, "id")) {
                    continue;
                }
                let d = it.get("data");
                if s(it, "id") == r || d.is_some_and(|d| s(d, "key") == r) {
                    let data = d.map_or(Value::Null, |d| jsontext::floats(d.clone()));
                    return Some(go_json(&json!({"type": t, "target": s(it, "target"), "data": data})));
                }
            }
        }
        None
    }

    /// The ratings of the change (`assess.Assessments`).
    fn assessments(&self) -> Vec<Value> {
        let mut order: Vec<String> = Vec::new();
        let mut last: HashMap<String, (usize, &Value, i64)> = HashMap::new();
        let mut last_comment: HashMap<String, usize> = HashMap::new();
        for (i, it) in self.items.items.iter().enumerate() {
            if !self.items.active(s(it, "id")) || s(it, "status") == ITEM_REJECTED {
                continue;
            }
            let d = it.get("data").unwrap_or(&Value::Null);
            match s(it, "type") {
                TYPE_COMMENT => {
                    last_comment.insert(s(d, "about").into(), i);
                }
                TYPE_ASSESSMENT => {
                    let key = s(d, "key");
                    if key.is_empty() {
                        continue;
                    }
                    let versions = match last.get(key) {
                        Some((_, _, n)) => n + 1,
                        None => {
                            order.push(key.into());
                            1
                        }
                    };
                    last.insert(key.into(), (i, it, versions));
                }
                _ => {}
            }
        }
        order
            .iter()
            .map(|key| {
                let (at, it, versions) = last[key];
                let d = it.get("data").unwrap_or(&Value::Null);
                let rated = s(d, "rated");
                let current = self.text(key).is_some_and(|text| {
                    let r = rated.trim();
                    r == text.trim() || r == fingerprint(&text)
                });
                let answered = last_comment.get(key).is_none_or(|&c| c < at);
                let findings: Vec<Value> = list(d, "findings")
                    .iter()
                    .filter_map(|f| match f {
                        Value::String(t) => Some(json!({"text": t, "severity": ""})),
                        Value::Object(_) => Some(json!({"text": s(f, "text"), "severity": s(f, "severity")})),
                        _ => None,
                    })
                    .collect();
                json!({"key": key, "score": data_int(d, "score"), "verdict": s(d, "verdict"), "findings": findings,
                    "suggestion": s(d, "suggestion"), "rated": rated, "current": current, "answered": answered,
                    "item": s(it, "id"), "versions": versions})
            })
            .collect()
    }

    /// The latest attestation of each item of a gate's checklist, by key (`attestations`).
    fn attestations(&self) -> Map<String, Value> {
        let mut out = Map::new();
        for it in self.items.items {
            let Some(key) = s(it, "target").strip_prefix(ATTESTATION_PREFIX) else {
                continue;
            };
            if s(it, "type") != TYPE_ARTIFACT || !self.items.active(s(it, "id")) {
                continue;
            }
            let d = it.get("data").unwrap_or(&Value::Null);
            out.insert(
                key.into(),
                json!({"verdict": s(d, "verdict"), "comment": s(d, "comment"), "by": s(it, "producedBy"), "item": s(it, "id")}),
            );
        }
        out
    }

    /// The moves of the lifecycle of the change (`Change.StateMoves`): its transition items of the main flow.
    fn moves(&self) -> Vec<(&'a str, &'a str, &'a str, &'a str)> {
        self.items
            .items
            .iter()
            .filter(|it| s(it, "type") == TYPE_TRANSITION && s(it, "flow").is_empty())
            .map(|it| {
                let d = it.get("data").unwrap_or(&Value::Null);
                (s(d, "transition"), s(d, "from"), s(d, "to"), s(d, "decision"))
            })
            .collect()
    }

    /// The transitions that stand: taken, not gone back from since (`Change.Passed`).
    fn passed(&self) -> Vec<Value> {
        let moves = self.moves();
        let move_from = |t: &str| moves.iter().rfind(|m| m.0 == t).map_or("", |m| m.1);
        let mut out: Vec<&str> = Vec::new();
        for m in &moves {
            out.retain(|t| move_from(t) != m.2 && *t != m.0);
            out.push(m.0);
        }
        out.into_iter().map(Value::from).collect()
    }

    fn options(&self) -> Vec<Value> {
        list(self.wire, "options")
            .iter()
            .map(|o| {
                let opt = o.get("option").unwrap_or(&Value::Null);
                let evaluated = s(o, "optionStatus") == "evaluated";
                let status = match s(o, "status") {
                    FLOW_ADOPTED => "selected",
                    FLOW_DISCARDED => "rejected",
                    _ if evaluated => "evaluated",
                    _ => "exploring",
                };
                json!({"id": s(o, "id"), "name": s(opt, "name"), "hypothesis": s(opt, "hypothesis"), "status": status,
                    "active": b(o, "active"), "evaluation": s(o, "evaluation"), "evaluated": evaluated})
            })
            .collect()
    }

    /// The decision points of the change and all their questions (`decisions`).
    fn decisions(&self) -> (Vec<Value>, Vec<Value>) {
        let (mut points, mut questions) = (Vec::new(), Vec::new());
        for d in list(self.wire, "decisionPoints") {
            let qs: Vec<Value> = list(d, "questions")
                .iter()
                .map(|q| {
                    json!({"id": s(q, "id"), "point": s(q, "point"), "text": s(q, "text"), "status": s(q, "status"),
                        "answer": s(q, "answer"), "answeredBy": s(q, "answeredBy")})
                })
                .collect();
            questions.extend(qs.iter().cloned());
            let open = qs.iter().filter(|q| s(q, "status") == "open").count() as i64;
            let ruling = d.get("ruling").map_or(Value::Null, |r| {
                json!({"outcome": s(r, "outcome"), "option": s(r, "option"),
                    "confidence": r.get("confidence").and_then(Value::as_f64).unwrap_or_default(),
                    "justification": s(r, "justification"), "by": s(r, "by"), "human": b(r, "human")})
            });
            // the policy values as an expression sees them: a whole number an int (`policyView`)
            let policy: Map<String, Value> = d
                .get("policy")
                .and_then(Value::as_object)
                .into_iter()
                .flatten()
                .map(|(k, v)| {
                    let v = match v.as_f64() {
                        Some(f) if f == f.trunc() && f.abs() < 9_007_199_254_740_992.0 => Value::from(f as i64),
                        Some(f) => Value::from(f),
                        None => jsontext::floats(v.clone()),
                    };
                    (k.clone(), v)
                })
                .collect();
            points.push(
                json!({"id": s(d, "id"), "question": s(d, "question"), "status": s(d, "status"),
                "options": strings(d, "options"), "criteria": strings(d, "criteria"), "policy": policy,
                "humanOnly": b(d, "humanOnly"), "escalation": s(d, "escalation"), "openQuestions": open,
                "questions": qs, "ruling": ruling, "option": s(d, "option"), "decidedBy": s(d, "decidedBy")}),
            );
        }
        (points, questions)
    }
}

/// The variables of the conditions of a change from its blackboard (`condition.Activation`).
pub fn activation(wire: &Value, facets: &Facets) -> Map<String, Value> {
    let change = wire.get("change").unwrap_or(&Value::Null);
    let bd = Board {
        wire,
        facets,
        items: Items::of(list(change, "items")),
    };
    let at = time(wire, "at").unwrap_or_else(Utc::now);
    let (mut items, mut decisions, mut artifacts) = (Vec::new(), Vec::new(), Vec::new());
    for it in bd.items.items {
        if !bd.items.active(s(it, "id")) {
            continue; // superseded by a rebase or a merge
        }
        let m = bd.item(it);
        match s(it, "type") {
            TYPE_DECISION => decisions.push(m.clone()),
            TYPE_ARTIFACT => artifacts.push(m.clone()),
            _ => {}
        }
        items.push(m);
    }
    let moves = bd.moves();
    let consumed: Vec<Value> = moves
        .iter()
        .filter(|m| !m.3.is_empty())
        .map(|m| Value::from(m.3))
        .collect();
    let data = change.get("data");
    let level = criticality::of(data.and_then(Value::as_object));
    let branch = match s(change, "branch") {
        "" => crate::model::MAIN_BRANCH,
        b => b,
    };
    let (points, questions) = bd.decisions();
    let policy = facets
        .criticality_policy
        .clone()
        .unwrap_or_else(|| Policy::default_of(level));
    let mut out = Map::new();
    out.insert(
        "change".into(),
        json!({"id": s(change, "id"), "title": s(change, "title"), "intent": s(change, "intent"),
            "status": s(change, "status"), "criticality": level, "lifecycle": s(change, "lifecycle"),
            "state": s(change, "state"), "goal": s(change, "goal"), "methodology": s(change, "methodology"),
            "branch": branch, "baseline": s(change, "baselineId"), "resultBaseline": s(change, "resultBaselineId"),
            "data": obj(data), "dependsOn": strings(change, "dependsOn"), "passed": bd.passed(), "consumed": consumed}),
    );
    out.insert("items".into(), items.into());
    out.insert("changeImpacts".into(), bd.impacts().into());
    out.insert("decisions".into(), decisions.into());
    out.insert("artifacts".into(), artifacts.into());
    out.insert("vars".into(), obj(Some(&Value::Object(facets.vars.clone()))));
    out.insert("options".into(), bd.options().into());
    out.insert("activeOption".into(), s(wire, "activeOption").into());
    out.insert("decisionPoints".into(), points.into());
    out.insert("questions".into(), questions.into());
    out.insert("risks".into(), risks(&bd.items).into());
    out.insert("actions".into(), action_items(&bd.items).into());
    out.insert("verifications".into(), verifications(&bd.items).into());
    out.insert("derogations".into(), derogations(&bd.items, at).into());
    out.insert("criticalityPolicy".into(), policy.map().into());
    out.insert("dependencies".into(), facets.dependencies.clone().into());
    out.insert(
        "history".into(),
        facets
            .history
            .clone()
            .unwrap_or_else(|| json!({"runs": [], "steps": [], "last": {}})),
    );
    out.insert("assessments".into(), bd.assessments().into());
    out.insert("attestations".into(), bd.attestations().into());
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A value with its CEL type, as the Go cases write it: an integer as `{"#int": n}`, a double as a number.
    fn typed(v: &Value) -> Value {
        match v {
            Value::Number(n) if n.is_i64() || n.is_u64() => json!({"#int": n}),
            // a double as Go's JSON writes it: a whole one with no fraction
            Value::Number(n) => match n.as_f64() {
                Some(f) if f.fract() == 0.0 && f.abs() < 1e15 => Value::from(f as i64),
                _ => v.clone(),
            },
            Value::Array(a) => Value::Array(a.iter().map(typed).collect()),
            Value::Object(m) => Value::Object(m.iter().map(|(k, v)| (k.clone(), typed(v))).collect()),
            v => v.clone(),
        }
    }

    /// A typed value read back: `{"#int": n}` an integer, every other number a double.
    fn untyped(v: &Value) -> Value {
        match v {
            Value::Object(m) if m.len() == 1 && m.contains_key("#int") => {
                Value::from(m["#int"].as_i64().unwrap_or_default())
            }
            Value::Number(n) => n
                .as_f64()
                .and_then(serde_json::Number::from_f64)
                .map_or(Value::Null, Value::Number),
            Value::Array(a) => Value::Array(a.iter().map(untyped).collect()),
            Value::Object(m) => Value::Object(m.iter().map(|(k, v)| (k.clone(), untyped(v))).collect()),
            v => v.clone(),
        }
    }

    #[test]
    fn computes_the_variables_go_computes() {
        let cases: Value = serde_json::from_str(include_str!("activation_cases.gen.json")).unwrap();
        for c in cases["cases"].as_array().unwrap() {
            let f = untyped(&c["facets"]);
            let facets = Facets {
                types: serde_json::from_value(f["types"].clone()).unwrap_or_default(),
                vars: f["vars"].as_object().cloned().unwrap_or_default(),
                dependencies: f["dependencies"].as_array().cloned().unwrap_or_default(),
                history: Some(f["history"].clone()),
                criticality_policy: f.get("criticalityPolicy").map(Policy::from_map),
            };
            let got = typed(&Value::Object(activation(&c["wire"], &facets)));
            let want = &c["activation"];
            for (k, w) in want.as_object().unwrap() {
                assert_eq!(&got[k], w, "{}: {k}", c["name"]);
            }
            assert_eq!(&got, want, "{}", c["name"]);
        }
    }
}
