//! Access read from a real graph seeded by the graph service (ADR 0125: pkg/access names the graph through its contract
//! only).

mod common;

use common::*;
use goap_access as access;
use goap_authz::{Authorizer as _, Policy, Principal, Request, Resource};
use goap_domain::{self as domain, Link, Node, NodeID, NodeRef};
use goap_graph::{Commit, Graph, LinkEdit, NodeEdit};

fn node(id: &str, key: &str, typ: &str, ns: &str) -> Node {
    Node {
        id: NodeID::from(id),
        key: key.into(),
        r#type: typ.into(),
        namespace: ns.into(),
        ..Default::default()
    }
}

fn lnk(typ: &str, from: &str, to: &str) -> Link {
    Link {
        r#type: typ.into(),
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

async fn org_ref(g: &Graph, key: &str) -> NodeRef {
    g.node_by_key(&ctx(), access::NAMESPACE_ORGANISATION, key)
        .await
        .unwrap()
        .r#ref()
}

async fn commit(g: &Graph, title: &str, baseline_name: &str, edits: Vec<NodeEdit>) {
    let head = g
        .branch_head(&ctx(), access::NAMESPACE_ORGANISATION, domain::MAIN_BRANCH)
        .await
        .unwrap();
    Box::pin(g.commit(
        &ctx(),
        Commit {
            project_id: "PROJ-ROOT".into(),
            namespace: access::NAMESPACE_ORGANISATION.into(),
            title: title.into(),
            baseline: head.id,
            by: "test".into(),
            baseline_name: baseline_name.into(),
            edits,
            ..Default::default()
        },
    ))
    .await
    .unwrap();
}

fn assignment(key: String, roles: &[&str], links: Vec<LinkEdit>) -> NodeEdit {
    NodeEdit {
        key,
        r#type: access::NODE_TYPE_ASSIGNMENT.into(),
        props: access::Assignment {
            roles: strings(roles),
            ..Default::default()
        }
        .props(),
        rationale: "t".into(),
        links,
        ..Default::default()
    }
}

fn link_to(typ: &str, to: &NodeRef) -> LinkEdit {
    LinkEdit {
        r#type: typ.into(),
        to: Some(to.clone()),
        ..Default::default()
    }
}

fn link_to_key(typ: &str, key: &str) -> LinkEdit {
    LinkEdit {
        r#type: typ.into(),
        to_key: key.into(),
        ..Default::default()
    }
}

fn project_edit(key: &str, name: &str, root: &NodeRef) -> NodeEdit {
    NodeEdit {
        key: key.into(),
        r#type: access::NODE_TYPE_PROJECT_UNIT.into(),
        props: access::ProjectUnit {
            name: name.into(),
            ..Default::default()
        }
        .props(),
        rationale: "t".into(),
        links: vec![link_to(access::LINK_PROJECT_PART_OF, root)],
        ..Default::default()
    }
}

fn deny_admin() -> Policy {
    Policy {
        rule: r#"hasRole(r.sub, "admin")"#.into(),
        resource: "*".into(),
        action: "*".into(),
        effect: "deny".into(),
    }
}

#[tokio::test]
async fn defaults_while_the_graph_has_no_policy() {
    let (_, a) = setup();
    let admin = principal("root", "", &["admin"]);
    let contrib = principal("c", "o", &["contributor"]);
    assert!(
        can(&a, &admin, "policy", "write").await,
        "admin denied on an empty graph"
    );
    assert!(
        can(&a, &contrib, "process", "start").await
            && !can(&a, &contrib, "methodology", "write").await,
        "default policies not applied on an empty graph"
    );
    assert!(
        !can(&a, &Principal::default(), "process", "start").await,
        "anonymous allowed"
    );
}

#[tokio::test]
async fn policies_come_from_nodes() {
    let (g, a) = setup();
    assert!(goap_graphsvc::seed_access(&ctx(), &g).await.unwrap());
    assert!(
        !goap_graphsvc::seed_access(&ctx(), &g).await.unwrap(),
        "seed must be idempotent"
    );
    let contrib = principal("c", "o", &["contributor"]);
    assert!(
        can(&a, &contrib, "process", "start").await,
        "seeded default policy not applied"
    );
    // a new rule authorises what the defaults do not
    assert!(
        !can(&a, &contrib, "report", "read-all").await,
        "unexpected access"
    );
    goap_graphsvc::seed_policy(
        &ctx(),
        &g,
        &Policy {
            rule: r#"hasRole(r.sub, "contributor")"#.into(),
            resource: "report".into(),
            action: "read-all".into(),
            effect: "allow".into(),
        },
    )
    .await
    .unwrap();
    assert!(
        can(&a, &contrib, "report", "read-all").await,
        "policy node not applied"
    );
}

#[tokio::test]
async fn administrators_are_never_locked_out() {
    let (g, a) = setup();
    goap_graphsvc::seed_access(&ctx(), &g).await.unwrap();
    goap_graphsvc::seed_policy(&ctx(), &g, &deny_admin())
        .await
        .unwrap();
    let admin = principal("root", "", &["admin"]);
    assert!(
        can(&a, &admin, "policy", "write").await
            && can(&a.floor(), &admin, "policy", "write").await,
        "a deny policy locked the administrator out"
    );
}

#[tokio::test]
async fn admin_assignment_and_unit_of_the_user_node() {
    let (g, a) = setup();
    g.bootstrap(&ctx()).await.unwrap();
    goap_graphsvc::seed_access(&ctx(), &g).await.unwrap();
    goap_graphsvc::seed_unit(&ctx(), &g, "team-a", "Team A", "team", "")
        .await
        .unwrap();
    goap_graphsvc::seed_user(
        &ctx(),
        &g,
        &access::User {
            subject: "alice".into(),
            unit: "team-a".into(),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let alice_ref = org_ref(&g, &access::user_key("alice")).await;
    commit(
        &g,
        "alice administers",
        "",
        vec![assignment(
            access::platform_assignment_key(&access::user_key("alice")),
            &[access::ROLE_ADMIN],
            vec![link_to(access::LINK_ASSIGNS_ORG, &alice_ref)],
        )],
    )
    .await;
    let alice = principal("alice", "", &[]);
    assert!(
        can(&a, &alice, "policy", "write").await
            && can(&a.floor(), &alice, "policy", "write").await,
        "the platform admin assignment of the User node was not granted"
    );
    let dir = directory(&g);
    let p = dir.enrich(&ctx(), &alice).await;
    assert!(
        p.org == "team-a" && p.roles == strings(&[access::ROLE_ADMIN]),
        "enriched principal: {p:?}"
    );
    let p = dir.enrich(&ctx(), &principal("bob", "", &[])).await;
    assert!(
        p.roles.is_empty() && p.org.is_empty(),
        "unknown subject must be left alone: {p:?}"
    );
}

#[test]
fn roles_held_in_a_unit_hold_below_it() {
    let unit = |id: &str, key: &str| node(id, key, access::NODE_TYPE_ORG_UNIT, "organisation");
    let nodes = vec![
        unit("1", "ORG-DEFAULT"),
        unit("2", "DEP-IT"),
        unit("3", "TEAM-PAY"),
    ];
    let s = access::build_snapshot(
        &goap_typecat::builtin().structures(),
        &"b".into(),
        &nodes,
        &[
            lnk(access::LINK_PART_OF, "3", "2"),
            lnk(access::LINK_PART_OF, "2", "1"),
        ],
    );
    assert_eq!(
        s.chain("TEAM-PAY"),
        strings(&["TEAM-PAY", "DEP-IT", "ORG-DEFAULT"])
    );
    assert_eq!(
        s.chain("UNKNOWN"),
        strings(&["UNKNOWN", "ORG-DEFAULT"]),
        "an unknown unit is under the default unit"
    );
}

// An Assignment grants a role on a project (ADR 0039): a step check with no token-level role passes when an Assignment
// gives the principal's org the RACI role for the change's project, and fails for an unrelated project.
#[tokio::test]
async fn assignment_grants_role_on_a_project() {
    let (g, a) = setup();
    g.bootstrap(&ctx()).await.unwrap();
    goap_graphsvc::seed_unit(&ctx(), &g, "team-a", "Team A", "team", "")
        .await
        .unwrap();
    let team_ref = org_ref(&g, "team-a").await;
    let root_ref = org_ref(&g, access::DEFAULT_PROJECT).await;
    commit(
        &g,
        "assignment",
        "assignment",
        vec![
            project_edit("PROJ-X", "X", &root_ref),
            project_edit("PROJ-Y", "Y", &root_ref),
            assignment(
                "ASG:team-a/PROJ-X".into(),
                &["developer"],
                vec![
                    link_to(access::LINK_ASSIGNS_ORG, &team_ref),
                    link_to_key(access::LINK_ASSIGNS_PROJECT, "PROJ-X"),
                ],
            ),
        ],
    )
    .await;

    let dev = principal("dev", "team-a", &[]);
    let step = |project: &str| Resource {
        r#type: "step".into(),
        org: "team-a".into(),
        project_id: project.into(),
        role: "developer".into(),
        ..Default::default()
    };
    let req = |project: &str| Request {
        subject: dev.clone(),
        action: "perform".into(),
        resource: step(project),
    };
    assert!(
        a.authorize(&ctx(), &req("PROJ-X")).await.unwrap(),
        "the assignment must grant developer on PROJ-X"
    );
    assert!(
        !a.authorize(&ctx(), &req("PROJ-Y")).await.unwrap(),
        "the assignment must not grant developer on an unrelated project"
    );
}

// A platform-wide Assignment (no assigns_project link, ADR 0046) grants a role everywhere.
#[tokio::test]
async fn platform_assignment_grants_role_everywhere() {
    let (g, a) = setup();
    g.bootstrap(&ctx()).await.unwrap();
    goap_graphsvc::seed_unit(&ctx(), &g, "team-a", "Team A", "team", "")
        .await
        .unwrap();
    goap_graphsvc::seed_unit(&ctx(), &g, "other-org", "Other", "org", "")
        .await
        .unwrap();
    let team_ref = org_ref(&g, "team-a").await;
    commit(
        &g,
        "platform assignment",
        "platform assignment",
        vec![assignment(
            access::platform_assignment_key("team-a"),
            &[access::ROLE_READER],
            vec![link_to(access::LINK_ASSIGNS_ORG, &team_ref)],
        )],
    )
    .await;

    let read = |who: Principal, org: &str| Request {
        subject: who,
        action: "read".into(),
        resource: Resource {
            r#type: "x".into(),
            org: org.into(),
            ..Default::default()
        },
    };
    // a platform reader reads an object of an unrelated org, past the usual org/project scoping
    assert!(
        a.authorize(&ctx(), &read(principal("dev", "team-a", &[]), "other-org"))
            .await
            .unwrap(),
        "the platform assignment must grant read everywhere"
    );
    assert!(
        !a.authorize(
            &ctx(),
            &read(principal("stranger", "other-org", &[]), "team-a")
        )
        .await
        .unwrap(),
        "a unit without the platform assignment must not read another org's resource"
    );
}

// Administration is itself a platform role now (ADR 0047): a unit granted "admin" through a platform Assignment reaches
// the floor the same way a user's own Assignment does.
#[tokio::test]
async fn platform_assignment_grants_admin_past_the_floor() {
    let (g, a) = setup();
    g.bootstrap(&ctx()).await.unwrap();
    goap_graphsvc::seed_access(&ctx(), &g).await.unwrap();
    goap_graphsvc::seed_policy(&ctx(), &g, &deny_admin())
        .await
        .unwrap();
    goap_graphsvc::seed_unit(&ctx(), &g, "team-a", "Team A", "team", "")
        .await
        .unwrap();
    let team_ref = org_ref(&g, "team-a").await;
    commit(
        &g,
        "admin assignment",
        "admin assignment",
        vec![assignment(
            access::platform_assignment_key("team-a"),
            &[access::ROLE_ADMIN],
            vec![link_to(access::LINK_ASSIGNS_ORG, &team_ref)],
        )],
    )
    .await;
    let dev = principal("dev", "team-a", &[]);
    assert!(
        can(&a, &dev, "policy", "write").await && can(&a.floor(), &dev, "policy", "write").await,
        "the platform admin assignment must pass the floor, deny policy notwithstanding"
    );
}

#[test]
fn project_chain_and_roles() {
    let proj = |id: &str, key: &str| node(id, key, access::NODE_TYPE_PROJECT_UNIT, "organisation");
    let unit = |id: &str, key: &str| node(id, key, access::NODE_TYPE_ORG_UNIT, "organisation");
    let mut asg = node(
        "5",
        "ASG:DEP-IT/PROJ-A",
        access::NODE_TYPE_ASSIGNMENT,
        "organisation",
    );
    asg.properties = access::Assignment {
        roles: strings(&["developer"]),
        ..Default::default()
    }
    .props();
    let nodes = vec![
        proj("1", "PROJ-ROOT"),
        proj("2", "PROJ-A"),
        proj("3", "PROJ-A1"),
        unit("4", "DEP-IT"),
        asg,
    ];
    let links = vec![
        lnk(access::LINK_PROJECT_PART_OF, "1", "1"), // root self-link
        lnk(access::LINK_PROJECT_PART_OF, "2", "1"),
        lnk(access::LINK_PROJECT_PART_OF, "3", "2"),
        lnk(access::LINK_ASSIGNS_ORG, "5", "4"),
        lnk(access::LINK_ASSIGNS_PROJECT, "5", "2"),
    ];
    let s = access::build_snapshot(
        &goap_typecat::builtin().structures(),
        &"b".into(),
        &nodes,
        &links,
    );
    assert_eq!(
        s.project_chain("PROJ-A1"),
        strings(&["PROJ-A1", "PROJ-A", "PROJ-ROOT"])
    );
    assert_eq!(
        s.project_chain("PROJ-ROOT"),
        strings(&["PROJ-ROOT"]),
        "root project chain must not loop on its own self-link"
    );
    // a project and the ones below it (a search in a project and its sub-projects, ADR 0095)
    assert_eq!(s.sub_projects("PROJ-A"), strings(&["PROJ-A", "PROJ-A1"]));
    assert_eq!(
        s.sub_projects("PROJ-ROOT"),
        strings(&["PROJ-ROOT", "PROJ-A", "PROJ-A1"])
    );
    assert_eq!(s.sub_projects("PROJ-A1"), strings(&["PROJ-A1"]));
    assert_eq!(s.sub_projects("nope"), strings(&["nope"]));
    // the assignment is on PROJ-A: it holds for PROJ-A and PROJ-A1 (below it), not for PROJ-ROOT alone
    assert_eq!(
        s.project_roles(&s.chain("DEP-IT"), &s.project_chain("PROJ-A1")),
        strings(&["developer"])
    );
    assert!(
        s.project_roles(&s.chain("DEP-IT"), &s.project_chain("PROJ-ROOT"))
            .is_empty(),
        "the assignment must not leak to the root project"
    );
    assert!(
        s.project_roles(&s.chain("OTHER-UNIT"), &s.project_chain("PROJ-A1"))
            .is_empty(),
        "the assignment must not leak to another unit"
    );
}

// A user's roles depend on the project (ADR 0043): an Assignment to the user itself (User extends OrgUnit) and one to
// its unit add up on the project they name, and nowhere else.
#[tokio::test]
async fn roles_depend_on_the_project() {
    let (g, a) = setup();
    g.bootstrap(&ctx()).await.unwrap();
    goap_graphsvc::seed_unit(&ctx(), &g, "team-a", "Team A", "team", "")
        .await
        .unwrap();
    goap_graphsvc::seed_user(
        &ctx(),
        &g,
        &access::User {
            subject: "eve".into(),
            unit: "team-a".into(),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let root = org_ref(&g, access::DEFAULT_PROJECT).await;
    let eve_ref = org_ref(&g, "USR:eve").await;
    let team_ref = org_ref(&g, "team-a").await;
    let assign = |org: &str, r: &NodeRef, proj: &str, roles: &[&str]| {
        assignment(
            format!("ASG:{org}/{proj}"),
            roles,
            vec![
                link_to(access::LINK_ASSIGNS_ORG, r),
                link_to_key(access::LINK_ASSIGNS_PROJECT, proj),
            ],
        )
    };
    commit(
        &g,
        "assignments",
        "",
        vec![
            project_edit("PROJ-A", "PROJ-A", &root),
            project_edit("PROJ-B", "PROJ-B", &root),
            project_edit("PROJ-C", "PROJ-C", &root),
            assign("USR:eve", &eve_ref, "PROJ-A", &["developer"]),
            assign("team-a", &team_ref, "PROJ-A", &["tester"]),
            assign("USR:eve", &eve_ref, "PROJ-B", &["tech_lead"]),
        ],
    )
    .await;
    let run = async |who: &Principal, org: &str, project: &str, roles: &[&str]| {
        a.authorize(
            &ctx(),
            &Request {
                subject: who.clone(),
                action: "run".into(),
                resource: Resource {
                    r#type: "action".into(),
                    org: org.into(),
                    project_id: project.into(),
                    roles: strings(roles),
                    ..Default::default()
                },
            },
        )
        .await
        .unwrap()
    };
    let eve = principal("eve", "", &[]);
    let cases: &[(&str, &[&str], bool)] = &[
        ("PROJ-A", &["developer"], true),  // held by eve herself
        ("PROJ-A", &["tester"], true),     // held by her unit
        ("PROJ-A", &["tech_lead"], false), // held on another project
        ("PROJ-B", &["tech_lead"], true),
        ("PROJ-B", &["developer"], false),
        ("PROJ-C", &[], false), // not a member of PROJ-C at all
        ("PROJ-B", &[], true),  // any role on PROJ-B
    ];
    let mut failures = Vec::new();
    for (project, roles, want) in cases {
        let got = run(&eve, "", project, roles).await;
        if got != *want {
            failures.push(format!(
                "eve on {project} for {roles:?}: {got}, want {want}"
            ));
        }
    }
    assert!(failures.is_empty(), "{failures:?}");
    // someone outside team-a acting on a change team-a holds does not get team-a's roles
    let mallory = principal("mallory", "other", &[]);
    assert!(
        !run(&mallory, "team-a", "PROJ-A", &["tester"]).await,
        "mallory got team-a's role"
    );
}

// The snapshot reads the organisation and the projects the graph names (ADR 0054), never types of its own.
#[test]
fn snapshot_follows_the_structures() {
    let st = domain::Structures(vec![
        domain::StructureSet {
            structure: domain::Structure {
                kind: domain::STRUCTURE_ORGANISATION.into(),
                r#type: "hr@Team".into(),
                namespace: "hr".into(),
                parent: "hr@within".into(),
                root: "HR-ROOT".into(),
                ..Default::default()
            },
            types: strings(&["hr@Team"]),
        },
        domain::StructureSet {
            structure: domain::Structure {
                kind: domain::STRUCTURE_PROJECT.into(),
                r#type: "hr@Programme".into(),
                namespace: "hr".into(),
                parent: "hr@inside".into(),
                root: "PRG-ROOT".into(),
                self_parent: true,
                ..Default::default()
            },
            types: strings(&["hr@Programme"]),
        },
    ]);
    let n = |id: &str, key: &str, typ: &str| node(id, key, typ, "hr");
    let nodes = vec![
        n("1", "HR-ROOT", "hr@Team"),
        n("2", "PAY", "hr@Team"),
        n("3", "PRG-ROOT", "hr@Programme"),
        n("4", "PRG-A", "hr@Programme"),
        n("5", "OLD", "organisation@OrgUnit"),
    ];
    let links = vec![
        lnk("hr@within", "2", "1"),
        lnk("hr@inside", "4", "3"),
        lnk("hr@inside", "3", "3"),
        lnk(access::LINK_PART_OF, "5", "1"),
    ];
    let s = access::build_snapshot(&st, &"b".into(), &nodes, &links);
    assert_eq!(s.chain("PAY"), strings(&["PAY", "HR-ROOT"]));
    assert_eq!(
        s.chain("OLD"),
        strings(&["OLD", "HR-ROOT"]),
        "a link the structures do not name is no hierarchy"
    );
    assert_eq!(s.project_chain("PRG-A"), strings(&["PRG-A", "PRG-ROOT"]));
    assert_eq!(
        s.subject_chain(&principal("x", "", &[])),
        vec![access::user_key("x"), "HR-ROOT".to_string()],
        "a subject of no unit is under the root unit"
    );
}
