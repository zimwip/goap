//! Port of projectaccess_test.go, with the reading of the other node kinds of a snapshot.

use goap_access::*;
use goap_authz::{Policy, Principal};
use goap_criticality::{C2, C3};
use goap_domain::{BaselineID, Change, Link, Node, NodeID, NodeRef};
use serde_json::{Map, Value, json};

fn node(id: &str, key: &str, typ: &str, props: Map<String, Value>) -> Node {
    Node {
        id: NodeID::from(id),
        key: key.into(),
        r#type: typ.into(),
        namespace: "organisation".into(),
        properties: props,
        ..Node::default()
    }
}

fn lnk(typ: &str, from: &str, to: &str) -> Link {
    Link {
        r#type: typ.into(),
        from: NodeRef::new(from, 0),
        to: NodeRef::new(to, 0),
        ..Link::default()
    }
}

fn roles(r: &[&str]) -> Map<String, Value> {
    Assignment {
        roles: r.iter().map(|s| s.to_string()).collect(),
        ..Assignment::default()
    }
    .props()
}

fn who(subject: &str, org: &str) -> Principal {
    Principal {
        subject: subject.into(),
        org: org.into(),
        ..Principal::default()
    }
}

fn snapshot(nodes: &[Node], links: &[Link]) -> Snapshot {
    build_snapshot(
        &goap_typecat::builtin().structures(),
        &BaselineID::from("b"),
        nodes,
        links,
    )
}

/// A principal may take a project as its active one when it holds a platform role, or a role an Assignment grants on
/// that project or one above it; a stranger, or an Assignment on a sibling project, gives no access.
#[test]
fn may_access_project() {
    let nodes = [
        node("1", "PROJ-ROOT", NODE_TYPE_PROJECT_UNIT, Map::new()),
        node("2", "PROJ-A", NODE_TYPE_PROJECT_UNIT, Map::new()),
        node("3", "PROJ-A1", NODE_TYPE_PROJECT_UNIT, Map::new()),
        node("4", "PROJ-B", NODE_TYPE_PROJECT_UNIT, Map::new()),
        node("5", "DEP-ONE", NODE_TYPE_ORG_UNIT, Map::new()),
        node(
            "6",
            "ASG:DEP-ONE/PROJ-A",
            NODE_TYPE_ASSIGNMENT,
            roles(&["developer"]),
        ),
        node("7", "DEP-TWO", NODE_TYPE_ORG_UNIT, Map::new()),
        node(
            "8",
            "ASG:DEP-TWO/PLATFORM",
            NODE_TYPE_ASSIGNMENT,
            roles(&[ROLE_ADMIN]),
        ),
    ];
    let links = [
        lnk(LINK_PROJECT_PART_OF, "1", "1"),
        lnk(LINK_PROJECT_PART_OF, "2", "1"),
        lnk(LINK_PROJECT_PART_OF, "3", "2"),
        lnk(LINK_PROJECT_PART_OF, "4", "1"),
        lnk(LINK_ASSIGNS_ORG, "6", "5"),
        lnk(LINK_ASSIGNS_PROJECT, "6", "2"),
        lnk(LINK_ASSIGNS_ORG, "8", "7"),
    ];
    let s = snapshot(&nodes, &links);
    let dev = who("dev", "DEP-ONE");
    let admin = who("ada", "DEP-TWO");
    for (p, project, want) in [
        (&dev, "PROJ-A", true),
        (&dev, "PROJ-A1", true),
        (&dev, "PROJ-B", false),
        (&dev, "PROJ-ROOT", false),
        (&admin, "PROJ-B", true),
        (&who("stranger", "OTHER"), "PROJ-A", false),
        (&Principal::default(), "PROJ-A", false),
    ] {
        assert_eq!(
            s.may_access_project(p, project),
            want,
            "{} on {project}",
            p.subject
        );
    }
    assert_eq!(
        s.sub_projects("PROJ-ROOT"),
        ["PROJ-ROOT", "PROJ-A", "PROJ-A1", "PROJ-B"]
    );
    assert_eq!(s.sub_projects("PROJ-NOPE"), ["PROJ-NOPE"]);
    assert!(s.enrich(&admin).roles.iter().any(|r| r == ROLE_ADMIN));
    assert_eq!(
        s.project_roles(&s.subject_chain(&dev), &s.project_chain("PROJ-A1")),
        ["developer"]
    );
}

/// The methodologies of a project are its own and those of its ancestors, nearest first, without repeats; a project
/// that is not in the organisation is not one.
#[test]
fn applicable_methodologies() {
    let project = |id: &str, key: &str, methods: &[&str]| {
        node(
            id,
            key,
            NODE_TYPE_PROJECT_UNIT,
            json!({"methodologies": methods})
                .as_object()
                .unwrap()
                .clone(),
        )
    };
    let nodes = [
        project("1", "PROJ-ROOT", &["base"]),
        project("2", "PROJ-A", &["sdlc", "base"]),
        project("3", "PROJ-A1", &["risk"]),
    ];
    let links = [
        lnk(LINK_PROJECT_PART_OF, "1", "1"),
        lnk(LINK_PROJECT_PART_OF, "2", "1"),
        lnk(LINK_PROJECT_PART_OF, "3", "2"),
    ];
    let s = snapshot(&nodes, &links);
    assert_eq!(
        s.applicable_methodologies("PROJ-A1").join(","),
        "risk,sdlc,base"
    );
    assert_eq!(s.applicable_methodologies("PROJ-ROOT").join(","), "base");
    assert!(s.has_project("PROJ-A") && !s.has_project("PROJ-NOPE"));
}

/// Users, policies and criticality policies are read from their nodes; what does not read is a problem, a retired
/// node is not there.
#[test]
fn snapshot_reads_users_policies_and_criticality() {
    let policy = Policy::new("true", "change", "read", "allow");
    let mut retired = node(
        "9",
        "USR:gone",
        NODE_TYPE_USER,
        json!({"subject": "gone"}).as_object().unwrap().clone(),
    );
    retired.state = STATE_RETIRED.into();
    let crit = |id: &str, owner: &str, level: &str| {
        let mut n = node(
            id,
            &format!("CRIT:{id}"),
            NODE_TYPE_CRITICALITY_POLICY,
            json!({"level": level, "maxDerogationHours": 48, "oracles": ["human"]})
                .as_object()
                .unwrap()
                .clone(),
        );
        n.owner = NodeID::from(owner);
        n
    };
    let user = User {
        subject: "ann".into(),
        display_name: "Ann".into(),
        ..User::default()
    };
    let nodes = [
        node("1", "ORG-DEFAULT", NODE_TYPE_ORG_UNIT, Map::new()),
        node("2", "DEP", NODE_TYPE_ORG_UNIT, Map::new()),
        node("3", "USR:ann", NODE_TYPE_USER, user.props()),
        node("4", "USR:", NODE_TYPE_USER, Map::new()),
        node(
            "5",
            &policy_key(&policy),
            NODE_TYPE_POLICY,
            policy_props(&policy),
        ),
        crit("6", "2", "C3"),
        crit("7", "2", "C3"),
        crit("8", "nobody", "C3"),
        retired,
    ];
    let links = [
        lnk(LINK_PART_OF, "2", "1"),
        lnk(LINK_MEMBER_OF, "3", "2"),
        lnk(LINK_MEMBER_OF, "9", "2"),
    ];
    let s = snapshot(&nodes, &links);
    assert_eq!(s.user("ann").map(|u| u.unit.as_str()), Some("DEP"));
    assert!(s.user("gone").is_none(), "a retired user is out of force");
    assert_eq!(s.policies, [policy]);
    assert_eq!(s.problems.len(), 3, "{:?}", s.problems);
    assert!(s.problems.iter().any(|p| p == "USR:: user without subject"));
    assert!(
        s.problems
            .iter()
            .any(|p| p.contains("already has a policy for C3"))
    );
    assert!(
        s.problems
            .iter()
            .any(|p| p.contains("is not a unit of the organisation"))
    );
    let change = Change {
        owner_org: "DEP".into(),
        ..Change::default()
    };
    let p = s.criticality_of(&change, &C3);
    assert_eq!(p.max_derogation, chrono::TimeDelta::hours(48));
    assert_eq!(p.oracles, ["human"]);
    assert_eq!(
        s.criticality_of(&change, &C2),
        goap_criticality::defaults()[&C2],
        "a level no unit holds takes the compiled-in table"
    );
    assert_eq!(s.chain("DEP"), ["DEP", "ORG-DEFAULT"]);
    let props = criticality_props(&C3, &p);
    assert_eq!(props["maxDerogationHours"], json!(48));
    assert_eq!(criticality_policy_from_props(&props).unwrap(), (C3, p));
    assert_eq!(criticality_policy_key("DEP", &C3), "CRIT:DEP/C3");
}
