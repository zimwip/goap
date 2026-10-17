//! The unit checks of the hub's Connect handler.

mod common;

use std::sync::Arc;

use common::*;
use goap_access as access;
use goap_authz::Principal;
use goap_connect::{Code, ConnectError, Request};
use goap_gen::mcp::v1::{self as mcpv1, McpService};
use goap_mcpsvc::{CONNECTOR_TOKEN_HEADER, Handler};
use serde_json::json;

fn principal(subject: &str, org: &str, roles: &[&str]) -> Principal {
    Principal {
        subject: subject.into(),
        org: org.into(),
        roles: roles.iter().map(|r| r.to_string()).collect(),
        ..Default::default()
    }
}

fn code_of<T>(r: &Result<T, ConnectError>) -> Option<Code> {
    r.as_ref().err().map(|e| e.code())
}

// A caller reaches the adapters of its own unit and of the units below it, never those of another unit: the unit of
// a request is checked against the caller's. Administrators and platform services name any unit.
#[tokio::test]
async fn call_tool_stays_within_the_callers_unit() {
    let inv = FakeInvoker::answering(json!({"text": "hi"}));
    let svc = new_hub(&world().await, inv.clone(), &[]).await;
    let mut h = Handler::new(Arc::new(svc));
    h.connector_token = "t".into();
    let call = async |who: &Principal, unit: &str| {
        let mut req = Request::new(mcpv1::CallToolRequest {
            unit: unit.into(),
            name: "document-repository/read".into(),
            arguments: goap_pbconv::to_struct(&obj(json!({"path": "n.txt"}))),
            ..Default::default()
        });
        goap_identity::set_headers(who, &mut req.headers);
        let r = h.call_tool(&ctx(), req).await.map(|_| ());
        let root = inv
            .last
            .lock()
            .as_ref()
            .and_then(|l| {
                goap_pbconv::to_map(l.config.as_ref())
                    .get("root")
                    .and_then(|v| v.as_str())
                    .map(str::to_string)
            })
            .unwrap_or_default();
        (root, r)
    };
    let a = principal("alice", "ORG-A", &[]);
    let (root, r) = call(&a, "ORG-A1").await;
    assert!(r.is_ok() && root == "/a", "a sub-unit: {root:?} {r:?}");
    let (root, r) = call(&a, "").await; // no unit: the caller's own
    assert!(r.is_ok() && root == "/a", "no unit: {root:?} {r:?}");
    *inv.last.lock() = None;
    for unit in ["ORG-B", access::DEFAULT_ORG] {
        // a sibling tree, and the unit above
        let (_, r) = call(&a, unit).await;
        assert_eq!(
            code_of(&r),
            Some(Code::PermissionDenied),
            "unit {unit}: {r:?}"
        );
    }
    assert!(
        inv.last.lock().is_none(),
        "the connector was called for a unit the caller is not in"
    );
    let (_, r) = call(&principal("root", "ORG-A", &["admin"]), "ORG-B").await;
    assert!(r.is_ok(), "administrator: {r:?}");
    let (_, r) = call(&principal("system:trigger:x", "system", &[]), "ORG-B").await;
    assert!(r.is_ok(), "platform service: {r:?}");
    // the effective adapters follow the same rule
    let mut req = Request::new(mcpv1::ListEffectiveRequest {
        unit: "ORG-B".into(),
    });
    goap_identity::set_headers(&a, &mut req.headers);
    let r = h.list_effective(&ctx(), req).await;
    assert_eq!(
        code_of(&r),
        Some(Code::PermissionDenied),
        "ListEffective of another unit: {:?}",
        r.err()
    );
}

// With no connector token configured no connector can register; with one, only a request carrying it can.
#[tokio::test]
async fn connector_registration_needs_a_token() {
    let register = async |h: &Handler, token: &str| {
        let mut req = Request::new(mcpv1::RegisterConnectorRequest {
            info: Some(localfs_info()),
            endpoint: "http://localfs:8080".into(),
        });
        if !token.is_empty() {
            req.headers.insert(
                http::HeaderName::try_from(CONNECTOR_TOKEN_HEADER).unwrap(),
                http::HeaderValue::from_str(token).unwrap(),
            );
        }
        h.register_connector(&ctx(), req).await.map(|_| ())
    };
    let svc = Arc::new(new_hub(&world().await, Arc::new(FakeInvoker::default()), &[]).await);
    let r = register(&Handler::new(svc.clone()), "").await;
    assert_eq!(
        code_of(&r),
        Some(Code::PermissionDenied),
        "no token configured: {r:?}"
    );
    let mut h = Handler::new(svc);
    h.connector_token = "s3cret".into();
    let r = register(&h, "wrong").await;
    assert_eq!(
        code_of(&r),
        Some(Code::PermissionDenied),
        "wrong token: {r:?}"
    );
    let r = register(&h, "s3cret").await;
    assert!(r.is_ok(), "right token: {r:?}");
}
