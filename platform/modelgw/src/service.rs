//! The gateway: what is configured (providers, catalog, quotas, access levels, aliases) is read from the graph, the
//! Router serves the completions and the Store counts the token usage and keeps the ledger.

use std::collections::HashMap;
use std::sync::{Arc, Weak};
use std::time::Duration;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use futures::future::BoxFuture;
use goap_authz as authz;
use goap_ctx::Ctx;
use goap_llm as llm;
use goap_llmcfg as llmcfg;
use parking_lot::RwLock;

use crate::behaviorcost::{COST_MEASURED, CostBook, Price, cost_key_of};
use crate::error::{BoxError, Error};
use crate::ledger::{KIND_COMPLETE, KIND_EMBED};
use crate::protocols::{ModelInfo, Protocol, ProviderSpec, lookup_kind, lookup_protocol};
use crate::router::{Provider, Router, Target, parse_target, quote};
use crate::store::{AliasEntry, ModelEntry, ProviderRecord, Store, period_key};

/// Resolves the reference of a provider's API key.
pub type SecretsFn =
    Arc<dyn Fn(Ctx, String) -> BoxFuture<'static, Result<String, BoxError>> + Send + Sync>;
/// Writes a raw secret to the backing store: path, field, value.
pub type VaultFn = Arc<
    dyn Fn(Ctx, String, String, String) -> BoxFuture<'static, Result<(), BoxError>> + Send + Sync,
>;
/// The clock of the service.
pub type NowFn = Arc<dyn Fn() -> DateTime<Utc> + Send + Sync>;

#[derive(Default)]
struct State {
    snap: Option<Arc<llmcfg::Snapshot>>,
    /// provider -> "" (loaded) | reason it is not
    active: HashMap<String, String>,
}

/// Service is the gateway. Build it with `Service::new`, set the public fields, then share it with `shared` (the
/// background calibration of the behaviour costs needs the shared service).
pub struct Service {
    pub config: Arc<llmcfg::Directory>,
    pub store: Arc<dyn Store>,
    pub router: Router,
    /// Resolves the reference of a provider's API key.
    pub secrets: Option<SecretsFn>,
    /// Writes a raw secret to the backing store (store_provider_key); None when no write support is configured, in
    /// which case store_provider_key fails with a fallback message pointing at env:VAR.
    pub vault: Option<VaultFn>,
    /// Used to list the models of a provider.
    pub http: reqwest::Client,
    pub now: Option<NowFn>,
    /// Resolves the roles a caller holds on its project (ADR 0043) for the models restricted to some roles; None
    /// checks the roles the caller's principal carries.
    pub authz: Option<Arc<dyn authz::Authorizer>>,
    /// Stores the exchange of the calls whose prompt no change log keeps (ADR 0089, GOAP_LLM_CALL_PROMPTS); false
    /// keeps counters only. `new` sets it.
    pub call_prompts: bool,
    /// How long a measured cost of a behaviour is trusted (ADR 0093; GOAP_LLM_BEHAVIOR_COST_TTL_DAYS); zero:
    /// DEFAULT_COST_TTL.
    pub cost_ttl: Duration,
    /// The wait after a failed calibration, zero: DEFAULT_COST_BACKOFF.
    pub cost_backoff: Duration,

    pub(crate) cost: CostBook,
    state: RwLock<State>,
    loading: tokio::sync::Mutex<()>,
    pub(crate) me: Weak<Service>,
}

impl Service {
    /// A service; the router is built from the graph on first use.
    pub fn new(
        config: Arc<llmcfg::Directory>,
        store: Arc<dyn Store>,
        secrets: Option<SecretsFn>,
    ) -> Service {
        Service {
            config,
            store,
            router: Router::new(),
            secrets,
            vault: None,
            http: reqwest::Client::builder()
                .timeout(Duration::from_secs(20))
                .build()
                .unwrap_or_default(),
            now: None,
            authz: None,
            call_prompts: true,
            cost_ttl: Duration::ZERO,
            cost_backoff: Duration::ZERO,
            cost: CostBook::default(),
            state: RwLock::new(State::default()),
            loading: tokio::sync::Mutex::new(()),
            me: Weak::new(),
        }
    }

    /// The service shared, knowing itself (the background calibrations run on it).
    pub fn shared(self) -> Arc<Service> {
        Arc::new_cyclic(|me| {
            let mut s = self;
            s.me = me.clone();
            s
        })
    }

    pub(crate) fn now(&self) -> DateTime<Utc> {
        match &self.now {
            Some(f) => f(),
            None => Utc::now(),
        }
    }

    /// The configuration at the head of the graph, rebuilding the router when it moved. When the graph cannot be
    /// read the last configuration keeps serving; without any, the error is returned.
    pub(crate) async fn sync(&self, ctx: &Ctx) -> Result<Arc<llmcfg::Snapshot>, Error> {
        let snap = match self.config.snapshot(ctx).await {
            Ok(s) => s,
            Err(e) => match e.last {
                Some((s, _)) => s,
                None => return Err(Error::Message(e.error.to_string())),
            },
        };
        if self.same(&snap) {
            return Ok(snap);
        }
        let _g = self.loading.lock().await;
        if self.same(&snap) {
            return Ok(snap);
        }
        self.load(ctx, snap.clone()).await;
        Ok(snap)
    }

    fn same(&self, snap: &Arc<llmcfg::Snapshot>) -> bool {
        self.state
            .read()
            .snap
            .as_ref()
            .is_some_and(|s| Arc::ptr_eq(s, snap))
    }

    /// Rebuilds the router from the graph now.
    pub async fn reload(&self, ctx: &Ctx) -> Result<(), Error> {
        self.sync(ctx).await.map(|_| ())
    }

    /// Builds the router of a snapshot; the caller holds the loading lock. A provider that cannot be built (unknown
    /// protocol, no API key) is reported as inactive, not as an error.
    async fn load(&self, ctx: &Ctx, snap: Arc<llmcfg::Snapshot>) {
        let mut built: HashMap<String, Arc<dyn Provider>> = HashMap::new();
        let mut active = HashMap::new();
        for p in &snap.providers {
            if !p.enabled {
                active.insert(p.name.clone(), "disabled".to_string());
                continue;
            }
            match self.build(ctx, p, "").await {
                Err(e) => {
                    tracing::warn!(provider = %p.name, reason = %e, "provider not loaded");
                    active.insert(p.name.clone(), e);
                }
                Ok(prov) => {
                    built.insert(p.name.clone(), prov);
                    active.insert(p.name.clone(), String::new());
                }
            }
        }
        let models: std::collections::HashSet<String> = snap
            .models
            .iter()
            .map(|m| format!("{}/{}", m.provider, m.model))
            .collect();
        let mut targets = HashMap::new();
        for a in &snap.aliases {
            // a protected alias may point to nothing
            if let Some(t) = parse_target(&a.target)
                && models.contains(&a.target)
            {
                targets.insert(a.alias.clone(), t);
            }
        }
        for pr in &snap.problems {
            tracing::warn!(problem = %pr, "model configuration");
        }
        self.router.replace(built, targets);
        let mut st = self.state.write();
        st.snap = Some(snap);
        st.active = active;
    }

    /// The API key of a provider from its reference.
    pub(crate) async fn key(&self, ctx: &Ctx, p: &ProviderRecord) -> Result<String, BoxError> {
        match &self.secrets {
            Some(f) if !p.api_key_ref.is_empty() => f(ctx.clone(), p.api_key_ref.clone()).await,
            _ => Ok(String::new()),
        }
    }

    /// Writes api_key to the secret store under goap/modelgw/<provider_name> (field api_key) and returns the
    /// reference to store as Provider.api_key_ref. The key is never logged or echoed beyond the returned reference.
    pub async fn store_provider_key(
        &self,
        ctx: &Ctx,
        provider_name: &str,
        api_key: &str,
    ) -> Result<String, Error> {
        if !llmcfg::valid_name(provider_name) {
            return Err(Error::Invalid("invalid provider name".into()));
        }
        if api_key.is_empty() {
            return Err(Error::Invalid("empty key".into()));
        }
        let Some(vault) = &self.vault else {
            return Err(Error::Message(
                "vault is not configured on this server: enter env:VARNAME instead".into(),
            ));
        };
        let path = format!("goap/modelgw/{provider_name}");
        vault(
            ctx.clone(),
            path.clone(),
            "api_key".into(),
            api_key.to_string(),
        )
        .await
        .map_err(|e| Error::Message(e.to_string()))?;
        Ok(format!("{path}#api_key"))
    }

    /// Creates the runtime provider of a record; override_key (when set) replaces the referenced key.
    async fn build(
        &self,
        ctx: &Ctx,
        p: &ProviderRecord,
        override_key: &str,
    ) -> Result<Arc<dyn Provider>, String> {
        let mut key = override_key.to_string();
        if key.is_empty() {
            key = self
                .key(ctx, p)
                .await
                .map_err(|e| format!("API key: {e}"))?;
        }
        if let Some(k) = lookup_kind(&p.kind)
            && k.key_required
            && key.is_empty()
        {
            return Err("no API key".into());
        }
        let proto = protocol_of(&p.protocol)?;
        (proto.new)(ProviderSpec {
            name: p.name.clone(),
            kind: p.kind.clone(),
            protocol: p.protocol.clone(),
            base_url: p.base_url.clone(),
            api_key: key,
        })
    }

    // ---- completions ---------------------------------------------------------

    /// Resolves the model, applies the catalog policy (availability, required roles, global quota) and calls the
    /// provider. Callers without identity are trusted internal services and bypass the role check only. Every call,
    /// refused ones included, is a row of the ledger (ADR 0089).
    pub async fn complete(&self, ctx: &Ctx, req: llm::Request) -> Result<llm::Response, Error> {
        self.complete_as(ctx, req, false).await
    }

    /// complete; calibration marks the gateway's own call measuring a behaviour (ADR 0093, "Measured cost"): no
    /// behaviour applies to it and the roles of the model are not checked, everything else (admission, quota,
    /// ledger) is the normal path.
    pub(crate) async fn complete_as(
        &self,
        ctx: &Ctx,
        mut req: llm::Request,
        calibration: bool,
    ) -> Result<llm::Response, Error> {
        let mut rec = self.begin(ctx, KIND_COMPLETE, &req.model);
        rec.request(&req.system, &req.messages);
        let (t, admitted) = self.admit(ctx, &req.model, calibration).await;
        let (m, period) = match admitted {
            Ok(v) => v,
            Err(e) => {
                self.finish(ctx, rec, &t, 0, 0, Some(e.to_string())).await;
                return Err(e);
            }
        };
        // the global behaviours join the instructions here, after the admission and before the provider (ADR 0093);
        // req is the gateway's own copy, the caller's request is never changed
        let mut ap = llmcfg::Applied {
            system: req.system.clone(),
            ..Default::default()
        };
        let mut price = Price::default();
        if !calibration {
            ap = self.behave(ctx, &req, &t).await;
            price = self.price(ctx, &ap, &t).await;
        }
        req.system = ap.system.clone();
        rec.request(&req.system, &req.messages);
        rec.behaved(&ap, price.tokens, price.estimated);
        let res = self.router.complete(ctx, req).await;
        let mut resp = match &res {
            Ok(r) => r.clone(),
            Err(_) => llm::Response::default(),
        };
        resp.behaviors = behavior_names(&ap);
        resp.behavior_tokens = price.tokens;
        resp.behaviors_estimated = price.estimated;
        rec.answer(resp.transcript());
        self.record_usage(
            ctx,
            &t,
            &m,
            &period,
            resp.usage.input_tokens + resp.usage.output_tokens,
        )
        .await;
        let err = res.as_ref().err();
        self.finish(
            ctx,
            rec,
            &t,
            resp.usage.input_tokens,
            resp.usage.output_tokens,
            err.map(|e| e.to_string()),
        )
        .await;
        match res {
            Ok(_) => Ok(resp),
            Err(e) => Err(Error::Llm(e)),
        }
    }

    /// Embeds texts on the embedding model of the platform (alias "embed" by default) under the same catalog policy
    /// as complete: the model must be enabled, the caller allowed, the quota not exhausted.
    pub async fn embed(
        &self,
        ctx: &Ctx,
        mut req: llm::EmbedRequest,
    ) -> Result<llm::EmbedResponse, Error> {
        if req.model.is_empty() {
            req.model = llm::EMBED_ALIAS.to_string();
        }
        let mut rec = self.begin(ctx, KIND_EMBED, &req.model);
        rec.request_embed(&req.texts);
        let (t, admitted) = self.admit(ctx, &req.model, false).await;
        let (m, period) = match admitted {
            Ok(v) => v,
            Err(e) => {
                self.finish(ctx, rec, &t, 0, 0, Some(e.to_string())).await;
                return Err(e);
            }
        };
        let res = self.router.embed(ctx, req).await;
        let tokens = res.as_ref().map(|r| r.tokens).unwrap_or(0);
        if let Ok(r) = &res
            && let Some(v) = r.vectors.first()
        {
            rec.answer(format!(
                "{} vectors of {} dimensions",
                r.vectors.len(),
                v.len()
            ));
        }
        self.record_usage(ctx, &t, &m, &period, tokens).await;
        let err = res.as_ref().err();
        self.finish(ctx, rec, &t, tokens, 0, err.map(|e| e.to_string()))
            .await;
        res.map_err(Error::Llm)
    }

    /// Resolves a model and applies the catalog policy (availability, required roles, global quota); internal skips
    /// the roles only (the gateway's own calibration calls). The target is the resolved one, empty when the model
    /// did not resolve.
    pub(crate) async fn admit(
        &self,
        ctx: &Ctx,
        model: &str,
        internal: bool,
    ) -> (Target, Result<(ModelEntry, String), Error>) {
        if let Err(e) = self.sync(ctx).await {
            return (Target::default(), Err(e));
        }
        let t = match self.router.resolve(model) {
            Ok((t, _)) => t,
            Err(e) => return (Target::default(), Err(Error::Invalid(e.to_string()))),
        };
        let r = self
            .admit_model(ctx, &t, &authz::principal_from(ctx), internal)
            .await;
        (t, r)
    }

    /// Applies the catalog policy to a resolved model: enabled in the catalog, the roles held by who (unless
    /// internal), the quota not exhausted. It returns the entry and the period of its quota.
    pub(crate) async fn admit_model(
        &self,
        ctx: &Ctx,
        t: &Target,
        who: &authz::Principal,
        internal: bool,
    ) -> Result<(ModelEntry, String), Error> {
        let snap = self.sync(ctx).await?;
        let m = find_model(&snap, &t.provider, &t.model);
        let m = match m {
            Some(m) if m.enabled => m,
            _ => {
                return Err(Error::ModelDisabled(format!(
                    "{}/{} is not in the platform catalog or is disabled",
                    t.provider, t.model
                )));
            }
        };
        if !internal && !self.allowed(ctx, who, &m).await {
            return Err(Error::Forbidden(format!(
                "{}/{} requires one of the roles {}",
                t.provider,
                t.model,
                m.roles.join(", ")
            )));
        }
        let period = period_key(&m.quota_period, self.now());
        if m.quota_tokens > 0 {
            let used = self.store.usage(ctx, &m.key(), &period).await?;
            if used >= m.quota_tokens {
                return Err(Error::QuotaExceeded(format!(
                    "{}/{} used {} of {} tokens ({})",
                    t.provider, t.model, used, m.quota_tokens, m.quota_period
                )));
            }
        }
        Ok((m, period))
    }

    pub(crate) async fn record_usage(
        &self,
        ctx: &Ctx,
        t: &Target,
        m: &ModelEntry,
        period: &str,
        tokens: i64,
    ) {
        if tokens > 0
            && let Err(e) = self
                .store
                .add_usage(&ctx.without_cancel(), &m.key(), period, tokens)
                .await
        {
            tracing::error!(model = %format!("{}/{}", t.provider, t.model), err = %e, "usage not recorded");
        }
    }

    /// Applies the access level of a catalog model to a caller. Callers without identity are trusted internal
    /// services. The roles a model requires are held on the caller's project (ADR 0043): resolved by authz
    /// (resource model, act use) when set.
    pub(crate) async fn allowed(&self, ctx: &Ctx, p: &authz::Principal, m: &ModelEntry) -> bool {
        if p.anonymous() || m.roles.is_empty() || p.roles.iter().any(|r| r == "admin") {
            return true;
        }
        if let Some(a) = &self.authz {
            let req = authz::Request {
                subject: p.clone(),
                action: "use".into(),
                resource: authz::Resource {
                    r#type: "model".into(),
                    name: m.key(),
                    project_id: p.project.clone(),
                    roles: m.roles.clone(),
                    ..Default::default()
                },
            };
            return matches!(a.authorize(ctx, &req).await, Ok(true));
        }
        m.roles.iter().any(|r| p.roles.contains(r))
    }

    /// The models the caller may use (enabled catalog models of a loaded provider, with the required role) and the
    /// aliases pointing to them.
    pub async fn available(&self, ctx: &Ctx) -> Result<(Vec<ModelEntry>, Vec<AliasEntry>), Error> {
        let snap = self.sync(ctx).await?;
        let p = authz::principal_from(ctx);
        let active = self.state.read().active.clone();
        let mut ok = std::collections::HashSet::new();
        let mut models = Vec::new();
        for m in &snap.models {
            let loaded = active.get(&m.provider).is_some_and(|r| r.is_empty());
            if m.enabled && loaded && self.allowed(ctx, &p, m).await {
                models.push(m.clone());
                ok.insert(format!("{}/{}", m.provider, m.model));
            }
        }
        let aliases = snap
            .aliases
            .iter()
            .filter(|a| ok.contains(&a.target))
            .cloned()
            .collect();
        Ok((models, aliases))
    }

    // ---- administration (read-only: the configuration is changed through changes on the graph) ------

    /// The configured providers.
    pub async fn providers(&self, ctx: &Ctx) -> Result<Vec<ProviderView>, Error> {
        let snap = self.sync(ctx).await?;
        let st = self.state.read();
        Ok(snap
            .providers
            .iter()
            .map(|p| {
                let reason = st.active.get(&p.name);
                ProviderView {
                    provider: p.clone(),
                    has_key: !p.api_key_ref.is_empty(),
                    active: reason.is_some_and(|r| r.is_empty()),
                    reason: reason.cloned().unwrap_or_default(),
                }
            })
            .collect())
    }

    /// Asks a provider for its models. The spec need not exist on the graph; an empty api_key resolves the key from
    /// the reference of the spec, or of the provider of that name.
    pub async fn discover(
        &self,
        ctx: &Ctx,
        mut inp: ProviderRecord,
        api_key: &str,
    ) -> Result<Vec<Discovered>, Error> {
        let snap = self.sync(ctx).await?;
        let stored = snap.providers.iter().rfind(|p| p.name == inp.name).cloned();
        if let Some(k) = lookup_kind(&inp.kind) {
            if inp.protocol.is_empty() {
                inp.protocol = k.protocol.clone();
            }
            if inp.base_url.is_empty() {
                inp.base_url = k.default_base_url.clone();
            }
        }
        if inp.api_key_ref.is_empty()
            && let Some(s) = &stored
        {
            inp.api_key_ref = s.api_key_ref.clone();
        }
        let mut api_key = api_key.to_string();
        if api_key.is_empty() {
            api_key = self
                .key(ctx, &inp)
                .await
                .map_err(|e| Error::Invalid(format!("API key: {e}")))?;
        }
        let proto = protocol_of(&inp.protocol).map_err(Error::Invalid)?;
        let spec = ProviderSpec {
            name: inp.name.clone(),
            kind: inp.kind.clone(),
            protocol: inp.protocol.clone(),
            base_url: inp.base_url.clone(),
            api_key,
        };
        let models = (proto.list)(ctx.clone(), self.http.clone(), spec)
            .await
            .map_err(|e| Error::Llm(e).context("list models"))?;
        let have: std::collections::HashSet<&str> = snap
            .models
            .iter()
            .filter(|e| e.provider == inp.name)
            .map(|e| e.model.as_str())
            .collect();
        let mut out: Vec<Discovered> = models
            .into_iter()
            .map(|m| Discovered {
                registered: have.contains(m.id.as_str()),
                model: m,
            })
            .collect();
        out.sort_by(|a, b| a.model.id.cmp(&b.model.id));
        Ok(out)
    }

    /// The catalog with the usage of each model in its current period, and the aliases.
    pub async fn catalog(&self, ctx: &Ctx) -> Result<(Vec<CatalogEntry>, Vec<AliasEntry>), Error> {
        let snap = self.sync(ctx).await?;
        let mut out = Vec::with_capacity(snap.models.len());
        for m in &snap.models {
            let used = self
                .store
                .usage(ctx, &m.key(), &period_key(&m.quota_period, self.now()))
                .await?;
            out.push(CatalogEntry {
                model: m.clone(),
                used,
            });
        }
        Ok((out, snap.aliases.clone()))
    }

    // ---- global behaviours (ADR 0093) ---------------------------------------------

    /// Applies the behaviours of the configuration to a completion resolved to t. A configuration that cannot be
    /// read applies none: a style never fails a call.
    async fn behave(&self, ctx: &Ctx, req: &llm::Request, t: &Target) -> llmcfg::Applied {
        let Ok(snap) = self.sync(ctx).await else {
            return llmcfg::Applied {
                system: req.system.clone(),
                ..Default::default()
            };
        };
        let ap = snap.apply(
            &req.system,
            &call_info(&llm::meta_from(ctx).source, &req.model, t, req.json),
        );
        if !ap.skipped.is_empty() {
            tracing::warn!(skipped = ?ap.skipped, cap_bytes = llmcfg::MAX_BEHAVIOR_BYTES,
                model = %format!("{}/{}", t.provider, t.model), "behaviors dropped by the cap");
        }
        ap
    }

    /// The behaviours of the configuration, the enabled and the disabled ones, by order then name.
    pub async fn behaviors(&self, ctx: &Ctx) -> Result<Vec<llmcfg::Behavior>, Error> {
        let snap = self.sync(ctx).await?;
        let mut all: Vec<llmcfg::Behavior> = snap
            .behaviors
            .iter()
            .chain(snap.off.iter())
            .cloned()
            .collect();
        all.sort_by(|a, b| a.order.cmp(&b.order).then_with(|| a.name.cmp(&b.name)));
        Ok(all)
    }

    /// The system text a completion of the given alias, source and mode would be sent with, and which behaviours
    /// made it, without calling any model. An alias that resolves to nothing is Invalid.
    pub async fn preview_behaviors(
        &self,
        ctx: &Ctx,
        alias: &str,
        source: &str,
        json: bool,
        system: &str,
    ) -> Result<(llmcfg::Applied, Price), Error> {
        let snap = self.sync(ctx).await?;
        let (t, _) = self
            .router
            .resolve(alias)
            .map_err(|e| Error::Invalid(e.to_string()))?;
        let source = llm::CallMeta {
            source: source.to_string(),
            ..Default::default()
        }
        .clean()
        .source;
        let ap = snap.apply(system, &call_info(&source, alias, &t, json));
        // the preview reads the costs, it never asks for a calibration
        let mut p = Price::default();
        for u in &ap.uses {
            match self.cached_cost(ctx, &cost_key_of(u, &t)).await {
                Some(c) => {
                    p.tokens += c.tokens;
                    p.estimated = p.estimated || c.source != COST_MEASURED;
                }
                None => {
                    p.tokens += llmcfg::estimate_tokens(u.bytes) as i64;
                    p.estimated = true;
                }
            }
        }
        Ok((ap, p))
    }
}

fn protocol_of(id: &str) -> Result<Protocol, String> {
    lookup_protocol(id).ok_or_else(|| format!("unknown protocol {}", quote(id)))
}

fn find_model(snap: &llmcfg::Snapshot, provider: &str, model: &str) -> Option<ModelEntry> {
    snap.models
        .iter()
        .find(|m| m.provider == provider && m.model == model)
        .cloned()
}

/// The call as the behaviours match it: the alias is the name requested ("default" when none, "" for a literal
/// provider/model, as the ledger names it).
pub(crate) fn call_info(source: &str, requested: &str, t: &Target, json: bool) -> llmcfg::CallInfo {
    let mut alias = if requested.is_empty() {
        "default".to_string()
    } else {
        requested.to_string()
    };
    if alias.contains('/') {
        alias = String::new();
    }
    llmcfg::CallInfo {
        alias,
        provider: t.provider.clone(),
        model: t.model.clone(),
        source: source.to_string(),
        kind: llmcfg::KIND_COMPLETE.to_string(),
        json,
    }
}

/// The behaviours of a call for the ledger and the caller: the applied ones, then the dropped ones prefixed "!".
pub(crate) fn behavior_names(ap: &llmcfg::Applied) -> Vec<String> {
    let mut out = ap.names.clone();
    out.extend(ap.skipped.iter().map(|n| format!("!{n}")));
    out
}

/// A provider as shown to administrators.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ProviderView {
    pub provider: ProviderRecord,
    /// The provider references an API key.
    pub has_key: bool,
    pub active: bool,
    /// Why it is not active.
    pub reason: String,
}

/// A model reported by a provider.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Discovered {
    pub model: ModelInfo,
    pub registered: bool,
}

/// A catalog model with its usage in the current period.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct CatalogEntry {
    pub model: ModelEntry,
    pub used: i64,
}

#[async_trait]
impl llm::Client for Service {
    async fn complete(&self, ctx: &Ctx, req: llm::Request) -> Result<llm::Response, llm::Error> {
        Service::complete(self, ctx, req)
            .await
            .map_err(Error::into_llm)
    }
}

#[async_trait]
impl llm::Embedder for Service {
    async fn embed(
        &self,
        ctx: &Ctx,
        req: llm::EmbedRequest,
    ) -> Result<llm::EmbedResponse, llm::Error> {
        Service::embed(self, ctx, req)
            .await
            .map_err(Error::into_llm)
    }
}
