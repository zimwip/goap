use std::sync::Arc;

use goap_condition::{Expectation, ProduceSpec};
use goap_def::{Domain, LinkType, NodeType, Schema};

use crate::{Action, Condition, Goal, KIND_HUMAN, Methodology};

pub(crate) fn test_domain() -> Domain {
    Domain {
        name: "alm".into(),
        version: "1".into(),
        schema: Schema {
            node_types: vec![
                NodeType {
                    name: "Requirement".into(),
                    ..NodeType::default()
                },
                NodeType {
                    name: "TestCase".into(),
                    ..NodeType::default()
                },
            ],
            link_types: vec![LinkType {
                name: "verifies".into(),
                from: "TestCase".into(),
                to: "Requirement".into(),
                ..LinkType::default()
            }],
            ..Schema::default()
        },
        ..Domain::default()
    }
}

pub(crate) fn ref_methodology() -> Methodology {
    Methodology {
        name: "m".into(),
        version: "1".into(),
        namespace: "alm".into(),
        conditions: vec![Condition {
            name: "c".into(),
            expr: r#"changeImpacts.exists(n, n.hasPost && n.post.out.exists(l, l.type == "alm@verifies"))"#
                .into(),
            ..Condition::default()
        }],
        actions: vec![Action {
            name: "a".into(),
            kind: KIND_HUMAN.into(),
            effects: [("c".to_string(), true)].into(),
            ..Action::default()
        }],
        goals: vec![Goal {
            name: "g".into(),
            pre: [("c".to_string(), true)].into(),
            ..Goal::default()
        }],
        ..Methodology::default()
    }
}

fn types() -> Arc<goap_def::DomainTypeSet> {
    Arc::new(goap_def::domain_types(&[test_domain()]))
}

#[test]
fn resolve_types() {
    let mut m = ref_methodology().resolve(types());
    let issues = m.validate();
    assert!(issues.0.is_empty(), "{issues}");
    let issues = ref_methodology().validate_stored();
    assert!(issues.0.is_empty(), "{issues}");
    m.namespace = String::new();
    let issues = m.validate_stored();
    assert!(
        issues.0.len() == 1 && issues.0[0].path == "namespace",
        "a stored methodology names its target namespace: {issues}"
    );
}

#[test]
fn reference_lint() {
    let mut m = ref_methodology();
    m.conditions[0].expr = r#"changeImpacts.exists(n, n.hasPost && n.post.out.exists(l, l.type == "alm@satisfies")) && changeImpacts.exists(n, "alm@Component" in n.types) && changeImpacts.exists(n, n.type == "Requirement")"#.into();
    m.actions[0].expects = Some(Expectation {
        for_each: "changeImpacts".into(),
        produce: ProduceSpec {
            op: "create_node".into(),
            node_type: "crm@Customer".into(),
        },
        ..Expectation::default()
    });
    let got = m.resolve(types()).validate().to_string();
    for want in [
        "unknown link type alm@satisfies",
        "unknown node type alm@Component",
        r#""Requirement" must be qualified"#,
        "unknown node type crm@Customer",
    ] {
        assert!(got.contains(want), "missing {want:?} in {got:?}");
    }
    m.actions[0].expects.as_mut().unwrap().produce.node_type = "organisation@OrgUnit".into(); // built in
    assert!(
        m.resolve(types())
            .validate()
            .to_string()
            .contains("a methodology creates nodes of its namespace"),
        "a methodology creates nodes of its target namespace only"
    );
}
