//! The measured cost of a behaviour (ADR 0093, "Measured cost"): the input tokens a model reports for a minimal
//! request carrying the instruction as its system text, minus those it reports for the same request with no system
//! text (the baseline of that model). The estimate (four bytes a token) serves until a measure exists.

use std::collections::{HashMap, HashSet};
use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;
use std::time::Duration;

use chrono::{DateTime, TimeDelta, Utc};
use goap_authz as authz;
use goap_ctx::Ctx;
use goap_llm as llm;
use goap_llmcfg as llmcfg;

use crate::error::Error;
use crate::router::{Target, parse_target};
use crate::service::Service;

/// The difference of the input tokens the provider reported.
pub const COST_MEASURED: &str = "measured";
/// The provider reported no usable difference, so the byte estimate is kept; not measured again before the TTL.
pub const COST_ESTIMATED: &str = "estimated";

/// Defaults of the calibration.
pub const DEFAULT_COST_TTL: Duration = Duration::from_secs(30 * 24 * 3600);
pub const DEFAULT_COST_BACKOFF: Duration = Duration::from_secs(10 * 60);
/// The calibration calls in flight.
const COST_WORKERS: usize = 2;
/// How long the cache of the store is trusted (another instance may have measured).
const COST_RELOAD: TimeDelta = TimeDelta::minutes(1);
/// Bounds one calibration call.
const CALIBRATION_TIMEOUT: Duration = Duration::from_secs(60);
/// The user message of a calibration request.
pub(crate) const CALIBRATION_TEXT: &str = ".";

/// CostKey identifies a cost: valid for one instruction text on one model.
#[derive(Debug, Clone, Default, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub struct CostKey {
    pub behavior: String,
    pub hash: String,
    pub provider: String,
    pub model: String,
}

/// The stored cost of a behaviour on a model.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct BehaviorCost {
    pub key: CostKey,
    pub tokens: i64,
    pub baseline: i64,
    pub measured_at: DateTime<Utc>,
    /// COST_MEASURED or COST_ESTIMATED.
    pub source: String,
}

/// The input tokens of the calibration request with no instruction, on a model.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ModelBaseline {
    pub provider: String,
    pub model: String,
    pub tokens: i64,
    pub measured_at: DateTime<Utc>,
}

#[derive(Default)]
pub(crate) struct CostState {
    loaded: Option<DateTime<Utc>>,
    costs: Option<HashMap<CostKey, BehaviorCost>>,
    bases: HashMap<(String, String), ModelBaseline>,
    inflight: HashSet<CostKey>,
    /// not before
    failed: HashMap<CostKey, DateTime<Utc>>,
}

/// The in-memory side of the cost: a cache of the store, the calibrations in flight and the failures to wait out.
pub(crate) struct CostBook {
    mu: tokio::sync::Mutex<CostState>,
    sem: Arc<tokio::sync::Semaphore>,
    /// one baseline call at a time
    base_mu: tokio::sync::Mutex<()>,
    /// the calibrations in flight
    pending: tokio::sync::watch::Sender<usize>,
}

impl Default for CostBook {
    fn default() -> Self {
        CostBook {
            mu: tokio::sync::Mutex::new(CostState::default()),
            sem: Arc::new(tokio::sync::Semaphore::new(COST_WORKERS)),
            base_mu: tokio::sync::Mutex::new(()),
            pending: tokio::sync::watch::Sender::new(0),
        }
    }
}

/// Price is the tokens the behaviours added to a call: the measured cost of each where there is one, its byte
/// estimate otherwise (estimated is then true).
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Price {
    pub tokens: i64,
    pub estimated: bool,
}

pub(crate) fn cost_key_of(u: &llmcfg::Use, t: &Target) -> CostKey {
    CostKey {
        behavior: u.name.clone(),
        hash: llmcfg::instruction_hash(&u.instruction),
        provider: t.provider.clone(),
        model: t.model.clone(),
    }
}

/// The arithmetic of a measure: the tokens of the instruction request minus the baseline, never negative. A provider
/// that reported no usage (a zero), or none between the two requests, gives no measure: the byte estimate is kept
/// and the source says so.
pub(crate) fn cost_of(
    with_instruction: i64,
    baseline: i64,
    instruction: &str,
) -> (i64, &'static str) {
    let d = with_instruction - baseline;
    if baseline > 0 && with_instruction > 0 && d > 0 {
        return (d, COST_MEASURED);
    }
    (
        llmcfg::estimate_tokens(instruction.len() + 2) as i64,
        COST_ESTIMATED,
    )
}

fn delta(d: Duration) -> TimeDelta {
    TimeDelta::from_std(d).unwrap_or(TimeDelta::MAX)
}

impl Service {
    fn cost_ttl_or_default(&self) -> Duration {
        if self.cost_ttl.is_zero() {
            DEFAULT_COST_TTL
        } else {
            self.cost_ttl
        }
    }

    fn cost_backoff_or_default(&self) -> Duration {
        if self.cost_backoff.is_zero() {
            DEFAULT_COST_BACKOFF
        } else {
            self.cost_backoff
        }
    }

    /// Waits until the calibrations in flight are over (tests, shutdown).
    pub async fn wait_costs(&self) {
        let mut rx = self.cost.pending.subscribe();
        let _ = rx.wait_for(|n| *n == 0).await;
    }

    /// Refreshes the cache from the store when it is older than COST_RELOAD. The caller holds the lock.
    async fn load_costs(&self, ctx: &Ctx, b: &mut CostState) {
        if b.costs.is_some() && b.loaded.is_some_and(|l| self.now() - l < COST_RELOAD) {
            return;
        }
        let costs = match self.store.behavior_costs(ctx).await {
            Ok(c) => c,
            Err(e) => {
                tracing::warn!(err = %e, "behavior costs not read");
                if b.costs.is_none() {
                    b.costs = Some(HashMap::new());
                    b.bases = HashMap::new();
                }
                return;
            }
        };
        let bases = match self.store.model_baselines(ctx).await {
            Ok(b) => b,
            Err(e) => {
                tracing::warn!(err = %e, "model baselines not read");
                Vec::new()
            }
        };
        b.costs = Some(costs.into_iter().map(|c| (c.key.clone(), c)).collect());
        b.bases = bases
            .into_iter()
            .map(|m| ((m.provider.clone(), m.model.clone()), m))
            .collect();
        b.loaded = Some(self.now());
    }

    /// The cost stored for a key.
    pub(crate) async fn cached_cost(&self, ctx: &Ctx, k: &CostKey) -> Option<BehaviorCost> {
        let mut b = self.cost.mu.lock().await;
        self.load_costs(ctx, &mut b).await;
        b.costs.as_ref().and_then(|c| c.get(k).cloned())
    }

    fn fresh(&self, c: &BehaviorCost) -> bool {
        self.now() - c.measured_at < delta(self.cost_ttl_or_default())
    }

    /// Prices the behaviours applied to a call served by t and, for each one without a fresh cost, asks for a
    /// calibration in the background. It reads the cache, never calls a model, and never fails.
    pub(crate) async fn price(&self, ctx: &Ctx, ap: &llmcfg::Applied, t: &Target) -> Price {
        let mut p = Price::default();
        for u in &ap.uses {
            let k = cost_key_of(u, t);
            let c = self.cached_cost(ctx, &k).await;
            match &c {
                Some(c) => {
                    p.tokens += c.tokens;
                    p.estimated = p.estimated || c.source != COST_MEASURED;
                }
                None => {
                    p.tokens += llmcfg::estimate_tokens(u.bytes) as i64;
                    p.estimated = true;
                }
            }
            if c.as_ref().is_none_or(|c| !self.fresh(c)) {
                self.enqueue_cost(k, u.instruction.clone()).await;
            }
        }
        p
    }

    /// Measures a cost in the background: once per key at a time, never again before the backoff of a failure is
    /// over, with at most COST_WORKERS calls in flight. The caller does not wait and never sees an error. A service
    /// that was not shared (`Service::shared`) calibrates nothing.
    async fn enqueue_cost(&self, k: CostKey, instruction: String) {
        let Some(me) = self.me.upgrade() else {
            return;
        };
        {
            let mut b = self.cost.mu.lock().await;
            if b.inflight.contains(&k) || b.failed.get(&k).is_some_and(|nb| self.now() < *nb) {
                return;
            }
            b.inflight.insert(k.clone());
        }
        self.cost.pending.send_modify(|n| *n += 1);
        tokio::spawn(async move {
            let sem = me.cost.sem.clone();
            let permit = sem.acquire_owned().await;
            let (ctx, _cancel) = Ctx::background().with_timeout(CALIBRATION_TIMEOUT);
            let res = me.measure(&ctx, &k, &instruction, false).await;
            drop(permit);
            {
                let mut b = me.cost.mu.lock().await;
                b.inflight.remove(&k);
                if res.is_err() {
                    let nb = me.now() + delta(me.cost_backoff_or_default());
                    b.failed.insert(k.clone(), nb);
                }
            }
            match res {
                Err(e) => {
                    tracing::info!(behavior = %k.behavior, model = %format!("{}/{}", k.provider, k.model),
                    retry_after = ?me.cost_backoff_or_default(), err = %e, "behavior cost not measured")
                }
                Ok(_) => me.prune_costs(&Ctx::background()).await,
            }
            me.cost.pending.send_modify(|n| *n -= 1);
        });
    }

    /// One calibration call: the input tokens the provider reports. It goes through the normal path of the gateway
    /// (admission, quota, ledger: source "calibration", subject system:modelgw) but no behaviour applies to it and
    /// the roles of the model do not (it is the gateway's own call).
    fn calibrate<'a>(
        &'a self,
        ctx: &'a Ctx,
        t: &'a Target,
        system: &'a str,
    ) -> Pin<Box<dyn Future<Output = Result<i64, Error>> + Send + 'a>> {
        Box::pin(async move {
            let ctx = authz::with_principal(ctx, authz::system("modelgw", &[]));
            let ctx = llm::with_meta(
                &ctx,
                llm::CallMeta {
                    source: llm::SOURCE_CALIBRATION.into(),
                    ..Default::default()
                },
            );
            let req = llm::Request {
                model: format!("{}/{}", t.provider, t.model),
                system: system.to_string(),
                max_tokens: 1,
                messages: vec![llm::Message {
                    role: "user".into(),
                    content: CALIBRATION_TEXT.into(),
                    ..Default::default()
                }],
                ..Default::default()
            };
            let resp = self.complete_as(&ctx, req, true).await?;
            Ok(resp.usage.input_tokens)
        })
    }

    /// The input tokens of the request with no instruction on a model: stored, measured once per TTL (force: now).
    async fn baseline(&self, ctx: &Ctx, t: &Target, force: bool) -> Result<i64, Error> {
        let _g = self.cost.base_mu.lock().await;
        let key = (t.provider.clone(), t.model.clone());
        if !force {
            let mut b = self.cost.mu.lock().await;
            self.load_costs(ctx, &mut b).await;
            if let Some(m) = b.bases.get(&key)
                && self.now() - m.measured_at < delta(self.cost_ttl_or_default())
            {
                return Ok(m.tokens);
            }
        }
        let n = self.calibrate(ctx, t, "").await?;
        let b = ModelBaseline {
            provider: t.provider.clone(),
            model: t.model.clone(),
            tokens: n,
            measured_at: self.now(),
        };
        if let Err(e) = self.store.put_model_baseline(ctx, b.clone()).await {
            tracing::warn!(model = %format!("{}/{}", t.provider, t.model), err = %e, "model baseline not stored");
        }
        self.cost.mu.lock().await.bases.insert(key, b);
        Ok(n)
    }

    /// Calibrates one cost and stores it (force: measures the baseline again too). Nothing is stored when a call
    /// fails (the quota of the model is spent, the provider is down): the estimate stays.
    pub(crate) async fn measure(
        &self,
        ctx: &Ctx,
        k: &CostKey,
        instruction: &str,
        force: bool,
    ) -> Result<BehaviorCost, Error> {
        let t = Target {
            provider: k.provider.clone(),
            model: k.model.clone(),
        };
        let base = self
            .baseline(ctx, &t, force)
            .await
            .map_err(|e| e.context("baseline"))?;
        let input = self.calibrate(ctx, &t, instruction).await?;
        let (tokens, source) = cost_of(input, base, instruction);
        let c = BehaviorCost {
            key: k.clone(),
            tokens,
            baseline: base,
            measured_at: self.now(),
            source: source.to_string(),
        };
        self.store
            .put_behavior_cost(ctx, c.clone())
            .await
            .map_err(|e| e.context("store"))?;
        let mut b = self.cost.mu.lock().await;
        b.costs
            .get_or_insert_with(HashMap::new)
            .insert(k.clone(), c.clone());
        b.failed.remove(k);
        Ok(c)
    }

    /// Deletes, lazily after a measure, the costs of an instruction that changed, of a behaviour that left, and of a
    /// model that left the catalog, and the baselines of those models.
    pub(crate) async fn prune_costs(&self, ctx: &Ctx) {
        let Ok(snap) = self.sync(ctx).await else {
            return;
        };
        let mut hashes: HashMap<&str, HashSet<String>> = HashMap::new();
        for b in snap.behaviors.iter().chain(snap.off.iter()) {
            hashes
                .entry(b.name.as_str())
                .or_default()
                .insert(llmcfg::instruction_hash(&b.instruction));
        }
        let models: HashSet<String> = snap
            .models
            .iter()
            .map(|m| format!("{}/{}", m.provider, m.model))
            .collect();
        let (stale, stale_bases) = {
            let mut b = self.cost.mu.lock().await;
            self.load_costs(ctx, &mut b).await;
            let stale: Vec<CostKey> = b
                .costs
                .as_ref()
                .map(|c| {
                    c.keys()
                        .filter(|k| {
                            !hashes
                                .get(k.behavior.as_str())
                                .is_some_and(|h| h.contains(&k.hash))
                                || !models.contains(&format!("{}/{}", k.provider, k.model))
                        })
                        .cloned()
                        .collect()
                })
                .unwrap_or_default();
            let stale_bases: Vec<(String, String)> = b
                .bases
                .keys()
                .filter(|k| !models.contains(&format!("{}/{}", k.0, k.1)))
                .cloned()
                .collect();
            if let Some(c) = b.costs.as_mut() {
                for k in &stale {
                    c.remove(k);
                }
            }
            for k in &stale_bases {
                b.bases.remove(k);
            }
            (stale, stale_bases)
        };
        if !stale.is_empty()
            && let Err(e) = self.store.delete_behavior_costs(ctx, &stale).await
        {
            tracing::warn!(err = %e, "behavior costs not purged");
        }
        if !stale_bases.is_empty()
            && let Err(e) = self.store.delete_model_baselines(ctx, &stale_bases).await
        {
            tracing::warn!(err = %e, "model baselines not purged");
        }
    }
}

// ---- read side --------------------------------------------------------------

/// The cost of a behaviour on a model, as the administrators see it.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct CostView {
    pub behavior: String,
    pub provider: String,
    pub model: String,
    /// The aliases of the platform resolving to the model that the behaviour applies to.
    pub aliases: Vec<String>,
    pub tokens: i64,
    pub baseline: i64,
    pub source: String,
    /// None: never measured (the estimate).
    pub measured_at: Option<DateTime<Utc>>,
    /// Why a measure asked for failed.
    pub error: String,
}

/// A behaviour with its costs.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct BehaviorView {
    pub behavior: llmcfg::Behavior,
    pub costs: Vec<CostView>,
}

#[derive(Debug, Clone)]
pub(crate) struct CostTarget {
    pub t: Target,
    pub aliases: Vec<String>,
}

/// The models a behaviour can apply to: the enabled catalog models reached by an alias that the behaviour's alias
/// and model selectors accept, and the models its selector names without an alias.
pub(crate) fn behavior_targets(snap: &llmcfg::Snapshot, b: &llmcfg::Behavior) -> Vec<CostTarget> {
    let enabled: HashSet<String> = snap
        .models
        .iter()
        .filter(|m| m.enabled)
        .map(|m| format!("{}/{}", m.provider, m.model))
        .collect();
    let mut by: HashMap<String, CostTarget> = HashMap::new();
    let mut add = |target: &str, alias: &str| {
        let Some(t) = parse_target(target) else {
            return;
        };
        if !enabled.contains(target) {
            return;
        }
        let ct = by.entry(target.to_string()).or_insert_with(|| CostTarget {
            t,
            aliases: Vec::new(),
        });
        if !alias.is_empty() && !ct.aliases.iter().any(|a| a == alias) {
            ct.aliases.push(alias.to_string());
        }
    };
    let within = |list: &[String], v: &str| list.is_empty() || list.iter().any(|x| x == v);
    for a in &snap.aliases {
        if within(&b.aliases, &a.alias) && within(&b.models, &a.target) {
            add(&a.target, &a.alias);
        }
    }
    if b.aliases.is_empty() {
        for m in &b.models {
            add(m, "");
        }
    }
    let mut out: Vec<CostTarget> = by
        .into_values()
        .map(|mut ct| {
            ct.aliases.sort();
            ct
        })
        .collect();
    out.sort_by(|x, y| {
        format!("{}/{}", x.t.provider, x.t.model).cmp(&format!("{}/{}", y.t.provider, y.t.model))
    });
    out
}

impl Service {
    /// The cost of a behaviour on a model: the stored one, else the byte estimate.
    async fn cost_view(&self, ctx: &Ctx, b: &llmcfg::Behavior, ct: &CostTarget) -> CostView {
        let mut v = CostView {
            behavior: b.name.clone(),
            provider: ct.t.provider.clone(),
            model: ct.t.model.clone(),
            aliases: ct.aliases.clone(),
            ..Default::default()
        };
        let k = CostKey {
            behavior: b.name.clone(),
            hash: llmcfg::instruction_hash(&b.instruction),
            provider: ct.t.provider.clone(),
            model: ct.t.model.clone(),
        };
        if let Some(c) = self.cached_cost(ctx, &k).await {
            v.tokens = c.tokens;
            v.baseline = c.baseline;
            v.source = c.source;
            v.measured_at = Some(c.measured_at);
            return v;
        }
        v.tokens = llmcfg::estimate_tokens(b.instruction.trim().len() + 2) as i64;
        v.source = COST_ESTIMATED.into();
        v
    }

    /// The behaviours with their cost on each model they apply to.
    pub async fn behavior_views(&self, ctx: &Ctx) -> Result<Vec<BehaviorView>, Error> {
        let list = self.behaviors(ctx).await?;
        let snap = self.sync(ctx).await?;
        let mut out = Vec::with_capacity(list.len());
        for b in list {
            let mut costs = Vec::new();
            for ct in behavior_targets(&snap, &b) {
                costs.push(self.cost_view(ctx, &b, &ct).await);
            }
            out.push(BehaviorView { behavior: b, costs });
        }
        Ok(out)
    }

    /// Measures now the cost of the named behaviours (none named: all, disabled ones included, so an administrator
    /// sees the cost before enabling) on the named provider/models (none: every model they apply to), whatever the
    /// age of the stored costs and the backoff of an earlier failure. A call that fails is reported on its row (the
    /// estimate is kept), not as an error.
    pub async fn measure_behaviors(
        &self,
        ctx: &Ctx,
        names: &[String],
        models: &[String],
    ) -> Result<Vec<CostView>, Error> {
        let list = self.behaviors(ctx).await?;
        let snap = self.sync(ctx).await?;
        let mut out = Vec::new();
        for b in &list {
            if !names.is_empty() && !names.contains(&b.name) {
                continue;
            }
            for ct in behavior_targets(&snap, b) {
                if !models.is_empty()
                    && !models.contains(&format!("{}/{}", ct.t.provider, ct.t.model))
                {
                    continue;
                }
                let k = CostKey {
                    behavior: b.name.clone(),
                    hash: llmcfg::instruction_hash(&b.instruction),
                    provider: ct.t.provider.clone(),
                    model: ct.t.model.clone(),
                };
                let mut v = self.cost_view(ctx, b, &ct).await;
                match self.measure(ctx, &k, b.instruction.trim(), true).await {
                    Err(e) => {
                        v.error = e.to_string();
                        if e.is_quota_exceeded() {
                            v.error = format!("quota exhausted: {}", v.error);
                        }
                    }
                    Ok(c) => {
                        v.tokens = c.tokens;
                        v.baseline = c.baseline;
                        v.source = c.source;
                        v.measured_at = Some(c.measured_at);
                    }
                }
                out.push(v);
            }
        }
        self.prune_costs(ctx).await;
        Ok(out)
    }
}
