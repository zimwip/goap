//! A version of a methodology is a node (its header: the scalar fields, the status, the
//! timestamps) and one node per element (condition, action, goal, agent, process, method, role, step), keyed
//! `<header key>/<kind>/<name>` and typed by the built-in meta-domain methodology (`methodology@Agent`, ...; ADR
//! 0023); the references between elements are links (ADR 0120, refs.rs). The version is the graph content; the
//! definition a caller gets is assembled from it.

use std::collections::{BTreeMap, HashSet};

use goap_methodology::{
    ELEMENT_ACTION, ELEMENT_AGENT, ELEMENT_CONDITION, ELEMENT_GOAL, ELEMENT_METHOD,
    ELEMENT_METHOD_STEP, ELEMENT_PROCESS, ELEMENT_ROLE, ELEMENT_STEP, Methodology,
};
use serde_json::{Map, Value};

use crate::error::{Error, Result};

pub(crate) const KIND_CONDITION: &str = ELEMENT_CONDITION;
pub(crate) const KIND_ACTION: &str = ELEMENT_ACTION;
pub(crate) const KIND_GOAL: &str = ELEMENT_GOAL;
pub(crate) const KIND_AGENT: &str = ELEMENT_AGENT;
pub(crate) const KIND_PROCESS: &str = ELEMENT_PROCESS;
pub(crate) const KIND_METHOD: &str = ELEMENT_METHOD;
pub(crate) const KIND_ROLE: &str = ELEMENT_ROLE;
/// The steps of a process / method, materialized as their own nodes (one per step and sub-step): they fill no
/// top-level collection of their own (field ""), the definition reads them back as the steps of their process or
/// method, from its sub_activity links (ADR 0120, steps.rs).
pub(crate) const KIND_STEP: &str = ELEMENT_STEP;
pub(crate) const KIND_METHOD_STEP: &str = ELEMENT_METHOD_STEP;

/// A kind of element: its kind, the JSON field of the collection it belongs to and its node type.
pub(crate) struct DefKind {
    pub kind: &'static str,
    pub field: &'static str,
    pub node_type: &'static str,
}

/// defKinds lists the element kinds with the JSON field of the collection they belong to.
pub(crate) const DEF_KINDS: &[DefKind] = &[
    DefKind {
        kind: KIND_CONDITION,
        field: "conditions",
        node_type: "methodology@Condition",
    },
    DefKind {
        kind: KIND_ACTION,
        field: "actions",
        node_type: "methodology@Action",
    },
    DefKind {
        kind: KIND_GOAL,
        field: "goals",
        node_type: "methodology@Goal",
    },
    DefKind {
        kind: KIND_AGENT,
        field: "agents",
        node_type: "methodology@Agent",
    },
    DefKind {
        kind: KIND_PROCESS,
        field: "processes",
        node_type: "methodology@Process",
    },
    DefKind {
        kind: KIND_METHOD,
        field: "methods",
        node_type: "methodology@Method",
    },
    DefKind {
        kind: KIND_ROLE,
        field: "roles",
        node_type: "methodology@Role",
    },
    DefKind {
        kind: KIND_STEP,
        field: "",
        node_type: "methodology@Step",
    },
    DefKind {
        kind: KIND_METHOD_STEP,
        field: "",
        node_type: "methodology@MethodStep",
    },
];

/// nodeTypeOf returns the graph node type of an element kind ("" when unknown).
pub(crate) fn node_type_of(kind: &str) -> &'static str {
    DEF_KINDS
        .iter()
        .find(|k| k.kind == kind)
        .map(|k| k.node_type)
        .unwrap_or("")
}

/// kindOfType returns the element kind of a node type ("" when it is none).
pub(crate) fn kind_of_type(t: &str) -> &'static str {
    DEF_KINDS
        .iter()
        .find(|k| k.node_type == t)
        .map(|k| k.kind)
        .unwrap_or("")
}

/// An element of a definition.
#[derive(Debug, Clone, PartialEq)]
pub(crate) struct DefEl {
    pub kind: String,
    pub name: String,
    /// The element as JSON, with its position in the collection.
    pub props: Map<String, Value>,
}

/// The `position` property of an element (0 when it has none).
pub(crate) fn position(p: &Map<String, Value>) -> f64 {
    p.get("position").and_then(Value::as_f64).unwrap_or(0.0)
}

/// split cuts a definition, given as JSON, into its header (what is not a collection) and its elements.
pub(crate) fn split(raw: &Map<String, Value>) -> (Map<String, Value>, Vec<DefEl>) {
    let mut header = raw.clone();
    let mut els = Vec::new();
    for k in DEF_KINDS {
        if k.field.is_empty() {
            continue;
        }
        let list = match header.remove(k.field) {
            Some(Value::Array(l)) => l,
            _ => Vec::new(),
        };
        let mut seen: HashSet<String> = HashSet::new();
        for (i, item) in list.into_iter().enumerate() {
            let Value::Object(mut p) = item else { continue };
            let mut name = p
                .get("name")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_string();
            if seen.contains(&name) {
                // names that repeat are told apart by position
                name = format!("{name}~{i}");
            }
            seen.insert(name.clone());
            p.insert("position".into(), Value::from(i as i64));
            els.push(DefEl {
                kind: k.kind.to_string(),
                name,
                props: p,
            });
        }
    }
    (header, els)
}

/// join assembles the JSON of a definition from its header and elements (in position order).
pub(crate) fn join(
    header: &Map<String, Value>,
    els: Vec<Map<String, Value>>,
    kinds: &[String],
) -> Map<String, Value> {
    let mut raw = header.clone();
    let mut by_kind: BTreeMap<&str, Vec<Map<String, Value>>> = BTreeMap::new();
    for (e, k) in els.into_iter().zip(kinds) {
        by_kind.entry(k.as_str()).or_default().push(e);
    }
    for k in DEF_KINDS {
        let Some(mut list) = by_kind.remove(k.kind) else {
            continue;
        };
        if list.is_empty() || k.field.is_empty() {
            continue;
        }
        list.sort_by(|a, b| position(a).total_cmp(&position(b)));
        let out: Vec<Value> = list
            .into_iter()
            .map(|mut p| {
                p.remove("position");
                Value::Object(p)
            })
            .collect();
        raw.insert(k.field.into(), Value::Array(out));
    }
    raw
}

/// The JSON object of a value.
pub(crate) fn to_map<T: serde::Serialize>(v: &T) -> Result<Map<String, Value>> {
    match serde_json::to_value(v).map_err(|e| Error::Other(e.to_string()))? {
        Value::Object(m) => Ok(m),
        Value::Null => Ok(Map::new()),
        other => Err(Error::Other(format!("not an object: {other}"))),
    }
}

/// encodeMethodology cuts a definition into its header and elements.
pub(crate) fn encode_methodology(m: &Methodology) -> Result<(Map<String, Value>, Vec<DefEl>)> {
    let raw = to_map(m)?;
    Ok(split(&raw))
}

/// decodeInto assembles a definition from its header and elements.
pub(crate) fn decode_into(
    header: &Map<String, Value>,
    els: Vec<Map<String, Value>>,
    kinds: &[String],
) -> Result<Methodology> {
    let raw = join(header, els, kinds);
    serde_json::from_value(Value::Object(raw)).map_err(|e| Error::Other(format!("definition: {e}")))
}
