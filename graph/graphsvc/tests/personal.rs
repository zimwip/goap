mod common;

use std::sync::Arc;

use common::{by, code, rpc, serve, subject, typed_graph};
use goap_access as access;
use goap_connect::{Code, Interceptors};
use goap_ctx::Ctx;
use goap_domain::{
    IMPACT_CREATED, IMPACT_LANDED, IMPACT_REVIEWED, IMPACT_TRANSITIONED, MAIN_BRANCH,
};
use goap_gen::graph::v1::{self as pb, GraphService};
use goap_graph::{Commit, Graph, LinkEdit, NodeEdit};
use goap_graphsvc::{Handler, OWNER_ME, ensure_user};

/// The changes that are not the creation of a User (ADR 0042: each caller seen first gets one, public).
fn not_users(cs: &[pb::Change]) -> usize {
    cs.iter().filter(|c| !c.title.starts_with("User ")).count()
}

/// A personal change (ADR 0037) belongs to its owner: another caller neither sees nor touches it, and only its owner
/// removes it, as long as it landed nothing.
#[tokio::test]
async fn personal_change() {
    let ctx = &Ctx::background();
    let g = Arc::new(typed_graph());
    g.bootstrap(ctx).await.unwrap();
    let h = Handler::new(g.clone());
    let interceptors = Interceptors::new(vec![Arc::new(h.personal_scope())]);
    let cl = rpc(&serve(h, interceptors).await);

    let base = g.branch_head(ctx, "platform", MAIN_BRANCH).await.unwrap();
    let list = async |s: &str| {
        cl.list_changes(ctx, by(pb::ListChangesRequest::default(), &subject(s)))
            .await
    };
    let before = list("bob").await.unwrap();
    // the changes the bootstrap made, visible to everyone (not personal)
    let seeded = not_users(&before.msg.changes);

    let created = cl
        .create_change(
            ctx,
            by(
                pb::CreateChangeRequest {
                    title: "prefs".into(),
                    namespace: "platform".into(),
                    baseline_id: base.id.to_string(),
                    own_branch: true,
                    owner_org: OWNER_ME.into(),
                    ..Default::default()
                },
                &subject("alice"),
            ),
        )
        .await
        .unwrap()
        .msg
        .change
        .unwrap();
    let id = created.id.clone();
    assert_eq!(created.owner_org, "USR:alice", "owner");

    // bob: not found, absent from the lists, cannot remove it, cannot claim alice's unit
    let get = |s: &str| by(pb::GetChangeRequest { id: id.clone() }, &subject(s));
    let del = |s: &str| {
        by(
            pb::DeleteChangeRequest {
                change_id: id.clone(),
            },
            &subject(s),
        )
    };
    let r = cl.get_change(ctx, get("bob")).await;
    assert_eq!(code(&r), Some(Code::NotFound), "bob GetChange: {r:?}");
    let r = cl.delete_change(ctx, del("bob")).await;
    assert_eq!(code(&r), Some(Code::NotFound), "bob DeleteChange: {r:?}");
    let l = list("bob").await;
    assert!(
        matches!(&l, Ok(l) if not_users(&l.msg.changes) == seeded),
        "bob ListChanges = {l:?}"
    );
    let claim = by(
        pb::CreateChangeRequest {
            title: "x".into(),
            namespace: "platform".into(),
            baseline_id: base.id.to_string(),
            owner_org: "USR:alice".into(),
            ..Default::default()
        },
        &subject("bob"),
    );
    let r = cl.create_change(ctx, claim).await;
    assert_eq!(
        code(&r),
        Some(Code::PermissionDenied),
        "bob claims alice's unit: {r:?}"
    );

    // alice sees it, and no sub-change is split off it
    let l = list("alice").await;
    assert!(
        matches!(&l, Ok(l) if not_users(&l.msg.changes) == seeded + 1),
        "alice ListChanges = {l:?}"
    );
    let sub = by(
        pb::CreateChangeRequest {
            title: "sub".into(),
            parent_id: id.clone(),
            owner_org: "@me".into(),
            ..Default::default()
        },
        &subject("alice"),
    );
    let r = cl.create_change(ctx, sub).await;
    assert_eq!(
        code(&r),
        Some(Code::FailedPrecondition),
        "sub-change of a personal change: {r:?}"
    );

    // discarding removes it for good
    cl.delete_change(ctx, del("alice"))
        .await
        .expect("alice DeleteChange");
    let r = cl.get_change(ctx, get("alice")).await;
    assert_eq!(
        code(&r),
        Some(Code::NotFound),
        "GetChange after the purge: {r:?}"
    );
}

/// A User node is created automatically the first time a subject is seen (ADR 0039), not only when they resolve
/// "@me": any authenticated call ensures it, via the EnsureCaller interceptor. It doubles as the subject's personal
/// unit (organisation@User extends organisation@OrgUnit, same "USR:<subject>" key).
#[tokio::test]
async fn user_created_automatically() {
    let ctx = &Ctx::background();
    let g = Arc::new(typed_graph());
    g.bootstrap(ctx).await.unwrap();
    let h = Handler::new(g.clone());
    let interceptors = Interceptors::new(vec![
        Arc::new(h.personal_scope()),
        Arc::new(h.ensure_caller()),
    ]);
    let cl = rpc(&serve(h, interceptors).await);

    // bob only lists changes (no @me, no personal change involved): he still gets a User node.
    cl.list_changes(ctx, by(pb::ListChangesRequest::default(), &subject("bob")))
        .await
        .unwrap();
    let n = g
        .node_by_key(ctx, access::NAMESPACE_ORGANISATION, "USR:bob")
        .await
        .expect("bob's User node");
    assert_eq!(n.r#type, access::NODE_TYPE_USER);
    assert_eq!(
        n.properties.get("subject"),
        Some(&serde_json::json!("bob")),
        "properties = {:?}",
        n.properties
    );
    // calling again must not create a second version (deduplicated per subject)
    cl.list_changes(ctx, by(pb::ListChangesRequest::default(), &subject("bob")))
        .await
        .unwrap();
    let again = g
        .node_by_key(ctx, access::NAMESPACE_ORGANISATION, "USR:bob")
        .await;
    assert!(
        matches!(&again, Ok(a) if a.version == n.version),
        "a second call must not write again: {again:?}"
    );
}

async fn member_of(g: &Graph, n: &goap_domain::Node) -> Option<String> {
    let ctx = &Ctx::background();
    for l in g.out_links_of(ctx, &n.r#ref()).await.unwrap() {
        if l.r#type == access::LINK_MEMBER_OF {
            return Some(g.node(ctx, &l.to).await.unwrap().key);
        }
    }
    None
}

/// EnsureUser must not create a broken User (no member_of, no shot at the first-admin bootstrap) when the default
/// organisation does not exist yet — the bootstrap can still be running at startup (ADR 0040). Failing leaves nothing
/// behind, so a later, correctly-timed call for the same subject still succeeds.
#[tokio::test]
async fn ensure_user_waits_for_default_org() {
    let ctx = &Ctx::background();
    let g = typed_graph();

    let err = Box::pin(ensure_user(ctx, &g, "alice")).await;
    assert!(
        matches!(&err, Err(e) if e.is_not_found()),
        "EnsureUser before the bootstrap = {err:?}, want not found"
    );
    let r = g
        .node_by_key(ctx, access::NAMESPACE_ORGANISATION, "USR:alice")
        .await;
    assert!(
        matches!(&r, Err(e) if e.is_not_found()),
        "a failed EnsureUser must leave no node: {r:?}"
    );

    g.bootstrap(ctx).await.unwrap();
    Box::pin(ensure_user(ctx, &g, "alice"))
        .await
        .expect("EnsureUser after the bootstrap");
    let n = g
        .node_by_key(ctx, access::NAMESPACE_ORGANISATION, "USR:alice")
        .await
        .expect("alice's User node");
    // the first user is granted admin through a platform Assignment (ADR 0046, 0047)
    let asg_node = g
        .node_by_key(
            ctx,
            access::NAMESPACE_ORGANISATION,
            &access::platform_assignment_key("USR:alice"),
        )
        .await
        .expect("alice's platform assignment");
    let asg = access::assignment_from_props(&asg_node.properties).unwrap();
    assert!(
        asg.roles.iter().any(|r| r == access::ROLE_ADMIN),
        "first user must be granted admin: {asg:?}"
    );
    assert!(
        member_of(&g, &n).await.is_some(),
        "alice must be member_of the default org"
    );

    // a second subject is not the first user: no admin role
    Box::pin(ensure_user(ctx, &g, "bob")).await.unwrap();
    let r = g
        .node_by_key(
            ctx,
            access::NAMESPACE_ORGANISATION,
            &access::platform_assignment_key("USR:bob"),
        )
        .await;
    assert!(
        matches!(&r, Err(e) if e.is_not_found()),
        "a second user must get no platform assignment: {r:?}"
    );
}

/// A new user joins the waiting unit an administrator flagged (ADR 0042), ORG-DEFAULT when none is; with several
/// flagged, the smallest key wins; clearing the flag sends newcomers back to ORG-DEFAULT.
#[tokio::test]
async fn ensure_user_joins_waiting_unit() {
    let ctx = &Ctx::background();
    let g = typed_graph();
    g.bootstrap(ctx).await.unwrap();
    let commit = async |edits: Vec<NodeEdit>| {
        let head = g
            .branch_head(ctx, access::NAMESPACE_ORGANISATION, MAIN_BRANCH)
            .await
            .unwrap();
        Box::pin(g.commit(
            ctx,
            Commit {
                project_id: "PROJ-ROOT".into(),
                namespace: access::NAMESPACE_ORGANISATION.into(),
                title: "t".into(),
                intent: "t".into(),
                baseline: head.id,
                by: "test".into(),
                edits,
                ..Default::default()
            },
        ))
        .await
        .unwrap();
    };
    let unit_of = async |subject: &str| {
        Box::pin(ensure_user(ctx, &g, subject)).await.unwrap();
        let n = g
            .node_by_key(
                ctx,
                access::NAMESPACE_ORGANISATION,
                &access::user_key(subject),
            )
            .await
            .unwrap();
        member_of(&g, &n)
            .await
            .unwrap_or_else(|| panic!("{subject} has no member_of"))
    };

    let root = g
        .node_by_key(ctx, access::NAMESPACE_ORGANISATION, access::DEFAULT_ORG)
        .await
        .unwrap();
    assert!(
        !access::is_waiting_unit(&root.properties),
        "no unit is a waiting unit until an administrator says so: {:?}",
        root.properties
    );
    assert_eq!(unit_of("alice").await, access::DEFAULT_ORG, "alice joined");

    // the administrator creates a waiting unit
    let waiting = |key: &str| NodeEdit {
        key: key.into(),
        r#type: access::NODE_TYPE_ORG_UNIT.into(),
        props: obj! {"name" => "Waiting", access::PROP_WAITING_UNIT => true},
        links: vec![LinkEdit {
            r#type: access::LINK_PART_OF.into(),
            to: Some(root.r#ref()),
            ..Default::default()
        }],
        ..Default::default()
    };
    commit(vec![waiting("WAIT-B")]).await;
    assert_eq!(unit_of("bob").await, "WAIT-B", "bob joined");
    // a second one flagged: the smallest key wins
    commit(vec![waiting("WAIT-A")]).await;
    assert_eq!(
        unit_of("carol").await,
        "WAIT-A",
        "carol joined (smallest key of the flagged units)"
    );
    // flags cleared: back to ORG-DEFAULT
    for k in ["WAIT-A", "WAIT-B"] {
        let n = g
            .node_by_key(ctx, access::NAMESPACE_ORGANISATION, k)
            .await
            .unwrap();
        commit(vec![NodeEdit {
            pre: Some(n.r#ref()),
            props: obj! {access::PROP_WAITING_UNIT => null},
            ..Default::default()
        }])
        .await;
    }
    assert_eq!(unit_of("dave").await, access::DEFAULT_ORG, "dave joined");
}

/// A new user is created and activated in one change: the activation is a transition of its draft (ADR 0079), so the
/// User has one version, active, written at landing, and its log reads created, transitioned, reviewed, landed.
#[tokio::test]
async fn ensure_user_is_one_version() {
    let ctx = &Ctx::background();
    let g = typed_graph();
    g.bootstrap(ctx).await.unwrap();
    Box::pin(ensure_user(ctx, &g, "dora")).await.unwrap();
    let n = g
        .node_by_key(
            ctx,
            access::NAMESPACE_ORGANISATION,
            &access::user_key("dora"),
        )
        .await
        .unwrap();
    let vs = g.versions(ctx, &n.id).await;
    assert!(
        matches!(&vs, Ok(vs) if vs.len() == 1 && vs[0].version == 1 && vs[0].state == "active"),
        "one version, active: {vs:?}"
    );
    let evs = g.change_events(ctx, &n.change_id).await.unwrap();
    let ops: Vec<_> = evs
        .iter()
        .filter(|e| e.impact == n.change_impact)
        .map(|e| e.op.clone())
        .collect();
    assert!(
        ops.len() >= 4
            && ops[0] == IMPACT_CREATED
            && ops[1] == IMPACT_TRANSITIONED
            && ops[2] == IMPACT_REVIEWED
            && ops[3] == IMPACT_LANDED,
        "log of the user: {ops:?}"
    );
}
