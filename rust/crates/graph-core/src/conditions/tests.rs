//! The shipped methodologies, stored as the registry writes them: the definition read back, the conditions derived
//! and their values on the activation cases, each what Go computes (`registrysvc` `TestRustMethodologyCasesAreCurrent`).
use std::collections::BTreeMap;

use serde_json::{Map, Value};

use graph_script::conditions::Set;

use super::{derive, stored};

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

/// Every number a double, to compare JSON written by Go with JSON read by Rust.
fn doubles(v: &Value) -> Value {
    match v {
        Value::Number(n) => n
            .as_f64()
            .and_then(serde_json::Number::from_f64)
            .map_or(Value::Null, Value::Number),
        Value::Array(a) => Value::Array(a.iter().map(doubles).collect()),
        Value::Object(m) => Value::Object(m.iter().map(|(k, v)| (k.clone(), doubles(v))).collect()),
        v => v.clone(),
    }
}

fn cases() -> Vec<Value> {
    let v: Value = serde_json::from_str(include_str!("methodology_cases.gen.json")).unwrap();
    v["cases"].as_array().cloned().unwrap()
}

fn definition(c: &Value) -> Map<String, Value> {
    let nodes: Vec<stored::Node> = c["nodes"]
        .as_array()
        .unwrap()
        .iter()
        .map(|n| stored::Node {
            key: n["key"].as_str().unwrap(),
            r#type: n["type"].as_str().unwrap(),
            props: n["props"].as_object().unwrap(),
        })
        .collect();
    let links: Vec<stored::Link> = c["links"]
        .as_array()
        .unwrap()
        .iter()
        .map(|l| stored::Link {
            from: l["from"].as_str().unwrap(),
            r#type: l["type"].as_str().unwrap(),
            to: l["to"].as_str().unwrap(),
            props: l.get("props").and_then(Value::as_object),
        })
        .collect();
    let header = nodes.iter().find(|n| n.r#type == stored::TYPE_VERSION).unwrap().key;
    stored::definition(header, &nodes, &links).unwrap()
}

#[test]
fn reads_a_stored_version_back() {
    for c in cases() {
        let got = doubles(&Value::Object(definition(&c)));
        assert_eq!(got, doubles(&c["definition"]), "{}", c["file"]);
    }
}

#[test]
fn derives_the_conditions_go_compiles() {
    for c in cases() {
        let d = derive::derive(&definition(&c));
        let set = Set::compile(&d.conditions).unwrap_or_else(|e| panic!("{}: {e}", c["file"]));
        let want: BTreeMap<String, String> = serde_json::from_value(c["conditions"].clone()).unwrap();
        let got = set.exprs();
        for (k, w) in &want {
            assert_eq!(got.get(k), Some(w), "{}: {k}", c["file"]);
        }
        assert_eq!(got, want, "{}", c["file"]);
    }
}

#[test]
fn reads_the_step_dependencies_go_compiles() {
    for c in cases() {
        let got: BTreeMap<String, Vec<String>> = derive::derive(&definition(&c))
            .step_deps
            .into_iter()
            .filter(|(_, on)| !on.is_empty())
            .map(|(later, on)| (later, on.into_iter().collect()))
            .collect();
        let want: BTreeMap<String, Vec<String>> = serde_json::from_value(c["stepDeps"].clone()).unwrap_or_default();
        for (k, w) in &want {
            assert_eq!(got.get(k), Some(w), "{}: {k}", c["file"]);
        }
        assert_eq!(got, want, "{}", c["file"]);
    }
}

#[test]
fn evaluates_the_conditions_as_go_does() {
    let acts: Value = serde_json::from_str(include_str!("activation_cases.gen.json")).unwrap();
    let acts: BTreeMap<String, Map<String, Value>> = acts["cases"]
        .as_array()
        .unwrap()
        .iter()
        .map(|c| {
            (
                c["name"].as_str().unwrap().to_string(),
                untyped(&c["activation"]).as_object().cloned().unwrap(),
            )
        })
        .collect();
    for c in cases() {
        let set = Set::compile(&derive::derive(&definition(&c)).conditions).unwrap();
        for e in c["evaluations"].as_array().unwrap() {
            let board = e["board"].as_str().unwrap();
            let got = set.evaluate(&acts[board]);
            let want: BTreeMap<String, bool> = serde_json::from_value(e["state"].clone()).unwrap();
            let what = format!("{} on {board}", c["file"]);
            for (k, w) in &want {
                assert_eq!(got.state.get(k), Some(w), "{what}: {k} ({:?})", got.errors.get(k));
            }
            assert_eq!(got.state, want, "{what}");
            let errors: Vec<String> = serde_json::from_value(e["errors"].clone()).unwrap_or_default();
            assert_eq!(
                got.errors.keys().cloned().collect::<Vec<_>>(),
                errors,
                "{what}: errors {:?}",
                got.errors
            );
            let subjects = serde_json::to_value(&got.subjects).unwrap();
            let want = e.get("subjects").cloned().unwrap_or_else(|| Value::Object(Map::new()));
            let strip = |v: &Value| -> Value {
                // the words of an error are the engine's: which subject and condition failed is compared
                let mut v = v.clone();
                for list in v.as_object_mut().into_iter().flat_map(|m| m.values_mut()) {
                    for s in list.as_array_mut().into_iter().flatten() {
                        if let Some(Value::Object(errs)) = s.get_mut("errors") {
                            errs.values_mut().for_each(|m| *m = Value::Null);
                        }
                    }
                }
                v
            };
            assert_eq!(strip(&subjects), strip(&want), "{what}: subjects");
        }
    }
}
