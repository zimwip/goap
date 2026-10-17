mod common;

use std::sync::Arc;

use common::{as_, casbin, code, graph};
use goap_connect::{Code, Request};
use goap_ctx::Ctx;
use goap_domain::DATA_CRITICALITY;
use goap_gen::graph::v1::{self as pb, GraphService};
use goap_graph::NewChange;
use goap_graphsvc::Handler;
use goap_pbconv as pbconv;

/// The criticality of a change header: anyone may raise it, lowering it asks change:lower-criticality
/// (administrators), a value that is no level is refused (ADR 0075 §3).
#[tokio::test]
async fn change_criticality_header() {
    let ctx = &Ctx::background();
    let g = Arc::new(graph());
    let h = Handler {
        authz: Some(casbin()),
        ..Handler::new(g.clone())
    };
    let c = g
        .create_change(
            ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "t".into(),
                methodology: "m".into(),
                data: obj! {DATA_CRITICALITY => "C2"},
                ..Default::default()
            },
        )
        .await
        .unwrap();
    for (name, roles, level, want) in [
        ("raise", "developer", "C3", None),
        ("same level", "developer", "C3", None),
        (
            "lowering needs the permission",
            "developer",
            "C2",
            Some(Code::PermissionDenied),
        ),
        ("lowering by an administrator", "admin", "C1", None),
        (
            "not a level",
            "admin",
            "urgent",
            Some(Code::InvalidArgument),
        ),
    ] {
        let req = as_(
            pb::UpdateChangeRequest {
                id: c.id.to_string(),
                data: pbconv::to_struct(&obj! {DATA_CRITICALITY => level}),
                ..Default::default()
            },
            "u",
            roles,
        );
        let r = h.update_change(ctx, req).await;
        assert_eq!(code(&r), want, "{name}: {r:?}");
    }
    let got = g.change(ctx, &c.id).await.unwrap();
    assert_eq!(
        got.data.get(DATA_CRITICALITY),
        Some(&serde_json::json!("C1")),
        "criticality {:?}",
        got.data
    );
    // a change created with a value that is no level is refused too
    let req = Request::new(pb::CreateChangeRequest {
        title: "x".into(),
        methodology: "m".into(),
        data: pbconv::to_struct(&obj! {DATA_CRITICALITY => "C7"}),
        ..Default::default()
    });
    let r = h.create_change(ctx, req).await;
    assert_eq!(code(&r), Some(Code::InvalidArgument), "create: {r:?}");
}
