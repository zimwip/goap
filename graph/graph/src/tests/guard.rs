//! Port of guard_test.go (the parts of G1: the commit-based steps are written with the storage primitives).

use goap_domain::{
    Baseline, BaselineID, CHANGE_APPLIED, CHANGE_DRAFT, Change, ChangeID, DEFAULT_NAMESPACE, Link,
    LinkID, MAIN_BRANCH, Node, NodeID, NodeRef, REASON_CREATE,
};
use serde_json::json;

use crate::testsupport::*;
use crate::{ErrorKind, Graph, NewChange, Result, Tx};

async fn org_and_project(g: &Graph) -> (Node, Node) {
    let c = ctx();
    must(g.bootstrap(&c).await);
    let org = must(
        g.node_by_key(&c, NAMESPACE_ORGANISATION, &root_org(g))
            .await,
    );
    let proj = must(
        g.node_by_key(&c, NAMESPACE_ORGANISATION, &root_project(g))
            .await,
    );
    (org, proj)
}

// The bootstrap (ADR 0054) writes the root unit and the root project in one applied change: the root unit owns both,
// both were created in the root project, the root project is its own parent. Running it again changes nothing.
#[tokio::test]
async fn bootstrap() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let g = r.graph();
        let (org, proj) = org_and_project(&g).await;
        for n in [&org, &proj] {
            assert!(
                n.owner == org.id && n.project == proj.id && !n.change_id.is_empty(),
                "{}: owner {} project {} change {}",
                n.key,
                n.owner,
                n.project,
                n.change_id
            );
        }
        assert_eq!(org.r#type, NODE_TYPE_ORG_UNIT);
        assert_eq!(proj.r#type, NODE_TYPE_PROJECT_UNIT);
        let ch = must(g.change(&c, &org.change_id).await);
        assert!(
            ch.status == CHANGE_APPLIED
                && ch.owner_org == root_org(&g)
                && ch.project_id == root_project(&g)
                && !ch.result_baseline_id.is_empty(),
            "bootstrap change: {ch:?}"
        );
        let head = must(g.branch_head(&c, NAMESPACE_ORGANISATION, MAIN_BRANCH).await);
        assert!(
            head.contains(&org.r#ref()) && head.contains(&proj.r#ref()),
            "{:?}",
            head.nodes
        );
        let links = must(g.out_links_of(&c, &proj.r#ref()).await);
        assert!(
            links.len() == 1
                && links[0].r#type == LINK_PROJECT_PART_OF
                && links[0].to == proj.r#ref()
                && links[0].change_id == ch.id,
            "the root project is its own parent: {links:?}"
        );
        // again, on this graph and on another one over the same storage: nothing changes
        let before = must(g.changes(&c).await);
        must(g.bootstrap(&c).await);
        must(r.graph().bootstrap(&c).await);
        let after = must(g.changes(&c).await);
        assert_eq!(after.len(), before.len(), "bootstrapped twice");
    })
    .await;
}

async fn write<F>(g: &Graph, f: F) -> Result<()>
where
    F: AsyncFnOnce(&mut dyn Tx) -> Result<()>,
{
    let c = ctx();
    let mut tx = g.begin(&c).await?;
    f(&mut tx).await?;
    tx.commit(&c).await
}

fn note(g: &Graph, key: &str, change: &ChangeID) -> Node {
    Node {
        id: NodeID::new(g.new_id()),
        version: 1,
        branch: MAIN_BRANCH.into(),
        reason: REASON_CREATE.into(),
        key: key.into(),
        r#type: "Note".into(),
        change_id: change.clone(),
        created_at: g.now(),
        ..Default::default()
    }
}

// The guard (ADR 0054) refuses, whatever the storage, a write that names no change or a change that does not exist,
// and a change that names no unit or project, or names nodes that are not of their structures. Nothing of a refused
// transaction is kept.
#[tokio::test]
async fn guard_refuses_writes_outside_a_change() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let g = r.graph();
        let (org, proj) = org_and_project(&g).await;
        let mut boot = must(g.change(&c, &org.change_id).await);
        boot.items = Vec::new();
        boot.nodes = Vec::new();
        let none = ChangeID::default();
        let cases: Vec<(&str, Result<()>)> = vec![
            ("a node version without a change", {
                let n = note(&g, "N-1", &none);
                write(&g, async |tx| tx.put_node(&c, n).await).await
            }),
            ("a node version of a missing change", {
                let mut n = note(&g, "N-2", &ChangeID::new(g.new_id()));
                n.owner = org.id.clone();
                n.project = proj.id.clone();
                write(&g, async |tx| tx.put_node(&c, n).await).await
            }),
            ("a link without a change", {
                let l = Link {
                    id: LinkID::new(g.new_id()),
                    r#type: LINK_PROJECT_PART_OF.into(),
                    from: proj.r#ref(),
                    to: proj.r#ref(),
                    ..Default::default()
                };
                write(&g, async |tx| tx.put_link(&c, l).await).await
            }),
            ("a branch membership without a change", {
                let r = org.r#ref();
                write(&g, async |tx| {
                    tx.join_branch(&c, &r, "elsewhere", &none).await
                })
                .await
            }),
            ("a change without a unit", {
                let ch = Change {
                    id: ChangeID::new(g.new_id()),
                    title: "x".into(),
                    status: CHANGE_DRAFT,
                    project_id: root_project(&g),
                    ..Default::default()
                };
                write(&g, async |tx| tx.put_change(&c, ch).await).await
            }),
            ("a change held by a project", {
                let mut ch = boot.clone();
                ch.id = ChangeID::new(g.new_id());
                ch.owner_org = root_project(&g);
                write(&g, async |tx| tx.put_change(&c, ch).await).await
            }),
            ("a change acting in a unit", {
                let mut ch = boot.clone();
                ch.id = ChangeID::new(g.new_id());
                ch.project_id = root_org(&g);
                write(&g, async |tx| tx.put_change(&c, ch).await).await
            }),
            ("a version owned by a project", {
                let mut n = note(&g, "N-3", &org.change_id);
                n.owner = proj.id.clone();
                n.project = proj.id.clone();
                write(&g, async |tx| tx.put_node(&c, n).await).await
            }),
            ("a node created in a unit", {
                let mut n = note(&g, "N-4", &org.change_id);
                n.owner = org.id.clone();
                n.project = org.id.clone();
                write(&g, async |tx| tx.put_node(&c, n).await).await
            }),
        ];
        for (name, res) in cases {
            assert_kind(res, ErrorKind::Invalid, name);
        }
        for key in ["N-1", "N-2", "N-3", "N-4"] {
            assert_kind(
                g.node_by_key(&c, DEFAULT_NAMESPACE, key).await,
                ErrorKind::NotFound,
                &format!("{key} was kept"),
            );
        }
    })
    .await;
}

// A node is created in the project of the change that creates it and owned by the unit holding it; its next versions
// keep the project and the owner, unless a change transfers it to another unit (ADR 0054). (Go writes them through
// Commit; here through the storage primitives the commit uses.)
#[tokio::test]
async fn owner_and_project_of_nodes() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let g = r.graph();
        let (root, root_proj) = org_and_project(&g).await;
        let ns = NAMESPACE_ORGANISATION;
        let team = must(
            seed_node(
                &g,
                NewNode {
                    namespace: ns.into(),
                    key: "TEAM".into(),
                    r#type: NODE_TYPE_ORG_UNIT.into(),
                    properties: props(&[("name", json!("Team"))]),
                    links: vec![(LINK_PART_OF.into(), root.r#ref())],
                    ..Default::default()
                },
            )
            .await,
        );
        let pa = must(
            seed_node(
                &g,
                NewNode {
                    namespace: ns.into(),
                    key: "PROJ-A".into(),
                    r#type: NODE_TYPE_PROJECT_UNIT.into(),
                    properties: props(&[("name", json!("A"))]),
                    links: vec![(LINK_PROJECT_PART_OF.into(), root_proj.r#ref())],
                    ..Default::default()
                },
            )
            .await,
        );
        assert!(
            team.owner == root.id && team.project == root_proj.id,
            "a change naming no unit: the root unit, in the root project: {team:?}"
        );
        let head = must(g.branch_head(&c, ns, MAIN_BRANCH).await);
        let new_change = |owner: &str, project: &str| NewChange {
            namespace: ns.into(),
            title: "edit".into(),
            owner_org: owner.into(),
            project_id: project.into(),
            baseline_id: head.id.clone(),
            ..Default::default()
        };
        let by_team = must(g.create_change(&c, new_change("TEAM", "PROJ-A")).await);
        let mut n = note(&g, "NOTE", &by_team.id);
        n.namespace = ns.into();
        let id = n.id.clone();
        let n1 = n.clone();
        must(write(&g, async |tx| tx.put_node(&c, n1).await).await);
        let v1 = must(
            g.node(
                &c,
                &NodeRef {
                    id: id.clone(),
                    version: 1,
                },
            )
            .await,
        );
        assert!(
            v1.owner == team.id && v1.project == pa.id,
            "created by TEAM in PROJ-A: {v1:?}"
        );
        // a version of a change of the root unit in the root project: still TEAM's, still created in PROJ-A
        let by_root = must(g.create_change(&c, new_change("", "PROJ-ROOT")).await);
        let mut n2 = n.clone();
        n2.version = 2;
        n2.parents = vec![1];
        n2.change_id = by_root.id.clone();
        must(write(&g, async |tx| tx.put_node(&c, n2).await).await);
        let v2 = must(
            g.node(
                &c,
                &NodeRef {
                    id: id.clone(),
                    version: 2,
                },
            )
            .await,
        );
        assert!(
            v2.owner == team.id && v2.project == pa.id,
            "the next version keeps owner and project: {v2:?}"
        );
        // transferred to the root unit
        let mut n3 = n.clone();
        n3.version = 3;
        n3.parents = vec![2];
        n3.change_id = by_root.id.clone();
        n3.owner = root.id.clone();
        must(write(&g, async |tx| tx.put_node(&c, n3).await).await);
        let v3 = must(
            g.node(
                &c,
                &NodeRef {
                    id: id.clone(),
                    version: 3,
                },
            )
            .await,
        );
        assert!(
            v3.owner == root.id && v3.project == pa.id,
            "transferred: {v3:?}"
        );
        // the project never changes, and only a unit owns
        let mut n4 = n.clone();
        n4.version = 4;
        n4.change_id = by_root.id.clone();
        n4.project = root_proj.id.clone();
        assert_kind(
            write(&g, async |tx| tx.put_node(&c, n4).await).await,
            ErrorKind::Invalid,
            "another project",
        );
        let mut n5 = n.clone();
        n5.version = 4;
        n5.change_id = by_root.id.clone();
        n5.owner = pa.id.clone();
        assert_kind(
            write(&g, async |tx| tx.put_node(&c, n5).await).await,
            ErrorKind::Invalid,
            "a project as the owner",
        );
    })
    .await;
}

// A change names the project it acts in (ADR 0091): none is defaulted, a sub-change inherits its parent's.
#[tokio::test]
async fn change_needs_a_project() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let g = r.graph();
        must(g.bootstrap(&c).await);
        let base = must(g.branch_head(&c, DEFAULT_NAMESPACE, MAIN_BRANCH).await);
        let nc = |title: &str, project: &str| NewChange {
            title: title.into(),
            baseline_id: base.id.clone(),
            project_id: project.into(),
            ..Default::default()
        };
        assert_kind(
            g.create_change(&c, nc("x", "")).await,
            ErrorKind::Invalid,
            "no project",
        );
        assert!(
            g.create_change(&c, nc("x", "PROJ-NOPE")).await.is_err(),
            "an unknown project"
        );
        let parent = must(
            g.create_change(
                &c,
                NewChange {
                    own_branch: true,
                    ..nc("p", &root_project(&g))
                },
            )
            .await,
        );
        assert_eq!(parent.project_id, root_project(&g));
        let sub = must(
            g.create_change(
                &c,
                NewChange {
                    title: "s".into(),
                    parent_id: parent.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(
            sub.project_id, parent.project_id,
            "a sub-change inherits the project"
        );
    })
    .await;
}

// The storage repeats the rule (ADR 0054): a node version or a branch membership without a change is refused by the
// database itself, not only by the guard.
#[tokio::test]
async fn storage_requires_a_change() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let g = r.graph();
        let (org, _) = org_and_project(&g).await;
        let (id, change, other) = (org.id.to_string(), org.change_id.to_string(), g.new_id());
        let cases: Vec<(&str, String, Vec<String>)> = vec![
            ("a version without a change", "INSERT INTO node_version (node_id, version, props, change_id, owner_id, created_at) VALUES ($1, 99, '{}', NULL, $2, '2026-01-01T00:00:00Z')".into(), vec![id.clone(), id.clone()]),
            ("a version without an owner", "INSERT INTO node_version (node_id, version, props, change_id, owner_id, created_at) VALUES ($1, 99, '{}', $2, NULL, '2026-01-01T00:00:00Z')".into(), vec![id.clone(), change.clone()]),
            ("a branch membership without a change", "INSERT INTO node_branch (node_id, version, branch, change_id) VALUES ($1, 1, 'x', NULL)".into(), vec![id.clone()]),
            ("a version of a missing change", "INSERT INTO node_version (node_id, version, props, change_id, owner_id, created_at) VALUES ($1, 99, '{}', $2, $1, '2026-01-01T00:00:00Z')".into(), vec![id.clone(), other.clone()]),
        ];
        for (name, q, args) in cases {
            let res = match &r.store {
                Store::Memory => return, // no database constraints in memory: the guard alone
                Store::Sqlite(pool) => {
                    let q = q.replace("$1", "?1").replace("$2", "?2");
                    let mut qy = sqlx::query(sqlx::AssertSqlSafe(q));
                    for a in &args {
                        qy = qy.bind(a.clone());
                    }
                    qy.execute(pool).await.map(|_| ())
                }
                Store::Postgres(pool) => {
                    let mut qy = sqlx::query(sqlx::AssertSqlSafe(q));
                    for a in &args {
                        qy = qy.bind(uuid::Uuid::parse_str(a).expect("uuid"));
                    }
                    qy.execute(pool).await.map(|_| ())
                }
            };
            assert!(res.is_err(), "{name}: accepted by the database");
        }
        let _ = c;
    })
    .await;
}

// Every baseline is the result of a change (ADR 0056); what precedes the first change of a namespace is the empty
// state, which nothing stores.
#[tokio::test]
async fn guard_baseline_needs_a_change() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let g = r.graph();
        let (org, _) = org_and_project(&g).await;
        let put = async |mut b: Baseline| -> Result<()> {
            b.id = BaselineID::new(g.new_id());
            b.namespace = "scratch".into();
            b.branch = MAIN_BRANCH.into();
            b.created_at = g.now();
            write(&g, async |tx| tx.put_baseline(&c, b).await).await
        };
        let head = must(g.branch_head(&c, NAMESPACE_ORGANISATION, MAIN_BRANCH).await);
        for (name, b) in [
            (
                "empty",
                Baseline {
                    name: "e".into(),
                    ..Default::default()
                },
            ),
            (
                "with nodes",
                Baseline {
                    name: "n".into(),
                    nodes: [(org.id.clone(), 1)].into(),
                    ..Default::default()
                },
            ),
            (
                "with parent",
                Baseline {
                    name: "p".into(),
                    parent_id: head.id.clone(),
                    ..Default::default()
                },
            ),
        ] {
            assert_kind(put(b).await, ErrorKind::Invalid, name);
        }
        must(
            put(Baseline {
                name: "after the bootstrap".into(),
                change_id: org.change_id.clone(),
                ..Default::default()
            })
            .await,
        );
        assert_kind(
            put(Baseline {
                name: "ghost".into(),
                change_id: ChangeID::new(g.new_id()),
                ..Default::default()
            })
            .await,
            ErrorKind::Invalid,
            "a baseline of a change that does not exist",
        );
    })
    .await;
}

// A version is immutable once written (ADR 0079): the version and its outgoing links are written in one transaction,
// a later one adds no link to it, whatever the storage.
#[tokio::test]
async fn guard_keeps_versions_immutable() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let g = r.graph();
        let (org, _) = org_and_project(&g).await;
        let mut n = note(&g, "N-1", &org.change_id);
        n.namespace = "notes".into();
        let link = |g: &Graph| Link {
            id: LinkID::new(g.new_id()),
            r#type: "refines".into(),
            from: n.r#ref(),
            to: n.r#ref(),
            change_id: org.change_id.clone(),
            ..Default::default()
        };
        let (n1, l1) = (n.clone(), link(&g));
        must(
            write(&g, async |tx| {
                tx.put_node(&c, n1).await?;
                tx.put_link(&c, l1).await
            })
            .await,
        );
        let l2 = link(&g);
        assert_kind(
            write(&g, async |tx| tx.put_link(&c, l2).await).await,
            ErrorKind::Conflict,
            "a link added to a version written earlier",
        );
        assert_eq!(must(g.out_links_of(&c, &n.r#ref()).await).len(), 1);
    })
    .await;
}
