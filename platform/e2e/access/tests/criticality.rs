//! The policy of a criticality level, resolved along the unit chain (ADR 0075 §3).

mod common;

use chrono::{DateTime, TimeDelta, Utc};
use common::*;
use goap_access as access;
use goap_criticality::{C1, C2, C3, Level, Policy};
use goap_domain::{Change, Link, Node, NodeID, NodeRef};
use serde_json::{Map, Value};

fn hours(h: i64) -> TimeDelta {
    TimeDelta::hours(h)
}

fn node(id: &str, key: &str, typ: &str, owner: &str, props: Map<String, Value>) -> Node {
    Node {
        id: NodeID::from(id),
        key: key.into(),
        r#type: typ.into(),
        namespace: "organisation".into(),
        properties: props,
        owner: NodeID::from(owner),
        ..Default::default()
    }
}

fn pol(id: &str, owner: &str, l: &Level, p: Policy) -> Node {
    node(
        id,
        &format!("CP-{id}"),
        access::NODE_TYPE_CRITICALITY_POLICY,
        owner,
        access::criticality_props(l, &p),
    )
}

fn part_of(from: &str, to: &str) -> Link {
    Link {
        r#type: access::LINK_PART_OF.into(),
        from: NodeRef {
            id: NodeID::from(from),
            ..Default::default()
        },
        to: NodeRef {
            id: NodeID::from(to),
            ..Default::default()
        },
        ..Default::default()
    }
}

fn change_of(unit: &str) -> Change {
    Change {
        owner_org: unit.into(),
        ..Default::default()
    }
}

fn zero_time() -> DateTime<Utc> {
    DateTime::<Utc>::MIN_UTC
}

// The policy of a criticality level is resolved along the unit chain, the nearest unit holding one for the level
// winning, the compiled-in table where none does (ADR 0075 §3, design rule 3).
#[test]
fn criticality_policy_resolution() {
    let unit = |id: &str, key: &str| node(id, key, access::NODE_TYPE_ORG_UNIT, "", Map::new());
    let mut bad = Map::new();
    bad.insert("level".into(), "C9".into());
    let nodes = vec![
        unit("1", "ORG-DEFAULT"),
        unit("2", "DEP"),
        unit("3", "TEAM"),
        unit("4", "OTHER"),
        pol(
            "10",
            "1",
            &C3,
            Policy {
                oracles: strings(&["human", "tool"]),
                signatory_role: "ciso".into(),
                max_derogation: hours(48),
                ..Default::default()
            },
        ),
        pol(
            "11",
            "2",
            &C3,
            Policy {
                oracles: strings(&["human"]),
                signatory_role: "dep_head".into(),
                max_derogation: hours(24),
                ..Default::default()
            },
        ),
        pol(
            "12",
            "4",
            &C1,
            Policy {
                oracles: strings(&["human"]),
                ..Default::default()
            },
        ),
        // a policy whose owner is no unit of the organisation, and one that names no level, are problems
        pol("13", "99", &C2, Policy::default()),
        node(
            "14",
            "CP-bad",
            access::NODE_TYPE_CRITICALITY_POLICY,
            "1",
            bad,
        ),
    ];
    let links = vec![part_of("2", "1"), part_of("3", "2"), part_of("4", "1")];
    let s = access::build_snapshot(
        &goap_typecat::builtin().structures(),
        &"b".into(),
        &nodes,
        &links,
    );
    assert_eq!(s.problems.len(), 2, "problems: {:?}", s.problems);
    let p = s.criticality_policy("TEAM", &C3);
    assert!(
        p.signatory_role == "dep_head" && p.max_derogation == hours(24),
        "the nearest unit wins: {p:?}"
    );
    let p = s.criticality_policy("OTHER", &C3);
    assert!(
        p.signatory_role == "ciso" && p.oracles.len() == 2,
        "a unit inherits its ancestor: {p:?}"
    );
    let p = s.criticality_policy("TEAM", &C1);
    assert!(
        p.accepts("model") && p.sampling,
        "the policy of a sibling does not apply, the table does: {p:?}"
    );
    let p = s.criticality_policy("OTHER", &C1);
    assert!(!p.accepts("tool"), "OTHER holds its own C1: {p:?}");
    let p = s.criticality_of(&change_of("TEAM"), &C3);
    assert_eq!(
        p.signatory_role, "dep_head",
        "a change is resolved from its unit: {p:?}"
    );
    let p = access::criticality_of(None, &change_of("TEAM"), &C2);
    assert!(
        p.accepts("tool") && !p.accepts("model"),
        "no snapshot, the table: {p:?}"
    );
    // round trip of the properties
    let (l, p) = access::criticality_policy_from_props(&access::criticality_props(
        &C2,
        &Policy {
            oracles: strings(&["tool"]),
            sampling: true,
            signatory_role: "r".into(),
            max_derogation: hours(36),
        },
    ))
    .unwrap();
    assert!(
        l == C2
            && p.sampling
            && p.signatory_role == "r"
            && p.max_derogation == hours(36)
            && p.oracles.len() == 1,
        "props: {l} {p:?}"
    );
    let mut neg = Map::new();
    neg.insert("level".into(), "C1".into());
    neg.insert("maxDerogationHours".into(), Value::from(-1.0));
    assert!(
        access::criticality_policy_from_props(&neg).is_err(),
        "a negative lifetime is refused"
    );
}

// Through a real graph: a unit owns a CriticalityPolicy node of the organisation domain, its sub-unit resolves it, the
// facet reads the snapshot last built and the compiled-in table applies before any.
#[tokio::test]
async fn criticality_policy_from_the_graph() {
    let (g, _) = setup();
    let dir = directory(&g);
    let facet = dir.criticality_facet();
    let v = facet(&change_of("TEAM"), zero_time());
    let p = v.downcast_ref::<Policy>().unwrap();
    assert!(p.accepts("tool"), "before any snapshot: {p:?}");
    g.bootstrap(&ctx()).await.unwrap();
    goap_graphsvc::seed_unit(&ctx(), &g, "DEP", "Department", "department", "")
        .await
        .unwrap();
    goap_graphsvc::seed_unit(&ctx(), &g, "TEAM", "Team", "team", "DEP")
        .await
        .unwrap();
    let want = Policy {
        oracles: strings(&["human"]),
        signatory_role: "dep_head".into(),
        max_derogation: hours(12),
        ..Default::default()
    };
    goap_graphsvc::seed_criticality_policy(&ctx(), &g, "DEP", &C2, &want)
        .await
        .unwrap();
    let s = dir.snapshot(&ctx()).await.map_err(|e| e.error).unwrap();
    assert!(s.problems.is_empty(), "snapshot: {:?}", s.problems);
    let mut c = change_of("TEAM");
    c.data
        .insert(goap_domain::DATA_CRITICALITY.into(), "C2".into());
    let p = s.criticality_of(&c, &C2);
    assert!(
        p.signatory_role == "dep_head" && p.max_derogation == hours(12) && !p.accepts("tool"),
        "resolved: {p:?}"
    );
    let p = dir.criticality_resolver().resolve(&ctx(), &c, &C2).await;
    assert_eq!(p.signatory_role, "dep_head", "resolver: {p:?}");
    let v = facet(&c, zero_time());
    let p = v.downcast_ref::<Policy>().unwrap();
    assert_eq!(p.signatory_role, "dep_head", "facet: {p:?}");
}
