//! The shipped methodologies (and broken ones) compile as in Go: the goldens of testdata were dumped from the Go
//! implementation (issues, view, compiled actions and agents, conditions, reactions, step dependencies, levels,
//! graphs, automatic reviews), compared once normalised (no null, empty or zero-valued key: Go writes nil collections
//! as null, the port omits them; numbers as floats; issues as a set, Go's order within one path following its map
//! iteration).

use std::path::{Path, PathBuf};
use std::sync::Arc;

use serde_json::{Map, Value, json};

use crate::compile::CompileOptions;
use crate::domain::load_file;

fn normalise(v: Value) -> Option<Value> {
    match v {
        Value::Null => None,
        Value::Bool(false) => None,
        Value::String(s) if s.is_empty() => None,
        Value::Number(n) => Some(json!(n.as_f64().unwrap_or_default())),
        Value::Array(a) => {
            let out: Vec<Value> = a
                .into_iter()
                .map(|x| normalise(x).unwrap_or(Value::Null))
                .collect();
            if out.is_empty() {
                None
            } else {
                Some(Value::Array(out))
            }
        }
        Value::Object(o) => {
            let mut out = Map::new();
            for (k, x) in o {
                if let Some(x) = normalise(x) {
                    out.insert(k, x);
                }
            }
            if out.is_empty() {
                None
            } else {
                Some(Value::Object(out))
            }
        }
        x => Some(x),
    }
}

fn sorted(v: &Value) -> Value {
    let mut list: Vec<String> = v
        .as_array()
        .map(|a| a.iter().map(|x| x.to_string()).collect())
        .unwrap_or_default();
    list.sort();
    Value::Array(list.into_iter().map(Value::String).collect())
}

fn dump(path: &Path) -> Value {
    let m = load_file(path)
        .unwrap_or_else(|e| panic!("{}: {e}", path.display()))
        .with_builtins(Arc::new(goap_builtins::Known));
    let mut out = Map::new();
    let (c, issues) = m.compile_with(CompileOptions {
        lenient: true,
        stored: true,
    });
    out.insert("issues".into(), serde_json::to_value(&issues.0).unwrap());
    let (_, strict) = m.compile_with(CompileOptions::default());
    out.insert(
        "strictIssues".into(),
        serde_json::to_value(&strict.0).unwrap(),
    );
    if let Some(c) = c {
        out.insert("view".into(), serde_json::to_value(&*c.view()).unwrap());
        let acts: Vec<Value> = c
            .actions
            .values()
            .map(|a| {
                json!({"name": a.name, "kind": a.kind, "step": a.step, "implements": a.implements, "pre": a.pre,
                    "effects": a.effects, "scopedPre": a.scoped_pre, "scopedEffects": a.scoped_effects,
                    "lifted": a.lifted, "narrowed": a.narrowed, "call": a.call, "where": a.r#where, "cost": a.cost,
                    "description": a.description, "instructions": a.instructions})
            })
            .collect();
        out.insert("actions".into(), Value::Array(acts));
        let ags: Vec<Value> = c
            .agent_list()
            .iter()
            .map(|ag| {
                json!({"name": ag.name, "planner": ag.planner, "actions": ag.actions, "goals": ag.goals,
                    "process": ag.process_of(), "role": ag.role, "description": ag.description})
            })
            .collect();
        out.insert("agents".into(), Value::Array(ags));
        out.insert("conditions".into(), json!(c.conditions.names()));
        let reacts: Vec<Value> = c
            .reactions()
            .iter()
            .map(|r| {
                json!({"activity": r.activity, "owner": r.owner, "kind": r.kind, "agent": r.agent, "goal": r.goal,
                    "own": r.own_actions()})
            })
            .collect();
        out.insert("reactions".into(), Value::Array(reacts));
        out.insert("stepDependencies".into(), json!(c.step_dependencies()));
        let mut levels = Map::new();
        let mut graphs = Map::new();
        let names = m
            .processes
            .iter()
            .map(|p| p.name.clone())
            .chain(m.methods.iter().map(|me| me.name.clone()));
        for n in names {
            if let Some(ls) = c.check_levels(&n) {
                levels.insert(n.clone(), serde_json::to_value(ls).unwrap());
            }
            if let Some(g) = c.process_graph(&n) {
                graphs.insert(n.clone(), serde_json::to_value(g).unwrap());
            }
        }
        out.insert("levels".into(), Value::Object(levels));
        out.insert("graphs".into(), Value::Object(graphs));
        out.insert("auto".into(), json!(c.automatic_reviews));
    }
    Value::Object(out)
}

fn files() -> Vec<(PathBuf, String)> {
    let mut out = Vec::new();
    let mut add = |dir: &str, prefix: &str| {
        let mut list: Vec<PathBuf> = std::fs::read_dir(dir)
            .unwrap()
            .filter_map(|e| e.ok().map(|e| e.path()))
            .filter(|p| p.extension().is_some_and(|x| x == "yaml"))
            .collect();
        list.sort();
        for p in list {
            let stem = p.file_stem().unwrap().to_string_lossy().to_string();
            out.push((p, format!("{prefix}{stem}")));
        }
    };
    add("../../methodologies", "");
    add("../../methodologies/examples", "examples_");
    add("testdata/broken", "broken_");
    out
}

#[test]
fn shipped_methodologies_compile_as_in_go() {
    let mut failures = Vec::new();
    for (path, name) in files() {
        let golden = format!("testdata/{name}.golden.json");
        let want: Value = serde_json::from_str(
            &std::fs::read_to_string(&golden).unwrap_or_else(|e| panic!("{golden}: {e}")),
        )
        .unwrap();
        let got = dump(&path);
        if name == "sdlc" {
            // the comparison is not vacuous
            assert!(got["actions"].as_array().is_some_and(|a| a.len() > 20));
            assert!(got["levels"].as_object().is_some_and(|l| !l.is_empty()));
            assert!(
                got["view"]["processes"]
                    .as_array()
                    .is_some_and(|p| !p.is_empty())
            );
        }
        for key in [
            "issues",
            "strictIssues",
            "view",
            "actions",
            "agents",
            "conditions",
            "reactions",
            "stepDependencies",
            "levels",
            "graphs",
            "auto",
        ] {
            let (mut w, mut g) = (
                want.get(key).cloned().unwrap_or(Value::Null),
                got.get(key).cloned().unwrap_or(Value::Null),
            );
            if key == "issues" || key == "strictIssues" {
                w = sorted(&normalise(w).unwrap_or(Value::Null));
                g = sorted(&normalise(g).unwrap_or(Value::Null));
            }
            let (w, g) = (normalise(w), normalise(g));
            if w != g {
                if std::env::var("GOAP_GOLDEN_DIFF").is_ok() {
                    let dir = std::env::temp_dir().join("goap-methodology-golden");
                    std::fs::create_dir_all(&dir).unwrap();
                    std::fs::write(
                        dir.join(format!("{name}.{key}.want.json")),
                        serde_json::to_string_pretty(&w).unwrap(),
                    )
                    .unwrap();
                    std::fs::write(
                        dir.join(format!("{name}.{key}.got.json")),
                        serde_json::to_string_pretty(&g).unwrap(),
                    )
                    .unwrap();
                }
                failures.push(format!("{name}: {key} differs"));
            }
        }
    }
    assert!(failures.is_empty(), "{}", failures.join("\n"));
}
