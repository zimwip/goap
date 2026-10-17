//! Persistent agents (ADR 0112) (`persist.go`).

use std::collections::HashSet;

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::process::Add;
use crate::util::{Conds, is_zero_i64, nd, q, st};

/// Persistence makes an agent persistent (ADR 0112): one instance per scope, run again in place for every new
/// execution on that scope.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Persistence {
    /// Scope is what one instance is for: "change" (the default), or a path into the event that starts the run.
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub scope: String,
    /// Until closes the instance: once a run of it completes with these conditions holding.
    #[serde(skip_serializing_if = "Conds::is_empty", deserialize_with = "nd")]
    pub until: Conds,
    /// Memory is the number of exchanges of its llm actions the instance keeps (0: the default).
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub memory: i64,
}

// Scopes and bounds of a persistent agent.
pub const SCOPE_CHANGE: &str = "change";
pub const DEFAULT_MEMORY: i64 = 6;
pub const MAX_MEMORY: i64 = 20;

impl Persistence {
    /// Turns is the number of exchanges the instance keeps (0 for no persistence).
    pub fn turns(p: Option<&Persistence>) -> i64 {
        match p {
            None => 0,
            Some(p) if p.memory <= 0 => DEFAULT_MEMORY,
            Some(p) if p.memory > MAX_MEMORY => MAX_MEMORY,
            Some(p) => p.memory,
        }
    }

    /// ScopePath is the path of the scope ("change" when none is given).
    pub fn scope_path(p: Option<&Persistence>) -> &str {
        match p {
            Some(p) if !p.scope.is_empty() => &p.scope,
            _ => SCOPE_CHANGE,
        }
    }

    /// Resolve gives the value of the scope of a run on a change, with the event that starts it ("" when the path
    /// leads to nothing: the run is then not persistent).
    pub fn resolve(p: Option<&Persistence>, change: &str, event: &Map<String, Value>) -> String {
        let path = Self::scope_path(p);
        if path == SCOPE_CHANGE {
            return change.to_string();
        }
        let Some(rest) = path.strip_prefix("event.") else {
            return String::new();
        };
        let mut cur = Value::Object(event.clone());
        for part in rest.split('.') {
            let Value::Object(m) = cur else {
                return String::new();
            };
            cur = m.get(part).cloned().unwrap_or(Value::Null);
        }
        match cur {
            Value::String(s) => s,
            Value::Null => String::new(),
            Value::Bool(b) => b.to_string(),
            Value::Number(n) => go_number(&n),
            other => other.to_string(),
        }
    }
}

/// A JSON number as Go's fmt.Sprint prints a decoded float64 (or integer).
fn go_number(n: &serde_json::Number) -> String {
    if let Some(i) = n.as_i64() {
        return i.to_string();
    }
    if let Some(u) = n.as_u64() {
        return u.to_string();
    }
    let f = n.as_f64().unwrap_or(0.0);
    if f.fract() == 0.0 && f.abs() < 1e21 {
        format!("{f:.0}")
    } else {
        f.to_string()
    }
}

/// checkPersistence checks the persistence of an agent or a method at path.
pub(crate) fn check_persistence(
    add: &mut Add<'_>,
    path: &str,
    p: Option<&Persistence>,
    known: &HashSet<String>,
) {
    let Some(pp) = p else { return };
    let s = Persistence::scope_path(p);
    if s != SCOPE_CHANGE {
        let ok = match s.strip_prefix("event.") {
            Some(rest) => !rest.is_empty() && !rest.contains("..") && !rest.ends_with('.'),
            None => false,
        };
        if !ok {
            add(
                format!("{path}.persistent.scope"),
                format!(
                    "scope must be {} or a path into the event (event.<field>...), got {}",
                    q(SCOPE_CHANGE),
                    q(s)
                ),
            );
        }
    }
    for c in pp.until.keys() {
        if !known.contains(c) {
            add(
                format!("{path}.persistent.until.{c}"),
                format!("unknown condition {}", q(c)),
            );
        }
    }
    if pp.memory < 0 || pp.memory > MAX_MEMORY {
        add(
            format!("{path}.persistent.memory"),
            format!(
                "memory is the number of exchanges kept, from 0 (the default, {DEFAULT_MEMORY}) to {MAX_MEMORY}"
            ),
        );
    }
}
