//! The engine, its triggers and the handler the IDE and the built-in connectors call it through (Go's engine.go).

use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use goap_engine::{
    AuthzScope, Broker, Engine, EventingGraph, Executor, GraphPort, HumanExecutor, LLMExecutor,
    MethodologyPort, Publisher, Publishers, ScriptExecutor, ToolExecutor, ToolPort, TriggerManager,
    default_builtins,
};
use goap_intent::{Lexical, Resolver};
use goap_mcp::ToolInfo;
use goap_methodology::{KIND_BUILTIN, KIND_HUMAN, KIND_LLM, KIND_SCRIPT, KIND_TOOL};
use goap_platform::{env_bool, env_int, h2c_client};
use serde_json::{Map, Value};

use crate::app::{BoxError, Env, wrap};
use crate::graph::GraphPart;
use crate::local::Stores;
use crate::platform::PlatformPart;
use crate::registry::{InProcessDomains, RegistryPart};

/// EnginePart is the engine, its triggers and the handler the IDE and the built-in connectors call it through.
pub(crate) struct EnginePart {
    pub handler: Arc<goap_enginesvc::Handler>,
    /// None unless GOAP_SANDBOX runs scripts out of process.
    pub runtime: Option<Arc<goap_sandbox::Runtime>>,
}

/// build_engine creates the engine, registers the builtins and starts the triggers (the late binding of env.triggers
/// is set here), then adds the built-in connectors to the hub, which keeps them registered.
pub(crate) async fn build_engine(
    e: &Env,
    st: &Stores,
    gp: &GraphPart,
    rp: &RegistryPart,
    pp: &PlatformPart,
) -> Result<EnginePart, BoxError> {
    let g = &gp.g;
    // the engine calls the gateway in-process, without an identity: trusted
    let models: Arc<dyn goap_llm::Client> = Arc::new(goap_enginetel::LLMClient {
        next: pp.gw.clone(),
    });
    // scripts run in-process unless GOAP_SANDBOX selects a provisioner
    let sandboxes = goap_sandbox::from_env(h2c_client(), goap_telemetry::client_options())
        .map_err(|err| wrap("sandbox", err))?;
    let broker = Arc::new(Broker::new());
    let mut builtins = default_builtins();
    let graph: Arc<dyn GraphPort> =
        Arc::new(EventingGraph::new(g.clone(), Some(e.triggers.on_event())));
    let mut eg = Engine::new(
        graph,
        rp.reg.clone() as Arc<dyn MethodologyPort>,
        st.processes.clone(),
    );
    let executors: [(&str, Arc<dyn Executor>); 4] = [
        (
            KIND_LLM,
            Arc::new(LLMExecutor {
                client: Some(models.clone()),
            }),
        ),
        (
            KIND_SCRIPT,
            Arc::new(ScriptExecutor {
                sandboxes: Some(sandboxes.sandboxes.clone()),
            }),
        ),
        (KIND_HUMAN, Arc::new(HumanExecutor)),
        (KIND_TOOL, Arc::new(ToolExecutor)),
    ];
    eg.executors = executors
        .into_iter()
        .map(|(k, x)| (k.to_string(), x))
        .collect();
    eg.intent = Some(Resolver::new(Arc::new(Lexical)));
    eg.events = Some(Arc::new(Publishers(vec![
        broker.clone() as Arc<dyn Publisher>,
        rp.bus.clone(),
    ])));
    eg.scope = Some(Arc::new(AuthzScope {
        authz: Some(gp.authorizer.clone()),
        hub: Some(Arc::new(InprocHub(goap_mcpsvc::HubPort {
            service: pp.hub.clone(),
        }))),
    }));
    eg.llm = Some(models);
    eg.sandboxes = Some(sandboxes.sandboxes.clone());
    eg.tracer = Some(Arc::new(goap_enginetel::EngineTracer::new()));
    eg.max_parallel = env_int("GOAP_MAX_PARALLEL", 4);
    // the oversight floor (EU AI Act Art. 14)
    eg.person_reviews_model_output = env_bool("GOAP_PERSON_REVIEWS_MODEL_OUTPUT", true);
    {
        let types = rp.types.clone();
        eg.types = Some(Arc::new(move || {
            Some(types.get() as Arc<dyn goap_def::TypeSet>)
        }));
    }
    // a store shared by replicas (sqlite) serializes the work on a process through its locks, and the runs left by
    // the previous start are resumed (ADR 0124)
    eg.locks = st.locks.clone();
    // self-observation (methodology-improvement): journal, traces, drafts
    goap_selfimprove::register(
        &mut builtins,
        &eg,
        goap_enginetel::self_improvement_from_env(Arc::new(goap_registrysvc::Drafts {
            service: rp.reg.clone(),
        })),
    );
    eg.executors
        .insert(KIND_BUILTIN.to_string(), Arc::new(builtins));
    let eg = Arc::new(eg);
    if eg.locks.is_some() {
        let (eg, ctx) = (eg.clone(), e.ctx.clone());
        tokio::spawn(async move { eg.recover_loop(&ctx, Duration::ZERO).await });
    }
    let triggers = TriggerManager::new(eg.clone());
    e.triggers.set(triggers.clone());
    triggers.start(&e.ctx).await;
    {
        let (t, ctx, broker) = (triggers.clone(), e.ctx.clone(), broker.clone());
        tokio::spawn(async move { t.watch_processes(&ctx, &broker).await });
    }
    let mut handler = goap_enginesvc::Handler::new(eg.clone());
    handler.default_principal = Some(e.dev.clone());
    handler.authz = Some(gp.authorizer.clone());
    handler.broker = Some(broker);
    handler.triggers = Some(triggers);
    let handler = Arc::new(handler);
    // the built-in connectors call the platform in-process, for the caller of the tool
    let floor: Arc<dyn goap_authz::Authorizer> = Arc::new(gp.authorizer.floor());
    pp.connectors.add(goap_connector_builtin::connectors(
        goap_connector_builtin::Ports {
            graph: Some(Arc::new(EventingGraph::new(
                g.clone(),
                Some(e.triggers.on_event()),
            ))),
            engine: Some(handler.clone()),
            hub: Some(pp.hub.clone()),
            domains: Some(Arc::new(InProcessDomains(rp.domains.clone()))),
            methodologies: Some(Arc::new(goap_registrysvc::Handler::new(rp.reg.clone()))),
            authz: Some(gp.authorizer.clone()),
            floor: Some(floor),
        },
    ));
    pp.hub.keep_registered(&e.ctx, pp.connectors.all()).await;
    Ok(EnginePart {
        handler,
        runtime: sandboxes.runtime.clone(),
    })
}

/// InprocHub is the hub of this process as the engine's tool port (Go's `mcpsvc.HubPort` satisfies
/// `engine.ToolPort`).
struct InprocHub(goap_mcpsvc::HubPort);

#[async_trait]
impl ToolPort for InprocHub {
    async fn call_tool(
        &self,
        ctx: &goap_ctx::Ctx,
        org: &str,
        name: &str,
        args: Map<String, Value>,
    ) -> goap_engine::Result<Value> {
        self.0
            .call_tool(ctx, org, name, args)
            .await
            .map(Value::Object)
            .map_err(|e| goap_engine::Error::Msg(e.to_string()))
    }

    async fn tools(
        &self,
        ctx: &goap_ctx::Ctx,
        org: &str,
    ) -> goap_engine::Result<(Vec<ToolInfo>, Vec<String>)> {
        self.0
            .tools(ctx, org)
            .await
            .map_err(|e| goap_engine::Error::Msg(e.to_string()))
    }
}
