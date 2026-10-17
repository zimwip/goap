//! The tests of engine/condition, ported test by test.
use std::collections::BTreeMap;

use chrono::{TimeDelta, TimeZone, Utc};
use goap_condition::*;
use goap_domain::{
    Blackboard, Change, ChangeImpact, ChangeItem, Criterion, DATA_CRITICALITY, DecisionPoint,
    Dependency, FACET_ACTIVE_OPTION, FACET_CRITICALITY_POLICY, FACET_DECISION_POINTS,
    FACET_DEPENDENCIES, FACET_HISTORY, FACET_OPTIONS, FLOW_OPEN, Flow, INTENT_MODIFIED,
    ITEM_ACCEPTED, ITEM_PROPOSED, ItemType, Node, NodeRef, NodeView, OptionSpec, POINT_BLOCKED,
    QUESTION_OPEN, Question, REVIEW_ACCEPTED, REVIEW_PROPOSED, Transition,
};
use goap_journal::{History, OUTCOME_SUCCEEDED, StepRun};
use serde_json::{Map, Value, json};

fn obj(v: Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m,
        _ => panic!("not an object"),
    }
}

fn def(name: &str, expr: &str) -> Definition {
    Definition::new(name, expr)
}

fn world(pairs: &[(&str, bool)]) -> BTreeMap<String, bool> {
    pairs.iter().map(|(k, v)| (k.to_string(), *v)).collect()
}

// condition_test.go

#[test]
fn compile_errors() {
    assert!(
        compile(&[def("x", "size(changeImpacts)")]).is_err(),
        "non bool expression must be rejected"
    );
    let err = compile(&[def("x", "unknown_var")]).unwrap_err().to_string();
    assert!(
        err.contains("undeclared reference to 'unknown_var'"),
        "unknown variable must be rejected: {err}"
    );
    assert!(compile(&[def("x", "changeImpacts.exists(i, i.key == 'a')")]).is_ok());
    assert!(compile(&[def("x", "sets.contains(change.dependsOn, ['a'])")]).is_ok());
    assert!(compile(&[def("x", "change.id ==")]).is_err());
}

// types_test.go

#[test]
fn types_expose_supertypes() {
    let r = NodeRef {
        id: "n1".into(),
        version: 1,
    };
    let mut bb = Blackboard::default();
    bb.change.nodes = vec![ChangeImpact {
        id: "n".into(),
        key: "SEC-1".into(),
        r#type: "SecurityRequirement".into(),
        intent: INTENT_MODIFIED,
        pre: Some(r.clone()),
        ..Default::default()
    }];
    bb.nodes.insert(
        r,
        NodeView {
            node: Node {
                id: "n1".into(),
                version: 1,
                key: "SEC-1".into(),
                r#type: "SecurityRequirement".into(),
                ..Default::default()
            },
            ..Default::default()
        },
    );
    bb.supertypes
        .insert("SecurityRequirement".into(), vec!["Requirement".into()]);
    let set = compile(&[def(
        "req",
        r#"changeImpacts.exists(n, "Requirement" in n.types)"#,
    )])
    .unwrap();
    let res = set.evaluate(&bb);
    assert!(res.state["req"], "subtype not matched: {res:?}");
}

// list_test.go

#[test]
fn list_evaluates_to_plain_values() {
    assert!(check_list("1 + 1").is_err(), "a number is not a list");
    let l = compile_list("vars.components.filter(c, c.build)").unwrap();
    let mut bb = Blackboard::default();
    bb.vars.insert(
        "components".into(),
        json!([{"key": "api", "lang": "go", "build": true}, {"key": "doc", "lang": "md", "build": false}]),
    );
    let got = l.eval(&bb).unwrap();
    assert_eq!(got.len(), 1);
    assert_eq!(got[0]["key"], "api");
    assert_eq!(got[0]["lang"], "go");
    // a value of any type
    let v = compile_value("vars.components[0].lang").unwrap();
    assert_eq!(v.eval(&bb).unwrap(), json!("go"));
    // numbers
    assert!(check_number(r#""a""#).is_err());
    let ns = compile_numbers(&[
        def("n", "size(vars.components) * 2"),
        def("bad", "vars.nope"),
    ])
    .unwrap();
    let out = ns.evaluate(&bb);
    assert_eq!(out.get("n"), Some(&4.0));
    assert!(!out.contains_key("bad"));
}

// decision_test.go

#[test]
fn options_and_decisions_in_conditions() {
    let mut defs = vec![
        def(
            "all_evaluated",
            r#"options.all(o, o.status == "evaluated")"#,
        ),
        def(
            "on_b",
            r#"activeOption == "b" && options.exists(o, o.active && o.name == "adyen")"#,
        ),
        def(
            "fees_asked",
            r#"questions.exists(q, q.text.contains("fees") && q.status == "open")"#,
        ),
        def(
            "sure_enough",
            r#"decisionPoints.all(d, d.policy.threshold >= 0.5 && d.humanOnly == false)"#,
        ),
        def(
            "two_rounds_left",
            r#"decisionPoints.all(d, d.policy.maxRounds - d.policy.rounds == 2)"#,
        ),
    ];
    defs.extend(must_library(&[LIBRARY_DECISIONS]));
    let s = compile(&defs).unwrap();
    let bb = Blackboard::default()
        .with_facet(
            FACET_OPTIONS,
            vec![
                Flow {
                    id: "a".into(),
                    status: FLOW_OPEN,
                    option: Some(OptionSpec {
                        name: "stripe".into(),
                        ..Default::default()
                    }),
                    evaluated: true,
                    ..Default::default()
                },
                Flow {
                    id: "b".into(),
                    status: FLOW_OPEN,
                    option: Some(OptionSpec {
                        name: "adyen".into(),
                        ..Default::default()
                    }),
                    evaluated: true,
                    active: true,
                    ..Default::default()
                },
            ],
        )
        .with_facet(FACET_ACTIVE_OPTION, "b".to_string())
        .with_facet(
            FACET_DECISION_POINTS,
            vec![DecisionPoint {
                id: "p".into(),
                question: "which?".into(),
                status: POINT_BLOCKED.into(),
                policy: obj(json!({"rounds": 1, "maxRounds": 3.0, "threshold": 0.7})),
                questions: vec![Question {
                    id: "p:1".into(),
                    point: "p".into(),
                    text: "What are the fees?".into(),
                    status: QUESTION_OPEN.into(),
                    ..Default::default()
                }],
                ..Default::default()
            }],
        );
    let st = s.evaluate(&bb);
    assert!(st.errors.is_empty(), "{:?}", st.errors);
    let want = [
        ("all_evaluated", true),
        ("on_b", true),
        ("fees_asked", true),
        ("two_rounds_left", true),
        ("sure_enough", true),
        ("open_questions", true),
        ("no_open_questions", false),
        ("decision_ready", false),
        ("decision_pending", true),
        ("no_decision_pending", false),
        ("options_open", true),
        ("options_evaluated", true),
        ("option_selected", false),
    ];
    for (k, v) in want {
        assert_eq!(st.state.get(k).copied().unwrap_or(false), v, "{k}");
    }
    // without options nor decisions: no pending decision, no open question
    let st = s.evaluate(&Blackboard::default());
    assert!(
        st.state["no_decision_pending"]
            && st.state["no_open_questions"]
            && !st.state["options_evaluated"],
        "empty: {:?} {:?}",
        st.state,
        st.errors
    );
}

// risk_test.go

fn item(id: &str, kind: ItemType, data: Value) -> ChangeItem {
    ChangeItem {
        id: id.into(),
        r#type: kind,
        status: ITEM_PROPOSED,
        data: obj(data),
        ..Default::default()
    }
}

#[test]
fn risk_conditions() {
    goap_risk::register();
    let set = compile(&must_library(&[LIBRARY_RISKS])).unwrap();
    let mut bb = Blackboard::default();
    bb.change.items = vec![
        item(
            "1",
            goap_risk::TYPE_RISK,
            json!({"key": "RSK-1", "title": "high", "probability": 3.0, "impact": 4.0}),
        ),
        item(
            "2",
            goap_risk::TYPE_RISK,
            json!({"key": "RSK-2", "title": "low", "probability": 1.0, "impact": 2.0}),
        ),
    ];
    let state = set.evaluate(&bb).state;
    assert!(
        state["open_risks"]
            && state["unmitigated_risks"]
            && !state["risks_under_control"]
            && !state["open_actions"],
        "a high risk without action: {state:?}"
    );
    bb.change.items.push(item(
        "3",
        goap_risk::TYPE_ACTION,
        json!({"key": "ACT-1", "title": "mitigate", "for": "RSK-1"}),
    ));
    bb.change.items.push(item(
        "4",
        goap_risk::TYPE_RISK,
        json!({"key": "RSK-2", "status": "closed", "title": "low"}),
    ));
    let state = set.evaluate(&bb).state;
    assert!(
        !state["unmitigated_risks"]
            && state["risks_under_control"]
            && state["open_actions"]
            && state["open_risks"],
        "the high risk has an action: {state:?}"
    );
    let rs = goap_risk::risks(&bb.change);
    assert!(
        rs.len() == 2 && rs[1].status == "closed" && rs[1].probability == 1 && rs[1].versions == 2,
        "register {rs:?}"
    );
}

#[test]
fn dependency_conditions() {
    let set = compile(&must_library(&[LIBRARY_DEPENDENCIES])).unwrap();
    let bb = Blackboard::default();
    let state = set.evaluate(&bb).state;
    assert!(
        state["dependencies_met"] && !state["dependencies_pending"],
        "no dependency: {state:?}"
    );
    let bb = bb.with_facet(
        FACET_DEPENDENCIES,
        vec![
            Dependency {
                id: "a".into(),
                satisfied: true,
                ..Default::default()
            },
            Dependency {
                id: "b".into(),
                reason: "not merged yet".into(),
                ..Default::default()
            },
        ],
    );
    let state = set.evaluate(&bb).state;
    assert!(
        !state["dependencies_met"] && state["dependencies_pending"],
        "one dependency pending: {state:?}"
    );
    let ok = check_guard(
        "dependencies.size() == 2 && change.dependsOn.size() == 0",
        &bb,
        None,
        "",
        "",
    )
    .unwrap();
    assert!(ok, "the guards see the dependencies");
}

// explain_test.go

#[test]
fn explain() {
    let set = compile(&[
        def("ready", r#"change.status == "open" && size(vars.todo) > 0"#),
        def("bad", "vars.x.y == 1"),
    ])
    .unwrap();
    let mut bb = Blackboard::default();
    bb.vars.insert("todo".into(), json!(["a"]));
    bb.change.status = "open".into();
    let ex = set.explain("ready", &bb).unwrap();
    assert!(ex.value && ex.error.is_empty(), "{ex:?}");
    assert!(
        ex.root.value == "true" && ex.root.terms.len() == 2 && ex.root.terms[0].value == "true",
        "root {:?}",
        ex.root
    );
    assert_eq!(
        ex.root.text,
        r#"change.status == "open" && size(vars.todo) > 0"#
    );
    assert_eq!(ex.root.terms[1].text, "size(vars.todo) > 0");
    assert_eq!(ex.root.terms[1].terms[0].text, "size(vars.todo)");
    assert_eq!(ex.root.terms[1].terms[0].value, "1");
    assert!(
        !ex.inputs["change"].is_empty()
            && ex.inputs["vars"] == r#"{"todo":["a"]}"#
            && ex.inputs.len() == 2,
        "inputs {:?}",
        ex.inputs
    );
    let ex = set.explain("bad", &bb).unwrap();
    assert!(!ex.error.is_empty(), "an error is expected: {ex:?}");
    assert!(set.explain("nope", &bb).is_none(), "unknown condition");

    // a short-circuited term is skipped
    bb.change.status = "closed".into();
    let ex = set.explain("ready", &bb).unwrap();
    assert!(!ex.value);
    assert!(ex.root.terms[1].skipped, "{:?}", ex.root);
}

// gate_history_test.go

#[test]
fn guard_reads_the_history() {
    let expr = r#""intake/collect" in history.last && history.last["intake/collect"].current"#;
    let bb = Blackboard::default();
    assert!(
        !check_guard(expr, &bb, None, "close", "").unwrap(),
        "nothing ran"
    );
    // StepRun has a private field: built from its default
    let mut step = StepRun::default();
    step.path = "intake/collect".into();
    step.outcome = OUTCOME_SUCCEEDED.into();
    step.current = true;
    let h = History {
        steps: vec![step],
        ..Default::default()
    };
    let bb = bb.with_facet(FACET_HISTORY, h);
    assert!(
        check_guard(expr, &bb, None, "close", "").unwrap(),
        "the step ran and stands"
    );
}

#[test]
fn gate_objective_met() {
    let bb = Blackboard::default();
    let t = Transition {
        name: "t".into(),
        objectives: vec![Criterion {
            name: "docs".into(),
            expr: r#"world["docs"]"#.into(),
            ..Default::default()
        }],
        ..Default::default()
    };
    let res = check_gate(&t, &bb, &world(&[("docs", true)]), "").unwrap();
    assert!(res.passed() && !res.with_reserve(), "{res:?}");
}

// implies_test.go

#[test]
fn implied_conditions() {
    let mut defs = library("assessments").expect("no assessments library");
    defs.extend([
        def(
            "same_rated",
            r#"assessments.exists(x, x.current && "description" == x.key)"#,
        ),
        def(
            "rated_again",
            r#"assessments.exists(x, x.answered && x.current && "description" == x.key)"#,
        ),
        def(
            "both",
            r#"change.title != "" && assessments.exists(a, a.key == "description" && a.current && a.answered)"#,
        ),
        def(
            "other",
            r#"assessments.exists(a, a.key == "title" && a.current)"#,
        ),
    ]);
    let s = compile(&defs).unwrap();
    let cases: [(&str, &[&str]); 5] = [
        (
            "description_accepted",
            &["description_rated", "rated_again", "same_rated"],
        ),
        ("both", &["description_rated", "rated_again", "same_rated"]),
        // rated_again is the same condition, not a prerequisite
        ("description_rated", &["same_rated"]),
        ("same_rated", &[]),
        ("other", &[]),
    ];
    for (name, want) in cases {
        assert_eq!(s.implied(name), want, "{name}");
    }
    assert!(
        implies("a && b", "b")
            && !implies("a || b", "b")
            && !implies("x.exists(i, i.a)", "y.exists(i, i.a)"),
        "conjunctions only"
    );
}

// facts_test.go

/// The facts of a condition say which of its terms fails: a current rating of the description exists, its score is
/// not enough; the rating action establishes the rating, nothing establishes the score.
#[test]
fn facts_of_a_condition() {
    goap_assess::register();
    let defs = library("assessments").unwrap();
    let s = compile(&defs).unwrap();
    let c = Change {
        id: "C1".into(),
        title: "Note".into(),
        intent: "write a note".into(),
        ..Default::default()
    };
    let mut bb = Blackboard {
        change: c.clone(),
        ..Default::default()
    };
    let rated = goap_assess::fingerprint_of(&bb, goap_assess::SUBJECT_DESCRIPTION);
    bb.change.items = vec![ChangeItem {
        id: "i1".into(),
        r#type: goap_assess::TYPE_ASSESSMENT,
        status: ITEM_ACCEPTED,
        data: obj(json!({"key": "description", "score": 2, "rated": rated})),
        ..Default::default()
    }];

    let facts = s.facts("description_accepted", &bb).unwrap();
    assert!(
        facts.len() == 1
            && !facts[0].holds
            && facts[0].over == "assessments"
            && facts[0].parts.len() == 4,
        "facts {facts:?}"
    );
    let parts = &facts[0].parts;
    assert!(
        parts[0].holds
            && parts[1].holds
            && parts[2].holds
            && !parts[3].holds
            && parts[3].text == "a.score >= 3",
        "parts {parts:?}"
    );
    let miss = missing(&facts).unwrap();
    assert!(
        miss.text == "a.score >= 3" && !s.establishes("description_rated", &miss),
        "nothing but a rating scoring 3 makes the score: {miss:?}"
    );

    // before any rating, the rating itself is missing, and description_rated establishes it
    let bb = Blackboard {
        change: Change {
            id: "C1".into(),
            intent: "write a note".into(),
            ..Default::default()
        },
        ..Default::default()
    };
    let facts = s.facts("description_accepted", &bb).unwrap();
    let miss = missing(&facts).unwrap();
    assert!(
        miss.text == r#"a.key == "description""# && s.establishes("description_rated", &miss),
        "a rating is missing first: {miss:?}"
    );
}

// lazy_test.go

/// The variables of the decision and risk concepts are computed when an expression reads them (ADR 0064).
#[test]
fn activation_is_lazy() {
    let bb = Blackboard::default().with_facet(
        FACET_DECISION_POINTS,
        vec![DecisionPoint {
            id: "p".into(),
            status: POINT_BLOCKED.into(),
            ..Default::default()
        }],
    );
    let act = Activation::new(&bb);
    for name in [
        "options",
        "decisionPoints",
        "questions",
        "risks",
        "actions",
        "verifications",
        "derogations",
    ] {
        assert!(act.is_lazy(name), "{name} is computed eagerly");
        assert!(!act.is_computed(name), "{name} is computed eagerly");
    }
    let pts = act.get("decisionPoints").unwrap();
    assert_eq!(
        pts.as_array().map(|a| a.len()),
        Some(1),
        "decisionPoints {pts}"
    );
    assert!(act.is_computed("decisionPoints") && !act.is_computed("risks"));

    // an expression over one variable gets it, and a set that reads none of them never computes them
    let set = compile(&[
        def(
            "pending",
            r#"decisionPoints.exists(d, d.status != "decided")"#,
        ),
        def("plain", "items.size() == 0"),
    ])
    .unwrap();
    let st = set.evaluate(&bb);
    assert!(
        st.state["pending"] && st.state["plain"] && st.errors.is_empty(),
        "{:?} {:?}",
        st.state,
        st.errors
    );
}

#[test]
fn libraries() {
    for n in LIBRARY_NAMES {
        let defs = library(n).unwrap_or_else(|| panic!("library {n}"));
        assert!(!defs.is_empty(), "library {n}");
        if let Err(e) = compile(&defs) {
            panic!("library {n}: {e}");
        }
    }
    assert!(library("nope").is_none(), "unknown library");
}

#[test]
fn verification_conditions() {
    let set = compile(&must_library(&[LIBRARY_VERIFICATION])).unwrap();
    let it = |id: &str, data: Value| ChangeItem {
        id: id.into(),
        r#type: goap_verify::TYPE_VERIFICATION,
        data: obj(data),
        ..Default::default()
    };
    let mut bb = Blackboard::default();
    let st = set.evaluate(&bb).state;
    assert!(
        st["all_verified"] && !st["unverified_effects"] && !st["reserves_open"],
        "nothing produced: {st:?}"
    );
    bb.change.items = vec![it(
        "1",
        json!({"state": "produced", "action": "a", "impacts": ["i1", "i2"]}),
    )];
    let st = set.evaluate(&bb).state;
    assert!(
        !st["all_verified"] && st["unverified_effects"],
        "produced: {st:?}"
    );
    bb.change.items.push(it(
        "2",
        json!({"state": "accepted", "action": "b", "impact": "i1"}),
    ));
    bb.change.items.push(it(
        "3",
        json!({"state": "accepted_with_reserve", "action": "b", "impact": "i2"}),
    ));
    let st = set.evaluate(&bb).state;
    assert!(
        st["all_verified"] && !st["unverified_effects"] && st["reserves_open"],
        "settled: {st:?}"
    );
}

fn drg(id: &str, key: &str, exp: &str, status: &str) -> ChangeItem {
    let mut d = obj(
        json!({"key": key, "rule": "r", "target": "t", "reason": "why", "signatory": "alice", "expires": exp}),
    );
    if !status.is_empty() {
        d.insert("status".into(), Value::from(status));
    }
    ChangeItem {
        id: id.into(),
        r#type: goap_risk::TYPE_DEROGATION,
        data: d,
        ..Default::default()
    }
}

#[test]
fn derogation_conditions() {
    let set = compile(&must_library(&[LIBRARY_DEROGATIONS])).unwrap();
    let at = Utc.with_ymd_and_hms(2030, 1, 1, 12, 0, 0).unwrap();
    let mut bb = Blackboard {
        at: Some(at),
        ..Default::default()
    };
    let st = set.evaluate(&bb).state;
    assert!(
        st["no_expired_derogation"] && !st["derogation_debt"],
        "none: {st:?}"
    );
    bb.change.items = vec![
        drg("1", "D1", "2030-01-02T00:00:00Z", ""),
        drg("2", "D2", "2030-01-02T00:00:00Z", ""),
        drg("3", "D3", "2030-01-02T00:00:00Z", ""),
    ];
    let st = set.evaluate(&bb).state;
    assert!(
        st["no_expired_derogation"] && st["derogation_debt"],
        "three open: {st:?}"
    );
    bb.change
        .items
        .push(drg("4", "D3", "2030-01-02T00:00:00Z", "closed"));
    let st = set.evaluate(&bb).state;
    assert!(!st["derogation_debt"], "one closed: {st:?}");
    bb.at = Some(at + TimeDelta::hours(48));
    let st = set.evaluate(&bb).state;
    assert!(!st["no_expired_derogation"], "ran out: {st:?}");
    assert!(
        Activation::new(&bb).is_lazy("derogations"),
        "derogations is computed eagerly"
    );
}

/// change.criticality and criticalityPolicy are visible to conditions and guards: the facet of the organisation when
/// the blackboard has one, else the compiled-in table of the level (ADR 0075 §3). `policy` stays a field of the
/// decision points.
#[test]
fn criticality_variables() {
    let mut bb = Blackboard::default();
    bb.change.data.insert(DATA_CRITICALITY.into(), json!("C3"));
    let set = compile(&[
        def("critical", r#"change.criticality == "C3""#),
        def("human_only", r#"criticalityPolicy.oracles == ["human"]"#),
        def("short", "criticalityPolicy.maxDerogationHours <= 168"),
    ])
    .unwrap();
    let st = set.evaluate(&bb).state;
    assert!(
        st["critical"] && st["human_only"] && st["short"],
        "defaults: {st:?}"
    );
    assert!(
        Activation::new(&bb).is_lazy("criticalityPolicy"),
        "criticalityPolicy is computed eagerly"
    );
    let empty = Blackboard::default();
    assert_eq!(
        Activation::new(&empty).get("change").unwrap()["criticality"],
        "C2",
        "a change that names no level is C2"
    );
    let bb = bb.with_facet(
        FACET_CRITICALITY_POLICY,
        goap_criticality::Policy {
            oracles: vec!["tool".into()],
            max_derogation: TimeDelta::hours(1000),
            ..Default::default()
        },
    );
    let st = set.evaluate(&bb).state;
    assert!(
        !st["human_only"] && !st["short"],
        "the facet of the organisation wins: {st:?}"
    );
    // a guard reads them the way a condition does
    let ok = check_guard(
        r#"change.criticality == "C3" ? criticalityPolicy.oracles.exists(o, o == "tool") : true"#,
        &bb,
        None,
        "t",
        "",
    )
    .unwrap();
    assert!(ok, "guard");
}

/// CheckGate: vetos and objectives, an objective covered by the derogation of its name (ADR 0075 §3).
#[test]
fn check_gate_vetos_and_objectives() {
    let at = Utc.with_ymd_and_hms(2030, 1, 1, 12, 0, 0).unwrap();
    let drg = |key: &str, rule: &str, exp: &str| ChangeItem {
        id: key.into(),
        r#type: goap_risk::TYPE_DEROGATION,
        data: obj(
            json!({"key": key, "rule": rule, "target": "t", "reason": "why", "signatory": "alice", "expires": exp}),
        ),
        ..Default::default()
    };
    let crit = |name: &str, expr: &str| Criterion {
        name: name.into(),
        expr: expr.into(),
        ..Default::default()
    };
    let tr = Transition {
        name: "ship".into(),
        vetos: vec![crit(
            "safe",
            r#"change.criticality != "C3" || world["reviewed"]"#,
        )],
        objectives: vec![
            crit("docs", r#"world["docs"]"#),
            crit("perf", r#"world["perf"]"#),
        ],
        ..Default::default()
    };
    let mut bb = Blackboard {
        at: Some(at),
        ..Default::default()
    };
    bb.change.data.insert(DATA_CRITICALITY.into(), json!("C3"));
    let mut w = world(&[("reviewed", false), ("docs", false), ("perf", true)]);
    let res = check_gate(&tr, &bb, &w, "").unwrap();
    assert!(
        !res.passed() && res.vetoed == ["safe"] && res.unmet.len() == 1,
        "veto: {res:?}"
    );
    w.insert("reviewed".into(), true);
    let res = check_gate(&tr, &bb, &w, "").unwrap();
    assert!(
        !res.passed() && res.uncovered() == ["docs"],
        "uncovered: {res:?}"
    );
    bb.change.items = vec![
        drg("D-old", "docs", "2029-12-31T00:00:00Z"),
        drg("D-other", "perf", "2030-02-01T00:00:00Z"),
    ];
    let res = check_gate(&tr, &bb, &w, "").unwrap();
    assert!(
        !res.passed(),
        "an expired derogation and one of another rule cover nothing: {res:?}"
    );
    bb.change
        .items
        .push(drg("D-docs", "docs", "2030-02-01T00:00:00Z"));
    let res = check_gate(&tr, &bb, &w, "").unwrap();
    assert!(
        res.passed() && res.with_reserve() && res.reserve["docs"] == "D-docs",
        "covered: {res:?}"
    );
    let bad = Transition {
        vetos: vec![crit("x", r#"world["missing"]"#)],
        ..Default::default()
    };
    assert!(
        check_gate(&bad, &bb, &w, "").is_err(),
        "a criterion that cannot be evaluated is an error"
    );
}

// assessview_test.go

fn assessed_board(score: i64) -> Blackboard {
    let desc = "Login\n\nlet users sign in";
    let impact = |id: &str, key: &str, review| ChangeImpact {
        id: id.into(),
        key: key.into(),
        r#type: "alm@Requirement".into(),
        intent: INTENT_MODIFIED,
        review,
        ..Default::default()
    };
    let a = |id: &str, data: Value| ChangeItem {
        id: id.into(),
        r#type: goap_assess::TYPE_ASSESSMENT,
        data: obj(data),
        ..Default::default()
    };
    Blackboard {
        change: Change {
            id: "c".into(),
            title: "Login".into(),
            intent: "let users sign in".into(),
            nodes: vec![
                impact("i1", "REQ-1", REVIEW_ACCEPTED),
                impact("i2", "REQ-2", REVIEW_PROPOSED),
            ],
            items: vec![
                a(
                    "a1",
                    json!({"key": goap_assess::SUBJECT_DESCRIPTION, "score": score, "rated": desc}),
                ),
                a(
                    "a2",
                    json!({"key": format!("{}REQ-1", goap_assess::IMPACT_PREFIX), "score": 4, "verdict": "confirmed"}),
                ),
                a(
                    "a3",
                    json!({"key": format!("{}REQ-2", goap_assess::IMPACT_PREFIX), "score": 1, "verdict": "refuted"}),
                ),
            ],
            ..Default::default()
        },
        ..Default::default()
    }
}

fn state_of(states: &[AssessmentState], subject: &str) -> AssessmentState {
    states
        .iter()
        .find(|s| s.subject == subject)
        .cloned()
        .unwrap_or_else(|| panic!("no state for {subject} in {states:?}"))
}

#[test]
fn default_assessment_states() {
    let states = assessment_states(None, &assessed_board(4), &Result::default());
    let s = state_of(&states, "description");
    assert!(
        s.tone == TONE_OK && s.label == "accepted" && !s.open && s.score == 4,
        "description {s:?}"
    );
    let s = state_of(&states, "impact:REQ-1");
    assert!(
        s.tone == TONE_OK && s.label == "processed",
        "decided impact {s:?}"
    );
    let s = state_of(&states, "impact:REQ-2");
    assert!(
        s.tone == TONE_WARN && !s.open,
        "impact waiting for its review {s:?}"
    );
    let s = state_of(
        &assessment_states(None, &assessed_board(2), &Result::default()),
        "description",
    );
    assert!(
        s.tone == TONE_WARN && s.label == "to be improved" && !s.open,
        "low score {s:?}"
    );
    let mut bb = assessed_board(4);
    bb.change.intent = "let users sign in with SSO".into();
    let s = state_of(
        &assessment_states(None, &bb, &Result::default()),
        "description",
    );
    assert!(
        s.label == "changed since its rating" && !s.current,
        "stale {s:?}"
    );
}

#[test]
fn declared_assessment_states() {
    let set = compile(&[
        def(
            "gate",
            r#"assessments.exists(a, a.key == "description" && a.score >= 4)"#,
        ),
        Definition {
            name: "accepted".into(),
            scope: SCOPE_IMPACT.into(),
            expr: r#"self.review == "accepted""#.into(),
            ..Default::default()
        },
    ])
    .unwrap();
    let rule = |conds: &[(&str, bool)], when: &str, tone: &str, label: &str| StateRule {
        when: when.into(),
        conditions: world(conds),
        tone: tone.into(),
        label: label.into(),
        open: false,
    };
    let known = |n: &str| set.has(n);
    let (views, issues) = compile_assessment_views(
        &[
            SubjectView {
                subject: "description".into(),
                states: vec![
                    rule(&[("gate", true)], "", TONE_OK, "through the gate"),
                    rule(
                        &[],
                        "assessment.score <= 1 || subject.kind != 'description'",
                        TONE_DANGER,
                        "rewrite",
                    ),
                ],
            },
            SubjectView {
                subject: "impact:*".into(),
                states: vec![
                    rule(
                        &[("accepted", true)],
                        "",
                        TONE_INFO,
                        "accepted on its subject",
                    ),
                    rule(
                        &[],
                        "assessment.verdict == 'refuted' && subject.id == 'REQ-2' && !conditions.gate",
                        TONE_NEUTRAL,
                        "refuted",
                    ),
                ],
            },
        ],
        Some(&known),
    );
    assert!(issues.is_empty(), "{issues:?}");
    let bb = assessed_board(4);
    let states = views.states(&bb, &set.evaluate(&bb));
    let s = state_of(&states, "description");
    assert!(
        s.tone == TONE_OK && s.label == "through the gate",
        "description {s:?}"
    );
    let s = state_of(&states, "impact:REQ-1");
    assert_eq!(
        s.tone, TONE_INFO,
        "scoped condition read on the subject {s:?}"
    );
    let bb = assessed_board(3);
    let states = views.states(&bb, &set.evaluate(&bb));
    // no declared state holds: the defaults decide
    let s = state_of(&states, "description");
    assert!(
        s.tone == TONE_OK && s.label == "accepted",
        "falls back on the defaults {s:?}"
    );
    let s = state_of(&states, "impact:REQ-2");
    assert!(
        s.tone == TONE_NEUTRAL && s.label == "refuted",
        "when over the subject and the conditions {s:?}"
    );
}

#[test]
fn assessment_view_issues() {
    let st = |when: &str, conds: &[(&str, bool)], tone: &str| StateRule {
        when: when.into(),
        conditions: world(conds),
        tone: tone.into(),
        ..Default::default()
    };
    let none = |_: &str| false;
    let (_, issues) = compile_assessment_views(
        &[
            SubjectView {
                subject: "".into(),
                states: vec![st("", &[], TONE_OK)],
            },
            SubjectView {
                subject: "description".into(),
                states: vec![
                    st("", &[], "green"),
                    st("", &[("nope", true)], TONE_OK),
                    st("assessment.score +", &[], TONE_OK),
                    st("assessment.score", &[], TONE_OK),
                ],
            },
            SubjectView {
                subject: "title".into(),
                states: vec![],
            },
        ],
        Some(&none),
    );
    let got = issues
        .iter()
        .map(|i| i.path.clone())
        .collect::<Vec<_>>()
        .join(" ");
    for want in [
        "[0].subject",
        "[1].states[0].tone",
        "[1].states[1].conditions.nope",
        "[1].states[2].when",
        "[2].states",
    ] {
        assert!(got.contains(want), "missing issue at {want}: {issues:?}");
    }
}

// scope_test.go

fn scoped_board() -> Blackboard {
    let imp = |id: &str, key: &str, typ: &str, review, by: &str| ChangeImpact {
        id: id.into(),
        key: key.into(),
        r#type: typ.into(),
        intent: INTENT_MODIFIED,
        review,
        produced_by: by.into(),
        ..Default::default()
    };
    Blackboard {
        change: Change {
            id: "c".into(),
            nodes: vec![
                imp("i1", "REQ-1", "alm@Requirement", REVIEW_ACCEPTED, "scope"),
                imp(
                    "i2",
                    "REQ-2",
                    "alm@Requirement",
                    REVIEW_PROPOSED,
                    "propagate",
                ),
                imp("i3", "CMP-1", "alm@Component", REVIEW_PROPOSED, "propagate"),
            ],
            ..Default::default()
        },
        ..Default::default()
    }
}

fn scoped(name: &str, scope: &str, wh: &str, expr: &str) -> Definition {
    Definition {
        name: name.into(),
        scope: scope.into(),
        r#where: wh.into(),
        expr: expr.into(),
        ..Default::default()
    }
}

fn agg(name: &str, op: &str, of: &str, wh: &str, empty: bool) -> Definition {
    let mut d = Definition {
        name: name.into(),
        r#where: wh.into(),
        empty,
        ..Default::default()
    };
    match op {
        "all" => d.all = of.into(),
        "any" => d.any = of.into(),
        _ => d.none = of.into(),
    }
    d
}

#[test]
fn scoped_conditions_and_aggregates() {
    let set = compile(&[
        scoped("accepted", SCOPE_IMPACT, "", r#"self.review == "accepted""#),
        scoped(
            "requirement",
            SCOPE_IMPACT,
            r#"self.type == "alm@Requirement""#,
            r#"self.conditions.accepted || self.producedBy == "scope""#,
        ),
        agg("all_accepted", "all", "accepted", "", false),
        agg("requirements_ok", "all", "requirement", "", false),
        agg("any_accepted", "any", "accepted", "", false),
        agg("none_accepted", "none", "accepted", "", false),
        agg(
            "suspected_accepted",
            "all",
            "accepted",
            r#"self.producedBy == "propagate""#,
            false,
        ),
        agg(
            "no_tests",
            "all",
            "accepted",
            r#"self.type == "alm@TestCase""#,
            false,
        ),
        agg(
            "no_tests_ok",
            "all",
            "accepted",
            r#"self.type == "alm@TestCase""#,
            true,
        ),
        def(
            "reads",
            "changeImpacts.filter(i, i.conditions.accepted).size() == 1",
        ),
    ])
    .unwrap();
    let res = set.evaluate(&scoped_board());
    let want = [
        ("all_accepted", false),
        ("requirements_ok", false),
        ("any_accepted", true),
        ("none_accepted", false),
        ("suspected_accepted", false),
        ("no_tests", false),
        ("no_tests_ok", true),
        ("reads", true),
    ];
    for (k, v) in want {
        assert_eq!(res.state.get(k), Some(&v), "{k}; errors {:?}", res.errors);
    }
    assert!(
        !res.state.contains_key("accepted"),
        "a scoped condition is not in the world of the planner"
    );
    let subs = &res.subjects[SCOPE_IMPACT];
    assert!(
        subs.len() == 3
            && subs[0].id == "REQ-1"
            && subs[0].values["requirement"]
            && !subs[1].values["requirement"],
        "subjects {subs:?}"
    );
    assert!(
        !subs[2].values.contains_key("requirement"),
        "a subject outside the where has no value"
    );
    let ex = set.explain("suspected_accepted", &scoped_board()).unwrap();
    assert!(
        !ex.value
            && ex.inputs["failing"].contains("REQ-2")
            && ex.inputs["failing"].contains("CMP-1"),
        "explain {ex:?}"
    );
    let e = subject_entry(&scoped_board(), SCOPE_IMPACT, "CMP-1").expect("entry");
    assert_eq!(e["key"], "CMP-1");
    assert_eq!(set.scope_of("accepted"), SCOPE_IMPACT);
    assert_eq!(
        set.exprs()["suspected_accepted"],
        r#"all: accepted (where self.producedBy == "propagate")"#
    );
}

#[test]
fn scoped_compile_errors() {
    let cases: Vec<(&str, Vec<Definition>)> = vec![
        (
            "cycle",
            vec![
                scoped("a", SCOPE_IMPACT, "", "self.conditions.b"),
                scoped("b", SCOPE_IMPACT, "", "self.conditions.a"),
            ],
        ),
        (
            "other scope",
            vec![
                scoped("a", SCOPE_IMPACT, "", "true"),
                scoped("b", SCOPE_ITEM, "", "self.conditions.a"),
            ],
        ),
        ("unknown", vec![agg("x", "all", "nope", "", false)]),
        ("self", vec![def("x", "self.review == 'x'")]),
        ("scope", vec![scoped("x", "galaxy", "", "true")]),
        (
            "empty any",
            vec![
                scoped("a", SCOPE_IMPACT, "", "true"),
                agg("x", "any", "a", "", true),
            ],
        ),
    ];
    for (name, defs) in cases {
        assert!(compile(&defs).is_err(), "{name}: compiled");
    }
}

// event.go, expect.go

#[test]
fn event_filters() {
    let f = compile_event_filter(r#"event.type == "change.edited" && "title" in event.fields"#)
        .unwrap();
    assert!(f.matches(&obj(json!({"type": "change.edited", "fields": ["title"]}))));
    assert!(!f.matches(&obj(json!({"type": "change.edited", "fields": []}))));
    assert!(
        !f.matches(&obj(json!({}))),
        "an evaluation error does not match"
    );
    assert!(compile_event_filter("").unwrap().matches(&Map::new()));
    assert!(compile_event_filter("size(event)").is_err());
    assert!(compile_event_filter("change.id == 'x'").is_err());
}

#[test]
fn expectations() {
    let e = Expectation {
        for_each: "changeImpacts".into(),
        produce: ProduceSpec {
            op: "update_node".into(),
            ..Default::default()
        },
        ..Default::default()
    };
    let expr = e.expr().unwrap();
    assert!(compile(&[def("x", &expr)]).is_ok(), "{expr}");
    let e = Expectation {
        for_each: "changeImpacts".into(),
        r#where: r#"x.type == "alm@Requirement""#.into(),
        produce: ProduceSpec {
            op: "create_node".into(),
            node_type: "alm@TestCase".into(),
        },
        link: Some(LinkSpec {
            r#type: "alm@verifies".into(),
            direction: "".into(),
        }),
    };
    let expr = e.expr().unwrap();
    assert!(expr.contains(r#"n.type == "alm@TestCase""#), "{expr}");
    assert!(compile(&[def("x", &expr)]).is_ok(), "{expr}");
    let bad = Expectation {
        for_each: "items".into(),
        ..Default::default()
    };
    assert_eq!(
        bad.expr().unwrap_err().to_string(),
        r#"expects.forEach must be changeImpacts, got "items""#
    );
    let back: Expectation = serde_json::from_value(
        json!({"forEach": "changeImpacts", "produce": {"op": "update_node"}}),
    )
    .unwrap();
    assert_eq!(back.produce.op, "update_node");
    assert_eq!(
        serde_json::to_value(&back).unwrap(),
        json!({"forEach": "changeImpacts", "produce": {"op": "update_node"}})
    );
}

#[test]
fn check_definitions() {
    assert!(check_definition(&def("x", "true")).is_ok());
    assert_eq!(
        check_definition(&def("x", "size(items)"))
            .unwrap_err()
            .to_string(),
        "expression must return bool, got int"
    );
    assert!(check_definition(&scoped("x", SCOPE_IMPACT, "", "self.key == 'a'")).is_ok());
    assert_eq!(
        self_reads(r#"self.conditions.a && self.conditions["b"] || self.conditions.a"#),
        vec!["a".to_string(), "b".to_string()]
    );
}
