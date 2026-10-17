//! The log of a change (ADR 0030) holds its facts, its journal records and the events of its
//! change impacts: the repositories store entries ([`Tx::append_log`], [`Tx::log`]); these helpers write and read the
//! typed values through them. (Go's `logWhere` is in `sqlbuild.rs`, with the statements.)

use std::collections::BTreeMap;

use goap_ctx::Ctx;
use goap_domain::{
    ChangeID, ChangeItem, ImpactEvent, LOG_CHANGE, LOG_FACT, LOG_IMPACT, LogEntry, LogFilter,
    fact_entry, gotime, impact_entry,
};
use serde::de::DeserializeOwned;

use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

/// putItem appends a fact to the log of its change.
pub(crate) async fn put_item(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    change: &ChangeID,
    it: &ChangeItem,
) -> Result<()> {
    let e = fact_entry(change, it)?;
    tx.append_log(ctx, e).await?;
    Ok(())
}

/// appendImpactEvent appends an impact event to the log of its change and returns it with its position.
pub(crate) async fn append_impact_event(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    mut ev: ImpactEvent,
) -> Result<ImpactEvent> {
    let e = impact_entry(&ev)?;
    let e = tx.append_log(ctx, e).await?;
    ev.seq = e.seq;
    Ok(ev)
}

/// impactEvents is the impact log of a change (ADR 0029).
pub(crate) async fn impact_events(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    change: &ChangeID,
) -> Result<Vec<ImpactEvent>> {
    let entries = tx
        .log(
            ctx,
            &LogFilter {
                change: change.clone(),
                types: vec![format!("{LOG_IMPACT}.")],
                ..Default::default()
            },
        )
        .await?;
    decode_log(&entries, |e: &mut ImpactEvent, l| e.seq = l.seq)
}

/// itemsOf decodes the facts of a log.
pub(crate) fn items_of(entries: &[LogEntry]) -> Result<Vec<ChangeItem>> {
    decode_log(entries, |_: &mut ChangeItem, _| {})
}

/// decodeLog decodes the payloads of log entries; set completes each value from its entry.
pub(crate) fn decode_log<T: DeserializeOwned>(
    entries: &[LogEntry],
    set: impl Fn(&mut T, &LogEntry),
) -> Result<Vec<T>> {
    let mut out = Vec::with_capacity(entries.len());
    for l in entries {
        let mut v: T = serde_json::from_value(l.payload.clone())
            .map_err(|e| Error::msg(format!("log entry {} ({}): {e}", l.seq, l.r#type)))?;
        set(&mut v, l);
        out.push(v);
    }
    Ok(out)
}

/// factsFilter selects the facts of a change.
pub(crate) fn facts_filter(change: &ChangeID) -> LogFilter {
    LogFilter {
        change: change.clone(),
        types: vec![format!("{LOG_FACT}.")],
        ..Default::default()
    }
}

impl Graph {
    /// ChangeLog returns the entries of a change's log matching f (ADR 0030), and the number of entries of each type
    /// matching f without its types (what each type would add); f.change or f.processes is required (a process names
    /// its change).
    pub async fn change_log(
        &self,
        ctx: &Ctx,
        f: &LogFilter,
    ) -> Result<(Vec<LogEntry>, BTreeMap<String, i64>)> {
        if f.change.is_empty() && f.processes.is_empty() {
            return Err(Error::wrap(ErrorKind::Invalid, "the log of which change"));
        }
        let mut tx = self.begin(ctx).await?;
        if !f.change.is_empty() {
            tx.change(ctx, &f.change).await?;
        }
        let out = tx.log(ctx, f).await?;
        let mut all = f.clone();
        all.types = Vec::new();
        let counts = tx.log_counts(ctx, &all).await?;
        tx.commit(ctx).await?;
        Ok((out, counts))
    }

    /// AppendLog appends entries to the logs of their changes, as one write (ids are assigned and the time set when
    /// empty). The graph does not look into them: what an entry says is its stream's, a use case (the execution
    /// journal, goap-journal). The streams the graph writes itself (LOG_FACT, LOG_IMPACT, LOG_CHANGE: items and change
    /// impacts have their own operations, which check them) are refused, and so is an entry of an unknown change.
    pub async fn append_log(&self, ctx: &Ctx, entries: Vec<LogEntry>) -> Result<()> {
        for e in &entries {
            if e.change.is_empty() || !e.r#type.contains('.') {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    "a log entry needs a change and a type <stream>.<kind>",
                ));
            }
            let s = e.stream();
            if s == LOG_FACT || s == LOG_IMPACT || s == LOG_CHANGE {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    format!("the {s} stream of a log is written by the graph only"),
                ));
            }
        }
        let mut tx = self.begin(ctx).await?;
        for mut e in entries {
            if e.id.is_empty() {
                e.id = self.new_id();
            }
            if e.at.is_none_or(|t| t == gotime::zero()) {
                e.at = Some(self.now());
            }
            tx.append_log(ctx, e).await?;
        }
        tx.commit(ctx).await
    }
}
