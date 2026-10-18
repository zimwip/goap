mod common;

use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use common::{by, code, rpc, serve, typed_graph};
use goap_access as access;
use goap_authz::{Authorizer, Principal, with_principal};
use goap_connect::{Code, Interceptors};
use goap_ctx::Ctx;
use goap_domain::{Change, MAIN_BRANCH, Node};
use goap_gen::graph::v1::{self as pb, GraphService};
use goap_graph::{Commit, Graph, LinkEdit, NewChange, NodeEdit, ProjectMoveGate};
use goap_graphsvc::{Handler, OWNER_ME, SYSTEM_PRINCIPAL, project_move_gate, seed_unit};
use parking_lot::{Mutex, RwLock};

/// The gate of the graph, set once the graph is shared (the directory of the gate reads the graph it gates: Go
/// assigns the field after building both).
#[derive(Default)]
struct Swap(RwLock<Option<Arc<dyn ProjectMoveGate>>>);

#[async_trait]
impl ProjectMoveGate for Swap {
    async fn check(&self, ctx: &Ctx, family: &[Change], to: &str) -> goap_graph::Result<()> {
        let g = self.0.read().clone();
        match g {
            Some(g) => g.check(ctx, family, to).await,
            None => Ok(()),
        }
    }
}

struct MoveEnv {
    g: Arc<Graph>,
    gate: Arc<Swap>,
}

fn directory(g: &Arc<Graph>) -> Arc<access::Directory> {
    Arc::new(access::Directory::new(g.clone()).with_ttl(Duration::from_nanos(1)))
}

async fn must_node(g: &Graph, key: &str) -> Node {
    g.node_by_key(&Ctx::background(), access::NAMESPACE_ORGANISATION, key)
        .await
        .unwrap()
}

/// moveWorld seeds the projects X, Y (both applying m1), Z (m2) and W (m1), a team whose developers work on X, Y and
/// Z but not on W.
async fn move_world() -> MoveEnv {
    let ctx = &Ctx::background();
    let gate = Arc::new(Swap::default());
    let mut g = typed_graph();
    g.project_move_gate = Some(gate.clone());
    let g = Arc::new(g);
    g.bootstrap(ctx).await.unwrap();
    let dir = directory(&g);
    let a: Arc<dyn Authorizer> = Arc::new(access::Authorizer::new(dir.clone()).unwrap());
    *gate.0.write() = Some(project_move_gate(Some(a), Some(dir)));
    Box::pin(seed_unit(ctx, &g, "team-a", "Team A", "team", ""))
        .await
        .unwrap();
    let root = must_node(&g, access::DEFAULT_PROJECT).await.r#ref();
    let team = must_node(&g, "team-a").await.r#ref();
    let mut edits = Vec::new();
    for (key, ms) in [
        ("PROJ-X", "m1"),
        ("PROJ-Y", "m1"),
        ("PROJ-Z", "m2"),
        ("PROJ-W", "m1"),
    ] {
        edits.push(NodeEdit {
            key: key.into(),
            r#type: access::NODE_TYPE_PROJECT_UNIT.into(),
            props: access::ProjectUnit {
                name: key.into(),
                methodologies: vec![ms.into()],
                ..Default::default()
            }
            .props(),
            rationale: "t".into(),
            links: vec![LinkEdit {
                r#type: access::LINK_PROJECT_PART_OF.into(),
                to: Some(root.clone()),
                ..Default::default()
            }],
            ..Default::default()
        });
    }
    for p in ["PROJ-X", "PROJ-Y", "PROJ-Z"] {
        edits.push(NodeEdit {
            key: format!("ASG:team-a/{p}"),
            r#type: access::NODE_TYPE_ASSIGNMENT.into(),
            props: access::Assignment {
                roles: vec!["developer".into()],
                ..Default::default()
            }
            .props(),
            rationale: "t".into(),
            links: vec![
                LinkEdit {
                    r#type: access::LINK_ASSIGNS_ORG.into(),
                    to: Some(team.clone()),
                    ..Default::default()
                },
                LinkEdit {
                    r#type: access::LINK_ASSIGNS_PROJECT.into(),
                    to_key: p.into(),
                    ..Default::default()
                },
            ],
            ..Default::default()
        });
    }
    let head = g
        .branch_head(ctx, access::NAMESPACE_ORGANISATION, MAIN_BRANCH)
        .await
        .unwrap();
    Box::pin(g.commit(
        ctx,
        Commit {
            project_id: access::DEFAULT_PROJECT.into(),
            namespace: access::NAMESPACE_ORGANISATION.into(),
            title: "projects".into(),
            baseline: head.id,
            by: "test".into(),
            baseline_name: "projects".into(),
            edits,
            ..Default::default()
        },
    ))
    .await
    .unwrap();
    MoveEnv { g, gate }
}

impl MoveEnv {
    async fn change(&self, project: &str, methodology: &str) -> Change {
        let ctx = with_principal(&Ctx::background(), SYSTEM_PRINCIPAL.clone());
        self.g
            .create_change(
                &ctx,
                NewChange {
                    title: "c".into(),
                    methodology: methodology.into(),
                    project_id: project.into(),
                    namespace: "platform".into(),
                    own_branch: true,
                    ..Default::default()
                },
            )
            .await
            .unwrap()
    }
}

fn dev() -> Principal {
    Principal {
        subject: "dev".into(),
        org: "team-a".into(),
        ..Default::default()
    }
}

/// A change moves between projects the caller works on and whose methodologies both include the change's (ADR 0091).
#[tokio::test]
async fn project_move_gate_checks_access_and_methodologies() {
    let e = Box::pin(move_world()).await;
    let dev = &with_principal(&Ctx::background(), dev());
    let admin = &with_principal(
        &Ctx::background(),
        Principal {
            subject: "root".into(),
            roles: vec![access::ROLE_ADMIN.into()],
            ..Default::default()
        },
    );
    let g = &e.g;

    let c = e.change("PROJ-X", "m1").await;
    let sub = g
        .create_change(
            dev,
            NewChange {
                title: "s".into(),
                parent_id: c.id.clone(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let got = Box::pin(g.move_change(dev, &c.id, "PROJ-Y")).await;
    assert!(
        matches!(&got, Ok(c) if c.project_id == "PROJ-Y"),
        "X to Y: {got:?}"
    );
    let got = g.change(dev, &sub.id).await.unwrap();
    assert_eq!(got.project_id, "PROJ-Y", "sub-change");

    // the methodology is not listed by the target
    let err = Box::pin(g.move_change(dev, &c.id, "PROJ-Z")).await;
    assert!(
        matches!(&err, Err(e) if e.is_invalid() && e.to_string().contains("m1") && e.to_string().contains("PROJ-Z")),
        "Y to Z: {err:?}"
    );
    // ... or by the source
    let z = e.change("PROJ-Z", "m1").await;
    let err = Box::pin(g.move_change(dev, &z.id, "PROJ-X")).await;
    assert!(
        matches!(&err, Err(e) if e.is_invalid() && e.to_string().contains("PROJ-Z")),
        "Z to X: {err:?}"
    );
    // no access to the target
    let err = Box::pin(g.move_change(dev, &c.id, "PROJ-W")).await;
    assert!(
        err.as_ref().is_err_and(goap_rpcerr::is_forbidden),
        "Y to W without access: {err:?}"
    );
    // ... which an administrator has
    let got = Box::pin(g.move_change(admin, &c.id, "PROJ-W")).await;
    assert!(
        matches!(&got, Ok(c) if c.project_id == "PROJ-W"),
        "admin Y to W: {got:?}"
    );
    // a change with no methodology moves wherever the caller works
    let free = e.change("PROJ-X", "").await;
    let got = Box::pin(g.move_change(dev, &free.id, "PROJ-Z")).await;
    assert!(
        matches!(&got, Ok(c) if c.project_id == "PROJ-Z"),
        "no methodology: {got:?}"
    );
    // a sub-change of another methodology holds the family back
    let p = e.change("PROJ-X", "m1").await;
    g.create_change(
        dev,
        NewChange {
            title: "s".into(),
            parent_id: p.id.clone(),
            methodology: "m2".into(),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let err = Box::pin(g.move_change(dev, &p.id, "PROJ-Y")).await;
    assert!(
        matches!(&err, Err(e) if e.is_invalid()),
        "a sub-change of m2: {err:?}"
    );
    let got = g.change(dev, &p.id).await.unwrap();
    assert_eq!(got.project_id, "PROJ-X", "a refused family stays");
}

/// An authorizer of a function (Go's authorizerFunc).
struct AuthorizerFn<F>(F);

#[async_trait]
impl<F> Authorizer for AuthorizerFn<F>
where
    F: Fn(&goap_authz::Request) -> bool + Send + Sync,
{
    async fn authorize(&self, _: &Ctx, r: &goap_authz::Request) -> Result<bool, goap_authz::Error> {
        Ok((self.0)(r))
    }
}

/// change:move is asked on the project the change leaves and the one it reaches.
#[tokio::test]
async fn project_move_gate_asks_change_move_on_both_projects() {
    let e = Box::pin(move_world()).await;
    let asked: Arc<Mutex<Vec<String>>> = Arc::default();
    let seen = asked.clone();
    let deny = AuthorizerFn(move |r: &goap_authz::Request| {
        if r.resource.r#type == "change" && r.action == "move" {
            seen.lock().push(r.resource.project_id.clone());
            return r.resource.project_id != "PROJ-Y";
        }
        true
    });
    *e.gate.0.write() = Some(project_move_gate(
        Some(Arc::new(deny)),
        Some(directory(&e.g)),
    ));
    let dev = &with_principal(&Ctx::background(), dev());
    let c = e.change("PROJ-X", "m1").await;
    let err = Box::pin(e.g.move_change(dev, &c.id, "PROJ-Y")).await;
    assert!(
        err.as_ref().is_err_and(goap_rpcerr::is_forbidden),
        "change:move denied on the target: {err:?}"
    );
    assert_eq!(
        *asked.lock(),
        vec!["PROJ-X".to_string(), "PROJ-Y".to_string()],
        "asked on"
    );
}

/// Over RPC (ADR 0091): a change is created in the project the request names, else the caller's active project, else
/// the root project; MoveChange moves it, the caller's rights and the methodology rule applying as in the graph.
#[tokio::test]
async fn move_change_rpc() {
    let ctx = &Ctx::background();
    let e = Box::pin(move_world()).await;
    let h = Handler::new(e.g.clone());
    let interceptors = Interceptors::new(vec![Arc::new(h.personal_scope())]);
    let cl = rpc(&serve(h, interceptors).await);
    let dev_x = Principal {
        project: "PROJ-X".into(),
        ..dev()
    };
    let create = async |p: &Principal, project: &str| {
        let r = by(
            pb::CreateChangeRequest {
                title: "c".into(),
                namespace: "platform".into(),
                own_branch: true,
                methodology: "m1".into(),
                project_id: project.into(),
                ..Default::default()
            },
            p,
        );
        cl.create_change(ctx, r).await.unwrap().msg.change.unwrap()
    };
    let c = create(&dev_x, "").await;
    assert_eq!(c.project_id, "PROJ-X", "the active project of the caller");
    let c = create(&dev(), "").await;
    assert_eq!(
        c.project_id,
        access::DEFAULT_PROJECT,
        "an empty claim is the root project"
    );
    let c = create(&dev_x, "PROJ-Y").await;
    assert_eq!(c.project_id, "PROJ-Y", "the project of the request");
    let mv = |project: &str, id: &str| {
        by(
            pb::MoveChangeRequest {
                change_id: id.into(),
                project_id: project.into(),
            },
            &dev_x,
        )
    };
    let moved = cl.move_change(ctx, mv("PROJ-X", &c.id)).await;
    assert!(
        matches!(&moved, Ok(m) if m.msg.change.as_ref().is_some_and(|c| c.project_id == "PROJ-X")),
        "move: {moved:?}"
    );
    let r = cl.move_change(ctx, mv("PROJ-Z", &c.id)).await;
    assert_eq!(
        code(&r),
        Some(Code::InvalidArgument),
        "methodology not applicable: {r:?}"
    );
    let r = cl.move_change(ctx, mv("PROJ-W", &c.id)).await;
    assert_eq!(code(&r), Some(Code::PermissionDenied), "no access: {r:?}");
    // a personal change of someone else is not found
    let personal = by(
        pb::CreateChangeRequest {
            title: "p".into(),
            namespace: "platform".into(),
            own_branch: true,
            owner_org: OWNER_ME.into(),
            project_id: "PROJ-X".into(),
            ..Default::default()
        },
        &common::subject("alice"),
    );
    let pc = cl
        .create_change(ctx, personal)
        .await
        .unwrap()
        .msg
        .change
        .unwrap();
    let r = cl.move_change(ctx, mv("PROJ-Y", &pc.id)).await;
    assert_eq!(
        code(&r),
        Some(Code::NotFound),
        "a personal change of another: {r:?}"
    );
}
