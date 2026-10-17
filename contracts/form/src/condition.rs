//! Conditional fields (`condition.go`).
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{
    form::{Field, Issue, Issues, empty, normalize, quote, values_equal},
    widgets::{ValueKind, lookup},
};

/// A test on the value of another field of the form, which comes before it (ADR 0115, conditional fields): exactly
/// one of `equals`, `in` or `set`. On a list value (a multiselect, several nodes) `equals` and `in` hold when the
/// list holds one of the values.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct Condition {
    /// Names the field tested: an earlier field of the same list of fields.
    #[serde(default)]
    pub field: String,
    /// Holds when the value is this one.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub equals: Option<Value>,
    /// Holds when the value is one of these (`Some` of an empty list is a test, written as none).
    #[serde(default, rename = "in", skip_serializing_if = "none_or_empty")]
    pub r#in: Option<Vec<Value>>,
    /// Holds when the field is filled in (true) or left blank (false).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub set: Option<bool>,
}

fn none_or_empty(v: &Option<Vec<Value>>) -> bool {
    v.as_ref().is_none_or(Vec::is_empty)
}

impl Condition {
    /// Reports whether the condition holds on the value of its field (`Null`: left blank or hidden).
    pub fn holds(&self, v: &Value) -> bool {
        if let Some(set) = self.set {
            return empty(v) != set;
        }
        if empty(v) {
            return false;
        }
        let want: Vec<Value> = match &self.equals {
            Some(e) => vec![e.clone()],
            None => self.r#in.clone().unwrap_or_default(),
        };
        let got = match normalize(v) {
            Value::Array(l) => l,
            g => vec![g],
        };
        got.iter()
            .any(|g| want.iter().any(|w| values_equal(g, &normalize(w))))
    }
}

/// Reports whether every condition holds on values (none: true).
pub fn all_hold(cs: &[Condition], values: &Map<String, Value>) -> bool {
    cs.iter()
        .all(|c| c.holds(values.get(&c.field).unwrap_or(&Value::Null)))
}

impl Field {
    /// Reports whether a field is shown, checked and submitted given the values of the fields before it.
    pub fn visible(&self, values: &Map<String, Value>) -> bool {
        all_hold(&self.visible_when, values)
    }

    /// Reports whether a field must be filled in given the values of the fields before it.
    pub fn required_for(&self, values: &Map<String, Value>) -> bool {
        self.required || !self.required_when.is_empty() && all_hold(&self.required_when, values)
    }
}

/// Reports the problems of the conditions of the j-th field of a list.
pub(crate) fn check_conditions(
    fields: &[Field],
    j: usize,
    key: &str,
    cs: &[Condition],
    at: &str,
) -> Issues {
    let mut out = Issues::default();
    for (i, c) in cs.iter().enumerate() {
        let p = format!("{at}.{key}[{i}]");
        let n = [c.equals.is_some(), c.r#in.is_some(), c.set.is_some()]
            .iter()
            .filter(|b| **b)
            .count();
        if n != 1 {
            out.push(Issue::at(
                p.clone(),
                "a condition tests exactly one of equals, in or set",
            ));
        }
        let k = (0..j).rev().find(|&x| fields[x].name == c.field);
        match k {
            None => out.push(Issue::at(
                format!("{p}.field"),
                format!("no field {} before this one", quote(&c.field)),
            )),
            Some(k) => {
                if let Some(w) = lookup(&fields[k].widget)
                    && matches!(
                        w.value,
                        ValueKind::None | ValueKind::Links | ValueKind::Objects | ValueKind::Object
                    )
                {
                    out.push(Issue::at(
                        format!("{p}.field"),
                        format!(
                            "field {} is a {}: a condition tests a value",
                            quote(&c.field),
                            w.name
                        ),
                    ));
                }
            }
        }
    }
    out
}
