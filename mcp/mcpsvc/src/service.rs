//! The logic of the hub (`service.go`): the registry of connectors, the check and template of adapters, and the
//! resolution and call of tools for a unit.

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use chrono::{DateTime, SecondsFormat, Utc};
use goap_adapter as adapter;
use goap_algo as algo;
use goap_ctx::Ctx;
use goap_gen::connector::v1::{ConnectorInfo, InvokeRequest, InvokeResponse};
use goap_mcp as mcp;
use parking_lot::Mutex;
use serde_json::{Map, Value};

use crate::{ConnectorReg, Directory, Effective, Error, Snapshot, Store, ToolError};

/// DefaultLease is how long a connector registration stays live without renewal.
pub const DEFAULT_LEASE: Duration = Duration::from_secs(30);

/// DefaultModelTokenTTL outlasts the longest run of a coding session (four hours, ADR 0123 §2).
pub const DEFAULT_MODEL_TOKEN_TTL: Duration = Duration::from_secs(5 * 3600);

/// Invoker calls the ConnectorService of a registered connector.
#[async_trait]
pub trait Invoker: Send + Sync {
    async fn invoke(
        &self,
        ctx: &Ctx,
        endpoint: &str,
        req: InvokeRequest,
    ) -> Result<InvokeResponse, Error>;
}

/// The error of a secret resolver.
pub type SecretError = Box<dyn std::error::Error + Send + Sync>;

/// Resolves a secret reference of a binding ("<vault path>#<field>" or "env:<VAR>"): Go's
/// `func(ctx, ref) (string, error)` field.
#[async_trait]
pub trait SecretResolver: Send + Sync {
    async fn resolve(&self, ctx: &Ctx, reference: &str) -> Result<String, SecretError>;
}

/// The clock of the service.
pub type NowFn = Arc<dyn Fn() -> DateTime<Utc> + Send + Sync>;

/// Service holds the logic of the hub.
#[derive(Clone)]
pub struct Service {
    pub store: Arc<dyn Store>,
    /// Directory reads the unit hierarchy, the MCPs and the adapter instances from the graph.
    pub directory: Option<Arc<Directory>>,
    pub invoker: Option<Arc<dyn Invoker>>,
    /// Secrets resolves a secret reference of a binding ("<vault path>#<field>" or "env:<VAR>").
    pub secrets: Option<Arc<dyn SecretResolver>>,
    /// Lease is the validity of a registration (zero: DEFAULT_LEASE).
    pub lease: Duration,
    pub now: Option<NowFn>,
    /// Models, when set, gives each call of a connector that calls a model by itself (ConnectorInfo.calls_models,
    /// the Claude coding agent) the gateway's Anthropic endpoint and a token of the call, so its model calls are rows
    /// of the ledger (ADR 0123 §6); None: such a connector calls the model with the adapter's key, outside the
    /// ledger.
    pub models: Option<ModelEndpoint>,
}

/// ModelEndpoint is the model gateway as the hub hands it to a connector (ADR 0123 §6).
#[derive(Debug, Clone, Default)]
pub struct ModelEndpoint {
    /// URL is the base URL of the gateway's Anthropic endpoint (<gateway>/anthropic), as the connector's host
    /// reaches it.
    pub url: String,
    /// Secret signs the tokens (goap-modeltoken), shared with the gateway (GOAP_MODEL_PROXY_SECRET).
    pub secret: Vec<u8>,
    /// TTL is the validity of a token (zero: DEFAULT_MODEL_TOKEN_TTL).
    pub ttl: Duration,
}

impl ModelEndpoint {
    /// token mints the token of a call of connector for unit: a system subject of the connector acting for the
    /// caller's end user, on the caller's project, for the change and the process of the call.
    fn token(
        &self,
        ctx: &Ctx,
        connector: &str,
        unit: &str,
        cc: &mcp::CallContext,
        now: DateTime<Utc>,
    ) -> Result<String, Error> {
        let p = goap_authz::principal_from(ctx);
        let ttl = if self.ttl.is_zero() {
            DEFAULT_MODEL_TOKEN_TTL
        } else {
            self.ttl
        };
        Ok(goap_modeltoken::sign(
            &self.secret,
            goap_modeltoken::Claims {
                subject: goap_authz::system(&format!("connector:{connector}"), &[]).subject,
                end_user: p.end_user().to_string(),
                org: unit.to_string(),
                project: p.project.clone(),
                change: cc.change.clone(),
                process: cc.process.clone(),
                source: format!("connector:{connector}"),
                ..Default::default()
            },
            now,
            ttl,
        )?)
    }
}

/// ConnectorView is a registration and whether it is live.
#[derive(Debug, Clone, PartialEq)]
pub struct ConnectorView {
    pub reg: ConnectorReg,
    pub live: bool,
}

impl std::ops::Deref for ConnectorView {
    type Target = ConnectorReg;
    fn deref(&self) -> &ConnectorReg {
        &self.reg
    }
}

/// Tool is a tool available to a unit.
pub type Tool = mcp::ToolInfo;

/// adapterDef returns the definition an adapter instance refers to and checks that it implements the MCP the
/// instance says it does.
fn adapter_def(snap: &Snapshot, a: &adapter::Instance) -> Result<adapter::Def, Error> {
    let Some(def) = snap.adapter_def(&a.adapter) else {
        return Err(Error::AdapterDef(format!(
            "adapter of {} in {}: no adapter definition {:?} (declare it in the platform namespace): {}",
            a.mcp,
            a.unit,
            a.adapter,
            crate::ERR_ADAPTER_DEF
        )));
    };
    if def.mcp != a.mcp {
        return Err(Error::AdapterDef(format!(
            "adapter definition {} implements the MCP {}, not {}: {}",
            def.name,
            def.mcp,
            a.mcp,
            crate::ERR_ADAPTER_DEF
        )));
    }
    Ok(def.clone())
}

impl Service {
    /// A service over a store, with nothing else set.
    pub fn new(store: Arc<dyn Store>) -> Self {
        Service {
            store,
            directory: None,
            invoker: None,
            secrets: None,
            lease: Duration::ZERO,
            now: None,
            models: None,
        }
    }

    pub(crate) fn lease(&self) -> Duration {
        if self.lease.is_zero() {
            DEFAULT_LEASE
        } else {
            self.lease
        }
    }

    fn now(&self) -> DateTime<Utc> {
        match &self.now {
            Some(f) => f(),
            None => Utc::now(),
        }
    }

    /// The directory (Go dereferenced a nil one: here an error).
    pub(crate) fn dir(&self) -> Result<&Arc<Directory>, Error> {
        self.directory
            .as_ref()
            .ok_or_else(|| Error::Message("mcp hub: no directory".into()))
    }

    async fn snapshot(&self, ctx: &Ctx) -> Result<Arc<Snapshot>, Error> {
        self.dir()?.snapshot(ctx).await
    }

    /// RegisterConnector records (or renews) a connector and returns its lease.
    pub async fn register_connector(
        &self,
        ctx: &Ctx,
        info: Option<ConnectorInfo>,
        endpoint: &str,
    ) -> Result<Duration, Error> {
        let Some(info) = info.filter(|i| mcp::valid_name(&i.id)) else {
            return Err(Error::Invalid(format!(
                "connector id must be a lowercase name: {}",
                mcp::ERR_INVALID
            )));
        };
        if endpoint.is_empty() {
            return Err(Error::Invalid(format!(
                "connector {}: endpoint is required: {}",
                info.id,
                mcp::ERR_INVALID
            )));
        }
        if info.operations.iter().any(|op| op.name.is_empty()) {
            return Err(Error::Invalid(format!(
                "connector {}: an operation has no name: {}",
                info.id,
                mcp::ERR_INVALID
            )));
        }
        self.store
            .save_connector(
                ctx,
                ConnectorReg {
                    info,
                    endpoint: endpoint.to_string(),
                    last_seen: self.now(),
                },
            )
            .await?;
        Ok(self.lease())
    }

    /// Connectors lists the registrations.
    pub async fn connectors(&self, ctx: &Ctx) -> Result<Vec<ConnectorView>, Error> {
        let regs = self.store.connectors(ctx).await?;
        Ok(regs
            .into_iter()
            .map(|r| {
                let live = self.live(&r);
                ConnectorView { reg: r, live }
            })
            .collect())
    }

    fn live(&self, r: &ConnectorReg) -> bool {
        let lease = chrono::Duration::from_std(self.lease()).unwrap_or(chrono::Duration::MAX);
        self.now() - r.last_seen <= lease
    }

    /// CheckAdapter validates an adapter instance before it is saved: the MCP and the algorithm must exist and
    /// match, and the parameter values must fit the algorithm (errors); what does not match the connector currently
    /// registered is reported as warnings.
    pub async fn check_adapter(
        &self,
        ctx: &Ctx,
        a: &adapter::Instance,
    ) -> Result<Vec<String>, Error> {
        let snap = self.snapshot(ctx).await?;
        let mut warnings = Vec::new();
        let Some(def) = snap.def(&a.mcp) else {
            return Err(Error::Invalid(format!(
                "unknown MCP {:?} (declare it in the platform namespace): {}",
                a.mcp,
                mcp::ERR_INVALID
            )));
        };
        a.validate(def)?;
        if !a.implements() {
            // a restriction only: the implementation is inherited
            if !a.unit.is_empty() && snap.resolve(&a.unit, &a.mcp).is_none() {
                warnings.push(format!(
                    "no ancestor of {} implements {}: the restriction has nothing to restrict",
                    a.unit, a.mcp
                ));
            }
            return Ok(warnings);
        }
        let alg = match adapter_def(&snap, a) {
            Err(Error::AdapterDef(m)) => {
                return Err(Error::Invalid(format!("{m}: {}", mcp::ERR_INVALID)));
            }
            r => r?,
        };
        let (_, secrets, issues) = alg.resolve(&a.params);
        if let Some(first) = issues.first() {
            return Err(Error::Invalid(format!(
                "adapter {}: {first}: {}",
                a.adapter,
                mcp::ERR_INVALID
            )));
        }
        for (name, r) in &secrets {
            if !r.starts_with("env:") && !r.contains('#') {
                warnings.push(format!(
                    "secret {name}: {r:?} is neither env:<VAR> nor <vault path>#<field>"
                ));
            }
        }
        let reg = match self.store.connector(ctx, &alg.connector).await {
            Err(e) if e.is_not_found() => {
                warnings.push(format!(
                    "connector {} is not registered: not checked against its operations",
                    alg.connector
                ));
                return Ok(warnings);
            }
            r => r?,
        };
        for name in &reg.info.secret_names {
            if !secrets.contains_key(name) {
                warnings.push(format!(
                    "connector {} needs the secret {name:?}",
                    alg.connector
                ));
            }
        }
        Ok(warnings)
    }

    /// Template generates the skeleton of the code of an adapter between an MCP and a registered connector, and the
    /// parameters the connector needs.
    pub async fn template(
        &self,
        ctx: &Ctx,
        mcp_name: &str,
        connector: &str,
    ) -> Result<(String, Vec<algo::Param>), Error> {
        let snap = self.snapshot(ctx).await?;
        let Some(def) = snap.def(mcp_name) else {
            return Err(Error::not_found(format!("mcp {mcp_name}")));
        };
        let reg = self.store.connector(ctx, connector).await?;
        let ops: Vec<adapter::Operation> = reg
            .info
            .operations
            .iter()
            .map(|o| adapter::Operation {
                name: o.name.clone(),
                description: o.description.clone(),
                input_schema: goap_pbconv::to_map(o.input_schema.as_ref()),
            })
            .collect();
        Ok((
            adapter::template(def, connector, &ops),
            adapter::params(
                &goap_pbconv::to_map(reg.info.config_schema.as_ref()),
                &reg.info.secret_names,
            ),
        ))
    }

    /// Effective lists the MCPs a unit can use with their resolved adapters, and the unit chain the adapters were
    /// looked up along (nearest first).
    pub async fn effective(
        &self,
        ctx: &Ctx,
        unit: &str,
    ) -> Result<(Vec<String>, Vec<Effective>), Error> {
        let snap = self.snapshot(ctx).await?;
        Ok((snap.chain(unit), snap.effective(unit)))
    }

    /// MCPs lists the MCP definitions of the platform namespace.
    pub async fn mcps(&self, ctx: &Ctx) -> Result<Vec<mcp::Def>, Error> {
        Ok(self.snapshot(ctx).await?.defs())
    }

    /// ConnectorOf returns the id of the connector an adapter instance calls (from its definition), or "" when it
    /// cannot be resolved.
    pub async fn connector_of(&self, ctx: &Ctx, a: &adapter::Instance) -> String {
        match self.snapshot(ctx).await {
            Ok(snap) => adapter_def(&snap, a)
                .map(|d| d.connector)
                .unwrap_or_default(),
            Err(_) => String::new(),
        }
    }

    /// Tools lists the tools a unit can call (its restrictions applied), and the MCPs it can use (those with at
    /// least one tool left).
    pub async fn tools(&self, ctx: &Ctx, unit: &str) -> Result<(Vec<Tool>, Vec<String>), Error> {
        let (_, eff) = self.effective(ctx, unit).await?;
        let mut tools = Vec::new();
        let mut mcps = Vec::new();
        for e in eff {
            let allowed = e.allowed();
            if allowed.tools.is_empty() {
                continue;
            }
            mcps.push(e.mcp.name.clone());
            for t in allowed.tools {
                tools.push(Tool {
                    name: mcp::tool_name(&e.mcp.name, &t.name),
                    description: t.description,
                    input_schema: t.input_schema,
                    read_only: t.read_only,
                    scope: mcp::scope_of(&e.mcp.scope).to_string(),
                });
            }
        }
        Ok((tools, mcps))
    }

    /// Call runs a tool ("<mcp>/<tool>") for the unit holding a change: it takes the adapter instance of the nearest
    /// unit, takes its definition from the platform namespace, and runs the code, which calls the operations the
    /// connector exposes (configured with the instance's parameters and secrets). A failure reported by the code or
    /// the connector is an [`Error::Tool`].
    pub async fn call(
        &self,
        ctx: &Ctx,
        org: &str,
        name: &str,
        args: Map<String, Value>,
    ) -> Result<Map<String, Value>, Error> {
        let (mcp_name, tool) = mcp::split_tool(name)?;
        let snap = self.snapshot(ctx).await?;
        let Some(def) = snap.def(&mcp_name) else {
            return Err(Error::NotBound(format!(
                "unknown MCP {mcp_name}: {}",
                crate::ERR_NOT_BOUND
            )));
        };
        let Some(t) = def.tool(&tool) else {
            return Err(Error::NotFound(format!(
                "mcp {mcp_name} has no tool {tool}: {}",
                crate::ERR_NOT_FOUND
            )));
        };
        t.check_args(&args)?;
        let org = snap.unit(org);
        let Some((a, _)) = snap.resolve(&org, &mcp_name) else {
            return Err(Error::NotBound(format!(
                "mcp {mcp_name} has no adapter for {org} or its ancestors: {}",
                crate::ERR_NOT_BOUND
            )));
        };
        let r = snap.restriction(&org, &mcp_name);
        if !r.allows(t) {
            return Err(Error::NotBound(format!(
                "tool {name} is restricted for {org} (by {}): {}",
                r.by.join(", "),
                crate::ERR_NOT_BOUND
            )));
        }
        // connectors of the platform itself read what the call runs for (ADR 0028)
        let mut ctx = mcp::with_call(
            ctx,
            mcp::CallContext {
                unit: org.clone(),
                ..Default::default()
            },
        );
        let alg = match adapter_def(&snap, &a) {
            Err(Error::AdapterDef(m)) => {
                return Err(Error::Unavailable(format!(
                    "{m}: {}",
                    crate::ERR_UNAVAILABLE
                )));
            }
            r => r?,
        };
        let (config, secret_refs, issues) = alg.resolve(&a.params);
        if let Some(first) = issues.first() {
            return Err(Error::Invalid(format!(
                "adapter of {mcp_name} in {}: {first}: {}",
                a.unit,
                mcp::ERR_INVALID
            )));
        }
        let reg = match self.store.connector(&ctx, &alg.connector).await {
            Err(e) if e.is_not_found() => {
                return Err(Error::Unavailable(format!(
                    "connector {} is not registered: {}",
                    alg.connector,
                    crate::ERR_UNAVAILABLE
                )));
            }
            r => r?,
        };
        if !self.live(&reg) {
            return Err(Error::Unavailable(format!(
                "connector {} has not renewed its registration since {}: {}",
                alg.connector,
                reg.last_seen.to_rfc3339_opts(SecondsFormat::Secs, true),
                crate::ERR_UNAVAILABLE
            )));
        }
        let secrets = self
            .resolve_secrets(&ctx, &a, &secret_refs, &reg.info)
            .await?;
        let ops: Vec<String> = reg.info.operations.iter().map(|o| o.name.clone()).collect();
        let mut cc = mcp::call_from(&ctx);
        if reg.info.calls_models
            && let Some(m) = &self.models
        {
            let tok = m.token(&ctx, &alg.connector, &a.unit, &cc, self.now())?;
            cc.model_url = m.url.clone();
            cc.model_token = tok;
            ctx = mcp::with_call(&ctx, cc.clone()); // an in-process connector reads it from the context
        }
        let invoker = self
            .invoker
            .clone()
            .ok_or_else(|| Error::Message("mcp hub: no invoker".into()))?;
        let transport = Arc::new(Mutex::new(None::<String>));
        let call = Arc::new(OpCall {
            invoker,
            endpoint: reg.endpoint.clone(),
            connector: alg.connector.clone(),
            config: config.clone(),
            secrets,
            unit: a.unit.clone(),
            cc,
            transport: transport.clone(),
        });
        let bound = algo::Bound {
            instance: adapter::key(&a.unit, &a.mcp),
            algorithm: alg.name.clone(),
            language: alg.language.clone(),
            code: alg.code.clone(),
            params: config,
            ..Default::default()
        };
        let out = goap_dsl::run_adapter(
            &ctx,
            &bound,
            goap_dsl::AdapterInput {
                tool,
                args,
                operations: ops,
                call: Some(call),
            },
        )
        .await;
        if let Some(t) = transport.lock().take() {
            return Err(Error::Unavailable(t));
        }
        let out = out.map_err(|e| ToolError { msg: e.to_string() })?;
        if !out.ok() {
            return Err(ToolError {
                msg: out.failures.join("; "),
            }
            .into());
        }
        Ok(out.result.unwrap_or_default())
    }

    /// resolveSecrets resolves the secret parameters of an adapter instance; only the secrets the connector
    /// declares leave the hub.
    async fn resolve_secrets(
        &self,
        ctx: &Ctx,
        a: &adapter::Instance,
        refs: &std::collections::BTreeMap<String, String>,
        info: &ConnectorInfo,
    ) -> Result<HashMap<String, String>, Error> {
        let mut out = HashMap::new();
        let Some(resolver) = &self.secrets else {
            return Ok(out);
        };
        for name in &info.secret_names {
            let Some(r) = refs.get(name) else { continue };
            let v = resolver.resolve(ctx, r).await.map_err(|e| {
                Error::Message(format!(
                    "secret {name} of the adapter of {} in {}: {e}",
                    a.mcp, a.unit
                ))
            })?;
            out.insert(name.clone(), v);
        }
        Ok(out)
    }
}

/// The connector operation an adapter calls: the invoker closed over the configuration and the secrets.
struct OpCall {
    invoker: Arc<dyn Invoker>,
    endpoint: String,
    connector: String,
    config: Map<String, Value>,
    secrets: HashMap<String, String>,
    unit: String,
    cc: mcp::CallContext,
    /// the failure to reach the connector, which the call reports whatever the script made of it
    transport: Arc<Mutex<Option<String>>>,
}

#[async_trait]
impl goap_dsl::OperationCall for OpCall {
    async fn call(
        &self,
        ctx: &Ctx,
        operation: &str,
        args: Map<String, Value>,
    ) -> Result<Map<String, Value>, goap_dsl::Error> {
        let req = InvokeRequest {
            operation: operation.to_string(),
            arguments: goap_pbconv::to_struct(&args),
            config: goap_pbconv::to_struct(&self.config),
            secrets: self.secrets.clone(),
            org_id: self.unit.clone(),
            change_id: self.cc.change.clone(),
            process_id: self.cc.process.clone(),
            model_url: self.cc.model_url.clone(),
            model_token: self.cc.model_token.clone(),
        };
        match self.invoker.invoke(ctx, &self.endpoint, req).await {
            Err(err) => {
                let msg = format!(
                    "connector {}: {}: {err}",
                    self.connector,
                    crate::ERR_UNAVAILABLE
                );
                *self.transport.lock() = Some(msg.clone());
                Err(goap_dsl::Error::host(msg))
            }
            Ok(resp) if resp.is_error => Err(goap_dsl::Error::host(resp.error)),
            Ok(resp) => Ok(goap_pbconv::to_map(resp.result.as_ref())),
        }
    }
}
