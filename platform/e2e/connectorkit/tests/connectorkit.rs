//! The connector kit against a hub over a real graph (ADR 0125).

use std::path::PathBuf;
use std::sync::Arc;
use std::time::{Duration, Instant};

use goap_access as access;
use goap_authz::Principal;
use goap_connect::{Client, Code, Interceptors, Request};
use goap_connectorkit::Connector as _;
use goap_ctx::Ctx;
use goap_gen::connector::v1::{self as connectorv1, ConnectorService, ConnectorServiceClient};
use goap_gen::mcp::v1::mcp_service_router;
use goap_graph::{Graph, Memory};
use goap_identity::Extractor;
use goap_mcp as mcp;
use goap_mcpsvc::{ConnectInvoker, Directory, Handler, MemoryStore, Service};
use serde_json::{Map, Value, json};

const TOKEN: &str = "s3cret";

/// A temporary directory removed when dropped (Go's `t.TempDir()`).
struct TempDir(PathBuf);

impl TempDir {
    fn new(name: &str) -> TempDir {
        let nanos = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_nanos())
            .unwrap_or_default();
        let p = std::env::temp_dir().join(format!(
            "goap-e2e-connectorkit-{name}-{}-{nanos}",
            std::process::id()
        ));
        std::fs::create_dir_all(&p).unwrap();
        TempDir(p)
    }

    fn path(&self) -> String {
        self.0.to_string_lossy().into_owned()
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn obj(v: Value) -> Map<String, Value> {
    v.as_object().cloned().unwrap_or_default()
}

/// Serves a router on a free local port and returns its base URL.
async fn serve(app: goap_connect::axum::Router) -> String {
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = ln.local_addr().unwrap();
    tokio::spawn(async move { goap_connect::axum::serve(ln, app).await.unwrap() });
    format!("http://{addr}")
}

fn dev() -> Principal {
    Principal {
        subject: "u".into(),
        org: "acme".into(),
        roles: vec!["admin".into()],
        ..Default::default()
    }
}

/// The hub over g, served with the connector token and a default principal; its service and URL.
async fn hub(g: Graph) -> (Arc<Service>, String) {
    let mut svc = Service::new(Arc::new(MemoryStore::new()));
    svc.directory = Some(Arc::new(Directory::new(Arc::new(g))));
    let mut inv = ConnectInvoker::new(TOKEN);
    inv.http = Some(reqwest::Client::new());
    svc.invoker = Some(Arc::new(inv));
    svc.lease = Duration::from_secs(3);
    let svc = Arc::new(svc);
    let mut h = Handler::new(svc.clone());
    h.identity = Extractor {
        default: Some(dev()),
    };
    h.connector_token = TOKEN.into();
    let url = serve(mcp_service_router(Arc::new(h), Interceptors::none())).await;
    (svc, url)
}

// A connector started later registers by itself; a call from an organization goes through binding, adapter and
// connector, over real HTTP.
#[tokio::test]
async fn auto_registration_and_call() {
    let (ctx, cancel) = Ctx::background().with_cancel();
    let dir = TempDir::new("auto");
    let g = Graph::new(Arc::new(Memory::new()));
    g.bootstrap(&ctx).await.unwrap();
    Box::pin(goap_devseed::document_repository(&ctx, &g))
        .await
        .unwrap();
    Box::pin(goap_graphsvc::seed_adapter(
        &ctx,
        &g,
        &goap_devseed::local_fs_adapter(access::DEFAULT_ORG, &dir.path()),
    ))
    .await
    .unwrap();
    let (svc, hub_url) = hub(g).await;
    let conn = goap_connector_localfs::Connector::default();
    let conn_url = serve(goap_connectorkit::handler(
        Arc::new(conn.clone()),
        TOKEN,
        Interceptors::none(),
    ))
    .await;

    // the hub knows nothing about the connector until it registers itself
    let cs = svc.connectors(&ctx).await.unwrap();
    assert!(cs.is_empty(), "connectors before start = {cs:?}");
    {
        let (ctx, hub_url, conn_url, info) =
            (ctx.clone(), hub_url.clone(), conn_url.clone(), conn.info());
        tokio::spawn(async move {
            goap_connectorkit::register(
                &ctx,
                reqwest::Client::new(),
                &hub_url,
                &conn_url,
                TOKEN,
                info,
            )
            .await
        });
    }
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        let cs = svc.connectors(&ctx).await.unwrap_or_default();
        if cs.len() == 1 && cs[0].live {
            break;
        }
        assert!(Instant::now() < deadline, "connector did not register");
        tokio::time::sleep(Duration::from_millis(20)).await;
    }

    let client = goap_mcpsvc::Client::connect(&hub_url);
    let cctx = goap_authz::with_principal(&ctx, dev());
    client
        .call_tool(
            &cctx,
            "acme",
            "document-repository/write",
            &obj(json!({"path": "n.txt", "content": "hi"})),
        )
        .await
        .unwrap();
    let out = client
        .call_tool(
            &cctx,
            "acme",
            "document-repository/read",
            &obj(json!({"path": "n.txt"})),
        )
        .await
        .unwrap();
    assert_eq!(out["text"], "hi", "read = {out:?}");
    assert!(
        client
            .call_tool(
                &cctx,
                "acme",
                "document-repository/read",
                &obj(json!({"path": "../x"}))
            )
            .await
            .is_err(),
        "escape not reported"
    );
    assert!(
        client
            .call_tool(&cctx, "acme", "ticketing/create", &Map::new())
            .await
            .is_err(),
        "a tool of an MCP nobody implements can be called"
    );
    let (tools, mcps) = client.tools(&cctx, "acme").await.unwrap();
    assert!(
        tools.len() == 3 && mcps.len() == 1,
        "tools = {tools:?} {mcps:?}"
    );

    // the connector refuses calls that do not carry the token
    let c = ConnectorServiceClient::new(Client::new(&conn_url));
    let err = c
        .invoke(
            &ctx,
            Request::new(connectorv1::InvokeRequest {
                operation: "list_dir".into(),
                ..Default::default()
            }),
        )
        .await
        .unwrap_err();
    assert_eq!(
        err.code(),
        Code::PermissionDenied,
        "call without token = {err}"
    );
    cancel.cancel();
}

// The change a tool call runs for reaches a remote connector (ADR 0118): the workspace of a change is found from it,
// through the engine's client, the hub and the connector over HTTP.
#[tokio::test]
async fn call_context_reaches_the_connector() {
    let ctx = Ctx::background();
    let root = TempDir::new("ctx");
    let g = Graph::new(Arc::new(Memory::new()));
    g.bootstrap(&ctx).await.unwrap();
    Box::pin(goap_graphsvc::seed_builtins(&ctx, &g))
        .await
        .unwrap();
    Box::pin(goap_graphsvc::seed_adapter(
        &ctx,
        &g,
        &goap_mcpbuiltin::workspace_adapter(access::DEFAULT_ORG, &root.path()),
    ))
    .await
    .unwrap();
    let (svc, hub_url) = hub(g).await;
    let conn = goap_connector_workspace::new(vec![]);
    let conn_url = serve(goap_connectorkit::handler(
        Arc::new(conn.clone()),
        TOKEN,
        Interceptors::none(),
    ))
    .await;
    svc.register_connector(&ctx, Some(conn.info()), &conn_url)
        .await
        .unwrap();

    let client = goap_mcpsvc::Client::connect(&hub_url);
    let cctx = mcp::with_call(
        &goap_authz::with_principal(&ctx, dev()),
        mcp::CallContext {
            change: "CHG-42".into(),
            process: "P-1".into(),
            ..Default::default()
        },
    );
    client
        .call_tool(
            &cctx,
            "acme",
            "workspace/write",
            &obj(json!({"path": "a.txt", "content": "hi"})),
        )
        .await
        .unwrap();
    let b = std::fs::read_to_string(root.0.join("CHG-42").join("a.txt"));
    assert!(
        b.as_deref().is_ok_and(|s| s == "hi"),
        "workspace of CHG-42: {b:?}"
    );
    let err = client
        .call_tool(
            &goap_authz::with_principal(&ctx, dev()),
            "acme",
            "workspace/ls",
            &Map::new(),
        )
        .await
        .unwrap_err();
    assert!(
        err.to_string().contains("runs for none"),
        "a call for no change: {err}"
    );
}
