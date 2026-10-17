use super::load;
use crate::{FROM_INTEGRATED, Goal, Guardian, Integration, Methodology, Subscription};

// The release-based methodology integrates sdlc (ADR 0106).
#[test]
fn release_based_methodology() {
    let m = load("../../methodologies/release-based.yaml");
    let issues = m.validate_stored();
    assert!(!issues.has_errors(), "{issues}");
    m.compile().unwrap();
    assert!(
        m.integrates_changes_of("sdlc")
            && !m.integrates_changes_of("release-based")
            && !m.integrates_changes_of("risk-management"),
        "integrates {:?}",
        m.integrates
    );
    assert_eq!(
        m.merge_guard("sdlc"),
        r#"integrator.attached && (integrator.state == "integrating")"#
    );
    for s in &m.on {
        assert_eq!(s.origin(), FROM_INTEGRATED, "subscription {s:?}");
    }
}

#[test]
fn integration_issues() {
    let base = || Methodology {
        name: "rel".into(),
        namespace: "alm".into(),
        goals: vec![Goal {
            name: "g".into(),
            pre: [("x".to_string(), true)].into(),
            ..Goal::default()
        }],
        conditions: vec![crate::Condition {
            name: "x".into(),
            expr: "true".into(),
            ..crate::Condition::default()
        }],
        ..Methodology::default()
    };
    let integ = |names: &[&str], attach: &str| Integration {
        methodologies: names.iter().map(|s| s.to_string()).collect(),
        attach: attach.into(),
        ..Integration::default()
    };
    type Mut = Box<dyn Fn(&mut Methodology)>;
    let cases: Vec<(&str, Mut)> = vec![
        (
            "integrates[0].methodologies",
            Box::new(|m| m.integrates = vec![Integration::default()]),
        ),
        (
            "integrates[0].methodologies[0]",
            Box::new(move |m| m.integrates = vec![integ(&["rel"], "")]),
        ),
        (
            "integrates[0].attach",
            Box::new(move |m| m.integrates = vec![integ(&["sdlc"], "now")]),
        ),
        (
            "guardian",
            Box::new(|m| {
                m.guardian = Some(Guardian {
                    merge: "true".into(),
                    ..Guardian::default()
                })
            }),
        ),
        (
            "guardian.merge",
            Box::new(move |m| {
                m.integrates = vec![integ(&["sdlc"], "")];
                m.guardian = Some(Guardian {
                    merge: "integrator.state ==".into(),
                    ..Guardian::default()
                });
            }),
        ),
        (
            "on[0].from",
            Box::new(|m| {
                m.on = vec![Subscription {
                    event: "change.committed".into(),
                    from: "integrated".into(),
                    ..Subscription::default()
                }]
            }),
        ),
    ];
    for (path, mutate) in cases {
        let mut m = base();
        mutate(&mut m);
        let issues = m.validate();
        assert!(
            issues.0.iter().any(|is| is.path == path),
            "{path}: no issue ({issues})"
        );
    }
    let mut m = base();
    m.integrates = vec![integ(&["*"], "")];
    assert!(
        m.integrates_changes_of("sdlc") && !m.integrates_changes_of("rel"),
        "* integrates every other methodology"
    );
}
