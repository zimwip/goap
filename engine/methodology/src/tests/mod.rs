//! The tests of the Go package, one module per Go test file.

mod agent_planner;
mod assessment;
mod builtins;
mod compile;
mod composition;
mod hints;
mod imports;
mod lenient;
mod levels;
mod method;
mod methodology;
mod namespace;
mod process;
mod releasebased;
mod riskmanagement;
mod sdlc;
mod subtyping;
mod types;
mod verify;

use crate::{CompileOptions, Compiled, Methodology};
use goap_def::Issues;

pub(crate) const PROCESS_BASE: &str = r#"
name: p
version: 1.0.0
namespace: alm
conditions:
  - {name: a, expr: 'artifacts.exists(x, x.target == "a")'}
  - {name: b, expr: 'artifacts.exists(x, x.target == "b")'}
  - {name: c, expr: 'artifacts.exists(x, x.target == "c")'}
  - {name: d, expr: 'artifacts.exists(x, x.target == "d")'}
actions:
  - {name: do_a, kind: human, effects: {a: true}}
  - {name: do_b, kind: human, pre: {a: true}, effects: {b: true}}
  - {name: do_c, kind: human, effects: {c: true}}
goals:
  - {name: all, pre: {c: true}}
agents:
  - {name: worker, actions: [do_c], goals: [all]}
"#;

pub(crate) fn parse(src: &str) -> Methodology {
    Methodology::parse(src.as_bytes()).unwrap_or_else(|e| panic!("{e}"))
}

pub(crate) fn compile_process(processes: &str) -> (Option<Compiled>, Issues) {
    parse(&format!("{PROCESS_BASE}{processes}")).compile_with(CompileOptions::default())
}

pub(crate) fn load(path: &str) -> Methodology {
    crate::load_file(path).unwrap_or_else(|e| panic!("{path}: {e}"))
}

pub(crate) fn has_issue(issues: &Issues, path: &str, msg: &str) -> bool {
    issues
        .0
        .iter()
        .any(|is| is.path == path && is.message.contains(msg))
}
