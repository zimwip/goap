//! Port of `demo_test.go`.

mod common;

use std::sync::Arc;

use common::{code, floor, graph, set};
use goap_authz::{Principal, principal_from};
use goap_connect::{Code, Request};
use goap_ctx::Ctx;
use goap_gen::graph::v1::{self as pb, GraphService};
use goap_graphsvc::{Handler, demo_fn};
use parking_lot::Mutex;

/// The demo data is loaded on demand by an administrator; a deployment shipping none answers unimplemented.
#[tokio::test]
async fn load_demo_data_is_for_administrators() {
    let ctx = &Ctx::background();
    let g = Arc::new(graph());
    let by: Arc<Mutex<Principal>> = Arc::default();
    let seen = by.clone();
    let mut h = Handler {
        floor: Some(floor()),
        demo: Some(demo_fn(move |ctx, _| {
            *seen.lock() = principal_from(ctx);
            Box::pin(async { Ok((true, "PROJ-DEMO".to_string())) })
        })),
        ..Handler::new(g)
    };
    let load = async |h: &Handler, roles: &str| {
        let mut req = Request::new(pb::LoadDemoDataRequest::default());
        set(&mut req, "u", roles);
        h.load_demo_data(ctx, req).await
    };
    let r = load(&h, "methodologist").await;
    assert_eq!(
        code(&r),
        Some(Code::PermissionDenied),
        "only administrators load the demo: {r:?}"
    );
    let r = load(&h, "admin").await;
    assert!(
        matches!(&r, Ok(r) if r.msg.loaded && r.msg.project == "PROJ-DEMO"),
        "admin load = {r:?}"
    );
    let p = by.lock().clone();
    assert!(
        p.is_system() && p.end_user() == "u",
        "the seed acts as the graph service for the administrator: {p:?}"
    );
    h.demo = None;
    let r = load(&h, "admin").await;
    assert_eq!(code(&r), Some(Code::Unimplemented), "no demo: {r:?}");
}
