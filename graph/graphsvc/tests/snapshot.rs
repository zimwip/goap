mod common;

use std::io::Cursor;
use std::sync::Arc;

use common::{as_, casbin, code, domains_catalog, graph, rpc, serve, with_types};
use futures::StreamExt;
use goap_connect::{Code, ConnectError, Interceptors, Request};
use goap_ctx::Ctx;
use goap_domain::CHANGE_ACTIVE;
use goap_gen::graph::v1::{self as pb, GraphService};
use goap_graph::{ChangesFilter, SNAPSHOT_FORMAT, Snapshot, SnapshotNode};
use goap_graphsvc::Handler;

/// A graph snapshot is imported as one change of the caller (ADR 0104), each type it writes authorized like a
/// creation; a refused import leaves no change behind. The export of an empty namespace is an empty snapshot.
#[tokio::test]
async fn import_graph_creates_a_change() {
    let mut g = graph();
    with_types(&mut g, domains_catalog());
    let g = Arc::new(g);
    let h = Handler {
        authz: Some(casbin()),
        ..Handler::new(g.clone())
    };
    let ctx = &Ctx::background();

    let exp = h
        .export_graph(
            ctx,
            Request::new(pb::ExportGraphRequest {
                namespace: "alm".into(),
                ..Default::default()
            }),
        )
        .await;
    assert!(
        matches!(&exp, Ok(e) if e.msg.nodes == 0 && e.msg.media_type == "application/json"),
        "export of an empty namespace: {exp:?}"
    );
    let s = Snapshot {
        format: SNAPSHOT_FORMAT.into(),
        namespace: "alm".into(),
        branch: String::new(),
        baseline: String::new(),
        exported_at: Default::default(),
        nodes: vec![SnapshotNode {
            key: "NEED-1".into(),
            r#type: "alm@Need".into(),
            properties: obj! {"title" => "Pay online"},
            ..Default::default()
        }],
    };
    let doc = serde_json::to_string(&s).unwrap();
    let call = async |roles: &str| {
        let req = as_(
            pb::ImportGraphRequest {
                document: doc.clone(),
                project_id: "PROJ-ROOT".into(),
                ..Default::default()
            },
            "u",
            roles,
        );
        Box::pin(h.import_graph(ctx, req)).await
    };
    let r = call("").await;
    assert_eq!(
        code(&r),
        Some(Code::PermissionDenied),
        "a caller who may not create the nodes cannot import them: {r:?}"
    );
    let cs = g
        .list_changes(
            ctx,
            &ChangesFilter {
                namespace: "alm".into(),
                ..Default::default()
            },
        )
        .await;
    assert!(
        matches!(&cs, Ok(cs) if cs.is_empty()),
        "a refused import leaves no change: {cs:?}"
    );
    let out = call("admin").await.unwrap();
    assert!(
        out.msg.created == 1
            && out
                .msg
                .change
                .as_ref()
                .is_some_and(|c| CHANGE_ACTIVE == c.status),
        "import as a change: {:?}",
        out.msg
    );
    let r = h
        .import_graph(
            ctx,
            Request::new(pb::ImportGraphRequest {
                document: "{".into(),
                ..Default::default()
            }),
        )
        .await;
    assert_eq!(
        code(&r),
        Some(Code::InvalidArgument),
        "a malformed snapshot: {r:?}"
    );
}

/// The OKF export streams a zip archive (ADR 0111): a first message naming it, then its bytes; an anonymous caller is
/// refused, a streaming call passing none of the unary interceptors.
#[tokio::test]
async fn export_graph_okf_streams_a_zip() {
    let g = Arc::new(graph());
    let client = rpc(&serve(Handler::new(g), Interceptors::none()).await);
    let ctx = &Ctx::background();

    let read = async |subject: &str| -> Result<(Option<pb::ExportGraphOkfResponse>, Vec<u8>), ConnectError> {
        let mut req = Request::new(pb::ExportGraphOkfRequest {
            namespace: "alm".into(),
            ..Default::default()
        });
        if !subject.is_empty() {
            req.headers.insert(
                http::HeaderName::from_bytes(goap_identity::HEADER_SUBJECT.as_bytes()).unwrap(),
                http::HeaderValue::from_str(subject).unwrap(),
            );
        }
        let mut st = client.export_graph_okf(ctx, req).await?.msg;
        let mut head = None;
        let mut data = Vec::new();
        while let Some(m) = st.next().await {
            let m = m?;
            if head.is_none() {
                head = Some(m);
                continue;
            }
            data.extend_from_slice(&m.chunk);
        }
        Ok((head, data))
    };
    let r = read("").await;
    assert_eq!(
        code(&r),
        Some(Code::Unauthenticated),
        "an anonymous caller: {r:?}"
    );
    let (head, data) = read("u").await.unwrap();
    assert!(
        matches!(&head, Some(h) if h.media_type == "application/zip" && h.filename.ends_with(".okf.zip")),
        "head: {head:?}"
    );
    let z = zip::ZipArchive::new(Cursor::new(data));
    let names: Option<Vec<String>> = z.as_ref().ok().map(|z| {
        z.file_names()
            .filter_map(|n| n.ok())
            .map(|n| n.to_string())
            .collect()
    });
    assert_eq!(
        names,
        Some(vec!["alm/index.md".to_string()]),
        "an empty namespace is its index: {:?}",
        z.err()
    );
}
