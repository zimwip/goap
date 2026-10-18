//! Helpers shared by the hub tests (the helpers of service_test.go).
#![allow(dead_code)]

use std::sync::Arc;

use async_trait::async_trait;
use goap_access as access;
use goap_adapter as adapter;
use goap_ctx::Ctx;
use goap_gen::connector::v1::{ConnectorInfo, InvokeRequest, InvokeResponse, Operation};
use goap_graph::{Graph, Memory};
use goap_mcpsvc::{Directory, Error, Invoker, MemoryStore, SecretError, SecretResolver, Service};
use parking_lot::Mutex;
use serde_json::{Map, Value};

pub fn ctx() -> Ctx {
    Ctx::background()
}

pub fn obj(v: Value) -> Map<String, Value> {
    v.as_object().cloned().unwrap_or_default()
}

/// What the fake invoker answers: a response, or a transport failure.
#[derive(Default)]
pub struct Answer {
    pub resp: Option<InvokeResponse>,
    pub err: Option<String>,
}

/// The fake invoker of the Go tests: it records the last request and answers what it is told.
#[derive(Default)]
pub struct FakeInvoker {
    pub last: Mutex<Option<InvokeRequest>>,
    pub answer: Mutex<Answer>,
}

impl FakeInvoker {
    /// An invoker answering a result.
    pub fn answering(result: Value) -> Arc<FakeInvoker> {
        let f = FakeInvoker::default();
        f.answer.lock().resp = Some(InvokeResponse {
            result: goap_pbconv::to_struct(&obj(result)),
            ..Default::default()
        });
        Arc::new(f)
    }

    /// The last request (panics when none).
    pub fn last(&self) -> InvokeRequest {
        self.last.lock().clone().expect("no invocation")
    }

    /// The configuration of the last request.
    pub fn last_config(&self) -> Map<String, Value> {
        goap_pbconv::to_map(self.last().config.as_ref())
    }
}

#[async_trait]
impl Invoker for FakeInvoker {
    async fn invoke(
        &self,
        _ctx: &Ctx,
        _endpoint: &str,
        req: InvokeRequest,
    ) -> Result<InvokeResponse, Error> {
        *self.last.lock() = Some(req);
        let a = self.answer.lock();
        if let Some(e) = &a.err {
            return Err(Error::Message(e.clone()));
        }
        Ok(a.resp.clone().unwrap_or_default())
    }
}

/// The secret resolver of the Go tests: "secret:" + the reference.
pub struct PrefixSecrets;

#[async_trait]
impl SecretResolver for PrefixSecrets {
    async fn resolve(&self, _ctx: &Ctx, reference: &str) -> Result<String, SecretError> {
        Ok(format!("secret:{reference}"))
    }
}

pub fn localfs_info() -> ConnectorInfo {
    ConnectorInfo {
        id: "localfs".into(),
        version: "1".into(),
        secret_names: vec!["token".into()],
        operations: ["list_dir", "read_file", "write_file"]
            .iter()
            .map(|n| Operation {
                name: n.to_string(),
                ..Default::default()
            })
            .collect(),
        ..Default::default()
    }
}

/// world is a graph with the default organisation and the document-repository MCP, and two trees:
///
/// ```text
/// ORG-DEFAULT (implicit root of every chain)   adapter: localfs, root /default
/// ORG-A      part_of ORG-DEFAULT               adapter: localfs, root /a   (overrides)
/// ORG-A1     part_of ORG-A                     (nothing: inherits ORG-A's)
/// ORG-B      no parent                         (nothing: inherits the default organisation's)
/// ```
pub async fn world() -> Arc<Graph> {
    let ctx = ctx();
    let g = Graph::new(Arc::new(Memory::new()));
    g.bootstrap(&ctx).await.unwrap();
    assert!(
        Box::pin(goap_devseed::document_repository(&ctx, &g))
            .await
            .unwrap(),
        "seed document repository"
    );
    assert!(
        !Box::pin(goap_devseed::document_repository(&ctx, &g))
            .await
            .unwrap(),
        "seeding twice"
    );
    for (u, parent) in [
        ("ORG-A", access::DEFAULT_ORG),
        ("ORG-A1", "ORG-A"),
        ("ORG-B", ""),
    ] {
        Box::pin(goap_graphsvc::seed_unit(&ctx, &g, u, u, "team", parent))
            .await
            .unwrap();
    }
    seed_adapter(
        &g,
        &goap_devseed::local_fs_adapter(access::DEFAULT_ORG, "/default"),
    )
    .await;
    seed_adapter(&g, &goap_devseed::local_fs_adapter("ORG-A", "/a")).await;
    Arc::new(g)
}

pub async fn seed_adapter(g: &Graph, a: &adapter::Instance) {
    Box::pin(goap_graphsvc::seed_adapter(&ctx(), g, a))
        .await
        .unwrap();
}

/// The service of a hub over g (no connector registered), extra adapter definitions added to the graph first.
pub async fn hub_service(
    g: &Arc<Graph>,
    inv: Option<Arc<dyn Invoker>>,
    extra: &[adapter::Def],
) -> Service {
    for d in extra {
        Box::pin(goap_graphsvc::seed_adapter_def(&ctx(), g, d))
            .await
            .unwrap();
    }
    let mut svc = Service::new(Arc::new(MemoryStore::new()));
    svc.directory = Some(Arc::new(Directory::new(g.clone())));
    svc.invoker = inv;
    svc.secrets = Some(Arc::new(PrefixSecrets));
    svc
}

/// newHub makes a hub over g with the localfs connector registered; extra adapter definitions are added to the graph
/// first (as a change of the platform namespace).
pub async fn new_hub(g: &Arc<Graph>, inv: Arc<FakeInvoker>, extra: &[adapter::Def]) -> Service {
    let svc = hub_service(g, Some(inv), extra).await;
    svc.register_connector(&ctx(), Some(localfs_info()), "http://localfs:8080")
        .await
        .unwrap();
    svc
}
