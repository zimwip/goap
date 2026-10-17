//! The platform bootstrap every composition runs (ADR 0071).

use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;

use goap_ctx::Ctx;
use goap_graph::{Error, Graph, Result};

use crate::personal::system;
use crate::seed_defaults::{
    seed_access, seed_behaviors, seed_builtins, seed_models, seed_protected_aliases,
};

/// Any error of a hook of a composition.
pub type BoxError = Box<dyn std::error::Error + Send + Sync>;
/// A boxed future, as the hooks return them.
pub type BoxFuture<'a, T> = Pin<Box<dyn Future<Output = T> + Send + 'a>>;

/// The development or demo data of a composition (Go's `func(ctx, *graph.Graph) error`).
pub type DevFn = Arc<
    dyn for<'a> Fn(&'a Ctx, &'a Graph) -> BoxFuture<'a, std::result::Result<(), BoxError>>
        + Send
        + Sync,
>;

/// Makes a [`DevFn`] of a function (`fn dev<'a>(ctx: &'a Ctx, g: &'a Graph) -> BoxFuture<'a, ...>`).
pub fn dev_fn<F>(f: F) -> DevFn
where
    F: for<'a> Fn(&'a Ctx, &'a Graph) -> BoxFuture<'a, std::result::Result<(), BoxError>>
        + Send
        + Sync
        + 'static,
{
    Arc::new(f)
}

/// Options says what differs between the compositions that boot the platform (goap-dev, cmd/graph).
#[derive(Clone, Default)]
pub struct Options {
    /// Models is the model gateway configuration seeded when the graph holds no provider yet (seed_models); empty: no
    /// model is seeded.
    pub models: ModelsConfig,
    /// RequireHooks makes boot panic when the guard-relevant hooks of the graph (authorizer, change_authorizer,
    /// validators) are not set: a composition that serves callers sets it, so that a seed never runs ahead of the
    /// access gating.
    pub require_hooks: bool,
    /// Dev, when set, runs last: the development or demo data of the composition (platform/devseed). The platform
    /// bootstrap never carries it, a production composition leaves it unset.
    pub dev: Option<DevFn>,
}

/// ModelsConfig is the initial configuration of the model gateway.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ModelsConfig {
    pub providers: Vec<goap_llmcfg::Provider>,
    pub models: Vec<goap_llmcfg::Model>,
    pub aliases: Vec<goap_llmcfg::Alias>,
}

/// Report says what boot wrote.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Report {
    pub access: bool,
    pub builtins: bool,
    pub models: bool,
    pub aliases: bool,
    pub behaviors: bool,
}

/// Boot is the platform bootstrap every composition runs, the same steps in the same order (ADR 0071). Ordering
/// contract:
///
///  1. Before boot, the caller has assigned every hook of the graph (authorizer, change_authorizer, validators and,
///     where it holds the registry, sub_change_validator and lifecycles (the guardian of the changes), plus
///     decision_policy and types) and has a type catalogue holding the domains boot and Options::dev write to (the
///     registry seeds the domains: seed_domains, then a reload of the types). Boot writes through those hooks: it
///     never runs ahead of them.
///  2. Boot: Graph::bootstrap (the roots ORG-DEFAULT and PROJ-ROOT, ADR 0054), seed_access (the default policies),
///     seed_builtins (the built-in MCPs, adapter definitions, roles), seed_models (the model gateway configuration),
///     seed_protected_aliases (the assistant and helper aliases, ADR 0084), seed_behaviors (the built-in LLM
///     behaviour, enabled, ADR 0093), then Options::dev if any. Every step is idempotent, so a second call writes
///     nothing.
///  3. After boot, the registry seeds the methodologies (Service::seed): they need the model aliases of seed_models.
///
/// A composition without the registry in process cannot wire sub_change_validator and lifecycles: boot logs one
/// warning saying so.
pub async fn boot(ctx: &Ctx, g: &Graph, o: &Options) -> Result<Report> {
    let mut r = Report::default();
    if o.require_hooks
        && (g.authorizer.is_none() || g.change_authorizer.is_none() || g.validators.is_empty())
    {
        panic!(
            "graphsvc::boot: the graph hooks (authorizer, change_authorizer, validators) must be set before the bootstrap"
        );
    }
    if g.sub_change_validator.is_none() || g.lifecycles.is_none() {
        tracing::warn!(
            "change lifecycle and activity gating are not available in this composition (no registry in process)"
        );
    }
    // the seeds are the platform acting by itself: its transitions are authorized for it
    let ctx = &system(ctx);
    Box::pin(g.bootstrap(ctx))
        .await
        .map_err(|e| e.context("bootstrap"))?;
    r.access = Box::pin(seed_access(ctx, g))
        .await
        .map_err(|e| e.context("seed access"))?;
    r.builtins = Box::pin(seed_builtins(ctx, g))
        .await
        .map_err(|e| e.context("seed built-in MCPs"))?;
    r.models = Box::pin(seed_models(
        ctx,
        g,
        &o.models.providers,
        &o.models.models,
        &o.models.aliases,
    ))
    .await
    .map_err(|e| e.context("seed models"))?;
    r.aliases = Box::pin(seed_protected_aliases(ctx, g))
        .await
        .map_err(|e| e.context("seed protected aliases"))?;
    r.behaviors = Box::pin(seed_behaviors(ctx, g))
        .await
        .map_err(|e| e.context("seed LLM behaviors"))?;
    if let Some(dev) = &o.dev {
        dev(ctx, g)
            .await
            .map_err(|e| boxed_error(e).context("dev seed"))?;
    }
    Ok(r)
}

/// boxed_error turns any error of a hook into an error of the graph, keeping a graph error as it is.
pub(crate) fn boxed_error(e: BoxError) -> Error {
    match e.downcast::<Error>() {
        Ok(ge) => *ge,
        Err(e) => Error::Other {
            message: e.to_string(),
            source: Some(Arc::from(e)),
        },
    }
}
