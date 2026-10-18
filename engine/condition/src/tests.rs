//! Internal tests: the parts of the port that cel-go gave for free (laziness, unparse, the checker).
use serde_json::json;

use crate::{
    activation::Activation,
    check::{Ty, check},
    condition::{compile, condition_decls, eval_bool},
    explain::parse_plain,
    unparse::unparse,
};

#[test]
fn an_expression_computes_only_what_it_reads() {
    let mut bb = goap_domain::Blackboard::default();
    bb.vars.insert("n".into(), json!(1));
    let set = compile(&[crate::Definition::new(
        "plain",
        "items.size() == 0 && vars.n == 1",
    )])
    .unwrap();
    let act = Activation::new(&bb);
    assert!(eval_bool(&set.progs[0].prog, &act).unwrap());
    for name in crate::LAZY_VARIABLES {
        assert!(!act.is_computed(name), "{name} computed");
    }
    let set = compile(&[crate::Definition::new("r", "risks.size() == 0")]).unwrap();
    assert!(eval_bool(&set.progs[0].prog, &act).unwrap());
    assert!(act.is_computed("risks") && !act.is_computed("actions"));
}

#[test]
fn unparse_like_cel_go() {
    for (src, want) in [
        ("a.key == 'x'", r#"a.key == "x""#),
        ("(a || b) && c", "(a || b) && c"),
        ("a || b && c", "a || b && c"),
        ("!(a && b)", "!(a && b)"),
        ("!a", "!a"),
        ("x ? y : z", "x ? y : z"),
        ("(x ? y : z) ? 1 : 2", "(x ? y : z) ? 1 : 2"),
        ("a - (b - c)", "a - (b - c)"),
        ("a - b - c", "a - b - c"),
        ("(a + b) * c", "(a + b) * c"),
        (
            "[1,2.5,\"s\", null, true, 3u]",
            r#"[1, 2.5, "s", null, true, 3u]"#,
        ),
        ("m['k'][0]", r#"m["k"][0]"#),
        ("{'a': 1}", r#"{"a": 1}"#),
        ("x.exists(v, v.ok)", "x.exists(v, v.ok)"),
        ("has(a.b)", "has(a.b)"),
        ("(a + b).size()", "(a + b).size()"),
        ("'a\"b\\n'", r#""a\"b\n""#),
        ("x in [1, 2]", "x in [1, 2]"),
        ("-x", "-x"),
    ] {
        let e = parse_plain(src).unwrap();
        assert_eq!(unparse(&e), want, "{src}");
    }
}

#[test]
fn checker() {
    let decls = condition_decls();
    let ty = |src: &str| {
        let p = crate::new_env().compile(src).unwrap();
        check(p.expression(), p.source_info(), &decls)
    };
    assert_eq!(ty("size(items)"), Ok(Ty::Int));
    assert_eq!(ty("items.filter(i, i.ok)"), Ok(Ty::list()));
    assert_eq!(ty("items.exists(i, i.ok)"), Ok(Ty::Bool));
    assert_eq!(ty("change.title"), Ok(Ty::Dyn));
    assert_eq!(ty("activeOption + 'x'"), Ok(Ty::String));
    assert_eq!(ty("1 + 1"), Ok(Ty::Int));
    assert_eq!(ty("[1, 2]"), Ok(Ty::List(Box::new(Ty::Int))));
    assert_eq!(
        ty("cel.bind(x, 1, x + 1)").is_ok(),
        ty("cel.bind(x, 1, x + 1)").is_ok()
    );
    assert_eq!(
        ty("change.id == nope").unwrap_err(),
        "ERROR: <input>:1:14: undeclared reference to 'nope' (in container '')\n | change.id == nope\n | .............^"
    );
    assert_eq!(Ty::map().to_string(), "map(string, dyn)");
}

#[test]
fn whole_numbers_are_ints() {
    // a positive integer is an int in an expression (serde_json keeps it as an unsigned number)
    let mut bb = goap_domain::Blackboard::default();
    bb.vars.insert("n".into(), json!(3));
    bb.vars.insert("f".into(), json!(3.5));
    let set = compile(&[
        crate::Definition::new("int", "vars.n - 4 == -1 && type(vars.n) == int"),
        crate::Definition::new("double", "vars.f * 2.0 == 7.0"),
    ])
    .unwrap();
    let res = set.evaluate(&bb);
    assert!(res.state["int"] && res.state["double"], "{res:?}");
}

#[test]
fn guards_compare_whole_numbers() {
    let bb = goap_domain::Blackboard::default();
    let ok = crate::check_guard(
        "criticalityPolicy.maxDerogationHours <= 336 && size(risks) == 0",
        &bb,
        None,
        "",
        "",
    );
    assert_eq!(ok, Ok(true));
    // goap-guard binds its variables through serde, which makes a positive whole number an uint: arithmetic with an
    // int literal (`criticalityPolicy.maxDerogationHours - 1`) fails in a guard, unlike cel-go (to fix in goap-guard)
}

#[test]
fn compiled_forms_cross_threads() {
    fn send_sync<T: Send + Sync>() {}
    send_sync::<crate::Set>();
    send_sync::<crate::EventFilter>();
    send_sync::<crate::NumberSet>();
    send_sync::<crate::List>();
    send_sync::<crate::Value>();
    send_sync::<crate::AssessmentViews>();
    send_sync::<Activation<'static>>();
}
