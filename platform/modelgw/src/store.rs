//! The state of the gateway: the quota counters, the ledger of calls and the measured costs of the behaviours.

use std::collections::HashMap;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_llmcfg as llmcfg;
use parking_lot::Mutex;

use crate::behaviorcost::{BehaviorCost, CostKey, ModelBaseline};
use crate::error::Error;
use crate::ledger::{Call, Exchange, SummaryRow, UsageFilter, summarize};

/// The configuration is graph data (llmcfg); these are the shapes the service works with.
pub type ProviderRecord = llmcfg::Provider;
pub type ModelEntry = llmcfg::Model;
pub type AliasEntry = llmcfg::Alias;

/// Quota periods.
pub const PERIOD_DAY: &str = llmcfg::PERIOD_DAY;
pub const PERIOD_MONTH: &str = llmcfg::PERIOD_MONTH;
pub const PERIOD_TOTAL: &str = llmcfg::PERIOD_TOTAL;

/// PeriodKey is the usage bucket of t for a quota period.
pub fn period_key(period: &str, t: DateTime<Utc>) -> String {
    match period {
        PERIOD_DAY => t.format("%Y-%m-%d").to_string(),
        PERIOD_MONTH => t.format("%Y-%m").to_string(),
        _ => PERIOD_TOTAL.to_string(),
    }
}

/// Store persists the state of the gateway, which is the quota counters and the ledger of calls (ADR 0089): what is
/// configured (providers, models, aliases) is read from the graph. The counter is keyed by the key of the model node
/// (llmcfg::model_key) and period and serves the admission only; it outlives the purge of the ledger.
#[async_trait]
pub trait Store: Send + Sync {
    async fn usage(&self, ctx: &Ctx, model: &str, period: &str) -> Result<i64, Error>;
    async fn add_usage(
        &self,
        ctx: &Ctx,
        model: &str,
        period: &str,
        tokens: i64,
    ) -> Result<(), Error>;
    /// Writes a row of the ledger and returns its seq (monotonic); x, when some, is stored with it (one step).
    async fn append_call(&self, ctx: &Ctx, c: Call, x: Option<Exchange>) -> Result<i64, Error>;
    /// The row of a seq, Error::NotFound when there is none (purged or never written).
    async fn call(&self, ctx: &Ctx, seq: i64) -> Result<Call, Error>;
    /// The stored exchange of a seq, Error::NotFound when there is none.
    async fn exchange(&self, ctx: &Ctx, seq: i64) -> Result<Exchange, Error>;
    /// The calls of the filter ascending by seq (see Service::list_calls) and whether more match.
    async fn calls(&self, ctx: &Ctx, f: &UsageFilter) -> Result<(Vec<Call>, bool), Error>;
    async fn summary(
        &self,
        ctx: &Ctx,
        f: &UsageFilter,
        group: &str,
    ) -> Result<Vec<SummaryRow>, Error>;
    /// Deletes the calls before t, with their exchanges, and returns how many calls.
    async fn purge_calls(&self, ctx: &Ctx, before: DateTime<Utc>) -> Result<i64, Error>;

    /// The measured cost of the behaviours (ADR 0093, "Measured cost"); the tables are small.
    async fn behavior_costs(&self, ctx: &Ctx) -> Result<Vec<BehaviorCost>, Error>;
    /// Inserts or replaces the cost of its key.
    async fn put_behavior_cost(&self, ctx: &Ctx, c: BehaviorCost) -> Result<(), Error>;
    async fn delete_behavior_costs(&self, ctx: &Ctx, keys: &[CostKey]) -> Result<(), Error>;
    async fn model_baselines(&self, ctx: &Ctx) -> Result<Vec<ModelBaseline>, Error>;
    async fn put_model_baseline(&self, ctx: &Ctx, b: ModelBaseline) -> Result<(), Error>;
    async fn delete_model_baselines(
        &self,
        ctx: &Ctx,
        keys: &[(String, String)],
    ) -> Result<(), Error>;
}

#[derive(Default)]
struct Mem {
    usage: HashMap<(String, String), i64>,
    calls: Vec<Call>,
    xs: HashMap<i64, Exchange>,
    seq: i64,
    costs: HashMap<CostKey, BehaviorCost>,
    bases: HashMap<(String, String), ModelBaseline>,
}

impl Mem {
    fn selected(&self, f: &UsageFilter) -> Vec<Call> {
        self.calls
            .iter()
            .filter(|c| c.seq > f.after_seq && f.matches(c))
            .cloned()
            .collect()
    }
}

/// MemoryStore is an in-memory Store (tests, GOAP_STORE=memory).
#[derive(Default)]
pub struct MemoryStore {
    mu: Mutex<Mem>,
}

impl MemoryStore {
    /// An empty in-memory store.
    pub fn new() -> Self {
        Self::default()
    }
}

#[async_trait]
impl Store for MemoryStore {
    async fn usage(&self, _ctx: &Ctx, model: &str, period: &str) -> Result<i64, Error> {
        Ok(*self
            .mu
            .lock()
            .usage
            .get(&(model.to_string(), period.to_string()))
            .unwrap_or(&0))
    }

    async fn add_usage(
        &self,
        _ctx: &Ctx,
        model: &str,
        period: &str,
        tokens: i64,
    ) -> Result<(), Error> {
        *self
            .mu
            .lock()
            .usage
            .entry((model.to_string(), period.to_string()))
            .or_default() += tokens;
        Ok(())
    }

    async fn append_call(
        &self,
        _ctx: &Ctx,
        mut c: Call,
        x: Option<Exchange>,
    ) -> Result<i64, Error> {
        let mut s = self.mu.lock();
        s.seq += 1;
        c.seq = s.seq;
        c.has_exchange = x.is_some();
        if let Some(mut xc) = x {
            xc.seq = c.seq;
            s.xs.insert(c.seq, xc);
        }
        let seq = c.seq;
        s.calls.push(c);
        Ok(seq)
    }

    async fn call(&self, _ctx: &Ctx, seq: i64) -> Result<Call, Error> {
        self.mu
            .lock()
            .calls
            .iter()
            .find(|c| c.seq == seq)
            .cloned()
            .ok_or_else(|| Error::NotFound(String::new()))
    }

    async fn exchange(&self, _ctx: &Ctx, seq: i64) -> Result<Exchange, Error> {
        self.mu
            .lock()
            .xs
            .get(&seq)
            .cloned()
            .ok_or_else(|| Error::NotFound(String::new()))
    }

    async fn calls(&self, _ctx: &Ctx, f: &UsageFilter) -> Result<(Vec<Call>, bool), Error> {
        let mut out = self.mu.lock().selected(f);
        let n = f.limit();
        if out.len() <= n {
            return Ok((out, false));
        }
        if f.after_seq > 0 {
            out.truncate(n);
            return Ok((out, true));
        }
        let tail = out.split_off(out.len() - n);
        Ok((tail, true))
    }

    async fn summary(
        &self,
        _ctx: &Ctx,
        f: &UsageFilter,
        group: &str,
    ) -> Result<Vec<SummaryRow>, Error> {
        Ok(summarize(&self.mu.lock().selected(f), group))
    }

    async fn purge_calls(&self, _ctx: &Ctx, before: DateTime<Utc>) -> Result<i64, Error> {
        let mut s = self.mu.lock();
        let mut n = 0;
        let calls = std::mem::take(&mut s.calls);
        let mut kept = Vec::with_capacity(calls.len());
        for c in calls {
            if c.at < before {
                n += 1;
                s.xs.remove(&c.seq);
                continue;
            }
            kept.push(c);
        }
        s.calls = kept;
        Ok(n)
    }

    async fn behavior_costs(&self, _ctx: &Ctx) -> Result<Vec<BehaviorCost>, Error> {
        Ok(self.mu.lock().costs.values().cloned().collect())
    }

    async fn put_behavior_cost(&self, _ctx: &Ctx, c: BehaviorCost) -> Result<(), Error> {
        self.mu.lock().costs.insert(c.key.clone(), c);
        Ok(())
    }

    async fn delete_behavior_costs(&self, _ctx: &Ctx, keys: &[CostKey]) -> Result<(), Error> {
        let mut s = self.mu.lock();
        for k in keys {
            s.costs.remove(k);
        }
        Ok(())
    }

    async fn model_baselines(&self, _ctx: &Ctx) -> Result<Vec<ModelBaseline>, Error> {
        Ok(self.mu.lock().bases.values().cloned().collect())
    }

    async fn put_model_baseline(&self, _ctx: &Ctx, b: ModelBaseline) -> Result<(), Error> {
        self.mu
            .lock()
            .bases
            .insert((b.provider.clone(), b.model.clone()), b);
        Ok(())
    }

    async fn delete_model_baselines(
        &self,
        _ctx: &Ctx,
        keys: &[(String, String)],
    ) -> Result<(), Error> {
        let mut s = self.mu.lock();
        for k in keys {
            s.bases.remove(k);
        }
        Ok(())
    }
}
