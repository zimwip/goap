//! The tests of the crate (the Go *_test.go files), with fakes of the model, the graph, the engine and the
//! organisation, over a real conversation service on its memory store.

mod agents;
mod compact;
mod frame;
mod methodquery;
mod relate;
mod service;
mod toolspecs;
mod trace;
mod uitools;

use std::collections::HashMap;
use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};

use async_trait::async_trait;
use goap_access::personal_unit;
use goap_authz::{Principal, principal_from, with_principal};
use goap_convsvc as convsvc;
use goap_ctx::Ctx;
use goap_domain::{CHANGE_ACTIVE, CHANGE_APPLIED, CHANGE_DRAFT, Change, ChangeID};
use goap_graphapi::{ChangesFilter, NewChange};
use goap_llm as llm;
use goap_methodology::{Compiled, Goal, Methodology};
use goap_modelgw::{AliasEntry, ModelEntry};
use parking_lot::Mutex;
use serde_json::Value;

use crate::context::{App, Context, Tab};
use crate::engine::{AgentChecks, Engine, Point, Points, ProcessInfo, StartProcess};
use crate::searchtool::ACTION_SEARCH_CHANGES;
use crate::service::{
    ALIAS, BoxError, Graph, Methodologies, PRINCIPAL, Projects, Runner, SendInput, Service,
    TurnFuture,
};
use crate::uitools::UITool;

pub(crate) type BoxFut = Pin<Box<dyn Future<Output = ()> + Send>>;
pub(crate) type OnCall = Arc<dyn Fn(usize) -> BoxFut + Send + Sync>;

// ---- fakes ----------------------------------------------------------------------------------------------------

#[derive(Default)]
pub(crate) struct ModelState {
    pub(crate) answers: Vec<String>,
    /// when set, answered instead of answers (native tool calls, ADR 0098)
    pub(crate) responses: Vec<llm::Response>,
    pub(crate) err: Option<fn() -> goap_modelgw::Error>,
    pub(crate) aliases: Vec<AliasEntry>,
    pub(crate) got: Vec<llm::Request>,
    pub(crate) who: Vec<Principal>,
    pub(crate) metas: Vec<llm::CallMeta>,
    /// when set, runs before the n-th call (1-based) is answered
    pub(crate) on_call: Option<OnCall>,
}

#[derive(Default)]
pub(crate) struct FakeModel(pub(crate) Mutex<ModelState>);

impl FakeModel {
    pub(crate) fn got(&self) -> Vec<llm::Request> {
        self.0.lock().got.clone()
    }
}

#[async_trait]
impl goap_modelgw::SuggestModel for FakeModel {
    async fn complete(
        &self,
        ctx: &Ctx,
        req: llm::Request,
    ) -> Result<llm::Response, goap_modelgw::Error> {
        let (n, on_call) = {
            let mut m = self.0.lock();
            m.got.push(req);
            m.metas.push(llm::meta_from(ctx));
            m.who.push(principal_from(ctx));
            (m.got.len(), m.on_call.clone())
        };
        if let Some(f) = on_call {
            f(n).await;
        }
        let m = self.0.lock();
        if let Some(e) = m.err {
            return Err(e());
        }
        let i = n - 1;
        if !m.responses.is_empty() {
            return Ok(m.responses[i.min(m.responses.len() - 1)].clone());
        }
        let i = i.min(m.answers.len().saturating_sub(1));
        Ok(llm::Response {
            text: m.answers.get(i).cloned().unwrap_or_default(),
            ..Default::default()
        })
    }

    async fn available(
        &self,
        _ctx: &Ctx,
    ) -> Result<(Vec<ModelEntry>, Vec<AliasEntry>), goap_modelgw::Error> {
        Ok((Vec::new(), self.0.lock().aliases.clone()))
    }
}

#[derive(Default)]
pub(crate) struct GraphState {
    pub(crate) created: Vec<NewChange>,
    pub(crate) who: Vec<Principal>,
    pub(crate) changes: HashMap<String, Change>,
    pub(crate) err: Option<String>,
}

#[derive(Default)]
pub(crate) struct FakeGraph(pub(crate) Mutex<GraphState>);

impl FakeGraph {
    pub(crate) fn created(&self) -> Vec<NewChange> {
        self.0.lock().created.clone()
    }
    pub(crate) fn change(&self, id: &str) -> Change {
        self.0.lock().changes.get(id).cloned().unwrap_or_default()
    }
}

#[async_trait]
impl Graph for FakeGraph {
    async fn create_change(
        &self,
        ctx: &Ctx,
        in_: NewChange,
    ) -> Result<Change, goap_graphapi::Error> {
        let mut g = self.0.lock();
        if let Some(e) = &g.err {
            return Err(goap_graphapi::Error::msg(e.clone()));
        }
        let c = Change {
            id: ChangeID::from("CHG-NEW"),
            title: in_.title.clone(),
            methodology: in_.methodology.clone(),
            project_id: in_.project_id.clone(),
            ..Default::default()
        };
        g.created.push(in_);
        g.who.push(principal_from(ctx));
        Ok(c)
    }

    async fn change(&self, _ctx: &Ctx, id: &ChangeID) -> Result<Change, goap_graphapi::Error> {
        self.0
            .lock()
            .changes
            .get(id.as_str())
            .cloned()
            .ok_or_else(|| goap_graphapi::Error::not_found("not found"))
    }

    async fn list_changes(
        &self,
        _ctx: &Ctx,
        f: ChangesFilter,
    ) -> Result<Vec<Change>, goap_graphapi::Error> {
        Ok(self
            .0
            .lock()
            .changes
            .values()
            .filter(|c| f.matches(c))
            .cloned()
            .collect())
    }

    async fn attach_change(
        &self,
        _ctx: &Ctx,
        id: &ChangeID,
        parent: &ChangeID,
    ) -> Result<Change, goap_graphapi::Error> {
        let mut g = self.0.lock();
        if let Some(e) = &g.err {
            return Err(goap_graphapi::Error::msg(e.clone()));
        }
        let c = g.changes.entry(id.as_str().to_string()).or_default();
        c.parent_id = parent.clone();
        Ok(c.clone())
    }

    async fn detach_change(
        &self,
        _ctx: &Ctx,
        id: &ChangeID,
    ) -> Result<Change, goap_graphapi::Error> {
        let mut g = self.0.lock();
        if let Some(e) = &g.err {
            return Err(goap_graphapi::Error::msg(e.clone()));
        }
        let c = g.changes.entry(id.as_str().to_string()).or_default();
        c.parent_id = ChangeID::from("");
        Ok(c.clone())
    }
}

/// fakeProjects: projects maps a project to its own methodologies; access maps a subject to the projects it may work
/// on; subs, when set, gives the projects below a project (Go's subProjectsOf).
#[derive(Default)]
pub(crate) struct ProjectsState {
    pub(crate) projects: HashMap<String, Vec<String>>,
    pub(crate) access: HashMap<String, Vec<String>>,
    pub(crate) subs: Option<HashMap<String, Vec<String>>>,
}

#[derive(Default)]
pub(crate) struct FakeProjects(pub(crate) Mutex<ProjectsState>);

#[async_trait]
impl Projects for FakeProjects {
    async fn root_project(&self, _ctx: &Ctx) -> Result<String, BoxError> {
        Ok("PROJ-ROOT".into())
    }
    async fn has_project(&self, _ctx: &Ctx, p: &str) -> Result<bool, BoxError> {
        Ok(self.0.lock().projects.contains_key(p))
    }
    async fn may_access_project(
        &self,
        _ctx: &Ctx,
        who: &Principal,
        p: &str,
    ) -> Result<bool, BoxError> {
        Ok(self
            .0
            .lock()
            .access
            .get(&who.subject)
            .is_some_and(|a| a.iter().any(|x| x == p)))
    }
    async fn applicable_methodologies(&self, _ctx: &Ctx, p: &str) -> Result<Vec<String>, BoxError> {
        Ok(self.0.lock().projects.get(p).cloned().unwrap_or_default())
    }
    async fn sub_projects(&self, _ctx: &Ctx, p: &str) -> Option<Result<Vec<String>, BoxError>> {
        let s = self.0.lock();
        let subs = s.subs.as_ref()?;
        let mut out = vec![p.to_string()];
        out.extend(subs.get(p).cloned().unwrap_or_default());
        Some(Ok(out))
    }
}

#[derive(Default, Clone)]
pub(crate) struct FakeMethodologies(pub(crate) HashMap<String, Arc<Compiled>>);

#[async_trait]
impl Methodologies for FakeMethodologies {
    async fn methodology(&self, _ctx: &Ctx, n: &str) -> Result<Arc<Compiled>, BoxError> {
        self.0.get(n).cloned().ok_or_else(|| "not found".into())
    }
}

/// fakeEngine decides the roles from the agents of the methodologies and the roles `held` gives each subject.
#[derive(Default)]
pub(crate) struct EngineState {
    pub(crate) held: HashMap<String, Vec<String>>,
    pub(crate) active: Vec<ProcessInfo>,
    pub(crate) starts: Vec<StartProcess>,
    pub(crate) start_who: Vec<Principal>,
    pub(crate) start_err: Option<String>,
    pub(crate) points: HashMap<String, Points>,
}

#[derive(Default)]
pub(crate) struct FakeEngine {
    pub(crate) state: Mutex<EngineState>,
    pub(crate) methodologies: FakeMethodologies,
    pub(crate) checks: AtomicUsize,
    pub(crate) point_calls: AtomicUsize,
}

impl FakeEngine {
    pub(crate) fn starts(&self) -> Vec<StartProcess> {
        self.state.lock().starts.clone()
    }
}

#[async_trait]
impl Engine for FakeEngine {
    async fn starting_points(&self, _ctx: &Ctx, change_id: &str) -> Result<Points, BoxError> {
        self.point_calls.fetch_add(1, Ordering::SeqCst);
        Ok(self
            .state
            .lock()
            .points
            .get(change_id)
            .cloned()
            .unwrap_or_else(|| Points {
                reason: "no step of the methodology works towards the goal".into(),
                ..Default::default()
            }))
    }

    async fn check_agents(
        &self,
        ctx: &Ctx,
        name: &str,
        _project: &str,
        _agents: &[String],
    ) -> Result<AgentChecks, BoxError> {
        self.checks.fetch_add(1, Ordering::SeqCst);
        let mut out = AgentChecks {
            may_start: true,
            may_run: HashMap::new(),
        };
        let who = principal_from(ctx);
        let held = self
            .state
            .lock()
            .held
            .get(&who.subject)
            .cloned()
            .unwrap_or_default();
        if let Some(m) = self.methodologies.0.get(name) {
            for ag in m.agent_list() {
                let ok = ag.roles.is_empty() || ag.roles.iter().any(|r| held.contains(r));
                out.may_run.insert(ag.name.clone(), ok);
            }
        }
        Ok(out)
    }

    async fn start_process(&self, ctx: &Ctx, in_: StartProcess) -> Result<ProcessInfo, BoxError> {
        let mut s = self.state.lock();
        if let Some(e) = &s.start_err {
            return Err(e.clone().into());
        }
        let change = if in_.change_id.is_empty() {
            "CHG-AUTO".to_string()
        } else {
            in_.change_id.clone()
        };
        let out = ProcessInfo {
            id: "PRC-1".into(),
            methodology: in_.methodology.clone(),
            agent: in_.agent.clone(),
            change_id: change,
            status: "running".into(),
        };
        s.starts.push(in_);
        s.start_who.push(principal_from(ctx));
        Ok(out)
    }

    async fn active(&self, _ctx: &Ctx) -> Result<Vec<ProcessInfo>, BoxError> {
        Ok(self.state.lock().active.clone())
    }
}

// ---- fixtures -------------------------------------------------------------------------------------------------

pub(crate) const DELIVERY_YAML: &str = r#"
name: delivery
version: 1.0.0
namespace: alm
description: Deliver software
roles:
  - {name: developer}
  - {name: release_manager}
conditions:
  - {name: built, expr: 'artifacts.exists(a, a.target == "build")'}
  - {name: released, expr: 'artifacts.exists(a, a.target == "release")'}
actions:
  - {name: build, kind: builtin, builtin: test.emit, effects: {built: true}, params: {type: artifact, target: build}}
  - {name: release, kind: builtin, builtin: test.emit, pre: {built: true}, effects: {released: true}, params: {type: artifact, target: release}}
goals:
  - {name: built, description: The build exists, pre: {built: true}}
  - {name: ship, description: Released, pre: {released: true}}
agents:
  - {name: builder, description: Builds the software, examples: ["build it"], roles: [developer], actions: [build], goals: [built]}
  - {name: shipper, description: Ships the release, roles: [release_manager], actions: [build, release], goals: [built, ship]}
  - {name: helper, description: Anyone may run it, actions: [build], goals: [built]}
"#;

pub(crate) const OTHER_YAML: &str = r#"
name: other
version: 1.0.0
namespace: alm
conditions:
  - {name: built, expr: 'artifacts.exists(a, a.target == "build")'}
actions:
  - {name: build, kind: builtin, builtin: test.emit, effects: {built: true}, params: {type: artifact, target: build}}
goals:
  - {name: built, pre: {built: true}}
agents:
  - {name: stranger, actions: [build], goals: [built]}
"#;

pub(crate) fn compiled(src: &str) -> Compiled {
    let m = Methodology::parse(src.as_bytes()).expect("parse");
    m.compile().expect("compile")
}

pub(crate) fn defined(m: Methodology) -> Arc<Compiled> {
    Arc::new(Compiled::default().with_definition(m))
}

// ---- environment ----------------------------------------------------------------------------------------------

pub(crate) struct Env {
    pub(crate) svc: Arc<Service>,
    pub(crate) model: Arc<FakeModel>,
    pub(crate) graph: Arc<FakeGraph>,
    pub(crate) engine: Arc<FakeEngine>,
    pub(crate) projects: Arc<FakeProjects>,
    pub(crate) convs: Arc<convsvc::Service>,
    pub(crate) user: Principal,
    pub(crate) conv: convsvc::Conversation,
    pub(crate) ctx: Ctx,
    /// turns not run yet, when deferred
    pub(crate) later: Arc<Mutex<Vec<TurnFuture>>>,
    /// searched was called (propose seeds it once)
    pub(crate) did_search: bool,
}

pub(crate) fn sv(a: &str) -> String {
    a.to_string()
}

fn change(id: &str, title: &str) -> Change {
    Change {
        id: ChangeID::from(id),
        title: title.into(),
        ..Default::default()
    }
}

pub(crate) async fn new_env(answers: &[&str]) -> Env {
    let model = Arc::new(FakeModel::default());
    {
        let mut m = model.0.lock();
        m.answers = answers.iter().map(|s| s.to_string()).collect();
        m.aliases = vec![AliasEntry {
            alias: ALIAS.into(),
            target: "fake/echo".into(),
            ..Default::default()
        }];
    }
    let graph = Arc::new(FakeGraph::default());
    {
        let mut g = graph.0.lock();
        let mut c1 = change("CHG-1", "Existing");
        c1.project_id = "PROJ-A".into();
        c1.status = CHANGE_DRAFT;
        let mut mine = change("CHG-MINE", "Mine");
        mine.owner_org = personal_unit("u1");
        let mut hers = change("CHG-HERS", "Hers");
        hers.owner_org = personal_unit("u2");
        let mut d = change("CHG-D", "Delivery");
        d.methodology = "delivery".into();
        d.namespace = "alm".into();
        d.project_id = "PROJ-A".into();
        d.status = CHANGE_ACTIVE;
        d.state = "design".into();
        let mut done = change("CHG-DONE", "Done");
        done.methodology = "delivery".into();
        done.namespace = "alm".into();
        done.project_id = "PROJ-A".into();
        done.status = CHANGE_APPLIED;
        for c in [c1, mine, hers, d, done] {
            g.changes.insert(c.id.as_str().to_string(), c);
        }
    }
    let convs = Arc::new(convsvc::Service::new(Arc::new(convsvc::MemoryStore::new())));
    let user = Principal {
        subject: "u1".into(),
        project: "PROJ-A".into(),
        ..Default::default()
    };
    let ctx = with_principal(&Ctx::background(), user.clone());
    let mut ms = HashMap::new();
    ms.insert(
        sv("sdlc"),
        defined(Methodology {
            name: "sdlc".into(),
            namespace: "alm".into(),
            description: "Software lifecycle".into(),
            goals: vec![Goal {
                name: "release".into(),
                examples: vec!["ship v2".into()],
                ..Default::default()
            }],
            ..Default::default()
        }),
    );
    ms.insert(
        sv("risk"),
        defined(Methodology {
            name: "risk".into(),
            namespace: "alm".into(),
            ..Default::default()
        }),
    );
    ms.insert(sv("delivery"), Arc::new(compiled(DELIVERY_YAML)));
    ms.insert(sv("other"), Arc::new(compiled(OTHER_YAML)));
    let methodologies = FakeMethodologies(ms);
    let projects = Arc::new(FakeProjects::default());
    {
        let mut p = projects.0.lock();
        p.projects
            .insert(sv("PROJ-A"), vec![sv("sdlc"), sv("delivery")]);
        p.projects
            .insert(sv("PROJ-B"), vec![sv("sdlc"), sv("risk"), sv("other")]);
        p.projects.insert(sv("PROJ-SECRET"), Vec::new());
        p.access.insert(sv("u1"), vec![sv("PROJ-A"), sv("PROJ-B")]);
    }
    let engine = Arc::new(FakeEngine {
        methodologies: methodologies.clone(),
        ..Default::default()
    });
    {
        let mut s = engine.state.lock();
        s.held.insert(sv("u1"), vec![sv("developer")]);
        s.points.insert(
            sv("CHG-D"),
            Points {
                goal: "ship".into(),
                blocked_count: 2,
                blocked: vec!["delivery/design: noted".into()],
                points: vec![
                    Point {
                        id: "delivery/build".into(),
                        kind: "step".into(),
                        name: "build".into(),
                        description: "Build it".into(),
                        why: vec!["noted".into()],
                        produces: vec!["built".into()],
                        agent: "builder".into(),
                        agent_goal: "delivery/build".into(),
                        may_run: true,
                        ..Default::default()
                    },
                    Point {
                        id: "delivery/running".into(),
                        kind: "step".into(),
                        name: "running".into(),
                        agent: "builder".into(),
                        agent_goal: "delivery/running".into(),
                        may_run: true,
                        running: true,
                        ..Default::default()
                    },
                    Point {
                        id: "delivery/restricted".into(),
                        kind: "step".into(),
                        name: "restricted".into(),
                        responsible: "release_manager".into(),
                        agent: "shipper".into(),
                        agent_goal: "delivery/restricted".into(),
                        ..Default::default()
                    },
                ],
                ..Default::default()
            },
        );
    }
    let mut svc = Service::new(
        convs.clone(),
        model.clone(),
        graph.clone(),
        Arc::new(methodologies),
        projects.clone(),
    );
    svc.go = Runner::Inline;
    svc.engine = Some(engine.clone());
    let conv = convs.create(&ctx, &user, "t").await.expect("create");
    Env {
        svc: Arc::new(svc),
        model,
        graph,
        engine,
        projects,
        convs,
        user,
        conv,
        ctx,
        later: Arc::new(Mutex::new(Vec::new())),
        did_search: false,
    }
}

impl Env {
    /// The service, to change one of its ports (no turn holds it).
    pub(crate) fn svc_mut(&mut self) -> &mut Service {
        Arc::get_mut(&mut self.svc).expect("the service is shared")
    }

    /// The caller becomes p.
    pub(crate) fn set_user(&mut self, p: Principal) {
        self.user = p;
        self.ctx = with_principal(&Ctx::background(), self.user.clone());
    }

    pub(crate) fn set_answers(&self, answers: &[&str]) {
        let mut m = self.model.0.lock();
        m.answers = answers.iter().map(|s| s.to_string()).collect();
        m.got.clear();
    }

    pub(crate) fn got(&self) -> Vec<llm::Request> {
        self.model.got()
    }

    pub(crate) async fn send(
        &self,
        text: &str,
        c: Context,
    ) -> Result<(convsvc::Message, convsvc::Message), crate::Error> {
        self.send_tools_full(text, c, Vec::new()).await
    }

    pub(crate) async fn send_tools_full(
        &self,
        text: &str,
        c: Context,
        tools: Vec<UITool>,
    ) -> Result<(convsvc::Message, convsvc::Message), crate::Error> {
        self.svc
            .send(
                &self.ctx,
                SendInput {
                    conversation_id: self.conv.id.clone(),
                    text: text.into(),
                    context: c,
                    ui_tools: tools,
                },
            )
            .await
    }

    pub(crate) async fn send_tools(
        &self,
        text: &str,
        c: Context,
        tools: Vec<UITool>,
    ) -> Result<(), crate::Error> {
        self.send_tools_full(text, c, tools).await.map(|_| ())
    }

    pub(crate) async fn messages(&self) -> Vec<convsvc::Message> {
        self.convs
            .get(&self.ctx, &self.user, &self.conv.id)
            .await
            .expect("get")
            .1
    }

    /// The assistant message of the last turn.
    pub(crate) async fn answer(&self) -> convsvc::Message {
        self.messages().await.pop().expect("a message")
    }

    /// Records an earlier turn that searched the open changes: creating a change is offered from then on.
    pub(crate) async fn searched(&self) {
        self.convs
            .append(
                &self.ctx,
                &self.user,
                &self.conv.id,
                convsvc::ROLE_USER,
                convsvc::Content {
                    text: "is there a change for this?".into(),
                    ..Default::default()
                },
            )
            .await
            .expect("append");
        let a: convsvc::Action = serde_json::from_value(serde_json::json!({
            "type": ACTION_SEARCH_CHANGES, "args": {"q": "this"}, "result": {"count": 0}}))
        .expect("action");
        self.convs
            .append(
                &with_principal(&self.ctx, PRINCIPAL.clone()),
                &PRINCIPAL,
                &self.conv.id,
                convsvc::ROLE_ASSISTANT,
                convsvc::Content {
                    text: "None fits.".into(),
                    status: convsvc::STATUS_DONE.into(),
                    actions: vec![a],
                    ..Default::default()
                },
            )
            .await
            .expect("append");
    }

    /// The result of the tool of the last round given back to the model.
    pub(crate) fn tool_result(&self, round: usize) -> String {
        let got = self.got();
        got[round]
            .messages
            .last()
            .expect("a message")
            .content
            .clone()
    }
}

pub(crate) fn in_project(p: &str) -> Context {
    Context {
        app: App {
            project: p.into(),
            ..Default::default()
        },
        ..Default::default()
    }
}

pub(crate) fn change_ctx(id: &str) -> Context {
    Context {
        app: App {
            project: "PROJ-A".into(),
            tab: Tab {
                kind: "change".into(),
                params: [(sv("id"), sv(id))].into_iter().collect(),
            },
        },
        ..Default::default()
    }
}

/// call is an answer of the model calling one tool, as JSON text.
pub(crate) fn call(name: &str, arguments: &str) -> String {
    format!(
        r#"{{"message":"","tool_calls":[{{"name":{},"arguments":{arguments}}}]}}"#,
        crate::util::q(name)
    )
}

/// The JSON array of the outcomes in a tool result text.
pub(crate) fn outcomes(result: &str) -> Vec<Value> {
    let i = result
        .find("[{")
        .unwrap_or_else(|| panic!("no outcomes in {result}"));
    serde_json::from_str(&result[i..]).unwrap_or_else(|e| panic!("{e} in {result}"))
}

/// The string at a key of an action ("" when absent).
pub(crate) fn s<'a>(a: &'a convsvc::Action, k: &str) -> &'a str {
    a.get(k).and_then(Value::as_str).unwrap_or("")
}

/// The object at a key of an action.
pub(crate) fn obj(a: &serde_json::Map<String, Value>, k: &str) -> serde_json::Map<String, Value> {
    crate::util::map_of(a, k)
}
