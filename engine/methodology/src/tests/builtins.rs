use std::collections::HashSet;
use std::sync::Arc;

use super::parse;
use crate::BuiltinSet;

struct SetOf(HashSet<&'static str>);

impl BuiltinSet for SetOf {
    fn has_builtin(&self, name: &str) -> bool {
        self.0.contains(name)
    }
}

const BUILTIN_METHODOLOGY: &str = r#"
name: m
conditions: [{name: c, expr: "true"}]
actions: [{name: a, kind: builtin, builtin: nosuch, effects: {c: true}}]
goals: [{name: g, pre: {c: true}}]"#;

#[test]
fn unknown_builtin() {
    let m = parse(BUILTIN_METHODOLOGY);
    // no set: the name is not checked
    let is = m.validate();
    assert!(is.0.is_empty(), "unchecked: {is}");
    // a set that does not know it: refused, and compiling refuses it too
    let r = m.with_builtins(Arc::new(SetOf(HashSet::from(["other"]))));
    let is = r.validate();
    assert!(
        is.0.len() == 1
            && is.0[0].path == "actions[0].builtin"
            && is.0[0].message.contains(r#"unknown builtin "nosuch""#),
        "issues: {is}"
    );
    assert!(
        r.compile().is_err(),
        "compile must refuse an unknown builtin"
    );
    // registered: accepted
    let is = m
        .with_builtins(Arc::new(SetOf(HashSet::from(["nosuch"]))))
        .validate();
    assert!(is.0.is_empty(), "registered: {is}");
    // the platform's static list knows its own names
    let k = goap_builtins::Known;
    assert!(
        BuiltinSet::has_builtin(&k, goap_builtins::GRAPH_APPLY)
            && !BuiltinSet::has_builtin(&k, "nosuch"),
        "static set"
    );
}
