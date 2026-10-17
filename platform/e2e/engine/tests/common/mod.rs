//! The shared fixtures of the suite (Go's engine_test.go, agents_test.go, helpers_test.go and main_test.go helpers).
#![allow(dead_code)]

pub mod stub;

use std::sync::{Arc, Once};

use async_trait::async_trait;
use goap_authz::{self as authz, Principal};
use goap_ctx::Ctx;
use goap_domain::{
    self as domain, BaselineID, Blackboard, Change, ChangeID, GateResult, GuardianRef, Lifecycle,
    Transition,
};
use goap_dsl::NodeOp;
use goap_engine::{
    AuthzScope, BuiltinExecutor, BuiltinFunc, Engine, HumanExecutor, ItemInput, LLMExecutor,
    MemoryStore, MethodologyPort, StaticMethodologies, Store, default_builtins,
};
use goap_graph::{ChangeLifecycles, Graph, LinkWrite, Memory};
use goap_graphtest as graphtest;
use goap_llm as llm;
use goap_methodology::{self as methodology, Compiled};
use parking_lot::Mutex;
use serde_json::{Map, Value};

/// init registers the kinds of item of the risk register, as the services do at start (Go's TestMain, ADR 0065).
pub fn init() {
    static ONCE: Once = Once::new();
    ONCE.call_once(|| {
        goap_risk::register();
        goap_assess::register();
        goap_verify::register();
    });
}

/// ctx is a background context (after init).
pub fn ctx() -> Ctx {
    init();
    Ctx::background()
}

/// as_who is a context acting as p.
pub fn as_who(p: &Principal) -> Ctx {
    authz::with_principal(&ctx(), p.clone())
}

/// path is a path relative to the root of the repository.
pub fn path(rel: &str) -> String {
    format!("{}/../../../{rel}", env!("CARGO_MANIFEST_DIR"))
}

/// obj turns a JSON object into a map (anything else: empty).
pub fn obj(v: Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m,
        _ => Map::new(),
    }
}

/// testProject is the key of the project these tests start their (non-administrative) processes in (ADR 0039).
pub const TEST_PROJECT: &str = "PROJ-TEST";

/// Errs collects the failures noticed where a test cannot fail at once (Go's t.Errorf in a fake): the test fails when
/// it is dropped with any.
#[derive(Clone, Default)]
pub struct Errs(pub Arc<Mutex<Vec<String>>>);

impl Errs {
    pub fn push(&self, e: impl Into<String>) {
        self.0.lock().push(e.into());
    }
}

impl Drop for Errs {
    fn drop(&mut self) {
        if Arc::strong_count(&self.0) == 1 && !std::thread::panicking() {
            let errs = self.0.lock();
            assert!(errs.is_empty(), "{errs:#?}");
        }
    }
}

/// scripted answers by prompt content, like a deterministic LLM. The matched substrings come from the prompt
/// templates in methodologies/examples/impact-analysis.yaml.
pub fn scripted(errs: &Errs) -> Arc<dyn llm::Client> {
    let errs = errs.clone();
    llm::ClientFunc::shared(move |_ctx: Ctx, req: llm::Request| {
        let errs = errs.clone();
        async move {
            let p = req.messages[0].content.clone();
            let out = if p.contains("DIRECTLY impacted") {
                if !p.contains("REQ-1 (alm@Requirement)") {
                    errs.push(format!("prompt misses baseline nodes:\n{p}"));
                }
                r#"Here: {"items":[{"type":"changeImpact","changeImpact":{"op":"declare","intent":"modified","key":"REQ-1","rationale":"PSP API change"}}]}"#
            } else if p.contains("new version") {
                r#"{"items":[{"type":"changeImpact","changeImpact":{"op":"write","node":"REQ-1","props":{"title":"Use PSP v2"}}}]}"#
            } else if p.contains("test case") {
                r##"{"items":[{"type":"changeImpact","changeImpact":{"op":"declare","ref":"#t1","intent":"created","type":"TestCase","key":"TST-9","rationale":"cover REQ-1"}},
			{"type":"changeImpact","changeImpact":{"op":"write","node":"#t1","props":{"title":"PSP v2 test"},"links":[{"type":"verifies","to":"REQ-1"}]}}]}"##
            } else if p.contains("report") {
                r##"{"items":[{"type":"artifact","target":"report","data":{"markdown":"# Impact"}}]}"##
            } else {
                errs.push(format!("unexpected prompt {p}"));
                ""
            };
            Ok(llm::Response {
                text: out.to_string(),
                provider: "test".into(),
                model: req.model.clone(),
                ..Default::default()
            })
        }
    })
}

/// text_client answers every prompt with text.
pub fn text_client(text: &str) -> Arc<dyn llm::Client> {
    let text = text.to_string();
    llm::ClientFunc::shared(move |_ctx: Ctx, req: llm::Request| {
        let text = text.clone();
        async move {
            Ok(llm::Response {
                text,
                provider: "test".into(),
                model: req.model.clone(),
                ..Default::default()
            })
        }
    })
}

/// MainGoals is the registry's part of ChangeLifecycles the engine tests need: the default goal of a change (ADR
/// 0096).
pub struct MainGoals(pub Arc<Compiled>);

#[async_trait]
impl ChangeLifecycles for MainGoals {
    async fn methodology_version(&self, _: &Ctx, r: &str) -> goap_graph::Result<String> {
        let (name, version) = domain::split_methodology_ref(r);
        if name == self.0.definition().name {
            if !version.is_empty() {
                return Ok(version.to_string());
            }
            return Ok(self.0.definition().version.clone()); // the one published version (ADR 0113)
        }
        Ok(String::new())
    }
    async fn lifecycle(&self, _: &Ctx, _: &str) -> goap_graph::Result<Option<Lifecycle>> {
        Ok(None)
    }
    async fn default_goal(&self, _: &Ctx, r: &str) -> goap_graph::Result<String> {
        let (name, _) = domain::split_methodology_ref(r);
        if name == self.0.definition().name {
            return Ok(self.0.definition().main_goal());
        }
        Ok(String::new())
    }
    async fn guard(
        &self,
        _: &Ctx,
        _: &Blackboard,
        _: &str,
        _: &str,
        _: &str,
    ) -> goap_graph::Result<bool> {
        Ok(true)
    }
    async fn gate(
        &self,
        _: &Ctx,
        _: &Blackboard,
        _: &Transition,
        _: &str,
    ) -> goap_graph::Result<GateResult> {
        Ok(GateResult::default())
    }
    async fn merge(&self, _: &Ctx, _: &Change, _: &Blackboard) -> goap_graph::Result<(bool, bool)> {
        Ok((false, false))
    }
    async fn guardians(&self, _: &Ctx, _: &str, _: &str) -> goap_graph::Result<Vec<GuardianRef>> {
        Ok(vec![])
    }
    async fn integrates(&self, _: &Ctx, _: &str, _: &str) -> goap_graph::Result<bool> {
        Ok(false)
    }
    async fn check_guardians(
        &self,
        _: &Ctx,
        _: &Blackboard,
        _: &str,
        _: &[Change],
    ) -> goap_graph::Result<String> {
        Ok(String::new())
    }
}

/// must_casbin is the default authorizer.
pub fn must_casbin() -> Arc<dyn authz::Authorizer> {
    Arc::new(authz::Casbin::new().unwrap())
}

pub fn principal(subject: &str, org: &str, roles: &[&str]) -> Principal {
    Principal {
        subject: subject.into(),
        org: org.into(),
        roles: roles.iter().map(|r| r.to_string()).collect(),
        ..Default::default()
    }
}

pub fn contributor() -> Principal {
    principal("carol", "acme", &["contributor"])
}
pub fn approver() -> Principal {
    principal("alice", "acme", &["approver"])
}
pub fn admin() -> Principal {
    principal("root", "acme", &["admin"])
}

/// load_methodology compiles a methodology of methodologies/ (else methodologies/examples/).
pub fn load_methodology(file: &str) -> Arc<Compiled> {
    let mut p = path(&format!("methodologies/{file}"));
    if std::fs::metadata(&p).is_err() {
        // the examples are not seeded at start: they live in methodologies/examples
        p = path(&format!("methodologies/examples/{file}"));
    }
    let m = methodology::load_file(&p).unwrap();
    Arc::new(m.compile().unwrap())
}

/// parse compiles a methodology from its YAML.
pub fn compile_yaml(src: &str) -> Arc<Compiled> {
    let m = methodology::parse(src.as_bytes()).unwrap();
    Arc::new(m.compile().unwrap())
}

/// Setup is what setup builds: an engine not shared yet (its fields may still be set), the graph, the head of alm.
pub struct Setup {
    pub e: Engine,
    pub g: Arc<Graph>,
    pub base: BaselineID,
    pub ms: StaticMethodologies,
    pub store: Arc<MemoryStore>,
    pub builtins: BuiltinExecutor,
    pub errs: Errs,
}

impl Setup {
    /// add serves one more methodology.
    pub fn add(&mut self, m: Arc<Compiled>) {
        self.ms.insert(m.name().to_string(), m);
        self.e.methodologies = Arc::new(self.ms.clone());
    }

    /// builtin registers one more builtin (Go set it in the table of the engine).
    pub fn builtin(&mut self, name: &str, f: BuiltinFunc) {
        self.builtins.0.insert(name.to_string(), f);
        self.e.executors.insert(
            methodology::KIND_BUILTIN.into(),
            Arc::new(self.builtins.clone()),
        );
    }

    /// share is the engine, shared.
    pub fn share(self) -> (Arc<Engine>, Arc<Graph>, BaselineID, Errs) {
        (Arc::new(self.e), self.g, self.base, self.errs)
    }

    /// llm replaces the client of the llm executor.
    pub fn llm(&mut self, c: Arc<dyn llm::Client>) {
        self.e.executors.insert(
            methodology::KIND_LLM.into(),
            Arc::new(LLMExecutor { client: Some(c) }),
        );
    }
}

pub fn node(key: &str, typ: &str, p: Map<String, Value>, links: Vec<LinkWrite>) -> graphtest::Node {
    graphtest::Node {
        namespace: "alm".into(),
        key: key.into(),
        r#type: typ.into(),
        properties: p,
        links,
        ..Default::default()
    }
}

pub fn link_to(typ: &str, to: &domain::Node) -> LinkWrite {
    LinkWrite {
        r#type: typ.into(),
        to: to.r#ref(),
        ..Default::default()
    }
}

/// new_graph is a memory graph whose changes follow the main goal of m, with the test project.
pub async fn new_graph(m: &Arc<Compiled>) -> Arc<Graph> {
    new_graph_with(m, |_| {}).await
}

/// new_graph_with is new_graph, the graph configured by conf before it is shared.
pub async fn new_graph_with(m: &Arc<Compiled>, conf: impl FnOnce(&mut Graph)) -> Arc<Graph> {
    let c = ctx();
    let mut g = Graph::new(Arc::new(Memory::new()));
    g.lifecycles = Some(Arc::new(MainGoals(m.clone())));
    conf(&mut g);
    let g = Arc::new(g);
    // a project every test starts its (non-administrative) processes in (ADR 0039)
    graphtest::project(&c, &g, TEST_PROJECT, "Test")
        .await
        .unwrap();
    g
}

/// engine_over is an engine over g serving ms, with the executors of setup.
pub fn engine_over(
    g: &Arc<Graph>,
    ms: &StaticMethodologies,
    store: &Arc<MemoryStore>,
    client: Arc<dyn llm::Client>,
) -> Engine {
    let st: Arc<dyn Store> = store.clone();
    let mp: Arc<dyn MethodologyPort> = Arc::new(ms.clone());
    let mut e = Engine::new(g.clone(), mp, st);
    e.executors.insert(
        methodology::KIND_LLM.into(),
        Arc::new(LLMExecutor {
            client: Some(client),
        }),
    );
    e.executors
        .insert(methodology::KIND_HUMAN.into(), Arc::new(HumanExecutor));
    e.executors.insert(
        methodology::KIND_BUILTIN.into(),
        Arc::new(default_builtins()),
    );
    e.intent = Some(goap_intent::Resolver::new(Arc::new(goap_intent::Lexical)));
    e.scope = Some(Arc::new(AuthzScope {
        authz: Some(must_casbin()),
        hub: None,
    }));
    e
}

/// setup is the impact-analysis setup: the alm graph NEED-1, REQ-1, TST-1, CMP-1 and an engine over it.
pub async fn setup() -> Setup {
    setup_with(|_| {}).await
}

/// setup_with is setup, the graph configured by conf before it is shared (Go set the graph's seams after setup).
pub async fn setup_with(conf: impl FnOnce(&mut Graph)) -> Setup {
    let c = ctx();
    let cm = load_methodology("impact-analysis.yaml");
    let g = new_graph_with(&cm, conf).await;
    // the alm namespace the example methodologies act on (ADR 0013)
    let need = graphtest::import(
        &c,
        &g,
        node(
            "NEED-1",
            "alm@Need",
            obj(serde_json::json!({"title": "Pay online"})),
            vec![],
        ),
    )
    .await
    .unwrap();
    let req = graphtest::import(
        &c,
        &g,
        node(
            "REQ-1",
            "alm@Requirement",
            obj(serde_json::json!({"title": "Use PSP v1"})),
            vec![link_to("alm@satisfies", &need)],
        ),
    )
    .await
    .unwrap();
    graphtest::import(
        &c,
        &g,
        node(
            "TST-1",
            "alm@TestCase",
            Map::new(),
            vec![link_to("alm@verifies", &req)],
        ),
    )
    .await
    .unwrap();
    graphtest::import(
        &c,
        &g,
        node(
            "CMP-1",
            "alm@Component",
            Map::new(),
            vec![link_to("alm@implements", &req)],
        ),
    )
    .await
    .unwrap();
    let b = g.branch_head(&c, "alm", domain::MAIN_BRANCH).await.unwrap();
    let errs = Errs::default();
    let ms = StaticMethodologies::new().with(cm);
    let store = Arc::new(MemoryStore::new());
    let e = engine_over(&g, &ms, &store, scripted(&errs));
    Setup {
        e,
        g,
        base: b.id,
        ms,
        store,
        builtins: default_builtins(),
        errs,
    }
}

/// review_all accepts every change impact of the change that awaits a decision, as a human would.
pub async fn review_all(g: &Graph, id: &ChangeID) -> Vec<ItemInput> {
    let c = g.change(&ctx(), id).await.unwrap();
    let mut out = Vec::new();
    for n in &c.nodes {
        if n.review == domain::REVIEW_PROPOSED && n.items.is_empty() {
            // a candidate the run did not modify is not accepted (ADR 0102): the reviewer takes it out of the change
            let op = if n.post.is_none() {
                NodeOp {
                    op: "remove".into(),
                    node: n.key.clone(),
                    ..Default::default()
                }
            } else {
                NodeOp {
                    op: "review".into(),
                    node: n.key.clone(),
                    accept: true,
                    comment: "reviewed".into(),
                    ..Default::default()
                }
            };
            out.push(ItemInput {
                r#type: "changeImpact".into(),
                change_impact: Some(op),
                ..Default::default()
            });
        }
    }
    assert!(!out.is_empty(), "no change impact to review: {:?}", c.nodes);
    out
}

/// read_roles reads the methodology with roles the engine's own tests use too.
pub fn read_roles() -> String {
    std::fs::read_to_string(path("engine/engine/testdata/roles.yaml")).unwrap()
}

/// scope_engine is a bare engine over no graph with the authorizer a.
pub fn scope_engine(a: Arc<dyn authz::Authorizer>, ms: StaticMethodologies) -> Engine {
    let g = Arc::new(Graph::new(Arc::new(Memory::new())));
    let mut e = Engine::new(g, Arc::new(ms), Arc::new(MemoryStore::new()));
    e.scope = Some(Arc::new(AuthzScope {
        authz: Some(a),
        hub: None,
    }));
    e
}

/// item is a change impact operation of a submission.
pub fn impact_item(op: NodeOp) -> ItemInput {
    ItemInput {
        r#type: "changeImpact".into(),
        change_impact: Some(op),
        ..Default::default()
    }
}

/// artifact is an artifact item of a submission.
pub fn artifact(target: &str, data: Value) -> ItemInput {
    ItemInput {
        r#type: "artifact".into(),
        target: target.into(),
        data: obj(data),
        ..Default::default()
    }
}

/// names gives the actions of the steps of a process, comma separated.
pub fn actions_of(p: &goap_engine::Process) -> String {
    p.steps
        .iter()
        .map(|s| s.action.clone())
        .collect::<Vec<_>>()
        .join(",")
}

/// add_yaml compiles a methodology from its YAML and serves it.
pub fn add_yaml(s: &mut Setup, src: &str) -> Arc<Compiled> {
    let c = compile_yaml(src);
    s.add(c.clone());
    c
}

/// get reads a process from the store.
pub async fn get(e: &Engine, id: &str) -> goap_engine::Process {
    e.store.get(&ctx(), id).await.unwrap()
}

/// schedule runs a process the way the engine schedules it, then waits for the background work.
pub async fn schedule(e: &Arc<Engine>, id: &str) {
    Engine::internals_of(e).schedule(id);
    e.drain().await;
}

/// NopHost is a script host that does nothing.
pub struct NopHost;

#[async_trait]
impl goap_dsl::Host for NopHost {
    async fn complete(
        &self,
        _: &Ctx,
        _: goap_dsl::CompleteRequest,
    ) -> Result<goap_dsl::CompleteResult, goap_dsl::Error> {
        Ok(Default::default())
    }
    async fn run_agent(
        &self,
        _: &Ctx,
        _: &str,
        _: &str,
    ) -> Result<goap_dsl::AgentResult, goap_dsl::Error> {
        Ok(Default::default())
    }
    async fn call_tool(
        &self,
        _: &Ctx,
        _: &str,
        _: Map<String, Value>,
    ) -> Result<Value, goap_dsl::Error> {
        Ok(Value::Null)
    }
    async fn node(&self, _: &Ctx, _: &str) -> Result<goap_dsl::Node, goap_dsl::Error> {
        Ok(Default::default())
    }
    async fn nodes(&self, _: &Ctx, _: &str) -> Result<Vec<goap_dsl::Node>, goap_dsl::Error> {
        Ok(vec![])
    }
    async fn links(
        &self,
        _: &Ctx,
        _: &str,
        _: &str,
        _: &str,
    ) -> Result<Vec<goap_dsl::Link>, goap_dsl::Error> {
        Ok(vec![])
    }
}

/// run_script runs a JavaScript job on NopHost.
pub async fn run_script(c: &Ctx, job: goap_dsl::Job) -> goap_dsl::ScriptResult {
    let (res, err) = goap_dsl::run(c, job, &NopHost).await;
    err.unwrap();
    res
}

/// js is a JavaScript job of an action.
pub fn js(action: &str, code: &str) -> goap_dsl::Job {
    goap_dsl::Job {
        language: "javascript".into(),
        action: action.into(),
        code: code.into(),
        ..Default::default()
    }
}
