//! The connectors of the built-in MCPs against a real graph, served by the graph service (ADR 0125).

use std::collections::HashMap;
use std::sync::{Arc, Once};

use chrono::{Duration as TimeDelta, SecondsFormat, Utc};
use goap_access as access;
use goap_adapter as adapter;
use goap_authz::Principal;
use goap_connect::{Code, ConnectError, Ctx, Request, Response};
use goap_connector_builtin::{self as builtin, Ports};
use goap_def as def;
use goap_domain::{self as domain, ChangeID};
use goap_gen::engine::v1 as enginev1;
use goap_gen::registry::v1 as registryv1;
use goap_graph::{Commit, Graph, LinkEdit, Memory, NodeEdit};
use goap_mcp as mcp;
use goap_mcpbuiltin as mcpbuiltin;
use goap_mcpsvc::{Directory, InprocInvoker, MemoryStore, Service};
use parking_lot::Mutex;
use serde_json::{Map, Value, json};

/// Go's TestMain: registers the kinds of item of the risk register, as the services do at start (ADR 0065).
fn register() {
    static ONCE: Once = Once::new();
    ONCE.call_once(|| {
        goap_risk::register();
        goap_assess::register();
        goap_verify::register();
    });
}

/// fakeEngine records the requests of goap-scheduler.
#[derive(Default)]
struct FakeEngine {
    start: Mutex<Option<enginev1::StartProcessRequest>>,
    points: Mutex<Option<enginev1::ListStartingPointsRequest>>,
    header: Mutex<http::HeaderMap>,
}

#[goap_connect::async_trait]
impl enginev1::EngineService for FakeEngine {
    async fn start_process(
        &self,
        _: &Ctx,
        r: Request<enginev1::StartProcessRequest>,
    ) -> Result<Response<enginev1::StartProcessResponse>, ConnectError> {
        let methodology = r.msg.methodology.clone();
        *self.start.lock() = Some(r.msg);
        *self.header.lock() = r.headers;
        Ok(Response::new(enginev1::StartProcessResponse {
            process: Some(enginev1::Process {
                id: "P9".into(),
                status: "running".into(),
                methodology,
                steps: vec![enginev1::Step {
                    action: "a".into(),
                    ..Default::default()
                }],
                ..Default::default()
            }),
        }))
    }

    async fn attach_change(
        &self,
        _: &Ctx,
        r: Request<enginev1::AttachChangeRequest>,
    ) -> Result<Response<enginev1::AttachChangeResponse>, ConnectError> {
        Ok(Response::new(enginev1::AttachChangeResponse {
            process: Some(enginev1::Process {
                id: r.msg.process_id,
                status: "running".into(),
                change_id: r.msg.change_id,
                ..Default::default()
            }),
        }))
    }

    async fn get_process(
        &self,
        _: &Ctx,
        r: Request<enginev1::GetProcessRequest>,
    ) -> Result<Response<enginev1::GetProcessResponse>, ConnectError> {
        Ok(Response::new(enginev1::GetProcessResponse {
            process: Some(enginev1::Process {
                id: r.msg.id,
                status: "completed".into(),
                steps: vec![enginev1::Step {
                    action: "a".into(),
                    ..Default::default()
                }],
                ..Default::default()
            }),
        }))
    }

    async fn list_processes(
        &self,
        _: &Ctx,
        _: Request<enginev1::ListProcessesRequest>,
    ) -> Result<Response<enginev1::ListProcessesResponse>, ConnectError> {
        let p = |id: &str| enginev1::Process {
            id: id.into(),
            ..Default::default()
        };
        Ok(Response::new(enginev1::ListProcessesResponse {
            processes: vec![p("P1"), p("P2")],
        }))
    }

    async fn list_triggers(
        &self,
        _: &Ctx,
        _: Request<enginev1::ListTriggersRequest>,
    ) -> Result<Response<enginev1::ListTriggersResponse>, ConnectError> {
        Ok(Response::new(enginev1::ListTriggersResponse::default()))
    }

    async fn fire_trigger(
        &self,
        _: &Ctx,
        r: Request<enginev1::FireTriggerRequest>,
    ) -> Result<Response<enginev1::FireTriggerResponse>, ConnectError> {
        Ok(Response::new(enginev1::FireTriggerResponse {
            process: Some(enginev1::Process {
                id: "P3".into(),
                trigger: format!("{}/{}/{}", r.msg.methodology, r.msg.agent, r.msg.trigger),
                ..Default::default()
            }),
        }))
    }

    async fn list_starting_points(
        &self,
        _: &Ctx,
        r: Request<enginev1::ListStartingPointsRequest>,
    ) -> Result<Response<enginev1::ListStartingPointsResponse>, ConnectError> {
        let change_id = r.msg.change_id.clone();
        *self.points.lock() = Some(r.msg);
        *self.header.lock() = r.headers;
        Ok(Response::new(enginev1::ListStartingPointsResponse {
            methodology: "m".into(),
            version: "1.0.0".into(),
            goal: "deliver".into(),
            blocked_count: 2,
            points: vec![enginev1::StartingPoint {
                id: "p/s".into(),
                kind: "step".into(),
                launch: Some(enginev1::StartLaunch {
                    agent: "p".into(),
                    goal: "p/s".into(),
                    change_id,
                    ..Default::default()
                }),
                ..Default::default()
            }],
            ..Default::default()
        }))
    }
}

#[derive(Default)]
struct FakeRegistry {
    domains: Vec<def::Domain>,
}

#[goap_connect::async_trait]
impl registryv1::RegistryService for FakeRegistry {
    async fn list_methodologies(
        &self,
        _: &Ctx,
        _: Request<registryv1::ListMethodologiesRequest>,
    ) -> Result<Response<registryv1::ListMethodologiesResponse>, ConnectError> {
        Ok(Response::new(registryv1::ListMethodologiesResponse {
            methodologies: vec![registryv1::MethodologySummary {
                name: "impact-analysis".into(),
                namespace: "alm".into(),
                status: "published".into(),
                ..Default::default()
            }],
        }))
    }

    /// GetMethodologyView gives the step p/s of m 1.0.0 (the one FakeEngine binds).
    async fn get_methodology_view(
        &self,
        _: &Ctx,
        r: Request<registryv1::GetMethodologyViewRequest>,
    ) -> Result<Response<registryv1::GetMethodologyViewResponse>, ConnectError> {
        if r.msg.name != "m" || r.msg.version != "1.0.0" {
            return Err(ConnectError::new(Code::NotFound, "unknown methodology"));
        }
        Ok(Response::new(registryv1::GetMethodologyViewResponse {
            view: Some(registryv1::MethodologyView {
                methodology: "m".into(),
                version: "1.0.0".into(),
                processes: vec![registryv1::ViewActivity {
                    name: "p".into(),
                    kind: "process".into(),
                    steps: vec![registryv1::ViewStep {
                        path: "p/s".into(),
                        name: "s".into(),
                        owner: "p".into(),
                        description: "Do it".into(),
                        exit: HashMap::from([("done".to_string(), true)]),
                        roles: Some(registryv1::Responsibilities {
                            responsible: "dev".into(),
                            ..Default::default()
                        }),
                        ..Default::default()
                    }],
                    ..Default::default()
                }],
                ..Default::default()
            }),
            ..Default::default()
        }))
    }
}

#[async_trait::async_trait]
impl builtin::Domains for FakeRegistry {
    async fn domains(
        &self,
        _: &Ctx,
    ) -> Result<Vec<def::Domain>, Box<dyn std::error::Error + Send + Sync>> {
        Ok(self.domains.clone())
    }
}

struct Platform {
    g: Arc<Graph>,
    hub: Arc<Service>,
    engine: Arc<FakeEngine>,
    /// cancels the renewals of the registrations (Go's t.Context()).
    _cancel: goap_ctx::CancelHandle,
}

/// newPlatform is the demo graph (ALM repository and its organisation) with the defaults and the built-in MCPs, and
/// a hub serving the built-in connectors with the default policies.
async fn new_platform() -> Platform {
    register();
    let ctx = Ctx::background();
    let ds = def::load_domains("../../../domains").unwrap();
    let cat = Arc::new(goap_typecat::new(&ds).unwrap());
    let mut g = Graph::new(Arc::new(Memory::new()));
    g.set_types(move || Some(cat.clone() as Arc<dyn goap_graph::TypeCatalog>));
    Box::pin(goap_devseed::demo(&ctx, &g)).await.unwrap();
    Box::pin(goap_graphsvc::seed_access(&ctx, &g))
        .await
        .unwrap();
    Box::pin(goap_devseed::document_repository(&ctx, &g))
        .await
        .unwrap();
    Box::pin(goap_graphsvc::seed_builtins(&ctx, &g))
        .await
        .unwrap();
    let g = Arc::new(g);
    let az =
        Arc::new(access::Authorizer::new(Arc::new(access::Directory::new(g.clone()))).unwrap());
    let eng = Arc::new(FakeEngine::default());
    let mut hub = Service::new(Arc::new(MemoryStore::new()));
    hub.directory = Some(Arc::new(Directory::new(g.clone())));
    // the connectors read the hub (goap-admin) and the hub calls the connectors: the port is a copy sharing the
    // store and the directory, made before the invoker is set (Go's pointer to the same struct)
    let hub_port: Arc<dyn builtin::Hub> = Arc::new(hub.clone());
    let cs = builtin::connectors(Ports {
        graph: Some(g.clone()),
        engine: Some(eng.clone()),
        hub: Some(hub_port),
        domains: Some(Arc::new(FakeRegistry { domains: ds })),
        methodologies: Some(Arc::new(FakeRegistry::default())),
        authz: Some(az.clone()),
        floor: Some(Arc::new(az.floor())),
    });
    assert_eq!(cs.len(), 4, "built-in connectors");
    hub.invoker = Some(Arc::new(InprocInvoker {
        connectors: cs.clone(),
        remote: None,
    }));
    let hub = Arc::new(hub);
    let (rctx, cancel) = ctx.with_cancel();
    hub.keep_registered(&rctx, cs).await;
    Platform {
        g,
        hub,
        engine: eng,
        _cancel: cancel,
    }
}

impl Drop for Platform {
    fn drop(&mut self) {
        self._cancel.cancel();
    }
}

fn as_(subject: &str, org: &str, roles: &[&str]) -> Ctx {
    goap_authz::with_principal(
        &Ctx::background(),
        Principal {
            subject: subject.into(),
            org: org.into(),
            roles: roles.iter().map(|r| r.to_string()).collect(),
            ..Default::default()
        },
    )
}

fn with_change(ctx: &Ctx, change: &str, process: &str) -> Ctx {
    mcp::with_call(
        ctx,
        mcp::CallContext {
            change: change.into(),
            process: process.into(),
            ..Default::default()
        },
    )
}

fn obj(v: Value) -> Map<String, Value> {
    v.as_object().cloned().unwrap_or_default()
}

impl Platform {
    async fn try_call(
        &self,
        ctx: &Ctx,
        unit: &str,
        tool: &str,
        args: Value,
    ) -> Result<Value, goap_mcpsvc::Error> {
        let args = if args.is_null() {
            Map::new()
        } else {
            obj(args)
        };
        Box::pin(self.hub.call(ctx, unit, tool, args))
            .await
            .map(Value::Object)
    }

    async fn call(&self, ctx: &Ctx, unit: &str, tool: &str, args: Value) -> Value {
        self.try_call(ctx, unit, tool, args)
            .await
            .unwrap_or_else(|e| panic!("{tool}: {e}"))
    }

    async fn create(&self, ctx: &Ctx, unit: &str, args: Value) -> Value {
        self.call(ctx, unit, "goap-change/create", args).await["change"].clone()
    }
}

fn keys(list: &Value) -> Vec<String> {
    list.as_array()
        .map(|a| {
            a.iter()
                .map(|v| v["key"].as_str().unwrap_or_default().to_string())
                .collect()
        })
        .unwrap_or_default()
}

fn arr(v: &Value) -> Vec<Value> {
    v.as_array().cloned().unwrap_or_default()
}

fn s(v: &Value) -> String {
    v.as_str().unwrap_or_default().to_string()
}

// The operations of each built-in connector are the tools of the built-in MCP of the same name.
#[tokio::test]
async fn connectors_implement_the_builtin_mcps() {
    let cs = builtin::connectors(Ports {
        graph: Some(Arc::new(Graph::new(Arc::new(Memory::new())))),
        engine: Some(Arc::new(FakeEngine::default())),
        hub: Some(Arc::new(Service::new(Arc::new(MemoryStore::new())))),
        domains: Some(Arc::new(FakeRegistry::default())),
        methodologies: Some(Arc::new(FakeRegistry::default())),
        ..Default::default()
    });
    for d in mcpbuiltin::defs() {
        let c = cs
            .get(&d.name)
            .filter(|c| c.info().id == d.name)
            .unwrap_or_else(|| panic!("no connector {}", d.name));
        let ops: Vec<String> = c.info().operations.into_iter().map(|o| o.name).collect();
        let tools: Vec<String> = d.tools.iter().map(|t| t.name.clone()).collect();
        assert_eq!(ops, tools, "{}: operations, tools", d.name);
    }
}

// Every unit can use the built-in MCPs through the default organisation.
#[tokio::test]
async fn every_unit_gets_the_builtins() {
    let p = new_platform().await;
    let ctx = Ctx::background();
    for unit in ["ORG-CHECKOUT", "ORG-ACME", "", "ORG-UNKNOWN"] {
        let (_, mcps) = p.hub.tools(&ctx, unit).await.unwrap();
        assert_eq!(
            mcps,
            [
                mcpbuiltin::ADMIN,
                mcpbuiltin::CHANGE,
                mcpbuiltin::GRAPH,
                mcpbuiltin::SCHEDULER
            ],
            "{unit:?}: mcps"
        );
    }
    let (tools, _) = p.hub.tools(&ctx, "ORG-CHECKOUT").await.unwrap();
    for tl in tools {
        let (m, _) = mcp::split_tool(&tl.name).unwrap();
        assert_eq!(
            m == mcpbuiltin::SCHEDULER,
            tl.scope == mcp::SCOPE_AGENT,
            "{} has scope {}",
            tl.name,
            tl.scope
        );
    }
    let again = Box::pin(goap_graphsvc::seed_builtins(&ctx, &p.g))
        .await
        .unwrap();
    assert!(!again, "seeding the built-ins twice");
}

#[tokio::test]
async fn graph_tools() {
    let p = new_platform().await;
    let ctx = as_("alice", "ORG-CHECKOUT", &["contributor"]);
    let out = p
        .call(
            &ctx,
            "ORG-CHECKOUT",
            "goap-graph/read",
            json!({"namespace": "alm", "key": "REQ-1"}),
        )
        .await;
    let node = &out["node"];
    assert!(
        node["type"] == "alm@Requirement" && s(&node["properties"]["title"]).contains("PSP"),
        "read = {out}"
    );
    assert!(
        !arr(&out["in"]).is_empty(),
        "REQ-1 has incoming links (verifies, realizes): {out}"
    );
    let got = keys(
        &p.call(
            &ctx,
            "",
            "goap-graph/glob",
            json!({"namespace": "alm", "pattern": "REQ-*"}),
        )
        .await["nodes"],
    );
    assert_eq!(got, ["REQ-1", "REQ-2", "REQ-3", "REQ-4"], "glob");
    let got = keys(
        &p.call(
            &ctx,
            "",
            "goap-graph/glob",
            json!({"namespace": "alm", "type": "alm@Environment", "limit": 2.0}),
        )
        .await["nodes"],
    );
    assert_eq!(got.len(), 2, "glob limited = {got:?}");
    let got = keys(
        &p.call(
            &ctx,
            "",
            "goap-graph/grep",
            json!({"namespace": "alm", "pattern": "psp", "ignoreCase": true}),
        )
        .await["nodes"],
    );
    assert_eq!(got, ["REQ-1", "REQ-2"], "grep");
    let links = p
        .call(
            &ctx,
            "",
            "goap-graph/links",
            json!({"namespace": "alm", "key": "APP-1", "direction": "out", "type": "alm@composed_of"}),
        )
        .await;
    assert!(
        arr(&links["out"]).len() == 2 && arr(&links["in"]).is_empty(),
        "links = {links}"
    );
    let bs = p
        .call(
            &ctx,
            "",
            "goap-graph/baselines",
            json!({"namespace": "alm"}),
        )
        .await;
    assert!(
        !bs["head"].is_null() && !arr(&bs["baselines"]).is_empty(),
        "baselines = {bs}"
    );
    // the platform APIs act for a caller
    let err = p
        .try_call(
            &Ctx::background(),
            "",
            "goap-graph/read",
            json!({"namespace": "alm", "key": "REQ-1"}),
        )
        .await
        .unwrap_err();
    assert!(err.as_tool().is_some(), "anonymous read: {err}");
}

#[tokio::test]
async fn change_tools() {
    let p = new_platform().await;
    let ctx = as_("alice", "ORG-CHECKOUT", &["contributor"]);
    let created = p
        .create(
            &ctx,
            "ORG-CHECKOUT",
            json!({"title": "Refunds by voucher", "intent": "Customers may be refunded by voucher",
                "namespace": "alm", "methodology": "sdlc"}),
        )
        .await;
    let id = s(&created["id"]);
    assert!(
        created["unit"] == "ORG-CHECKOUT"
            && created["namespace"] == "alm"
            && created["methodology"] == "sdlc",
        "change = {created}"
    );
    // the tools work on the change of the calling process by default
    let ctx = with_change(&ctx, &id, "P1");
    let u = "ORG-CHECKOUT";
    p.call(
        &ctx,
        u,
        "goap-change/write",
        json!({"key": "REQ-9", "type": "alm@Requirement",
            "properties": {"title": "Refund by voucher", "priority": "medium"}, "rationale": "new refund mode"}),
    )
    .await;
    assert!(
        p.try_call(
            &ctx,
            u,
            "goap-change/write",
            json!({"key": "REQ-1", "type": "alm@Requirement", "rationale": "again"})
        )
        .await
        .is_err(),
        "write of an existing node accepted"
    );
    // REQ-2 starts proposed: move it to draft (the state of the work) before editing its properties
    p.call(
        &ctx,
        u,
        "goap-change/edit",
        json!({"key": "REQ-2", "state": "draft", "rationale": "reopen for refunds"}),
    )
    .await;
    // edit guards against a concurrent edit
    let err = p
        .try_call(
            &ctx,
            u,
            "goap-change/edit",
            json!({"key": "REQ-2", "properties": {"priority": "high"},
                "expect": {"priority": "low"}, "rationale": "refunds matter"}),
        )
        .await
        .unwrap_err();
    assert!(err.to_string().contains("expects"), "stale edit: {err}");
    p.call(
        &ctx,
        u,
        "goap-change/edit",
        json!({"key": "REQ-2", "properties": {"priority": "high"},
            "expect": {"priority": "medium"}, "rationale": "refunds matter"}),
    )
    .await;
    p.call(
        &ctx,
        u,
        "goap-change/link",
        json!({"from": "REQ-9", "type": "alm@satisfies", "to": "NEED-2"}),
    )
    .await;
    p.call(
        &ctx,
        u,
        "goap-change/note",
        json!({"text": "voucher refunds need finance approval", "type": "question"}),
    )
    .await;

    let read = p.call(&ctx, u, "goap-change/read", Value::Null).await;
    let nodes = arr(&read["nodes"]);
    assert_eq!(nodes.len(), 2, "nodes = {nodes:?}");
    for n in &nodes {
        match n["key"].as_str() {
            Some("REQ-9") => assert!(
                n["intent"] == "created" && n["properties"]["title"] == "Refund by voucher",
                "REQ-9 = {n}"
            ),
            Some("REQ-2") => assert!(
                n["intent"] == "modified" && n["properties"]["priority"] == "high",
                "REQ-2 = {n}"
            ),
            _ => {}
        }
    }
    let items = arr(&read["items"]);
    assert!(
        items.len() == 1 && !items[0]["data"]["text"].is_null(),
        "items = {items:?}"
    );
    let bb =
        p.g.blackboard(&Ctx::background(), &ChangeID::from(id.as_str()))
            .await
            .unwrap();
    for n in &bb.change.nodes {
        if n.key == "REQ-9" {
            let post = n.post.clone().unwrap();
            assert_eq!(bb.nodes[&post].out.len(), 1, "REQ-9 links");
        }
    }
    p.try_call(&ctx, u, "goap-change/validate", Value::Null)
        .await
        .unwrap();
    // main is untouched until the change is applied
    if let Ok(n) = p.g.node_by_key(&Ctx::background(), "alm", "REQ-9").await {
        assert_ne!(n.branch, domain::MAIN_BRANCH, "the change wrote on main");
    }
    // risks and actions (ADR 0036 §1): raised, updated as new versions, traced; the brief tells it all in a few lines
    let r1 = p
        .call(
            &ctx,
            u,
            "goap-change/risk",
            json!({"title": "Finance rejects voucher refunds", "probability": 3.0, "impact": 4.0, "owner": "product_owner"}),
        )
        .await;
    assert_eq!(r1["risk"]["data"]["key"], "RSK-1", "risk = {r1}");
    p.call(
        &ctx,
        u,
        "goap-change/action",
        json!({"title": "Get the finance approval", "owner": "business_analyst", "for": "RSK-1"}),
    )
    .await;
    p.call(
        &ctx,
        u,
        "goap-change/risk",
        json!({"key": "RSK-1", "status": "mitigating", "actions": ["ACT-1"]}),
    )
    .await;
    assert!(
        p.try_call(&ctx, u, "goap-change/risk", json!({"probability": 2.0}))
            .await
            .is_err(),
        "a new risk without a title"
    );
    let reg = p.call(&ctx, u, "goap-change/risks", Value::Null).await;
    let risks = arr(&reg["risks"]);
    assert_eq!(risks.len(), 1, "register = {reg}");
    let r = &risks[0];
    assert!(
        r["status"] == "mitigating"
            && r["title"] == "Finance rejects voucher refunds"
            && r["probability"].as_f64() == Some(3.0)
            && r["versions"].as_f64() == Some(2.0),
        "a new version keeps what it does not restate: {r}"
    );
    // derogations (ADR 0075 §2): signed by the caller, versioned by key, listed
    let dctx = goap_authz::with_principal(
        &ctx,
        Principal {
            subject: "alice".into(),
            roles: vec!["admin".into()],
            ..Default::default()
        },
    );
    let exp = (Utc::now() + TimeDelta::hours(24)).to_rfc3339_opts(SecondsFormat::Secs, true);
    assert!(
        p.try_call(
            &dctx,
            u,
            "goap-change/derogation",
            json!({"rule": "tests pass", "target": "REQ-9", "reason": "deadline"})
        )
        .await
        .is_err(),
        "a derogation without an expiry"
    );
    let d1 = p
        .call(
            &dctx,
            u,
            "goap-change/derogation",
            json!({"rule": "tests pass", "target": "REQ-9", "reason": "deadline", "expires": exp}),
        )
        .await;
    assert_eq!(
        d1["derogation"]["data"]["key"], "DRG-1",
        "derogation = {d1}"
    );
    p.call(
        &dctx,
        u,
        "goap-change/derogation",
        json!({"key": "DRG-1", "status": "closed"}),
    )
    .await;
    let drg = arr(&p
        .call(&dctx, u, "goap-change/derogations", Value::Null)
        .await["derogations"]);
    assert!(
        drg.len() == 1
            && drg[0]["status"] == "closed"
            && drg[0]["signatory"] == "alice"
            && drg[0]["versions"].as_f64() == Some(2.0),
        "derogations = {drg:?}"
    );
    let brief = s(&p.call(&ctx, u, "goap-change/brief", Value::Null).await["brief"]);
    for want in [
        "INTENT Customers may be refunded by voucher",
        "- REQ-9 Requirement created drafted",
        "- RSK-1 12=3x4 mitigating product_owner",
        "ACTIONS 1 open",
        "- ACT-1 business_analyst - RSK-1",
    ] {
        assert!(brief.contains(want), "brief lacks {want:?}:\n{brief}");
    }
    let trace = s(&p
        .call(&ctx, u, "goap-change/trace", json!({"ref": "RSK-1"}))
        .await["trace"]);
    assert_eq!(
        trace.matches("ITEM ").count(),
        2,
        "the trace of a risk shows its versions:\n{trace}"
    );
    let trace = s(&p
        .call(&ctx, u, "goap-change/trace", json!({"ref": "REQ-2"}))
        .await["trace"]);
    assert!(trace.contains("NODE REQ-2"), "trace of a node:\n{trace}");
    // several nodes taken out at once (all or none): an unknown key refuses the whole call
    assert!(
        p.try_call(
            &ctx,
            u,
            "goap-change/remove",
            json!({"keys": ["REQ-9", "REQ-404"]})
        )
        .await
        .is_err(),
        "remove of a node not in the change accepted"
    );
    let got = p
        .call(
            &ctx,
            u,
            "goap-change/remove",
            json!({"keys": ["REQ-9", "REQ-2"]}),
        )
        .await;
    assert_eq!(arr(&got["removed"]).len(), 2, "removed = {got}");
    let nodes = p.call(&ctx, u, "goap-change/read", Value::Null).await["nodes"].clone();
    assert!(arr(&nodes).is_empty(), "nodes left = {nodes}");
}

// Options through goap-change (ADR 0032 §6): an agent opens two hypotheses, works on each in turn (the edits go to the
// active option), and compares them.
#[tokio::test]
async fn change_option_tools() {
    let p = new_platform().await;
    let u = "ORG-CHECKOUT";
    let ctx = as_("alice", u, &["contributor"]);
    let id = s(&p
        .create(
            &ctx,
            u,
            json!({"title": "Refunds", "intent": "Refund faster", "namespace": "alm", "methodology": "sdlc"}),
        )
        .await["id"]);
    let ctx = with_change(&ctx, &id, "P1");
    let a = p
        .call(
            &ctx,
            u,
            "goap-change/option",
            json!({"name": "high", "hypothesis": "refunds are urgent", "activate": true}),
        )
        .await["option"]
        .clone();
    // REQ-2 starts proposed: move it to draft (the state of the work) before editing its properties, in each option
    p.call(
        &ctx,
        u,
        "goap-change/edit",
        json!({"key": "REQ-2", "state": "draft", "rationale": "reopen for refunds"}),
    )
    .await;
    p.call(
        &ctx,
        u,
        "goap-change/edit",
        json!({"key": "REQ-2", "properties": {"priority": "high"}, "rationale": "urgent"}),
    )
    .await;
    let b = p
        .call(
            &ctx,
            u,
            "goap-change/option",
            json!({"name": "low", "hypothesis": "refunds can wait"}),
        )
        .await["option"]
        .clone();
    let act = p
        .call(&ctx, u, "goap-change/activate", json!({"option": b["id"]}))
        .await;
    assert_eq!(act["active"], b["id"], "activate = {act}");
    p.call(
        &ctx,
        u,
        "goap-change/edit",
        json!({"key": "REQ-2", "state": "draft", "rationale": "reopen for refunds"}),
    )
    .await;
    p.call(
        &ctx,
        u,
        "goap-change/edit",
        json!({"key": "REQ-2", "properties": {"priority": "low"}, "rationale": "can wait"}),
    )
    .await;
    let list = p.call(&ctx, u, "goap-change/options", Value::Null).await;
    assert!(
        list["active"] == b["id"] && arr(&list["options"]).len() == 2,
        "options = {list}"
    );
    let cmp = p.call(&ctx, u, "goap-change/compare", Value::Null).await;
    let nodes = arr(&cmp["nodes"]);
    assert_eq!(nodes.len(), 1, "compare = {cmp}");
    let props = &nodes[0]["props"];
    assert!(
        props[s(&a["id"])]["priority"] == "high"
            && props[s(&b["id"])]["priority"] == "low"
            && props["main"]["priority"] == "medium",
        "compared properties = {props}"
    );
    p.call(
        &ctx,
        u,
        "goap-change/evaluate",
        json!({"option": a["id"], "comment": "finance agrees"}),
    )
    .await;
    let act = p
        .call(&ctx, u, "goap-change/activate", json!({"option": "main"}))
        .await;
    assert_eq!(act["active"], "main", "back to main = {act}");
    // the main flow did not see the options' edits
    let nodes = arr(&p.call(&ctx, u, "goap-change/read", Value::Null).await["nodes"]);
    assert!(nodes.is_empty(), "main flow nodes = {nodes:?}");
    // a decision point on the two options (ADR 0009 §4): undecidable, answered, then decided above the threshold
    let point = p
        .call(
            &ctx,
            u,
            "goap-change/decision",
            json!({"question": "How urgent are refunds?", "criteria": ["cost"]}),
        )
        .await["point"]
        .clone();
    assert!(
        point["status"] == "open" && arr(&point["options"]).len() == 2,
        "decision = {point}"
    );
    let ruled = p
        .call(
            &ctx,
            u,
            "goap-change/rule",
            json!({"outcome": "undecidable", "justification": "cost unknown",
                "questions": ["What does an urgent refund cost?"]}),
        )
        .await["point"]
        .clone();
    let q = arr(&ruled["questions"])[0].clone();
    assert_eq!(ruled["status"], "blocked", "undecidable = {ruled}");
    p.call(
        &ctx,
        u,
        "goap-change/answer",
        json!({"question": q["id"], "answer": "2 EUR"}),
    )
    .await;
    // the edits of the options reviewed nothing (ADR 0079: an edit never reviews); the reviewer accepts the draft of
    // the option explicitly, and a flow is adopted with its drafts accepted
    let c =
        p.g.change(&ctx, &ChangeID::from(id.as_str()))
            .await
            .unwrap();
    for cn in &c.nodes {
        assert_eq!(
            cn.review,
            domain::REVIEW_PROPOSED,
            "an edit through goap-change never reviews: {cn:?}"
        );
        if cn.key == "REQ-2" && cn.flow == s(&a["id"]) {
            p.g.impact_node_review_on(
                &ctx,
                &c.id,
                &cn.flow,
                "",
                &cn.id,
                domain::REVIEW_ACCEPTED,
                "reviewer",
                "agreed",
            )
            .await
            .unwrap();
        }
    }
    let decided = p
        .call(
            &ctx,
            u,
            "goap-change/rule",
            json!({"outcome": "decided", "option": "high", "confidence": 0.95,
                "justification": "cheap enough"}),
        )
        .await["point"]
        .clone();
    assert!(
        decided["status"] == "decided" && decided["option"] == a["id"],
        "decided = {decided}"
    );
    let pts = arr(&p.call(&ctx, u, "goap-change/decisions", Value::Null).await["points"]);
    assert_eq!(pts.len(), 1, "decisions = {pts:?}");
    let list = p.call(&ctx, u, "goap-change/options", Value::Null).await;
    assert_eq!(
        arr(&list["options"])[0]["status"],
        "selected",
        "the decision selects its option: {list}"
    );
}

#[tokio::test]
async fn change_signal() {
    let p = new_platform().await;
    let u = "ORG-CHECKOUT";
    let ctx = as_("alice", u, &["contributor"]);
    let id = s(&p
        .create(
            &ctx,
            u,
            json!({"title": "Refunds by voucher", "intent": "Customers may be refunded by voucher",
                "namespace": "alm", "methodology": "sdlc"}),
        )
        .await["id"]);
    let ctx = with_change(&ctx, &id, "P1");
    let out = p
        .call(
            &ctx,
            u,
            "goap-change/signal",
            json!({"target": "review_needed", "data": {"reason": "voucher policy"}, "recipient": "P0"}),
        )
        .await;
    let item = &out["item"];
    assert!(
        item["type"] == "signal"
            && item["target"] == "review_needed"
            && item["recipient"] == "P0"
            && item["producedBy"] == "P1",
        "item = {item}"
    );
    assert_eq!(item["data"]["reason"], "voucher policy", "data");
    assert!(
        p.try_call(&ctx, u, "goap-change/signal", json!({"data": {}}))
            .await
            .is_err(),
        "signal without a type accepted"
    );
}

// A request either continues an open change or needs a new one: goap-change.list finds the candidates, filtered by
// namespace, status and unit, the latest first, capped by limit.
#[tokio::test]
async fn change_list() {
    let p = new_platform().await;
    let u = "ORG-CHECKOUT";
    let ctx = as_("alice", u, &["contributor"]);
    let alm_id = p
        .create(
            &ctx,
            u,
            json!({"title": "Refunds by voucher", "intent": "refund by voucher",
                "namespace": "alm", "methodology": "sdlc"}),
        )
        .await["id"]
        .clone();
    let org_id = p
        .create(
            &ctx,
            u,
            json!({"title": "Reorg checkout", "intent": "reorg checkout", "namespace": "organisation"}),
        )
        .await["id"]
        .clone();

    let all = arr(&p.call(&ctx, u, "goap-change/list", Value::Null).await["changes"]);
    let ids: Vec<Value> = all.iter().map(|c| c["id"].clone()).collect();
    assert!(
        ids.contains(&alm_id) && ids.contains(&org_id),
        "list = {ids:?}, want {alm_id} and {org_id}"
    );

    let alm_only = arr(&p
        .call(&ctx, u, "goap-change/list", json!({"namespace": "alm"}))
        .await["changes"]);
    for c in &alm_only {
        assert_eq!(c["namespace"], "alm", "namespace filter leaked: {c}");
    }
    assert!(
        alm_only.iter().any(|c| c["id"] == alm_id),
        "namespace filter dropped {alm_id}: {alm_only:?}"
    );

    let none = arr(&p
        .call(&ctx, u, "goap-change/list", json!({"status": "applied"}))
        .await["changes"]);
    for c in &none {
        assert!(
            c["id"] != alm_id && c["id"] != org_id,
            "status filter leaked a draft change: {c}"
        );
    }

    let capped = p
        .call(&ctx, u, "goap-change/list", json!({"limit": 1.0}))
        .await;
    assert!(
        arr(&capped["changes"]).len() == 1 && capped["truncated"] == true,
        "limit = {capped}"
    );
}

// Reformulating a change never erases its definition: the previous title/intent is superseded as an "intent" item (the
// one set at creation included), the header always holding the current one.
#[tokio::test]
async fn change_reformulate() {
    let p = new_platform().await;
    let u = "ORG-CHECKOUT";
    let ctx = as_("alice", u, &["contributor"]);
    let id = s(&p
        .create(
            &ctx,
            u,
            json!({"title": "Refunds", "intent": "refund by voucher", "namespace": "alm", "methodology": "sdlc"}),
        )
        .await["id"]);
    let ctx = with_change(&ctx, &id, "P1");

    let out = p
        .call(
            &ctx,
            u,
            "goap-change/reformulate",
            json!({"title": "Refunds by voucher",
                "intent": "customers may be refunded by voucher up to 30 days after purchase",
                "rationale": "clarified with support"}),
        )
        .await;
    let ch = &out["change"];
    assert!(
        ch["title"] == "Refunds by voucher"
            && ch["intent"] == "customers may be refunded by voucher up to 30 days after purchase",
        "change after reformulate = {ch}"
    );
    let item1 = out["item"].clone();

    let out2 = p
        .call(
            &ctx,
            u,
            "goap-change/reformulate",
            json!({"intent": "customers may be refunded by voucher or bank transfer up to 30 days after purchase",
                "rationale": "finance asked for a bank transfer option"}),
        )
        .await;
    let ch2 = &out2["change"];
    assert!(
        ch2["title"] == "Refunds by voucher"
            && ch2["intent"]
                == "customers may be refunded by voucher or bank transfer up to 30 days after purchase",
        "change after 2nd reformulate = {ch2}"
    );
    let sup = arr(&out2["item"]["supersedes"]);
    assert!(
        sup.len() == 1 && sup[0] == item1["id"],
        "item2 supersedes = {sup:?}, want [{}]",
        item1["id"]
    );

    let read = p.call(&ctx, u, "goap-change/read", Value::Null).await;
    let intents: Vec<Value> = arr(&read["items"])
        .into_iter()
        .filter(|it| it["target"] == "intent")
        .collect();
    assert_eq!(
        intents.len(),
        3,
        "intent items = {intents:?}, want 3 (original snapshot + 2 reformulations)"
    );
    let orig = &intents[0]["data"];
    assert!(
        orig["title"] == "Refunds" && orig["intent"] == "refund by voucher",
        "original snapshot lost: {orig}"
    );
}

fn denied(r: &Result<Value, goap_mcpsvc::Error>) -> bool {
    matches!(r, Err(e) if e.to_string().contains("may not write policy"))
}

// User and Policy nodes stay behind the access gate of the platform (ADR 0020).
#[tokio::test]
async fn change_tools_keep_the_access_gate() {
    let p = new_platform().await;
    let u = "ORG-CHECKOUT";
    for (who, want) in [
        (as_("alice", u, &["contributor"]), true),
        (as_("root", u, &["admin"]), false),
    ] {
        let ch = p
            .create(
                &who,
                u,
                json!({"title": "Access", "intent": "a new user", "namespace": "organisation"}),
            )
            .await;
        let r = p
            .try_call(
                &who,
                u,
                "goap-change/write",
                json!({"change": ch["id"], "key": access::user_key("bob"), "type": access::NODE_TYPE_USER,
                    "properties": {"subject": "bob", "roles": ["admin"]}, "rationale": "promote bob"}),
            )
            .await;
        assert_eq!(
            denied(&r),
            want,
            "{:?}: {r:?}",
            goap_authz::principal_from(&who)
        );
    }
}

// Every adminOnly node type (ADR 0068) stays behind the gate through write, edit, link and unlink: a project member
// could otherwise write an organisation@Assignment granting admin through the in-process connector.
#[tokio::test]
async fn change_tools_gate_every_access_type() {
    let p = new_platform().await;
    let ctx = Ctx::background();
    Box::pin(goap_graphsvc::seed_adapter(
        &ctx,
        &p.g,
        &adapter::Instance {
            unit: "ORG-CRM".into(),
            mcp: mcpbuiltin::CHANGE.into(),
            ..Default::default()
        },
    ))
    .await
    .unwrap();
    let asg_key = access::platform_assignment_key("ORG-CHECKOUT");
    Box::pin(p.g.commit(
        &ctx,
        Commit {
            project_id: "PROJ-ROOT".into(),
            namespace: access::NAMESPACE_ORGANISATION.into(),
            title: "Assignment".into(),
            intent: "Assignment".into(),
            by: "test".into(),
            edits: vec![NodeEdit {
                key: asg_key.clone(),
                r#type: access::NODE_TYPE_ASSIGNMENT.into(),
                props: access::Assignment {
                    roles: vec![access::ROLE_READER.into()],
                    ..Default::default()
                }
                .props(),
                rationale: "seed".into(),
                links: vec![LinkEdit {
                    r#type: access::LINK_ASSIGNS_ORG.into(),
                    to_key: "ORG-CHECKOUT".into(),
                    ..Default::default()
                }],
                ..Default::default()
            }],
            ..Default::default()
        },
    ))
    .await
    .unwrap();
    let existing = [
        (asg_key.clone(), access::NODE_TYPE_ASSIGNMENT.to_string()),
        (
            "ORG-CHECKOUT".to_string(),
            access::NODE_TYPE_ORG_UNIT.to_string(),
        ),
        (
            access::DEFAULT_PROJECT.to_string(),
            access::NODE_TYPE_PROJECT_UNIT.to_string(),
        ),
        (
            adapter::key("ORG-CRM", mcpbuiltin::CHANGE),
            domain::TYPE_ADAPTER.to_string(),
        ),
    ];
    let u = "ORG-CHECKOUT";
    let alice = as_("alice", u, &["contributor"]);
    let mut failures = Vec::new();
    for (key, typ) in &existing {
        let ops = [
            (
                "write",
                json!({"key": format!("{key}-new"), "type": typ, "properties": {}, "rationale": "x"}),
            ),
            (
                "edit",
                json!({"key": key, "properties": {"description": "x"}, "rationale": "x"}),
            ),
            (
                "link",
                json!({"from": key, "type": access::LINK_ASSIGNS_ORG, "to": "ORG-DEFAULT"}),
            ),
            (
                "unlink",
                json!({"from": key, "type": access::LINK_ASSIGNS_ORG, "to": "ORG-DEFAULT"}),
            ),
            (
                "merge",
                json!({"sources": [key, "ORG-DEFAULT"], "key": format!("{key}-m"), "type": typ, "rationale": "x"}),
            ),
            (
                "split",
                json!({"source": key, "into": [
                    {"key": format!("{key}-s1"), "type": typ, "rationale": "x"},
                    {"key": format!("{key}-s2"), "type": typ, "rationale": "x"}]}),
            ),
        ];
        for (op, mut args) in ops {
            let change = p
                .create(
                    &alice,
                    u,
                    json!({"title": "Access", "intent": "grant", "namespace": "organisation"}),
                )
                .await["id"]
                .clone();
            args["change"] = change;
            let r = p
                .try_call(&alice, u, &format!("goap-change/{op}"), args)
                .await;
            if !denied(&r) {
                failures.push(format!("{op} of {typ} by a contributor: {r:?}"));
            }
        }
    }
    assert!(failures.is_empty(), "{failures:#?}");
    // an administrator passes the gate (the write itself may still fail on other grounds)
    let root = as_("root", u, &["admin"]);
    let ch = p
        .create(
            &root,
            u,
            json!({"title": "Access", "intent": "grant", "namespace": "organisation"}),
        )
        .await["id"]
        .clone();
    let r = p
        .try_call(
            &root,
            u,
            "goap-change/edit",
            json!({"change": ch, "key": asg_key, "properties": {"description": "x"}, "rationale": "x"}),
        )
        .await;
    assert!(!denied(&r), "administrator denied: {r:?}");
}

// A unit restricts a built-in MCP for itself and its sub-units: goap-change read-only for the CRM team.
#[tokio::test]
async fn unit_restricts_a_builtin() {
    let p = new_platform().await;
    let ctx = Ctx::background();
    for a in [
        adapter::Instance {
            unit: "ORG-CRM".into(),
            mcp: mcpbuiltin::CHANGE.into(),
            read_only: true,
            ..Default::default()
        },
        adapter::Instance {
            unit: "ORG-DIGITAL".into(),
            mcp: mcpbuiltin::ADMIN.into(),
            disabled: true,
            ..Default::default()
        },
    ] {
        Box::pin(goap_graphsvc::seed_adapter(&ctx, &p.g, &a))
            .await
            .unwrap();
    }
    let (tools, mcps) = p.hub.tools(&ctx, "ORG-CRM").await.unwrap();
    let change: Vec<String> = tools
        .iter()
        .filter_map(|tl| {
            let (m, name) = mcp::split_tool(&tl.name).ok()?;
            (m == mcpbuiltin::CHANGE).then_some(name)
        })
        .collect();
    assert!(
        change
            == [
                "dependencies",
                "read",
                "list",
                "validate",
                "options",
                "compare",
                "decisions",
                "brief",
                "trace",
                "risks",
                "derogations",
                "reviews"
            ]
            && !mcps.iter().any(|m| m == mcpbuiltin::ADMIN),
        "ORG-CRM: goap-change tools {change:?}, mcps {mcps:?}"
    );
    let carol = as_("carol", "ORG-CRM", &["contributor"]);
    let err = p
        .try_call(
            &carol,
            "ORG-CRM",
            "goap-change/create",
            json!({"title": "x", "intent": "y", "namespace": "alm"}),
        )
        .await
        .unwrap_err();
    assert!(err.is_not_bound(), "restricted tool called: {err}");
    // siblings keep everything but the admin MCP their direction disabled; the company keeps all
    let (_, mcps) = p.hub.tools(&ctx, "ORG-CHECKOUT").await.unwrap();
    assert!(
        !mcps.iter().any(|m| m == mcpbuiltin::ADMIN)
            && mcps.iter().any(|m| m == mcpbuiltin::CHANGE),
        "ORG-CHECKOUT: {mcps:?}"
    );
    let (_, mcps) = p.hub.tools(&ctx, "ORG-ACME").await.unwrap();
    assert_eq!(mcps.len(), 4, "ORG-ACME: {mcps:?}");
    // goap-admin describes the restriction
    let out = p
        .call(
            &as_("root", "ORG-ACME", &["admin"]),
            "ORG-ACME",
            "goap-admin/mcps",
            json!({"unit": "ORG-CRM"}),
        )
        .await;
    for m in arr(&out["mcps"]) {
        if m["mcp"] == mcpbuiltin::CHANGE {
            assert!(
                arr(&m["tools"]).len() == 12
                    && arr(&m["restrictedBy"])[0] == "ORG-CRM"
                    && m["definedIn"] == access::DEFAULT_ORG,
                "goap-change for ORG-CRM = {m}"
            );
        }
    }
}

#[tokio::test]
async fn scheduler_tools() {
    let p = new_platform().await;
    let ctx = with_change(&as_("alice", "ORG-CHECKOUT", &["contributor"]), "C1", "P1");
    let out = p
        .call(
            &ctx,
            "ORG-CHECKOUT",
            "goap-scheduler/start",
            json!({"intent": "assess the impact of REQ-1", "methodology": "impact-analysis"}),
        )
        .await;
    let proc_ = &out["process"];
    assert!(
        proc_["id"] == "P9" && proc_["steps"].is_null(),
        "start = {out}"
    );
    let head =
        p.g.branch_head(&Ctx::background(), "alm", domain::MAIN_BRANCH)
            .await
            .unwrap();
    // a new change on the head of main of the namespace of the methodology
    let r = p.engine.start.lock().clone().unwrap();
    assert!(
        r.owner_org == "ORG-CHECKOUT"
            && r.title == "assess the impact of REQ-1"
            && r.change_id.is_empty()
            && r.baseline_id == head.id.as_str(),
        "start request = {r:?}"
    );
    assert!(
        p.try_call(
            &as_("alice", "ORG-CHECKOUT", &["contributor"]),
            "",
            "goap-scheduler/start",
            json!({"intent": "anything"})
        )
        .await
        .is_err(),
        "a process started without knowing its namespace"
    );
    {
        let h = p.engine.header.lock();
        let get = |k: &str| {
            h.get(k)
                .and_then(|v| v.to_str().ok())
                .unwrap_or_default()
                .to_string()
        };
        assert!(
            get(goap_identity::HEADER_SUBJECT) == "alice"
                && get(goap_identity::HEADER_ROLES) == "contributor",
            "the caller is not forwarded: {h:?}"
        );
    }
    let got = p
        .call(&ctx, "", "goap-scheduler/get", json!({"id": "P9"}))
        .await["process"]
        .clone();
    assert!(!got["steps"].is_null(), "get = {got}");
    let got = p
        .call(&ctx, "", "goap-scheduler/list", json!({"limit": 1.0}))
        .await;
    assert!(
        arr(&got["processes"]).len() == 1 && got["truncated"] == true,
        "list = {got}"
    );
    // the possible steps of the calling change (ADR 0097)
    let sp = p
        .call(&ctx, "", "goap-scheduler/starting_points", Value::Null)
        .await;
    let points_req = p.engine.points.lock().clone().unwrap();
    assert!(
        points_req.change_id == "C1"
            && sp["goal"] == "deliver"
            && arr(&sp["points"]).len() == 1
            && sp["blockedCount"].as_f64() == Some(2.0),
        "starting_points = {sp} {points_req:?}"
    );
    // the engine binds the step, the view of its methodology version says what it is (ADR 0126 step 5)
    let pt = &arr(&sp["points"])[0];
    assert!(
        pt["name"] == "s"
            && pt["process"] == "p"
            && pt["description"] == "Do it"
            && pt["responsible"] == "dev"
            && pt["produces"] == json!(["done"])
            && pt["launch"]["agent"] == "p",
        "composed point {pt}"
    );
    let got = p
        .call(
            &ctx,
            "",
            "goap-scheduler/fire",
            json!({"methodology": "m", "agent": "a", "trigger": "t"}),
        )
        .await;
    assert_eq!(got["process"]["trigger"], "m/a/t", "fire = {got}");
}

#[tokio::test]
async fn admin_tools() {
    let p = new_platform().await;
    let ctx = as_("alice", "ORG-CHECKOUT", &["contributor"]);
    let units = arr(&p
        .call(&ctx, "ORG-CHECKOUT", "goap-admin/units", Value::Null)
        .await["units"]);
    let parents: HashMap<String, Value> = units
        .iter()
        .map(|u| (s(&u["key"]), u["parent"].clone()))
        .collect();
    assert!(
        parents["ORG-CHECKOUT"] == "ORG-DIGITAL"
            && parents["ORG-ACME"] == access::DEFAULT_ORG
            && parents[access::DEFAULT_ORG].is_null(),
        "units = {units:?}"
    );
    let mcps = p
        .call(&ctx, "ORG-CHECKOUT", "goap-admin/mcps", Value::Null)
        .await;
    assert!(
        mcps["unit"] == "ORG-CHECKOUT" && arr(&mcps["mcps"]).len() == 4,
        "mcps = {mcps}"
    );
    let conns = arr(&p.call(&ctx, "", "goap-admin/connectors", Value::Null).await["connectors"]);
    assert!(
        conns.len() == 4 && conns[0]["builtin"] == true && conns[0]["live"] == true,
        "connectors = {conns:?}"
    );
    let doms = arr(&p.call(&ctx, "", "goap-admin/domains", Value::Null).await["domains"]);
    assert!(!doms.is_empty(), "no domain");
    p.call(&ctx, "", "goap-admin/methodologies", Value::Null)
        .await;
    p.call(
        &ctx,
        "",
        "goap-admin/users",
        json!({"unit": "ORG-CHECKOUT"}),
    )
    .await;
}

// The review object through goap-change (ADR 0080): a reviewer builds a review up, submits it, and the impacts are
// reviewed together; only the author (or an administrator) changes it; a submitted review is final.
#[tokio::test]
async fn change_review_tools() {
    let p = new_platform().await;
    let u = "ORG-CHECKOUT";
    let ctx = as_("alice", u, &["contributor"]);
    let id = s(&p
        .create(
            &ctx,
            u,
            json!({"title": "Reviews", "intent": "Review together", "namespace": "alm", "methodology": "sdlc"}),
        )
        .await["id"]);
    let ctx = with_change(&ctx, &id, "");
    for k in ["REQ-8", "REQ-9"] {
        p.call(
            &ctx,
            u,
            "goap-change/write",
            json!({"key": k, "type": "alm@Requirement", "properties": {"title": k}, "rationale": "new"}),
        )
        .await;
    }
    let list = p.call(&ctx, u, "goap-change/reviews", Value::Null).await;
    assert!(
        arr(&list["awaiting"]).len() == 2 && arr(&list["reviews"]).is_empty(),
        "reviews = {list}"
    );
    let rv = p
        .call(
            &ctx,
            u,
            "goap-change/review_open",
            json!({"comment": "global", "impacts": ["REQ-8"]}),
        )
        .await["review"]
        .clone();
    let key = s(&rv["key"]);
    assert!(
        rv["status"] == "open" && rv["by"] == "alice" && arr(&rv["entries"]).len() == 1,
        "open = {rv}"
    );
    assert!(
        p.try_call(
            &ctx,
            u,
            "goap-change/review_open",
            json!({"impacts": ["REQ-8"]})
        )
        .await
        .is_err(),
        "an impact in two open reviews"
    );
    p.call(
        &ctx,
        u,
        "goap-change/review_update",
        json!({"review": key, "add": ["REQ-9"], "entries": [
            {"impact": "REQ-8", "outcome": "accept", "comment": "good"},
            {"impact": "REQ-9", "outcome": "reject"}]}),
    )
    .await;
    let aw = arr(&p.call(&ctx, u, "goap-change/reviews", Value::Null).await["awaiting"]);
    assert!(aw.is_empty(), "both impacts are in the review: {aw:?}");
    let mctx = with_change(&as_("mallory", u, &["contributor"]), &id, "");
    let err = p
        .try_call(
            &mctx,
            u,
            "goap-change/review_submit",
            json!({"review": key}),
        )
        .await
        .unwrap_err();
    assert!(
        err.to_string().contains(goap_authz::ERR_FORBIDDEN),
        "someone else submits: {err}"
    );
    let out = p
        .call(&ctx, u, "goap-change/review_submit", json!({"review": key}))
        .await["review"]
        .clone();
    assert_eq!(out["status"], "submitted", "submit = {out}");
    let bb =
        p.g.blackboard(&Ctx::background(), &ChangeID::from(id.as_str()))
            .await
            .unwrap();
    for n in &bb.change.nodes {
        let want = match n.key.as_str() {
            "REQ-8" => domain::REVIEW_ACCEPTED,
            "REQ-9" => domain::REVIEW_REJECTED,
            _ => domain::NodeReview::default(),
        };
        assert!(
            n.review == want && n.reviews[0].review_id == key && n.reviews[0].by == "alice",
            "{} = {n:?}",
            n.key
        );
    }
    assert!(
        p.try_call(
            &ctx,
            u,
            "goap-change/review_update",
            json!({"review": key, "comment": "late"})
        )
        .await
        .is_err(),
        "a submitted review is final"
    );
    let d = p.call(&ctx, u, "goap-change/review_open", json!({})).await["review"].clone();
    let got = p
        .call(
            &ctx,
            u,
            "goap-change/review_discard",
            json!({"review": d["key"]}),
        )
        .await["review"]
        .clone();
    assert_eq!(got["status"], "discarded", "discard = {got}");
}

// A change is created in a project (ADR 0091): the one the call names, else the caller's active project, else the
// root; move relocates a root change.
#[tokio::test]
async fn change_create_and_move_project() {
    let p = new_platform().await;
    let ctx = Ctx::background();
    let root =
        p.g.node_by_key(
            &ctx,
            access::NAMESPACE_ORGANISATION,
            access::DEFAULT_PROJECT,
        )
        .await
        .unwrap();
    let head =
        p.g.branch_head(&ctx, access::NAMESPACE_ORGANISATION, domain::MAIN_BRANCH)
            .await
            .unwrap();
    Box::pin(p.g.commit(
        &ctx,
        Commit {
            project_id: access::DEFAULT_PROJECT.into(),
            namespace: access::NAMESPACE_ORGANISATION.into(),
            title: "project".into(),
            baseline: head.id,
            by: "test".into(),
            edits: vec![NodeEdit {
                key: "PROJ-MV".into(),
                r#type: access::NODE_TYPE_PROJECT_UNIT.into(),
                props: obj(json!({"name": "MV"})),
                rationale: "t".into(),
                links: vec![LinkEdit {
                    r#type: access::LINK_PROJECT_PART_OF.into(),
                    to: Some(root.r#ref()),
                    ..Default::default()
                }],
                ..Default::default()
            }],
            ..Default::default()
        },
    ))
    .await
    .unwrap();
    let args = json!({"title": "t", "intent": "i", "namespace": "alm"});
    let u = "ORG-CHECKOUT";
    let alice = as_("alice", u, &["contributor"]);
    let c = p.create(&alice, u, args.clone()).await;
    assert_eq!(c["project"], access::DEFAULT_PROJECT, "no active project");
    let active = goap_authz::with_principal(
        &Ctx::background(),
        Principal {
            subject: "alice".into(),
            org: u.into(),
            project: "PROJ-MV".into(),
            roles: vec!["contributor".into()],
            ..Default::default()
        },
    );
    let c = p.create(&active, u, args.clone()).await;
    assert_eq!(c["project"], "PROJ-MV", "active project");
    let named = json!({"title": "t", "intent": "i", "namespace": "alm", "project": access::DEFAULT_PROJECT});
    let c = p.create(&active, u, named).await;
    assert_eq!(c["project"], access::DEFAULT_PROJECT, "named project");
    let moved = p
        .call(
            &alice,
            u,
            "goap-change/move",
            json!({"change": c["id"], "project": "PROJ-MV"}),
        )
        .await["change"]
        .clone();
    assert_eq!(moved["project"], "PROJ-MV", "moved: {moved}");
    // a change depends on another (ADR 0108): added, read, removed
    let other = p.create(&alice, u, args).await;
    let dep = p
        .call(
            &alice,
            u,
            "goap-change/depend",
            json!({"change": c["id"], "add": [other["id"]]}),
        )
        .await["change"]
        .clone();
    assert_eq!(dep["dependsOn"], json!([other["id"]]), "depends on");
    let read = p
        .call(
            &alice,
            u,
            "goap-change/dependencies",
            json!({"change": other["id"]}),
        )
        .await;
    assert_eq!(arr(&read["dependents"]).len(), 1, "dependents: {read}");
    let dep = p
        .call(
            &alice,
            u,
            "goap-change/depend",
            json!({"change": c["id"], "remove": [other["id"]]}),
        )
        .await["change"]
        .clone();
    assert!(
        dep.get("dependsOn").is_none_or(Value::is_null),
        "removed: {}",
        dep["dependsOn"]
    );
}
