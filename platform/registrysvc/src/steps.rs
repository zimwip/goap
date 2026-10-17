//! Port of `steps.go`: the steps of the processes and methods as nodes of their own (ADR 0120).

use std::collections::{BTreeMap, HashSet};

use serde_json::{Map, Value};

use crate::defs::{DefEl, KIND_METHOD, KIND_METHOD_STEP, KIND_PROCESS, KIND_STEP};

/// stepChildren takes the steps of every process and method element out of its properties and returns one DefEl per
/// step and sub-step (methodology@Step for a process, methodology@MethodStep for a method), keyed by its full path
/// (`<process>/<step>/<sub-step>`), plus the sub_activity children each parent (a process, method or step element,
/// named `<kind>/<name-or-path>`) is composed of, in their order. A step is a node of its own and nothing else holds
/// it (ADR 0120): the definition reads the steps of a process back from its sub_activity links, each step at its
/// position among its siblings.
pub(crate) fn step_children(els: &mut [DefEl]) -> (Vec<DefEl>, BTreeMap<String, Vec<String>>) {
    let mut step_els = Vec::new();
    let mut children: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for e in els.iter_mut() {
        let kind = match e.kind.as_str() {
            KIND_PROCESS => KIND_STEP,
            KIND_METHOD => KIND_METHOD_STEP,
            _ => continue,
        };
        let raw = match e.props.remove("steps") {
            Some(Value::Array(l)) => l,
            _ => Vec::new(),
        };
        if !raw.is_empty() {
            let parent = format!("{}/{}", e.kind, e.name);
            walk(&parent, &e.name, kind, &raw, &mut step_els, &mut children);
        }
    }
    (step_els, children)
}

fn walk(
    parent_rel: &str,
    path: &str,
    kind: &str,
    raw: &[Value],
    step_els: &mut Vec<DefEl>,
    children: &mut BTreeMap<String, Vec<String>>,
) {
    let mut seen: HashSet<String> = HashSet::new();
    for (i, item) in raw.iter().enumerate() {
        let Some(sm) = item.as_object() else { continue };
        let name = sm.get("name").and_then(Value::as_str).unwrap_or_default();
        if name.is_empty() {
            continue;
        }
        let mut seg = name.to_string();
        if seen.contains(&seg) {
            // names that repeat are told apart by position, as the elements are (split)
            seg = format!("{seg}~{i}");
        }
        seen.insert(seg.clone());
        let p = format!("{path}/{seg}");
        let rel = format!("{kind}/{p}");
        children
            .entry(parent_rel.to_string())
            .or_default()
            .push(rel.clone());
        let mut props = step_node_props(sm);
        props.insert("position".into(), Value::from(i as i64));
        step_els.push(DefEl {
            kind: kind.to_string(),
            name: p.clone(),
            props,
        });
        if let Some(Value::Array(sub)) = sm.get("steps")
            && !sub.is_empty()
        {
            walk(&rel, &p, kind, sub, step_els, children);
        }
    }
}

/// stepNodeProps is a step's own node properties: its JSON, with pre/done renamed to the Activity shape's
/// input/output, and its nested steps dropped (they are nodes of their own, reached through sub_activity).
pub(crate) fn step_node_props(raw: &Map<String, Value>) -> Map<String, Value> {
    let mut p = raw.clone();
    if let Some(v) = p.remove("pre") {
        p.insert("input".into(), v);
    }
    if let Some(v) = p.remove("done") {
        p.insert("output".into(), v);
    }
    p.remove("steps");
    p
}

/// stepJSON is the JSON of a step in its process or method, from the properties of its node (references put back):
/// input/output named pre/done again, its position and removed mark dropped.
pub(crate) fn step_json(props: &Map<String, Value>) -> Map<String, Value> {
    let mut p = props.clone();
    if let Some(v) = p.remove("input") {
        p.insert("pre".into(), v);
    }
    if let Some(v) = p.remove("output") {
        p.insert("done".into(), v);
    }
    p.remove("position");
    p.remove("removed");
    p
}
