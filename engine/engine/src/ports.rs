//! The ports of the engine: the graph, the methodologies, the publication of its events.

use std::any::Any;
use std::collections::BTreeMap;
use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{BaselineID, ChangeID, Link, Node, split_methodology_ref};
use goap_methodology::Compiled;
use serde::Serialize;
use serde_json::Value;

use crate::error::{Error, Result};

/// GraphPort is what the engine needs from the graph: the graph's contract (graphapi::Port), which the built-in MCPs
/// call too.
pub use goap_graphapi::Port as GraphPort;

/// read_graph is the graph a process reads: the graph of its change on its flow (the active option when it runs on
/// the main flow), else the baseline it started from.
pub(crate) async fn read_graph(
    ctx: &Ctx,
    g: &dyn GraphPort,
    change: &ChangeID,
    flow: &str,
    baseline: &BaselineID,
) -> Result<(Vec<Node>, Vec<Link>)> {
    if !change.is_empty() {
        return Ok(g.change_graph(ctx, change, flow).await?);
    }
    Ok(g.baseline_graph(ctx, baseline).await?)
}

/// MethodologyPort resolves methodologies (the registry): the latest published version of one, or of every
/// methodology. Methodology takes a name (the latest published version) or a reference at a version
/// (domain::methodology_ref, ADR 0113: what a change or a process follows).
#[async_trait]
pub trait MethodologyPort: Send + Sync {
    async fn methodology(&self, ctx: &Ctx, name: &str) -> Result<Arc<Compiled>>;
    async fn list(&self, ctx: &Ctx) -> Result<Vec<Arc<Compiled>>>;
}

/// A value published: typed for the subscribers in process (`downcast`), JSON for the others.
pub struct Payload<'a> {
    any: &'a (dyn Any + Send + Sync),
    to_json: fn(&(dyn Any + Send + Sync)) -> Value,
}

fn json_of<T: Serialize + Any + Send + Sync>(v: &(dyn Any + Send + Sync)) -> Value {
    v.downcast_ref::<T>()
        .and_then(|v| serde_json::to_value(v).ok())
        .unwrap_or(Value::Null)
}

impl<'a> Payload<'a> {
    pub fn new<T: Serialize + Any + Send + Sync>(v: &'a T) -> Payload<'a> {
        Payload {
            any: v,
            to_json: json_of::<T>,
        }
    }

    /// The value, when it is a T (Go's type assertion).
    pub fn downcast<T: Any>(&self) -> Option<&T> {
        self.any.downcast_ref::<T>()
    }

    /// The JSON form of the value.
    pub fn json(&self) -> Value {
        (self.to_json)(self.any)
    }
}

/// Publisher publishes process events (NATS in services).
#[async_trait]
pub trait Publisher: Send + Sync {
    async fn publish(&self, ctx: &Ctx, subject: &str, v: &Payload<'_>) -> Result<()>;
}

/// StaticMethodologies serves compiled methodologies from memory, one version of each, by name.
#[derive(Clone, Default)]
pub struct StaticMethodologies(pub BTreeMap<String, Arc<Compiled>>);

impl StaticMethodologies {
    pub fn new() -> StaticMethodologies {
        StaticMethodologies::default()
    }

    /// Adds a methodology under its name.
    pub fn with(mut self, m: Arc<Compiled>) -> StaticMethodologies {
        self.0.insert(m.name().to_string(), m);
        self
    }

    pub fn insert(&mut self, name: impl Into<String>, m: Arc<Compiled>) {
        self.0.insert(name.into(), m);
    }
}

#[async_trait]
impl MethodologyPort for StaticMethodologies {
    /// A reference naming another version than the one held is unknown.
    async fn methodology(&self, _: &Ctx, r: &str) -> Result<Arc<Compiled>> {
        let (name, version) = split_methodology_ref(r);
        match self.0.get(name) {
            Some(m) if version.is_empty() || m.version() == version => Ok(m.clone()),
            _ => Err(unknown_methodology(r)),
        }
    }

    async fn list(&self, _: &Ctx) -> Result<Vec<Arc<Compiled>>> {
        let mut out: Vec<Arc<Compiled>> = self.0.values().cloned().collect();
        out.sort_by(|a, b| a.name().cmp(b.name()));
        Ok(out)
    }
}

/// ErrUnknownMethodology is returned for unknown methodologies.
pub fn unknown_methodology(name: &str) -> Error {
    Error::UnknownMethodology(format!("unknown methodology {name}"))
}
