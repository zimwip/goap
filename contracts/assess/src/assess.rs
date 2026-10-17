//! Assessments of the subjects of a change and the threads about them.

use std::{collections::HashMap, sync::Arc};

use goap_domain::{
    Blackboard, Change, ChangeImpact, ChangeItem, Error, ITEM_REJECTED, ItemID, ItemType,
    register_item_type,
};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::canonical::canonical;

/// TypeAssessment is a rating of a subject: each item is the whole record of its key (data.key), the latest one in
/// effect is the rating.
pub const TYPE_ASSESSMENT: ItemType = ItemType::from_static("assessment");
/// TypeComment is a message of a person about a subject (data.about), in the thread of its assessment.
pub const TYPE_COMMENT: ItemType = ItemType::from_static("comment");

// The subjects of a rating (data.key of an assessment, data.about of a comment): the description of the change (its
// title and its intent), its title, its intent, an impact by the key of its node ("impact:<key>") and an item by the
// key of its record or its id ("item:<key or id>").
pub const SUBJECT_DESCRIPTION: &str = "description";
pub const SUBJECT_TITLE: &str = "title";
pub const SUBJECT_INTENT: &str = "intent";
pub const IMPACT_PREFIX: &str = "impact:";
pub const ITEM_PREFIX: &str = "item:";

/// MaxScore is the best score; a score goes from 0 to MAX_SCORE.
pub const MAX_SCORE: i64 = 5;

// Verdicts of an assessment: of a text (clear, unclear, insufficient) or of a claim made about its subject, such as a
// suspected impact (confirmed, refuted).
pub const VERDICT_CLEAR: &str = "clear";
pub const VERDICT_UNCLEAR: &str = "unclear";
pub const VERDICT_INSUFFICIENT: &str = "insufficient";
pub const VERDICT_CONFIRMED: &str = "confirmed";
pub const VERDICT_REFUTED: &str = "refuted";

fn null_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

fn is_zero(n: &i64) -> bool {
    *n == 0
}

/// Finding is one remark of an assessment.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Finding {
    pub text: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub severity: String,
}

/// Assessment is the latest rating of a subject.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Assessment {
    pub key: String,
    pub score: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub verdict: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub findings: Vec<Finding>,
    /// Suggestion is a wording the rater proposes, Reply what it says to the person (its answer in the thread).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub suggestion: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub reply: String,
    /// Rated is what the rating is about: the text of the subject or its fingerprint (fingerprint).
    pub rated: String,
    /// Current: the subject still reads as rated (a rating of another text says nothing of this one); a subject the
    /// change no longer has is not current.
    pub current: bool,
    /// Answered: no comment about the subject came after the rating.
    pub answered: bool,
    pub item: ItemID,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub by: String,
    pub versions: i64,
}

/// Message is a message of the thread of a subject: a comment of a person, or a rating (its reply).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Message {
    pub r#type: ItemType,
    pub item: ItemID,
    pub text: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub by: String,
    /// Score is the score of a rating.
    #[serde(skip_serializing_if = "is_zero")]
    pub score: i64,
}

/// Register gives the types their validation (domain::register_item_type; the built-in methodology domain declares
/// them, ADR 0128); the services writing or reading such items call it at start, and so does a test building them.
/// It may be called any number of times.
pub fn register() {
    register_item_type(TYPE_ASSESSMENT, Some(Arc::new(validate_assessment)));
    register_item_type(TYPE_COMMENT, Some(Arc::new(validate_comment)));
}

fn validate_assessment(it: &ChangeItem) -> Result<(), Error> {
    let d = &it.data;
    if str_of(d, "key").is_empty() {
        return Err(Error::invalid(
            "assessment item requires data.key, the subject it rates",
        ));
    }
    if d.get("score").is_none_or(Value::is_null) {
        return Err(Error::invalid("assessment item requires data.score"));
    }
    let s = num(d, "score");
    if !(0..=MAX_SCORE).contains(&s) {
        return Err(Error::invalid(format!(
            "assessment score must be between 0 and {MAX_SCORE}"
        )));
    }
    match str_of(d, "verdict") {
        "" | VERDICT_CLEAR | VERDICT_UNCLEAR | VERDICT_INSUFFICIENT | VERDICT_CONFIRMED
        | VERDICT_REFUTED => Ok(()),
        _ => Err(Error::invalid(format!(
            "assessment verdict must be {VERDICT_CLEAR}, {VERDICT_UNCLEAR}, {VERDICT_INSUFFICIENT}, {VERDICT_CONFIRMED} or {VERDICT_REFUTED}"
        ))),
    }
}

fn validate_comment(it: &ChangeItem) -> Result<(), Error> {
    if str_of(&it.data, "about").is_empty() || str_of(&it.data, "text").trim().is_empty() {
        return Err(Error::invalid(
            "comment item requires data.about (its subject) and data.text",
        ));
    }
    Ok(())
}

/// The value of a map, or a JSON null when it is empty (Go's nil map, the usual empty one).
fn map_or_null(m: &Map<String, Value>) -> Value {
    if m.is_empty() {
        Value::Null
    } else {
        Value::Object(m.clone())
    }
}

/// Text is the text of a subject as the change has it now: the description is the title and the intent; an impact
/// is its node as the change has it (key, type, state, properties, outgoing links), an item its data (the latest
/// version of a record). None when the change has no such subject.
pub fn text(bb: &Blackboard, key: &str) -> Option<String> {
    let c = &bb.change;
    if key == SUBJECT_DESCRIPTION {
        return Some(format!("{}\n\n{}", c.title.trim(), c.intent.trim()));
    }
    if key == SUBJECT_TITLE {
        return Some(c.title.trim().to_string());
    }
    if key == SUBJECT_INTENT {
        return Some(c.intent.trim().to_string());
    }
    if let Some(node_key) = key.strip_prefix(IMPACT_PREFIX) {
        return c
            .nodes
            .iter()
            .rev()
            .find(|n| n.key == node_key && !n.superseded)
            .map(|n| impact_text(bb, n));
    }
    if let Some(r) = key.strip_prefix(ITEM_PREFIX) {
        for it in c.items.iter().rev() {
            if it.r#type == TYPE_ASSESSMENT || it.r#type == TYPE_COMMENT || !c.active(&it.id) {
                continue;
            }
            if it.id == r || str_of(&it.data, "key") == r {
                let mut doc = Map::new();
                doc.insert("type".into(), it.r#type.as_str().into());
                doc.insert("target".into(), it.target.clone().into());
                doc.insert("data".into(), map_or_null(&it.data));
                return Some(canonical(&Value::Object(doc)));
            }
        }
    }
    None
}

/// impactText is the canonical text of the node of an impact as the change has it.
fn impact_text(bb: &Blackboard, n: &ChangeImpact) -> String {
    let r = n.post.as_ref().or(n.pre.as_ref());
    let mut doc = Map::new();
    doc.insert("key".into(), n.key.clone().into());
    doc.insert("type".into(), n.r#type.clone().into());
    doc.insert("intent".into(), n.intent.as_str().into());
    if let Some(r) = r {
        let v = bb.nodes.get(r).cloned().unwrap_or_default();
        let mut links: Vec<String> = v
            .out
            .iter()
            .map(|l| format!("{}>{}", l.r#type, l.to.id))
            .collect();
        links.sort();
        doc.insert("state".into(), v.state.clone().into());
        doc.insert("props".into(), map_or_null(&v.properties));
        doc.insert("links".into(), links.into());
    }
    canonical(&Value::Object(doc))
}

/// Fingerprint is the short fingerprint of the text of a subject (FNV-1a, 32 bits, hexadecimal): what a rater copies
/// in `rated` when the text is long, shown by the brief next to each subject.
pub fn fingerprint(text: &str) -> String {
    let mut h: u32 = 0x811c_9dc5;
    for b in text.as_bytes() {
        h ^= u32::from(*b);
        h = h.wrapping_mul(0x0100_0193);
    }
    format!("{h:08x}")
}

/// FingerprintOf is the fingerprint of a subject of the change, "" when the change has no such subject.
pub fn fingerprint_of(bb: &Blackboard, key: &str) -> String {
    text(bb, key).map(|t| fingerprint(&t)).unwrap_or_default()
}

/// rates reports whether a rating made on rated is about text.
fn rates(rated: &str, text: &str) -> bool {
    let rated = rated.trim();
    rated == text.trim() || rated == fingerprint(text)
}

/// Assessments returns the latest rating of each subject, in the order the subjects were first rated.
pub fn assessments(bb: &Blackboard) -> Vec<Assessment> {
    let c = &bb.change;
    let mut order: Vec<String> = Vec::new();
    let mut last: HashMap<String, &ChangeItem> = HashMap::new();
    let mut at: HashMap<String, usize> = HashMap::new();
    let mut versions: HashMap<String, i64> = HashMap::new();
    let mut last_comment: HashMap<String, usize> = HashMap::new();
    for (i, it) in c.items.iter().enumerate() {
        if !c.active(&it.id) || it.status == ITEM_REJECTED {
            continue;
        }
        if it.r#type == TYPE_COMMENT {
            last_comment.insert(str_of(&it.data, "about").to_string(), i);
        } else if it.r#type == TYPE_ASSESSMENT {
            let key = str_of(&it.data, "key").to_string();
            if key.is_empty() {
                continue;
            }
            if !last.contains_key(&key) {
                order.push(key.clone());
            }
            last.insert(key.clone(), it);
            at.insert(key.clone(), i);
            *versions.entry(key).or_default() += 1;
        }
    }
    order
        .into_iter()
        .map(|key| {
            let it = last[&key];
            let d = &it.data;
            let mut a = Assessment {
                score: num(d, "score"),
                verdict: str_of(d, "verdict").into(),
                findings: findings(d.get("findings")),
                suggestion: str_of(d, "suggestion").into(),
                reply: str_of(d, "reply").into(),
                rated: str_of(d, "rated").into(),
                item: it.id.clone(),
                by: it.produced_by.clone(),
                versions: versions[&key],
                ..Default::default()
            };
            if let Some(t) = text(bb, &key) {
                a.current = rates(&a.rated, &t);
            }
            a.answered = last_comment.get(&key).is_none_or(|&i| i < at[&key]);
            a.key = key;
            a
        })
        .collect()
}

/// Of returns the latest rating of a subject.
pub fn of(bb: &Blackboard, key: &str) -> Option<Assessment> {
    assessments(bb).into_iter().find(|a| a.key == key)
}

/// Thread returns the messages about a subject in the order they were written: the comments of the persons and the
/// ratings (their reply, else their verdict).
pub fn thread(c: &Change, key: &str) -> Vec<Message> {
    let mut out = Vec::new();
    for it in &c.items {
        if !c.active(&it.id) || it.status == ITEM_REJECTED {
            continue;
        }
        if it.r#type == TYPE_COMMENT && str_of(&it.data, "about") == key {
            out.push(Message {
                r#type: TYPE_COMMENT,
                item: it.id.clone(),
                text: str_of(&it.data, "text").into(),
                by: str_of(&it.data, "by").into(),
                score: 0,
            });
        } else if it.r#type == TYPE_ASSESSMENT && str_of(&it.data, "key") == key {
            let mut text = str_of(&it.data, "reply");
            if text.is_empty() {
                text = str_of(&it.data, "verdict");
            }
            out.push(Message {
                r#type: TYPE_ASSESSMENT,
                item: it.id.clone(),
                text: text.into(),
                by: it.produced_by.clone(),
                score: num(&it.data, "score"),
            });
        }
    }
    out
}

fn findings(v: Option<&Value>) -> Vec<Finding> {
    let Some(Value::Array(list)) = v else {
        return Vec::new();
    };
    list.iter()
        .filter_map(|f| match f {
            Value::String(x) => Some(Finding {
                text: x.clone(),
                severity: String::new(),
            }),
            Value::Object(x) => Some(Finding {
                text: str_of(x, "text").into(),
                severity: str_of(x, "severity").into(),
            }),
            _ => None,
        })
        .collect()
}

fn str_of<'a>(d: &'a Map<String, Value>, k: &str) -> &'a str {
    d.get(k).and_then(Value::as_str).unwrap_or_default()
}

/// A number of the data, truncated toward zero (Go's int conversion); 0 when absent or not a number.
fn num(d: &Map<String, Value>, k: &str) -> i64 {
    match d.get(k) {
        Some(Value::Number(n)) => n
            .as_i64()
            .or_else(|| n.as_f64().map(|f| f as i64))
            .unwrap_or_default(),
        _ => 0,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use goap_domain::{INTENT_CREATED, Node, NodeRef, NodeView, TYPE_ARTIFACT};
    use serde_json::json;

    fn obj(v: Value) -> Map<String, Value> {
        match v {
            Value::Object(m) => m,
            _ => unreachable!(),
        }
    }

    fn board() -> Blackboard {
        let r = NodeRef::new("n1", 0);
        let mut bb = Blackboard {
            change: Change {
                title: "Login".into(),
                intent: "let users sign in".into(),
                nodes: vec![ChangeImpact {
                    id: "i1".into(),
                    key: "REQ-1".into(),
                    r#type: "alm@Requirement".into(),
                    intent: INTENT_CREATED,
                    post: Some(r.clone()),
                    ..Default::default()
                }],
                items: vec![ChangeItem {
                    id: "a1".into(),
                    r#type: TYPE_ARTIFACT,
                    target: "note".into(),
                    data: obj(json!({"key": "spec", "text": "v1"})),
                    ..Default::default()
                }],
                ..Default::default()
            },
            ..Default::default()
        };
        bb.nodes.insert(r, node_view(json!({"title": "Sign in"})));
        bb
    }

    fn node_view(props: Value) -> NodeView {
        NodeView {
            node: Node {
                id: "n1".into(),
                key: "REQ-1".into(),
                properties: obj(props),
                ..Default::default()
            },
            ..Default::default()
        }
    }

    fn rating(key: &str, rated: &str, score: i64) -> ChangeItem {
        ChangeItem {
            id: format!("r-{key}{rated}").into(),
            r#type: TYPE_ASSESSMENT,
            data: obj(json!({"key": key, "score": score, "rated": rated})),
            ..Default::default()
        }
    }

    #[test]
    fn subjects_and_fingerprints() {
        let mut bb = board();
        for key in [
            SUBJECT_DESCRIPTION.to_string(),
            SUBJECT_TITLE.into(),
            SUBJECT_INTENT.into(),
            format!("{IMPACT_PREFIX}REQ-1"),
            format!("{ITEM_PREFIX}spec"),
            format!("{ITEM_PREFIX}a1"),
        ] {
            assert!(
                text(&bb, &key).is_some_and(|t| !t.is_empty()),
                "{key}: no text"
            );
        }
        assert!(
            text(&bb, &format!("{IMPACT_PREFIX}NOPE")).is_none(),
            "an impact the change does not have has no text"
        );
        assert_eq!(
            text(&bb, SUBJECT_DESCRIPTION).unwrap(),
            "Login\n\nlet users sign in"
        );
        // the canonical texts Go writes
        assert_eq!(
            text(&bb, &format!("{IMPACT_PREFIX}REQ-1")).unwrap(),
            r#"{"intent":"created","key":"REQ-1","links":[],"props":{"title":"Sign in"},"state":"","type":"alm@Requirement"}"#
        );
        assert_eq!(
            text(&bb, &format!("{ITEM_PREFIX}spec")).unwrap(),
            r#"{"data":{"key":"spec","text":"v1"},"target":"note","type":"artifact"}"#
        );

        // a rating by the fingerprint and one by the whole text are both current, until the subject changes
        let item = text(&bb, &format!("{ITEM_PREFIX}spec")).unwrap();
        let fp = fingerprint_of(&bb, &format!("{IMPACT_PREFIX}REQ-1"));
        bb.change
            .items
            .push(rating(&format!("{IMPACT_PREFIX}REQ-1"), &fp, 4));
        bb.change
            .items
            .push(rating(&format!("{ITEM_PREFIX}spec"), &item, 2));
        for key in [
            format!("{IMPACT_PREFIX}REQ-1"),
            format!("{ITEM_PREFIX}spec"),
        ] {
            let a = of(&bb, &key);
            assert!(
                a.as_ref().is_some_and(|a| a.current),
                "{key}: current rating expected, got {a:?}"
            );
        }
        bb.nodes.insert(
            NodeRef::new("n1", 0),
            node_view(json!({"title": "Sign in with SSO"})),
        );
        bb.change.items.push(ChangeItem {
            id: "a2".into(),
            r#type: TYPE_ARTIFACT,
            target: "note".into(),
            data: obj(json!({"key": "spec", "text": "v2"})),
            ..Default::default()
        });
        for key in [
            format!("{IMPACT_PREFIX}REQ-1"),
            format!("{ITEM_PREFIX}spec"),
        ] {
            assert!(
                !of(&bb, &key).unwrap().current,
                "{key}: the subject changed, the rating is stale"
            );
        }
        assert_eq!(fingerprint("abc"), "1a47e90b", "FNV-1a 32 of abc");
    }

    #[test]
    fn validation_and_thread() {
        register();
        let ok = |data: Value| {
            ChangeItem {
                r#type: TYPE_ASSESSMENT,
                data: obj(data),
                ..Default::default()
            }
            .validate()
        };
        assert!(ok(json!({"key": "description", "score": 3})).is_ok());
        assert!(ok(json!({"key": "description"})).is_err());
        assert!(ok(json!({"key": "description", "score": 6})).is_err());
        assert!(ok(json!({"key": "description", "score": 2, "verdict": "meh"})).is_err());
        let comment = ChangeItem {
            id: "c1".into(),
            r#type: TYPE_COMMENT,
            data: obj(json!({"about": "description", "text": "why?", "by": "ann"})),
            ..Default::default()
        };
        assert!(comment.validate().is_ok());
        let mut bb = board();
        let mut r = rating(SUBJECT_DESCRIPTION, "x", 2);
        r.data.insert("verdict".into(), "unclear".into());
        r.data.insert(
            "findings".into(),
            json!(["vague", {"text": "who?", "severity": "high"}]),
        );
        bb.change.items.push(r);
        bb.change.items.push(comment);
        let a = of(&bb, SUBJECT_DESCRIPTION).unwrap();
        assert!(
            !a.answered && !a.current && a.findings.len() == 2 && a.findings[1].severity == "high"
        );
        let t = thread(&bb.change, SUBJECT_DESCRIPTION);
        assert!(t.len() == 2 && t[0].text == "unclear" && t[0].score == 2 && t[1].by == "ann");
    }
}
