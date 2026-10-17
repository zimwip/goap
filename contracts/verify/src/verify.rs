//! The verification items of a change and their fold.

use std::{collections::HashMap, sync::Arc};

use goap_domain::{ChangeImpactID, ChangeItem, Error, ItemType, register_item_type};
use serde_json::Value;

/// TypeVerification is the type of the items that record a state of verification: fact.verification in the log.
pub const TYPE_VERIFICATION: ItemType = ItemType::from_static("verification");

// States of a produced effect.
pub const PRODUCED: &str = "produced";
pub const VERIFIED: &str = "verified";
pub const ACCEPTED: &str = "accepted";
pub const ACCEPTED_WITH_RESERVE: &str = "accepted_with_reserve";
pub const REJECTED: &str = "rejected";

// Keys of the data of a verification item.
pub const KEY_STATE: &str = "state";
pub const KEY_ACTION: &str = "action";
pub const KEY_IMPACT: &str = "impact";
pub const KEY_IMPACTS: &str = "impacts";
pub const KEY_ORACLE: &str = "oracle";
pub const KEY_INDEPENDENT: &str = "independent";
pub const KEY_MODEL: &str = "model";
pub const KEY_BY: &str = "by";
/// KeyDerogation is the key of the derogation an accepted_with_reserve entry stands on (goap-risk, ADR 0075 §2).
pub const KEY_DEROGATION: &str = "derogation";

/// Register gives the verification item type its validation (domain::register_item_type; the built-in methodology
/// domain declares it, ADR 0128). The services that write or read such items call it at start, and so does a test
/// that builds them; it may be called any number of times.
pub fn register() {
    register_item_type(TYPE_VERIFICATION, Some(Arc::new(validate)));
}

/// ValidState reports whether s is a state of verification.
pub fn valid_state(s: &str) -> bool {
    [
        PRODUCED,
        VERIFIED,
        ACCEPTED,
        ACCEPTED_WITH_RESERVE,
        REJECTED,
    ]
    .contains(&s)
}

fn str_of<'a>(it: &'a ChangeItem, k: &str) -> &'a str {
    it.data.get(k).and_then(Value::as_str).unwrap_or_default()
}

pub(crate) fn validate(it: &ChangeItem) -> Result<(), Error> {
    let state = str_of(it, KEY_STATE);
    if !valid_state(state) {
        return Err(Error::invalid(format!(
            "verification item: unknown state {state:?}"
        )));
    }
    if str_of(it, KEY_ACTION).is_empty() {
        return Err(Error::invalid("verification item requires data.action"));
    }
    if state == ACCEPTED_WITH_RESERVE && str_of(it, KEY_DEROGATION).is_empty() {
        return Err(Error::invalid(format!(
            "verification item {state} requires data.derogation: no reserve without a derogation"
        )));
    }
    if state != PRODUCED && str_of(it, KEY_IMPACT).is_empty() {
        return Err(Error::invalid(format!(
            "verification item {state} requires data.impact"
        )));
    }
    Ok(())
}

/// Subject is the verification of one effect of an action run: the state its last entry put it in.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Subject {
    pub execution: String,
    pub action: String,
    /// The change impact; empty for a run that produced no change impact.
    pub impact: String,
    pub oracle: String,
    pub independent: bool,
    pub model: String,
    pub state: String,
    /// Who produced it.
    pub producer: String,
    /// The last one who judged it.
    pub by: String,
    /// The derogation an accepted_with_reserve stands on.
    pub derogation: String,
    /// Position of the last entry in the items.
    pub seq: usize,
}

impl Subject {
    /// Open reports whether the effect still waits for a verdict.
    pub fn open(&self) -> bool {
        self.state == PRODUCED || self.state == VERIFIED
    }
}

/// Subjects folds the verification items of a change, in the order they were written, into the state of each effect.
/// It is pure: the same items give the same subjects. A produced entry creates one subject per change impact it covers
/// (one without impact when it covers none); the later entries of an impact move its subject.
pub fn subjects(items: &[ChangeItem]) -> Vec<Subject> {
    let mut out: Vec<Subject> = Vec::new();
    let mut by_impact: HashMap<String, usize> = HashMap::new(); // impact id -> index in out
    for (n, it) in items.iter().enumerate() {
        if it.r#type != TYPE_VERIFICATION {
            continue;
        }
        let state = str_of(it, KEY_STATE);
        let s = |k: &str| str_of(it, k).to_string();
        if state == PRODUCED {
            let base = Subject {
                execution: it.execution.clone(),
                action: s(KEY_ACTION),
                oracle: s(KEY_ORACLE),
                model: s(KEY_MODEL),
                state: PRODUCED.into(),
                producer: it.produced_by.clone(),
                seq: n,
                independent: it
                    .data
                    .get(KEY_INDEPENDENT)
                    .and_then(Value::as_bool)
                    .unwrap_or(false),
                ..Default::default()
            };
            let impacts = str_list(it.data.get(KEY_IMPACTS));
            if impacts.is_empty() {
                out.push(base);
                continue;
            }
            for id in impacts {
                let mut sub = base.clone();
                sub.impact = id.clone();
                if let Some(&i) = by_impact.get(&id) {
                    out[i] = sub; // produced again (a relaunch): starts over
                } else {
                    by_impact.insert(id, out.len());
                    out.push(sub);
                }
            }
            continue;
        }
        if let Some(&i) = by_impact.get(str_of(it, KEY_IMPACT))
            && valid_state(state)
        {
            let sub = &mut out[i];
            sub.state = state.to_string();
            sub.seq = n;
            sub.derogation = String::new();
            if state == ACCEPTED_WITH_RESERVE {
                sub.derogation = s(KEY_DEROGATION);
            }
            let by = s(KEY_BY);
            if !by.is_empty() {
                sub.by = by;
            }
        }
    }
    out
}

/// Independent returns the verification an action run declared for a change impact when it asked for a verifier that
/// differs from the producer, if there is one.
pub fn independent(items: &[ChangeItem], impact: &ChangeImpactID) -> Option<Subject> {
    subjects(items)
        .into_iter()
        .find(|s| s.impact == impact.as_str() && s.independent)
}

fn str_list(v: Option<&Value>) -> Vec<String> {
    match v {
        Some(Value::Array(l)) => l
            .iter()
            .filter_map(|x| x.as_str().map(str::to_string))
            .collect(),
        _ => Vec::new(),
    }
}

/// Impacts returns the change impacts a verification item covers (data.impacts, and data.impact).
pub fn impacts(it: &ChangeItem) -> Vec<String> {
    let mut out = str_list(it.data.get(KEY_IMPACTS));
    let s = str_of(it, KEY_IMPACT);
    if !s.is_empty() {
        out.push(s.to_string());
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::Policy;
    use goap_domain::{ChangeImpact, REVIEW_ACCEPTED, ReviewPolicy, ReviewRequest, TYPE_ARTIFACT};
    use serde_json::{Map, json};

    fn obj(v: Value) -> Map<String, Value> {
        match v {
            Value::Object(m) => m,
            _ => unreachable!(),
        }
    }

    fn item(id: &str, execution: &str, by: &str, data: Value) -> ChangeItem {
        ChangeItem {
            id: id.into(),
            r#type: TYPE_VERIFICATION,
            execution: execution.into(),
            produced_by: by.into(),
            data: obj(data),
            ..Default::default()
        }
    }

    fn produced(
        id: &str,
        execution: &str,
        by: &str,
        independent: bool,
        impacts: &[&str],
    ) -> ChangeItem {
        item(
            id,
            execution,
            by,
            json!({KEY_STATE: PRODUCED, KEY_ACTION: by, KEY_ORACLE: "human", KEY_INDEPENDENT: independent, KEY_IMPACTS: impacts}),
        )
    }

    fn judged(id: &str, state: &str, impact: &str, by: &str) -> ChangeItem {
        item(
            id,
            "e2",
            by,
            json!({KEY_STATE: state, KEY_ACTION: by, KEY_IMPACT: impact, KEY_BY: by}),
        )
    }

    #[test]
    fn validate_items() {
        for (name, data, msg) in [
            (
                "state",
                json!({KEY_STATE: "done", KEY_ACTION: "a"}),
                "unknown state",
            ),
            ("action", json!({KEY_STATE: PRODUCED}), "data.action"),
            (
                "impact",
                json!({KEY_STATE: ACCEPTED, KEY_ACTION: "a"}),
                "data.impact",
            ),
            (
                "ok",
                json!({KEY_STATE: REJECTED, KEY_ACTION: "a", KEY_IMPACT: "i1"}),
                "",
            ),
        ] {
            let r = validate(&item("1", "e", "a", data));
            match r {
                Ok(()) => assert!(msg.is_empty(), "{name}: accepted"),
                Err(e) => assert!(
                    !msg.is_empty() && e.to_string().contains(msg),
                    "{name}: {e}"
                ),
            }
        }
    }

    /// The fold is pure: one subject per effect, moved by the later entries; a produced entry again starts over.
    #[test]
    fn subjects_fold() {
        let mut items = vec![
            produced("1", "e1", "write", true, &["i1", "i2"]),
            judged("2", VERIFIED, "i1", "check"),
            judged("3", ACCEPTED, "i1", "check"),
            judged("4", REJECTED, "i2", "check"),
            ChangeItem {
                id: "x".into(),
                r#type: TYPE_ARTIFACT,
                ..Default::default()
            },
        ];
        let got = subjects(&items);
        assert!(
            got.len() == 2
                && got[0].impact == "i1"
                && got[0].state == ACCEPTED
                && got[0].by == "check"
                && got[0].producer == "write"
                && got[0].independent
                && got[1].state == REJECTED
                && !got[1].open()
                && !got[0].open(),
            "{got:?}"
        );
        items.push(produced("5", "e3", "write", true, &["i2"]));
        let again = subjects(&items);
        assert!(
            again.len() == 2
                && again[1].state == PRODUCED
                && again[1].open()
                && again[1].execution == "e3",
            "a rerun starts over: {again:?}"
        );
        let s = subjects(&items[..1]);
        assert!(s.len() == 2 && s[0].open(), "produced only: {s:?}");
        let s = subjects(&[produced("1", "e1", "a", true, &[])]);
        assert!(
            s.len() == 1 && s[0].impact.is_empty(),
            "a run with no impact: {s:?}"
        );
        assert!(
            subjects(&[judged("1", ACCEPTED, "i9", "b")]).is_empty(),
            "a verdict with no produced entry"
        );
    }

    /// The verifier is not the producer when the effect declared an independent verification, and only then.
    #[test]
    fn policy() {
        let imp = ChangeImpact {
            id: "i1".into(),
            key: "REQ-1".into(),
            produced_by: "write".into(),
            execution: "e1".into(),
            ..Default::default()
        };
        let req = |reviewer: &str, items: Vec<ChangeItem>| ReviewRequest {
            impact: imp.clone(),
            reviewer: reviewer.into(),
            status: REVIEW_ACCEPTED,
            items,
        };
        let p = Policy;
        let err = p
            .review(&req(
                "write",
                vec![produced("1", "e1", "write", true, &["i1"])],
            ))
            .unwrap_err();
        assert!(
            err.to_string().contains("may not verify"),
            "producer == reviewer: {err}"
        );
        p.review(&req(
            "check",
            vec![produced("1", "e1", "write", true, &["i1"])],
        ))
        .expect("another reviewer");
        p.review(&req(
            "write",
            vec![produced("1", "e1", "write", false, &["i1"])],
        ))
        .expect("not independent");
        p.review(&req("write", vec![]))
            .expect("no verification declared");
        // the producer is the one who wrote the effect, even when another declared the impact
        assert!(
            p.review(&req(
                "rewrite",
                vec![produced("1", "e2", "rewrite", true, &["i1"])]
            ))
            .is_err(),
            "the writer may not verify"
        );
        // a model alias recorded on the produced entry
        let mut m = produced("1", "e1", "write", true, &["i1"]);
        m.data.insert(KEY_MODEL.into(), "gpt-a".into());
        assert!(
            p.review(&req("gpt-a", vec![m])).is_err(),
            "the model alias of the producer"
        );
    }

    /// accepted_with_reserve stands on a derogation: the entry names it, and the subject keeps it until the next
    /// verdict.
    #[test]
    fn accepted_with_reserve_names_its_derogation() {
        register();
        let mut it = judged("2", ACCEPTED_WITH_RESERVE, "i1", "carol");
        let err = it.validate().unwrap_err();
        assert!(
            err.to_string().contains("data.derogation"),
            "a reserve without a derogation: {err}"
        );
        it.data.insert(KEY_DEROGATION.into(), "DRG-1".into());
        it.validate().expect("valid");
        let mut items = vec![produced("1", "e1", "bob", true, &["i1"]), it];
        let s = subjects(&items);
        assert!(
            s.len() == 1 && s[0].state == ACCEPTED_WITH_RESERVE && s[0].derogation == "DRG-1",
            "reserve: {s:?}"
        );
        // produced again (the derogation expired): starts over, with no derogation
        items.push(produced("3", "e1", "bob", true, &["i1"]));
        let s = subjects(&items);
        assert!(
            s.len() == 1 && s[0].state == PRODUCED && s[0].derogation.is_empty(),
            "starts over: {s:?}"
        );
    }

    #[test]
    fn impacts_of_an_item() {
        let it = item(
            "1",
            "e",
            "a",
            json!({KEY_IMPACTS: ["i1", "i2"], KEY_IMPACT: "i3"}),
        );
        assert_eq!(impacts(&it), ["i1", "i2", "i3"]);
    }
}
