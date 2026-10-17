//! The ledger of LLM calls (ADR 0089): one row per call the gateway served or refused. The exchange (request and
//! answer) of an engine call of a change stays in the change log (ADR 0059); every other call has it stored next to
//! its row.

use std::collections::HashMap;
use std::fmt::Write as _;
use std::time::Duration;

use chrono::{DateTime, TimeZone, Utc};
use goap_authz as authz;
use goap_ctx::Ctx;
use goap_journal as journal;
use goap_llm as llm;
use goap_llmcfg as llmcfg;

use crate::error::Error;
use crate::protocols::cut;
use crate::service::Service;

/// Call kinds.
pub const KIND_COMPLETE: &str = "complete";
pub const KIND_EMBED: &str = "embed";

/// MaxCallError caps the error text of a call (bytes).
pub const MAX_CALL_ERROR: usize = 500;

/// Call is one row of the ledger.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct Call {
    pub seq: i64,
    pub at: DateTime<Utc>,
    pub duration_ms: i64,
    /// Subject, project and org are the principal's, never declared by the caller.
    pub subject: String,
    pub project: String,
    pub org: String,
    /// The person the call is attributed to (ADR 0114): the subject when a person called, else the person the
    /// platform service acted for; "" for none.
    pub end_user: String,
    /// The name requested, "" for a literal provider/model.
    pub alias: String,
    pub provider: String,
    pub model: String,
    pub kind: String,
    pub input_tokens: i64,
    pub output_tokens: i64,
    pub error: String,
    /// Source and the correlation ids are declared by the caller (llm::CallMeta).
    pub source: String,
    pub conversation_id: String,
    pub process_id: String,
    pub change_id: String,
    /// Step and call_index are llm::NO_STEP without a process.
    pub step: i64,
    pub action: String,
    pub agent: String,
    pub call_index: i64,
    /// The global behaviours the gateway added to the instructions (ADR 0093; "!name": dropped by the cap).
    pub behaviors: Vec<String>,
    /// The tokens they added (an estimate).
    pub behavior_tokens: i64,
    /// behavior_tokens is the byte estimate, not a cost measured on the model.
    pub behavior_tokens_estimated: bool,
    /// The gateway stores the exchange of this row (read only: set by the store, ignored on write).
    pub has_exchange: bool,
}

/// Exchange is the request and the answer of a call the gateway stores (ADR 0089); seq is the row's.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct Exchange {
    pub seq: i64,
    pub system: String,
    pub messages: Vec<llm::Message>,
    pub response: String,
    /// A text was cut to journal::MAX_EXCHANGE_TEXT.
    pub truncated: bool,
}

impl Exchange {
    /// The exchange with every text cut to journal::MAX_EXCHANGE_TEXT.
    pub(crate) fn capped(&self) -> Exchange {
        let mut truncated = self.truncated;
        let mut cutf = |s: &str| -> String {
            if s.len() <= journal::MAX_EXCHANGE_TEXT {
                return s.to_string();
            }
            truncated = true;
            cut(s, journal::MAX_EXCHANGE_TEXT).to_string()
        };
        let system = cutf(&self.system);
        let response = cutf(&self.response);
        let messages = self
            .messages
            .iter()
            .map(|m| llm::Message {
                role: m.role.clone(),
                content: cutf(&m.transcript()), // tool calls and results as text (ADR 0098)
                ..Default::default()
            })
            .collect();
        Exchange {
            seq: self.seq,
            system,
            messages,
            response,
            truncated,
        }
    }
}

/// Whether the exchange of a call is kept by the change log (ADR 0059): an engine call of a change.
pub(crate) fn in_change_log(m: &llm::CallMeta) -> bool {
    m.source == llm::SOURCE_ENGINE && !m.change_id.is_empty()
}

/// UsageFilter selects calls; empty fields match everything.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct UsageFilter {
    pub from: Option<DateTime<Utc>>,
    pub to: Option<DateTime<Utc>>,
    pub subject: String,
    pub project: String,
    pub model: String,
    pub alias: String,
    pub source: String,
    /// The calls attributed to a person (ADR 0114): their own and the ones made on their behalf.
    pub end_user: String,
    pub process_id: String,
    pub change_id: String,
    pub conversation_id: String,
    pub after_seq: i64,
    pub limit: i64,
}

/// Limits of a listing.
pub const DEFAULT_USAGE_LIMIT: usize = 500;
pub const MAX_USAGE_LIMIT: usize = 5000;

impl UsageFilter {
    pub(crate) fn limit(&self) -> usize {
        if self.limit <= 0 {
            DEFAULT_USAGE_LIMIT
        } else if self.limit as usize > MAX_USAGE_LIMIT {
            MAX_USAGE_LIMIT
        } else {
            self.limit as usize
        }
    }

    /// Whether a call is selected by the filter (after_seq and limit aside).
    pub(crate) fn matches(&self, c: &Call) -> bool {
        let eq = |want: &str, got: &str| want.is_empty() || want == got;
        self.from.is_none_or(|f| c.at >= f)
            && self.to.is_none_or(|t| c.at < t)
            && eq(&self.subject, &c.subject)
            && eq(&self.end_user, &c.end_user)
            && eq(&self.project, &c.project)
            && eq(&self.alias, &c.alias)
            && eq(&self.source, &c.source)
            && eq(&self.process_id, &c.process_id)
            && eq(&self.change_id, &c.change_id)
            && eq(&self.conversation_id, &c.conversation_id)
            && (self.model.is_empty()
                || self.model == c.model
                || self.model == format!("{}/{}", c.provider, c.model))
    }
}

/// Grouping of a summary.
pub const GROUP_MODEL: &str = "model";
pub const GROUP_ALIAS: &str = "alias";
pub const GROUP_SOURCE: &str = "source";
pub const GROUP_SUBJECT: &str = "subject";
pub const GROUP_END_USER: &str = "end_user";
pub const GROUP_PROCESS: &str = "process";
pub const GROUP_ACTION: &str = "action";
pub const GROUP_AGENT: &str = "agent";
pub const GROUP_DAY: &str = "day";
pub const GROUP_HOUR: &str = "hour";

/// Buckets of the time groupings (milliseconds): the key of a row is the bucket number, formatted by bucket_key.
pub(crate) const DAY_MS: i64 = 24 * 3600 * 1000;
pub(crate) const HOUR_MS: i64 = 3600 * 1000;

/// SummaryRow is one group of a summary.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct SummaryRow {
    pub key: String,
    pub calls: i64,
    pub input: i64,
    pub output: i64,
    pub errors: i64,
    pub duration_ms: i64,
}

pub(crate) fn valid_group(g: &str) -> bool {
    [
        GROUP_MODEL,
        GROUP_ALIAS,
        GROUP_SOURCE,
        GROUP_SUBJECT,
        GROUP_END_USER,
        GROUP_PROCESS,
        GROUP_ACTION,
        GROUP_AGENT,
        GROUP_DAY,
        GROUP_HOUR,
    ]
    .contains(&g)
}

pub(crate) fn bucket_key(group: &str, n: i64) -> String {
    if group == GROUP_DAY {
        let t = Utc
            .timestamp_millis_opt(n * DAY_MS)
            .single()
            .unwrap_or_default();
        return t.format("%Y-%m-%d").to_string();
    }
    let t = Utc
        .timestamp_millis_opt(n * HOUR_MS)
        .single()
        .unwrap_or_default();
    t.format("%Y-%m-%dT%H").to_string()
}

fn group_key(c: &Call, group: &str) -> String {
    match group {
        GROUP_MODEL => c.model.clone(),
        GROUP_ALIAS => c.alias.clone(),
        GROUP_SOURCE => c.source.clone(),
        GROUP_SUBJECT => c.subject.clone(),
        GROUP_END_USER => c.end_user.clone(),
        GROUP_PROCESS => c.process_id.clone(),
        GROUP_ACTION => c.action.clone(),
        GROUP_AGENT => c.agent.clone(),
        GROUP_DAY => bucket_key(group, c.at.timestamp_millis().div_euclid(DAY_MS)),
        _ => bucket_key(GROUP_HOUR, c.at.timestamp_millis().div_euclid(HOUR_MS)),
    }
}

/// summarize groups calls in memory (the memory store).
pub(crate) fn summarize(calls: &[Call], group: &str) -> Vec<SummaryRow> {
    let mut by: HashMap<String, SummaryRow> = HashMap::new();
    for c in calls {
        let k = group_key(c, group);
        let r = by.entry(k.clone()).or_insert_with(|| SummaryRow {
            key: k,
            ..Default::default()
        });
        r.calls += 1;
        r.input += c.input_tokens;
        r.output += c.output_tokens;
        r.duration_ms += c.duration_ms;
        if !c.error.is_empty() {
            r.errors += 1;
        }
    }
    let mut out: Vec<SummaryRow> = by.into_values().collect();
    sort_summary(&mut out, group);
    out
}

/// Orders a summary: time groups ascending, the others by decreasing tokens.
pub(crate) fn sort_summary(rows: &mut [SummaryRow], group: &str) {
    rows.sort_by(|a, b| {
        if group == GROUP_DAY || group == GROUP_HOUR {
            return a.key.cmp(&b.key);
        }
        (b.input + b.output)
            .cmp(&(a.input + a.output))
            .then_with(|| a.key.cmp(&b.key))
    });
}

// ---- recording --------------------------------------------------------------

/// A call being made: what is known when it starts, completed by finish.
pub(crate) struct Pending {
    call: Call,
    start: DateTime<Utc>,
    /// The exchange to store, None when the call's is kept elsewhere or the storage is off.
    x: Option<Exchange>,
}

/// What an embedding's exchange keeps of its texts.
const MAX_PREVIEW_TEXTS: usize = 5;
pub(crate) const MAX_PREVIEW_TEXT: usize = 300;

impl Service {
    /// Opens the ledger row of a call: the principal is the context's, the declaration the meta of the context.
    pub(crate) fn begin(&self, ctx: &Ctx, kind: &str, requested: &str) -> Pending {
        let p = authz::principal_from(ctx);
        let m = llm::meta_from(ctx);
        let mut alias = requested.to_string();
        if alias.is_empty() {
            alias = if kind == KIND_EMBED {
                llm::EMBED_ALIAS.to_string()
            } else {
                "default".to_string()
            };
        }
        if alias.contains('/') {
            alias = String::new(); // a literal provider/model
        }
        let now = self.now();
        let x = (self.call_prompts && !in_change_log(&m)).then(Exchange::default);
        Pending {
            start: now,
            call: Call {
                at: now,
                subject: p.subject.clone(),
                end_user: p.end_user().to_string(),
                project: p.project.clone(),
                org: p.org.clone(),
                alias,
                kind: kind.to_string(),
                source: m.source,
                conversation_id: m.conversation_id,
                process_id: m.process_id,
                change_id: m.change_id,
                step: m.step,
                action: m.action,
                agent: m.agent,
                call_index: m.call,
                ..Default::default()
            },
            x,
        }
    }

    /// Writes the row: the provider and model that served (or were resolved), the tokens and the error. It never
    /// fails the call; a row that cannot be written is logged like the quota counter.
    pub(crate) async fn finish(
        &self,
        ctx: &Ctx,
        p: Pending,
        t: &crate::router::Target,
        input: i64,
        output: i64,
        err: Option<String>,
    ) {
        let (provider, model) = (&t.provider, &t.model);
        let mut c = p.call;
        c.provider = provider.clone();
        c.model = model.clone();
        c.input_tokens = input;
        c.output_tokens = output;
        c.duration_ms = (self.now() - p.start).num_milliseconds().max(0);
        if let Some(e) = err {
            c.error = cut(&e, MAX_CALL_ERROR).to_string();
        }
        let x = p.x.as_ref().map(Exchange::capped);
        let source = c.source.clone();
        if let Err(e) = self.store.append_call(&ctx.without_cancel(), c, x).await {
            tracing::error!(source = %source, model = %format!("{provider}/{model}"), err = %e, "call not recorded");
        }
    }
}

impl Pending {
    /// Notes what is sent, for the exchange.
    pub(crate) fn request(&mut self, system: &str, msgs: &[llm::Message]) {
        if let Some(x) = &mut self.x {
            x.system = system.to_string();
            x.messages = msgs.to_vec();
        }
    }

    /// Notes the behaviours added to the instructions of the call.
    pub(crate) fn behaved(&mut self, ap: &llmcfg::Applied, tokens: i64, estimated: bool) {
        self.call.behaviors = crate::service::behavior_names(ap);
        self.call.behavior_tokens = tokens;
        self.call.behavior_tokens_estimated = estimated;
    }

    /// Notes what came back, for the exchange.
    pub(crate) fn answer(&mut self, text: String) {
        if let Some(x) = &mut self.x {
            x.response = text;
        }
    }

    /// Notes the texts of an embedding: their number and a short preview (never the vectors).
    pub(crate) fn request_embed(&mut self, texts: &[String]) {
        let Some(x) = &mut self.x else {
            return;
        };
        let mut b = format!("{} texts", texts.len());
        for (i, t) in texts.iter().enumerate() {
            if i == MAX_PREVIEW_TEXTS {
                let _ = write!(b, "\n... {} more", texts.len() - i);
                break;
            }
            let t = if t.len() > MAX_PREVIEW_TEXT {
                format!("{}...", cut(t, MAX_PREVIEW_TEXT))
            } else {
                t.clone()
            };
            b.push_str("\n- ");
            b.push_str(&t);
        }
        x.messages = vec![llm::Message {
            role: "texts".into(),
            content: b,
            ..Default::default()
        }];
    }
}

// ---- reading ----------------------------------------------------------------

/// Applies who may read what: a caller reads the calls attributed to it (ADR 0114: its own and the ones made on its
/// behalf; the filter's end user is forced to it, naming another is refused, and the subject narrows them, to its
/// own calls or to the service that acted for it); an administrator any. A caller without identity reads nothing.
pub(crate) fn scope(ctx: &Ctx, mut f: UsageFilter, admin: bool) -> Result<UsageFilter, Error> {
    let p = authz::principal_from(ctx);
    if admin {
        return Ok(f);
    }
    if p.anonymous() {
        return Err(Error::Forbidden(
            "the ledger is read by an identified caller".into(),
        ));
    }
    let me = p.end_user().to_string();
    if me.is_empty() {
        // a platform service acting for nobody: its own calls
        if !f.subject.is_empty() && f.subject != p.subject {
            return Err(Error::Forbidden(format!(
                "only the calls of {} are readable by it",
                p.subject
            )));
        }
        f.subject = p.subject.clone();
        f.end_user = String::new();
        return Ok(f);
    }
    if (!f.end_user.is_empty() && f.end_user != me)
        || (!f.subject.is_empty()
            && f.subject != me
            && !f.subject.starts_with(authz::SYSTEM_PREFIX))
    {
        return Err(Error::Forbidden(format!(
            "only the calls attributed to {me} are readable by it"
        )));
    }
    f.end_user = me;
    Ok(f)
}

/// The page of a listing: the calls, the cursor to resume from, whether more match.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct CallPage {
    pub calls: Vec<Call>,
    pub next: i64,
    pub more: bool,
}

/// How long the ledger keeps a call (GOAP_LLM_CALL_RETENTION_DAYS); 0 keeps them forever. The quota counter is not
/// the ledger and is not purged. It is MIN_AUDIT_RETENTION_DAYS: the ledger is the record of what the models did for
/// whom, and the EU AI Act asks providers and deployers to keep such logs at least six months (Art. 19, Art. 26(6)).
pub const DEFAULT_CALL_RETENTION_DAYS: i64 = MIN_AUDIT_RETENTION_DAYS;

/// The shortest retention that keeps the logs six months (EU AI Act Art. 19, 26(6)); a shorter one is allowed, with
/// a warning at start.
pub const MIN_AUDIT_RETENTION_DAYS: i64 = 183;

impl Service {
    /// Lists the calls of the filter, ascending by seq: with after_seq the first ones after it, else the latest limit
    /// ones. admin: the caller may read the calls of any subject.
    pub async fn list_calls(
        &self,
        ctx: &Ctx,
        f: UsageFilter,
        admin: bool,
    ) -> Result<CallPage, Error> {
        let f = scope(ctx, f, admin)?;
        let (calls, more) = self.store.calls(ctx, &f).await?;
        let next = calls.last().map(|c| c.seq).unwrap_or(f.after_seq);
        Ok(CallPage { calls, next, more })
    }

    /// A call and the exchange the gateway stores for it. A caller reads its own calls, an administrator any; a call
    /// of someone else is NotFound for the others (its existence is not told), as is a call with no stored exchange.
    pub async fn call_exchange(
        &self,
        ctx: &Ctx,
        seq: i64,
        admin: bool,
    ) -> Result<(Call, Exchange), Error> {
        let p = authz::principal_from(ctx);
        if !admin && p.anonymous() {
            return Err(Error::Forbidden(
                "the ledger is read by an identified caller".into(),
            ));
        }
        let c = self.store.call(ctx, seq).await?;
        if !admin && c.subject != p.subject && (c.end_user.is_empty() || c.end_user != p.end_user())
        {
            return Err(Error::NotFound(format!("call {seq}")));
        }
        match self.store.exchange(ctx, seq).await {
            Ok(x) => Ok((c, x)),
            Err(e) if e.is_not_found() => Err(Error::NotFound(format!(
                "no exchange stored for call {seq} (not kept, or purged)"
            ))),
            Err(e) => Err(e),
        }
    }

    /// Groups the calls of the filter (after_seq and limit are ignored).
    pub async fn summary(
        &self,
        ctx: &Ctx,
        f: UsageFilter,
        group: &str,
        admin: bool,
    ) -> Result<Vec<SummaryRow>, Error> {
        if !valid_group(group) {
            return Err(Error::Invalid(format!(
                "group_by {} (model, alias, source, subject, end_user, process, action, agent, day or hour)",
                crate::router::quote(group)
            )));
        }
        let mut f = scope(ctx, f, admin)?;
        f.after_seq = 0;
        f.limit = 0;
        self.store.summary(ctx, &f, group).await
    }

    /// Deletes the calls older than days days; returns how many. days <= 0 deletes nothing.
    pub async fn purge_calls(&self, ctx: &Ctx, days: i64) -> Result<i64, Error> {
        if days <= 0 {
            return Ok(0);
        }
        self.store
            .purge_calls(ctx, self.now() - chrono::Days::new(days as u64))
            .await
    }

    /// Purges the ledger now and then every day until ctx ends. Spawn it.
    pub async fn keep_calls(&self, ctx: &Ctx, days: i64) {
        if days > 0 && days < MIN_AUDIT_RETENTION_DAYS {
            tracing::warn!(
                retention_days = days,
                minimum_days = MIN_AUDIT_RETENTION_DAYS,
                "the ledger of model calls is kept less than six months, the minimum of the EU AI Act for its logs (Art. 19, 26(6))"
            );
        }
        if !self.call_prompts {
            tracing::warn!(
                "the exchanges of the model calls no change log keeps are not stored (GOAP_LLM_CALL_PROMPTS off): the ledger keeps counters only"
            );
        }
        if days <= 0 {
            return;
        }
        loop {
            match self.purge_calls(ctx, days).await {
                Err(e) => tracing::error!(err = %e, "ledger purge"),
                Ok(n) if n > 0 => {
                    tracing::info!(deleted = n, older_than_days = days, "ledger purge")
                }
                Ok(_) => {}
            }
            tokio::select! {
                _ = ctx.done() => return,
                _ = tokio::time::sleep(Duration::from_secs(24 * 3600)) => {}
            }
        }
    }
}
