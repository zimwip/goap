//! The ports, the shared plumbing and the errors of the built-in connectors (`builtin.go`).

use std::collections::HashMap;
use std::sync::Arc;

use goap_authz::{self as authz, Authorizer, Principal};
use goap_connect::ConnectError;
use goap_connectorkit::{Connector, ErrUnknownOperation};
use goap_ctx::Ctx;
use goap_gen::connector::v1::{ConnectorInfo, Operation};
use goap_gen::engine::v1::EngineService;
use goap_gen::registry::v1::RegistryService;
use goap_graphapi as graphapi;
use goap_mcpbuiltin as mcpbuiltin;
use serde::Serialize;
use serde_json::{Map, Value};

use crate::admin::{Admin, Domains, Hub};
use crate::change::Change;
use crate::graph::Graph;
use crate::scheduler::Scheduler;

/// Version is the version the built-in connectors announce.
pub const VERSION: &str = "1";

/// DefaultLimit bounds the lists a tool returns when the call sets no limit.
pub const DEFAULT_LIMIT: usize = 50;

/// EngineAPI is the part of the engine service goap-scheduler calls (StartProcess, AttachChange, GetProcess,
/// ListProcesses, ListTriggers, FireTrigger, ListStartingPoints). The engine service handler (one process) and its
/// Connect client (`EngineServiceClient`, distributed platform) both implement it: the engine authorizes the caller
/// whose identity the request carries.
pub type EngineAPI = dyn EngineService;

/// Methodologies lists the methodologies of the methodology registry and gives the compiled view of a version
/// (ListMethodologies, GetMethodologyView): its client (`RegistryServiceClient`), or its handler in process. The hub
/// reads them through the contract, never compiled (ADR 0125 §4, ADR 0126 step 5).
pub type Methodologies = dyn RegistryService;

/// Ports are the platform APIs the built-in connectors reach. A port left out leaves its connector out.
#[derive(Clone, Default)]
pub struct Ports {
    /// Graph is the graph (in-process graph or the graph service client): goap-graph, goap-change and the
    /// organisation read by goap-admin.
    pub graph: Option<Arc<dyn graphapi::Port>>,
    /// Engine starts and reads processes (goap-scheduler): the engine service handler in-process, its client
    /// otherwise.
    pub engine: Option<Arc<EngineAPI>>,
    /// Hub describes the MCPs and connectors (goap-admin): the hub service.
    pub hub: Option<Arc<dyn Hub>>,
    /// Domains lists the published domains (goap-admin): the domain registry.
    pub domains: Option<Arc<dyn Domains>>,
    /// Methodologies lists the published methodologies (goap-admin, goap-scheduler): the methodology registry.
    pub methodologies: Option<Arc<Methodologies>>,
    /// Authz authorizes reads per node type; Floor guards the access nodes (User, Policy: ADR 0020), Authz when
    /// none. Without Authz only the platform APIs authorize.
    pub authz: Option<Arc<dyn Authorizer>>,
    pub floor: Option<Arc<dyn Authorizer>>,
}

impl Ports {
    pub(crate) fn graph(&self) -> Result<&dyn graphapi::Port, Error> {
        self.graph
            .as_deref()
            .ok_or_else(|| Error::Message("the built-in connector has no graph".into()))
    }

    pub(crate) fn authz(&self) -> Option<&dyn Authorizer> {
        self.authz.as_deref()
    }
}

/// Connectors returns the built-in connectors the ports allow, by id.
pub fn connectors(p: Ports) -> HashMap<String, Arc<dyn Connector>> {
    let mut out: HashMap<String, Arc<dyn Connector>> = HashMap::new();
    if p.graph.is_some() {
        out.insert(mcpbuiltin::GRAPH.into(), Arc::new(Graph::new(p.clone())));
        out.insert(mcpbuiltin::CHANGE.into(), Arc::new(Change::new(p.clone())));
    }
    if p.engine.is_some() {
        out.insert(
            mcpbuiltin::SCHEDULER.into(),
            Arc::new(Scheduler::new(p.clone())),
        );
    }
    if p.hub.is_some() && p.domains.is_some() && p.methodologies.is_some() && p.graph.is_some() {
        out.insert(mcpbuiltin::ADMIN.into(), Arc::new(Admin::new(p)));
    }
    out
}

/// The errors of the built-in connectors; each is reported to the caller of the tool as the failure of the
/// operation.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// A refusal of the connector itself (a missing argument, a node that is not there...): its whole message.
    #[error("{0}")]
    Message(String),
    /// A refusal of the access gate the connector applies itself: its whole message (Go's `%w` of
    /// `authz.ErrForbidden`).
    #[error("{0}")]
    Forbidden(String),
    /// An operation the connector does not offer (`connectorkit.ErrUnknownOperation`).
    #[error("{op}: {source}")]
    UnknownOperation {
        op: String,
        source: ErrUnknownOperation,
    },
    /// A refusal of the graph.
    #[error(transparent)]
    Graph(#[from] graphapi::Error),
    /// A refusal of the authorizer.
    #[error(transparent)]
    Authz(#[from] authz::Error),
    /// A refusal of a platform service reached over Connect (the engine, the registry).
    #[error(transparent)]
    Rpc(#[from] ConnectError),
    /// A refusal of the review use case.
    #[error(transparent)]
    Review(#[from] goap_review::Error),
    /// A failure of the hub.
    #[error(transparent)]
    Hub(#[from] goap_mcpsvc::Error),
    /// A trace that found nothing.
    #[error(transparent)]
    Brief(#[from] goap_brief::Error),
    /// A failure of the domain registry.
    #[error("{0}")]
    Domains(Box<dyn std::error::Error + Send + Sync>),
    /// A value that does not encode.
    #[error(transparent)]
    Json(#[from] serde_json::Error),
    /// Go's `fmt.Errorf("<message>: %w", err)`.
    #[error("{message}: {source}")]
    Wrapped { message: String, source: Box<Error> },
}

impl Error {
    pub(crate) fn msg(m: impl Into<String>) -> Self {
        Error::Message(m.into())
    }

    /// Whether the operation is unknown (`errors.Is(err, connectorkit.ErrUnknownOperation)`).
    pub fn is_unknown_operation(&self) -> bool {
        match self {
            Error::UnknownOperation { .. } => true,
            Error::Wrapped { source, .. } => source.is_unknown_operation(),
            _ => false,
        }
    }

    /// Whether the call was refused by an authorizer or an access gate (`errors.Is(err, authz.ErrForbidden)`).
    pub fn is_forbidden(&self) -> bool {
        match self {
            Error::Forbidden(_) | Error::Authz(authz::Error::Forbidden(_)) => true,
            Error::Graph(e) => goap_rpcerr_like_forbidden(e),
            Error::Review(goap_review::Error::Forbidden(_)) => true,
            Error::Wrapped { source, .. } => source.is_forbidden(),
            _ => false,
        }
    }
}

/// Whether a graph error carries an authorizer's refusal (the gate of a merge or a split).
fn goap_rpcerr_like_forbidden(e: &graphapi::Error) -> bool {
    e.downcast_ref::<authz::Error>()
        .is_some_and(|a| matches!(a, authz::Error::Forbidden(_)))
}

/// op is an operation of a built-in connector.
pub(crate) struct Op {
    pub name: &'static str,
    pub description: &'static str,
    pub schema: Map<String, Value>,
}

pub(crate) fn op(name: &'static str, description: &'static str, schema: Map<String, Value>) -> Op {
    Op {
        name,
        description,
        schema,
    }
}

pub(crate) fn info(id: &str, description: &str, ops: &[Op]) -> ConnectorInfo {
    ConnectorInfo {
        id: id.into(),
        version: VERSION.into(),
        description: description.into(),
        operations: ops
            .iter()
            .map(|o| Operation {
                name: o.name.into(),
                description: o.description.into(),
                input_schema: Some(goap_pbconv_struct(&o.schema)),
            })
            .collect(),
        ..Default::default()
    }
}

/// structpb.NewStruct: an empty map is an empty Struct, never none.
fn goap_pbconv_struct(m: &Map<String, Value>) -> pbjson_types::Struct {
    goap_pbconv::to_struct(m).unwrap_or_default()
}

/// schema is the input schema of an operation: its properties (all strings unless typed) and the required ones.
pub(crate) fn schema(props: &[(&str, &str)], required: &[&str]) -> Map<String, Value> {
    let mut p = Map::new();
    for (name, typ) in props {
        let mut t = Map::new();
        t.insert("type".into(), Value::String((*typ).into()));
        p.insert((*name).into(), Value::Object(t));
    }
    let mut s = Map::new();
    s.insert("type".into(), Value::String("object".into()));
    s.insert("properties".into(), Value::Object(p));
    if !required.is_empty() {
        s.insert(
            "required".into(),
            Value::Array(
                required
                    .iter()
                    .map(|n| Value::String((*n).into()))
                    .collect(),
            ),
        );
    }
    s
}

/// caller returns the principal the call runs for; the built-in connectors never act anonymously.
pub(crate) fn caller(ctx: &Ctx) -> Result<Principal, Error> {
    let p = authz::principal_from(ctx);
    if p.anonymous() {
        return Err(Error::msg(
            "the built-in connectors act for a caller: the call carries no principal",
        ));
    }
    Ok(p)
}

/// result converts a value to the JSON object an operation returns.
pub(crate) fn result(v: impl Serialize) -> Result<Map<String, Value>, Error> {
    match serde_json::to_value(v)? {
        Value::Object(m) => Ok(m),
        Value::Null => Ok(Map::new()),
        other => Err(Error::msg(format!(
            "the result of the operation is not an object: {other}"
        ))),
    }
}

/// A JSON value of v.
pub(crate) fn json(v: impl Serialize) -> Value {
    serde_json::to_value(v).unwrap_or(Value::Null)
}

/// A slice as Go encodes it when it may be nil: null when empty.
pub(crate) fn nullable<T: Serialize>(v: &[T]) -> Value {
    if v.is_empty() { Value::Null } else { json(v) }
}

/// An object of its fields, in order.
pub(crate) fn obj<const N: usize>(fields: [(&str, Value); N]) -> Map<String, Value> {
    fields
        .into_iter()
        .map(|(k, v)| (k.to_string(), v))
        .collect()
}

/// args reads the arguments of a call.
#[derive(Clone, Copy)]
pub(crate) struct Args<'a>(pub &'a Map<String, Value>);

impl<'a> Args<'a> {
    pub fn get(&self, name: &str) -> Option<&'a Value> {
        self.0.get(name)
    }

    pub fn has(&self, name: &str) -> bool {
        self.0.contains_key(name)
    }

    pub fn str(&self, name: &str) -> String {
        match self.0.get(name) {
            Some(Value::String(s)) => s.trim().to_string(),
            _ => String::new(),
        }
    }

    pub fn required(&self, name: &str) -> Result<String, Error> {
        let s = self.str(name);
        if s.is_empty() {
            return Err(Error::msg(format!("argument {name:?} is required")));
        }
        Ok(s)
    }

    pub fn boolean(&self, name: &str) -> bool {
        matches!(self.0.get(name), Some(Value::Bool(true)))
    }

    pub fn object(&self, name: &str) -> Map<String, Value> {
        match self.0.get(name) {
            Some(Value::Object(m)) => m.clone(),
            _ => Map::new(),
        }
    }

    /// A number argument (JSON numbers are float64 in Go), None when absent or not a number.
    pub fn number(&self, name: &str) -> Option<f64> {
        self.0.get(name).and_then(Value::as_f64)
    }

    pub fn limit(&self) -> usize {
        match self.number("limit") {
            Some(f) if f > 0.0 => f as usize,
            _ => DEFAULT_LIMIT,
        }
    }

    /// unit is the unit a call names, else the unit the call runs for, else the principal's.
    pub fn unit(&self, ctx: &Ctx, p: &Principal) -> String {
        let u = self.str("unit");
        if !u.is_empty() {
            return u;
        }
        let u = goap_mcp::call_from(ctx).unit;
        if !u.is_empty() {
            return u;
        }
        p.org.clone()
    }
}

/// ForwardIdentity is the client interceptor of the platform clients the built-in connectors use: the requests carry
/// the identity of the caller of the tool.
pub fn forward_identity() -> goap_identity::Forward {
    goap_identity::forward()
}

/// headers are the identity headers of the caller, for the platform APIs reached as Connect services.
pub(crate) fn request<T>(who: &Principal, msg: T) -> goap_connect::Request<T> {
    let mut r = goap_connect::Request::new(msg);
    goap_identity::set_headers(who, &mut r.headers);
    r
}

pub(crate) fn unknown(op: &str) -> Error {
    Error::UnknownOperation {
        op: op.into(),
        source: ErrUnknownOperation,
    }
}

/// Boxes an error as a connector's failure.
pub(crate) fn boxed(e: Error) -> goap_connectorkit::InvokeError {
    Box::new(e)
}
