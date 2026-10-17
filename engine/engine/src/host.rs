//! The host of an action execution (Go's `host.go`): every call leaving a script or an llm action goes through it,
//! is attributed to the process / agent / action and recorded in the step. Script actions (sandboxes) live here too.

use std::collections::{BTreeMap, HashMap};
use std::sync::Arc;
use std::time::Instant;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{Link, Node, NodeRef};
use goap_llm::{CallMeta, Client, Message, Request, Response};
use goap_mcp::ToolInfo;
use goap_methodology::{Action, KIND_LLM, KIND_SCRIPT};
use serde_json::{Map, Value};
use tokio::sync::OnceCell;

use crate::engine::{Engine, conversation_of};
use crate::error::{Error, Result};
use crate::executors::{ActionContext, ActionResult, ExecResult, Executor};
use crate::nodeops::change_impacts_from_blackboard;
use crate::ports::read_graph;
use crate::process::{LLMCall, LogLine, Process, Step, ToolCall, exchange_of};
use crate::scoped::action_vars;

#[derive(Default)]
struct HostState {
    llm_calls: Vec<LLMCall>,
    tool_calls: Vec<ToolCall>,
    /// new sub-agent calls
    children: BTreeMap<String, String>,
    /// their processes, in call order
    spawned: Vec<String>,
    /// child process blocking the action
    waiting_on: String,
    calls: i64,
}

/// Host implements goap_dsl::Host for one action execution. It is safe for concurrent use.
pub struct Host {
    e: Arc<Engine>,
    /// The process, read-only while the action runs.
    pub(crate) process: Arc<Process>,
    action: String,
    kind: String,
    /// The MCPs the action may call (declared by the action or its agent); action_mcps the ones the action declares,
    /// agent_mcps the ones of its agent (ADR 0028).
    pub(crate) mcps: Vec<String>,
    action_mcps: Vec<String>,
    agent_mcps: Vec<String>,
    tools: OnceCell<std::result::Result<Vec<ToolInfo>, String>>,
    /// The step the action runs as and the number of model calls that step already holds (ADR 0089).
    pub(crate) step: i64,
    pub(crate) base: i64,
    state: parking_lot::Mutex<HostState>,
    #[allow(clippy::type_complexity)]
    graph: OnceCell<std::result::Result<(Vec<Node>, Vec<Link>), String>>,
}

impl Engine {
    /// new_host serves the DSL calls of an action. It may call the tools of the MCPs the action requires and, for llm
    /// and script actions, of the MCPs its agent declares.
    pub(crate) fn new_host(
        self: &Arc<Self>,
        p: Arc<Process>,
        action: &Action,
        agent_mcps: &[String],
    ) -> Host {
        let own = action.required_mcps();
        let mut mcps = own.clone();
        let mut agent = Vec::new();
        if action.kind == KIND_LLM || action.kind == KIND_SCRIPT {
            agent = agent_mcps.to_vec();
            for m in agent_mcps {
                if !mcps.contains(m) {
                    mcps.push(m.clone());
                }
            }
        }
        Host {
            e: self.clone(),
            process: p,
            action: action.name.clone(),
            kind: action.kind.clone(),
            mcps,
            action_mcps: own,
            agent_mcps: agent,
            tools: OnceCell::new(),
            step: 0,
            base: 0,
            state: parking_lot::Mutex::new(HostState::default()),
            graph: OnceCell::new(),
        }
    }
}

/// Attributes identify the caller of platform calls (used for tracing).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Attributes {
    pub process_id: String,
    pub methodology: String,
    pub agent: String,
    pub action: String,
}

/// with_attributes returns a context carrying the caller attributes.
pub fn with_attributes(ctx: &Ctx, a: Attributes) -> Ctx {
    ctx.with(a)
}

/// attributes_from returns the caller attributes of ctx.
pub fn attributes_from(ctx: &Ctx) -> Option<Attributes> {
    ctx.value::<Attributes>().cloned()
}

impl Host {
    /// The engine the host serves.
    pub fn engine(&self) -> Arc<Engine> {
        self.e.clone()
    }

    /// The child process blocking the action ("" when none).
    pub fn waiting_on(&self) -> String {
        self.state.lock().waiting_on.clone()
    }

    pub(crate) fn set_waiting_on(&self, id: &str) {
        self.state.lock().waiting_on = id.to_string();
    }

    /// The sub-agent calls this execution started (key -> process id).
    pub(crate) fn children(&self) -> BTreeMap<String, String> {
        self.state.lock().children.clone()
    }

    pub(crate) fn add_child(&self, key: &str, id: &str) {
        let mut st = self.state.lock();
        st.children.insert(key.to_string(), id.to_string());
        st.spawned.push(id.to_string());
    }

    /// permitted reports whether the action may call a tool, given the scope of its MCP (ADR 0028).
    fn permitted(&self, t: &ToolInfo) -> bool {
        let Ok((m, _)) = goap_mcp::split_tool(&t.name) else {
            return false;
        };
        if self.action_mcps.contains(&m) && goap_mcp::for_actions(&t.scope) {
            return true;
        }
        self.agent_mcps.contains(&m)
            && goap_mcp::for_agents(&t.scope)
            && (goap_mcp::scope_of(&t.scope) != goap_mcp::SCOPE_AGENT || self.kind == KIND_LLM)
    }

    /// unit_tools lists, once per action run, the tools the unit holding the change can call.
    async fn unit_tools(&self, ctx: &Ctx) -> Result<Vec<ToolInfo>> {
        let r = self
            .tools
            .get_or_init(|| async {
                self.e
                    .scope()
                    .tools(ctx, &self.e.r#ref(&self.process))
                    .await
                    .map(|(t, _)| t)
                    .map_err(|e| e.to_string())
            })
            .await;
        r.clone().map_err(Error::Msg)
    }

    fn ctx(&self, ctx: &Ctx) -> Ctx {
        with_attributes(
            ctx,
            Attributes {
                process_id: self.process.id.clone(),
                methodology: self.process.methodology.clone(),
                agent: self.process.agent.clone(),
                action: self.action.clone(),
            },
        )
    }

    /// complete_llm calls the model through client, numbering the call and recording it in the step.
    pub async fn complete_llm(
        &self,
        ctx: &Ctx,
        client: Option<&Arc<dyn Client>>,
        req: Request,
    ) -> Result<Response> {
        let Some(client) = client else {
            return Err(Error::msg("no model gateway configured"));
        };
        // the slot of the call is reserved before it starts: its position is its number in the step
        let slot = {
            let mut st = self.state.lock();
            st.llm_calls.push(LLMCall::default());
            st.llm_calls.len() - 1
        };
        let ctx = goap_llm::with_meta(
            &self.ctx(ctx),
            CallMeta {
                source: goap_llm::SOURCE_ENGINE.to_string(),
                conversation_id: conversation_of(&self.process),
                process_id: self.process.id.clone(),
                change_id: self.process.change_id.to_string(),
                step: self.step,
                call: self.base + slot as i64,
                action: self.action.clone(),
                agent: self.process.agent.clone(),
            },
        );
        let start = Instant::now();
        let res = client.complete(&ctx, req.clone()).await;
        let resp = res.as_ref().cloned().unwrap_or_default();
        let mut call = LLMCall {
            provider: resp.provider.clone(),
            model: resp.model.clone(),
            input_tokens: resp.usage.input_tokens,
            output_tokens: resp.usage.output_tokens,
            duration_ms: start.elapsed().as_millis() as i64,
            exchange: Some(exchange_of(&req, &resp)),
            ..Default::default()
        };
        if call.model.is_empty() {
            call.model = req.model.clone();
        }
        if let Err(e) = &res {
            call.error = e.to_string();
        }
        self.state.lock().llm_calls[slot] = call;
        Ok(res?)
    }

    /// tools lists the tools the action may call: those of the MCPs it or its agent declares that the organization of
    /// the change binds, within the scope of each MCP.
    pub async fn tools(&self, ctx: &Ctx) -> Result<Vec<ToolInfo>> {
        if !self.e.scope().has_tools() || self.mcps.is_empty() {
            return Ok(Vec::new());
        }
        let all = self.unit_tools(ctx).await?;
        Ok(all.into_iter().filter(|t| self.permitted(t)).collect())
    }

    /// check_scope refuses a tool the unit cannot call or the action may not reach from where it is declared.
    async fn check_scope(&self, ctx: &Ctx, name: &str) -> Result<()> {
        let all = self.unit_tools(ctx).await?;
        for t in &all {
            if t.name != name {
                continue;
            }
            if !self.permitted(t) {
                return Err(Error::msg(format!(
                    "tool {name}: its MCP has scope {}, out of reach of action {} ({})",
                    goap_mcp::scope_of(&t.scope),
                    self.action,
                    self.kind
                )));
            }
            return Ok(());
        }
        Err(Error::msg(format!(
            "tool {name} is not available to {} (unbound or restricted)",
            self.e.r#ref(&self.process).org
        )))
    }

    async fn call_tool_inner(
        &self,
        ctx: &Ctx,
        name: &str,
        args: Map<String, Value>,
    ) -> Result<Value> {
        let start = Instant::now();
        let (ctx, end) =
            self.e
                .tracer()
                .start_tool(&self.ctx(ctx), &self.process, &self.action, name);
        let check: Result<()> = match goap_mcp::split_tool(name) {
            Err(e) => Err(Error::msg(e.to_string())),
            Ok((m, _)) if !self.mcps.contains(&m) => Err(Error::msg(format!(
                "tool {name}: action {} does not declare the MCP {m}",
                self.action
            ))),
            Ok(_) if !self.e.scope().has_tools() => {
                Err(Error::msg(format!("tool {name}: no MCP hub configured")))
            }
            Ok(_) => self.check_scope(&ctx, name).await,
        };
        let out = match check {
            Ok(()) => {
                self.e
                    .scope()
                    .call_tool(&ctx, &self.e.r#ref(&self.process), name, args)
                    .await
            }
            Err(e) => Err(e),
        };
        end(out.as_ref().err());
        let mut call = ToolCall {
            name: name.to_string(),
            duration_ms: start.elapsed().as_millis() as i64,
            ..Default::default()
        };
        if let Err(e) = &out {
            call.error = e.to_string();
        }
        self.state.lock().tool_calls.push(call);
        out
    }

    async fn graph(&self, ctx: &Ctx) -> Result<(Vec<Node>, Vec<Link>)> {
        let r = self
            .graph
            .get_or_init(|| async {
                read_graph(
                    ctx,
                    &*self.e.graph,
                    &self.process.change_id,
                    &self.process.flow,
                    &self.process.baseline_id,
                )
                .await
                .map_err(|e| e.to_string())
            })
            .await;
        r.clone().map_err(Error::Msg)
    }

    /// record merges the calls of the host into the step.
    pub(crate) fn record(&self, step: &mut Step) {
        let st = self.state.lock();
        step.llm_calls.extend(st.llm_calls.iter().cloned());
        step.tool_calls.extend(st.tool_calls.iter().cloned());
        for c in &st.llm_calls {
            step.usage.llm_calls += 1;
            step.usage.input_tokens += c.input_tokens;
            step.usage.output_tokens += c.output_tokens;
        }
        step.usage.tool_calls += st.tool_calls.len() as i64;
        step.children.extend(st.spawned.iter().cloned());
    }
}

fn to_dsl(n: &Node) -> goap_dsl::Node {
    goap_dsl::Node {
        id: n.id.to_string(),
        version: n.version,
        key: n.key.clone(),
        r#type: n.r#type.clone(),
        props: n.properties.clone(),
        ..Default::default()
    }
}

#[async_trait]
impl goap_dsl::Host for Host {
    async fn complete(
        &self,
        ctx: &Ctx,
        r: goap_dsl::CompleteRequest,
    ) -> std::result::Result<goap_dsl::CompleteResult, goap_dsl::Error> {
        let req = Request {
            model: r.model,
            system: r.system,
            json: r.json,
            max_tokens: r.max_tokens,
            messages: vec![Message {
                role: "user".into(),
                content: r.prompt,
                ..Default::default()
            }],
            ..Default::default()
        };
        let resp = self
            .complete_llm(ctx, self.e.llm.as_ref(), req.clone())
            .await?;
        let mut out = goap_dsl::CompleteResult {
            text: resp.text.clone(),
            model: resp.model.clone(),
            input_tokens: resp.usage.input_tokens,
            output_tokens: resp.usage.output_tokens,
            ..Default::default()
        };
        if req.json
            && let Ok(v) = goap_llm::decode_json::<Value>(&resp.text)
        {
            out.json = v;
        }
        Ok(out)
    }

    async fn run_agent(
        &self,
        ctx: &Ctx,
        name: &str,
        intent: &str,
    ) -> std::result::Result<goap_dsl::AgentResult, goap_dsl::Error> {
        let key = {
            let mut st = self.state.lock();
            let k = format!("{}#{}:{}", self.action, st.calls, name);
            st.calls += 1;
            k
        };
        let ctx = self.e.as_(ctx, &self.process);
        Ok(self.e.run_child(&ctx, self, &key, name, intent).await?)
    }

    async fn call_tool(
        &self,
        ctx: &Ctx,
        name: &str,
        args: Map<String, Value>,
    ) -> std::result::Result<Value, goap_dsl::Error> {
        Ok(self.call_tool_inner(ctx, name, args).await?)
    }

    async fn node(
        &self,
        ctx: &Ctx,
        key: &str,
    ) -> std::result::Result<goap_dsl::Node, goap_dsl::Error> {
        let (nodes, _) = self.graph(ctx).await?;
        nodes
            .iter()
            .find(|n| n.key == key)
            .map(to_dsl)
            .ok_or_else(|| Error::msg(format!("node {key:?} not in the reference baseline")).into())
    }

    async fn nodes(
        &self,
        ctx: &Ctx,
        node_type: &str,
    ) -> std::result::Result<Vec<goap_dsl::Node>, goap_dsl::Error> {
        let (nodes, _) = self.graph(ctx).await?;
        Ok(nodes
            .iter()
            .filter(|n| node_type.is_empty() || n.r#type == node_type)
            .map(to_dsl)
            .collect())
    }

    async fn links(
        &self,
        ctx: &Ctx,
        key: &str,
        direction: &str,
        link_type: &str,
    ) -> std::result::Result<Vec<goap_dsl::Link>, goap_dsl::Error> {
        let (nodes, links) = self.graph(ctx).await?;
        let by_ref: HashMap<NodeRef, &Node> = nodes.iter().map(|n| (n.r#ref(), n)).collect();
        let empty = Node::default();
        let end = |r: &NodeRef| {
            let n = by_ref.get(r).copied().unwrap_or(&empty);
            goap_dsl::LinkEnd {
                id: r.id.to_string(),
                version: r.version,
                key: n.key.clone(),
                r#type: n.r#type.clone(),
            }
        };
        let mut out = Vec::new();
        for l in &links {
            if !link_type.is_empty() && l.r#type != link_type {
                continue;
            }
            let from = by_ref.get(&l.from).copied().unwrap_or(&empty);
            let to = by_ref.get(&l.to).copied().unwrap_or(&empty);
            if (direction != "in" && from.key == key) || (direction != "out" && to.key == key) {
                out.push(goap_dsl::Link {
                    id: l.id.to_string(),
                    r#type: l.r#type.clone(),
                    from: end(&l.from),
                    to: end(&l.to),
                });
            }
        }
        Ok(out)
    }
}

// ---- script actions -------------------------------------------------------------------------------------------------

/// Sandbox executes script jobs in isolation.
#[async_trait]
pub trait Sandbox: Send + Sync {
    /// The id of the sandbox (container, pod, process...).
    fn id(&self) -> String;
    async fn execute(
        &self,
        ctx: &Ctx,
        job: goap_dsl::Job,
        host: &dyn goap_dsl::Host,
    ) -> (
        goap_dsl::ScriptResult,
        std::result::Result<(), goap_dsl::Error>,
    );
}

/// Sandboxes provides one sandbox per process run.
#[async_trait]
pub trait Sandboxes: Send + Sync {
    async fn acquire(&self, ctx: &Ctx, process_id: &str) -> Result<Arc<dyn Sandbox>>;
    /// release destroys the sandbox of a finished process.
    async fn release(&self, ctx: &Ctx, process_id: &str);
}

/// InprocSandboxes runs scripts inside the engine process. NOT an isolation boundary: tests and single-user
/// development only.
#[derive(Debug, Clone, Copy, Default)]
pub struct InprocSandboxes;

struct Inproc;

#[async_trait]
impl Sandbox for Inproc {
    fn id(&self) -> String {
        "inproc".to_string()
    }
    async fn execute(
        &self,
        ctx: &Ctx,
        job: goap_dsl::Job,
        host: &dyn goap_dsl::Host,
    ) -> (
        goap_dsl::ScriptResult,
        std::result::Result<(), goap_dsl::Error>,
    ) {
        goap_dsl::run(ctx, job, host).await
    }
}

#[async_trait]
impl Sandboxes for InprocSandboxes {
    async fn acquire(&self, _: &Ctx, _: &str) -> Result<Arc<dyn Sandbox>> {
        Ok(Arc::new(Inproc))
    }
    async fn release(&self, _: &Ctx, _: &str) {}
}

/// ScriptExecutor runs script actions (JavaScript / Go) in the sandbox of the process.
#[derive(Clone, Default)]
pub struct ScriptExecutor {
    pub sandboxes: Option<Arc<dyn Sandboxes>>,
}

#[async_trait]
impl Executor for ScriptExecutor {
    async fn execute(&self, ctx: &Ctx, ac: ActionContext) -> ExecResult {
        let Some(sbs) = &self.sandboxes else {
            return Err(Error::msg("no sandbox provider configured").into());
        };
        let sb = sbs
            .acquire(ctx, &ac.process.id)
            .await
            .map_err(|e| e.context("sandbox"))?;
        let intent = ac
            .process
            .intent
            .turns
            .iter()
            .find(|t| t.role == "user")
            .map(|t| t.text.clone())
            .unwrap_or_default();
        let job = goap_dsl::Job {
            language: ac.action.language.clone(),
            code: ac.action.code.clone(),
            process_id: ac.process.id.clone(),
            parent_id: ac.process.parent_id.clone(),
            agent: ac.process.agent.clone(),
            action: ac.action.name.clone(),
            intent,
            goal: ac.process.goal.clone(),
            params: ac.action.params.clone(),
            vars: action_vars(&ac),
            items: goap_dsl::items_from_blackboard(&ac.blackboard),
            nodes: change_impacts_from_blackboard(&ac.blackboard),
            options: goap_dsl::options_from_blackboard(&ac.blackboard),
            decision_points: goap_dsl::decision_points_from_blackboard(&ac.blackboard),
            ..Default::default()
        };
        let (res, err) = match &ac.host {
            Some(h) => sb.execute(ctx, job, &**h).await,
            None => (
                goap_dsl::ScriptResult::default(),
                Err(goap_dsl::Error::host("a script needs a host")),
            ),
        };
        let mut out = ActionResult {
            output: res.output.clone(),
            sandbox: sb.id(),
            vars_set: res.vars_set.clone(),
            ..Default::default()
        };
        for l in &res.logs {
            out.logs.push(LogLine {
                time: l.time,
                level: l.level.clone(),
                message: l.message.clone(),
                process_id: ac.process.id.clone(),
                action: ac.action.name.clone(),
                ..Default::default()
            });
        }
        if let Err(e) = err {
            return Err(crate::executors::ExecError {
                partial: out,
                error: Error::Dsl(e),
            });
        }
        if res.suspended {
            out.suspended = true;
            out.child = ac.host.as_ref().map(|h| h.waiting_on()).unwrap_or_default();
            out.wake_on = res.wake_on.clone();
            return Ok(out);
        }
        out.nodes = res.nodes.clone();
        let raw = Value::Array(res.items.iter().cloned().map(Value::Object).collect());
        match serde_json::from_value(raw) {
            Ok(items) => out.items = items,
            Err(e) => {
                return Err(crate::executors::ExecError {
                    partial: out,
                    error: Error::msg(format!("script items: {e}")),
                });
            }
        }
        Ok(out)
    }
}
