//! Port of `identify_test.go`.

mod common;

use std::sync::Arc;

use common::{by, code, rpc, serve, subject, typed_graph};
use goap_access as access;
use goap_connect::{Code, Interceptors, Request};
use goap_ctx::Ctx;
use goap_gen::graph::v1::{self as pb, GraphService};
use goap_graphsvc::Handler;

/// Every RPC is identified: an anonymous request is refused before it reaches the graph, mutating or not, and the
/// handler receives the principal of the request (Handler::identify).
#[tokio::test]
async fn anonymous_calls_are_refused() {
    let g = Arc::new(typed_graph());
    let h = Handler::new(g);
    let interceptors = Interceptors::new(vec![Arc::new(h.identify())]);
    let cl = rpc(&serve(h, interceptors).await);
    let ctx = &Ctx::background();

    let r = cl
        .create_change(
            ctx,
            Request::new(pb::CreateChangeRequest {
                title: "t".into(),
                intent: "i".into(),
                namespace: "organisation".into(),
                ..Default::default()
            }),
        )
        .await;
    assert_eq!(code(&r), Some(Code::Unauthenticated), "CreateChange: {r:?}");
    let r = cl
        .impact_node_create(
            ctx,
            Request::new(pb::ImpactNodeCreateRequest {
                change_id: "C1".into(),
                key: "X".into(),
                r#type: "organisation@OrgUnit".into(),
                ..Default::default()
            }),
        )
        .await;
    assert_eq!(
        code(&r),
        Some(Code::Unauthenticated),
        "ImpactNodeCreate: {r:?}"
    );
    let r = cl
        .update_change(
            ctx,
            Request::new(pb::UpdateChangeRequest {
                id: "C1".into(),
                ..Default::default()
            }),
        )
        .await;
    assert_eq!(code(&r), Some(Code::Unauthenticated), "UpdateChange: {r:?}");
    let r = cl
        .list_changes(ctx, Request::new(pb::ListChangesRequest::default()))
        .await;
    assert_eq!(code(&r), Some(Code::Unauthenticated), "ListChanges: {r:?}");

    // an identified request gets through
    cl.list_changes(
        ctx,
        by(pb::ListChangesRequest::default(), &subject("alice")),
    )
    .await
    .expect("identified ListChanges");
}

/// A platform service (system:registry, ...) calling the graph never becomes a User, so it cannot take the
/// first-administrator grant (ADR 0040): the first person who signs in after it does.
#[tokio::test]
async fn service_subjects_do_not_take_the_first_admin_grant() {
    let ctx = &Ctx::background();
    let g = Arc::new(typed_graph());
    g.bootstrap(ctx).await.unwrap();
    let h = Handler::new(g.clone());
    let interceptors = Interceptors::new(vec![Arc::new(h.identify()), Arc::new(h.ensure_caller())]);
    let cl = rpc(&serve(h, interceptors).await);
    let as_ = async |s: &str| {
        cl.list_changes(ctx, by(pb::ListChangesRequest::default(), &subject(s)))
            .await
            .unwrap();
    };
    as_("system:registry").await;
    as_("system:trigger:nightly").await;
    let r = g
        .node_by_key(
            ctx,
            access::NAMESPACE_ORGANISATION,
            &access::user_key("system:registry"),
        )
        .await;
    assert!(
        matches!(&r, Err(e) if e.is_not_found()),
        "a service must not become a User: {r:?}"
    );
    as_("alice").await;
    let n = g
        .node_by_key(
            ctx,
            access::NAMESPACE_ORGANISATION,
            &access::platform_assignment_key(&access::user_key("alice")),
        )
        .await
        .expect("alice's platform assignment");
    let asg = access::assignment_from_props(&n.properties);
    assert!(
        matches!(&asg, Ok(a) if a.roles.iter().any(|r| r == access::ROLE_ADMIN)),
        "alice must be the first administrator: {asg:?}"
    );
}
