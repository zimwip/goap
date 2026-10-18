//! The Store on SQL, for SQLite (local mode) and PostgreSQL.

use chrono::{DateTime, TimeZone, Utc};
use goap_ctx::Ctx;
use include_dir::{Dir, include_dir};
use sqlx::{AssertSqlSafe, PgPool, Row, SqlitePool};

use crate::behaviorcost::{BehaviorCost, CostKey, ModelBaseline};
use crate::error::Error;
use crate::ledger::{
    Call, DAY_MS, Exchange, GROUP_ACTION, GROUP_AGENT, GROUP_ALIAS, GROUP_DAY, GROUP_END_USER,
    GROUP_HOUR, GROUP_MODEL, GROUP_PROCESS, GROUP_SOURCE, GROUP_SUBJECT, HOUR_MS, SummaryRow,
    UsageFilter, bucket_key,
};
use crate::router::quote;
use crate::store::Store;

/// The PostgreSQL schema (`goap_platform::migrate(ctx, pool, &MIGRATIONS, ".")`); SQLITE_MIGRATIONS the local mode
/// one.
pub static MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/migrations");
pub static SQLITE_MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/migrations_sqlite");

/// SqlStore is the Store on SQL, for SQLite (local mode) and PostgreSQL (Go's `SQLStore`, `Dollar` set for the
/// latter).
#[derive(Debug, Clone)]
pub enum SqlStore {
    Sqlite(SqlitePool),
    Postgres(PgPool),
}

/// The queries are written once with `?` placeholders: numbered for PostgreSQL.
fn dialect(postgres: bool, query: &str) -> String {
    if !postgres {
        return query.to_string();
    }
    let mut b = String::with_capacity(query.len() + 8);
    let mut n = 0;
    for c in query.chars() {
        if c == '?' {
            n += 1;
            b.push('$');
            b.push_str(&n.to_string());
        } else {
            b.push(c);
        }
    }
    b
}

macro_rules! on_pool {
    ($store:expr, $pool:ident, $pg:ident => $body:expr) => {
        match $store {
            SqlStore::Sqlite($pool) => {
                let $pg = false;
                $body
            }
            SqlStore::Postgres($pool) => {
                let $pg = true;
                $body
            }
        }
    };
}

macro_rules! begin {
    ($pool:expr, $pg:expr) => {
        if $pg {
            $pool.begin().await?
        } else {
            $pool.begin_with("BEGIN IMMEDIATE").await?
        }
    };
}

macro_rules! in_tx {
    ($tx:ident, $body:block) => {{
        let res: Result<_, Error> = async { $body }.await;
        match res {
            Ok(v) => {
                $tx.commit().await?;
                Ok(v)
            }
            Err(e) => {
                let _ = $tx.rollback().await;
                Err(e)
            }
        }
    }};
}

/// An argument of a built WHERE clause.
#[derive(Debug, Clone)]
enum Arg {
    I(i64),
    S(String),
}

macro_rules! bind_args {
    ($q:expr, $args:expr) => {{
        let mut q = $q;
        for a in $args {
            q = match a {
                Arg::I(n) => q.bind(*n),
                Arg::S(s) => q.bind(s.clone()),
            };
        }
        q
    }};
}

const CALL_COLUMNS: &str = "seq, at_ms, duration_ms, subject, end_user, project, org, alias, provider, model, kind, input_tokens, output_tokens, error,
	source, conversation_id, process_id, change_id, step, action, agent, call_index, behaviors, behavior_tokens, behavior_tokens_estimated,
	CAST(CASE WHEN EXISTS (SELECT 1 FROM llm_call_exchange x WHERE x.seq = llm_call.seq) THEN 1 ELSE 0 END AS bigint)";

fn from_ms(ms: i64) -> DateTime<Utc> {
    Utc.timestamp_millis_opt(ms).single().unwrap_or_default()
}

/// Reads a row of CALL_COLUMNS.
fn scan_call<R: Row>(r: &R) -> Result<Call, Error>
where
    usize: sqlx::ColumnIndex<R>,
    String: for<'r> sqlx::Decode<'r, R::Database> + sqlx::Type<R::Database>,
    i64: for<'r> sqlx::Decode<'r, R::Database> + sqlx::Type<R::Database>,
    i32: for<'r> sqlx::Decode<'r, R::Database> + sqlx::Type<R::Database>,
    bool: for<'r> sqlx::Decode<'r, R::Database> + sqlx::Type<R::Database>,
{
    let behaviors: String = r.try_get(22)?;
    let has: i64 = r.try_get(25)?;
    Ok(Call {
        seq: r.try_get(0)?,
        at: from_ms(r.try_get(1)?),
        duration_ms: r.try_get(2)?,
        subject: r.try_get(3)?,
        end_user: r.try_get(4)?,
        project: r.try_get(5)?,
        org: r.try_get(6)?,
        alias: r.try_get(7)?,
        provider: r.try_get(8)?,
        model: r.try_get(9)?,
        kind: r.try_get(10)?,
        input_tokens: r.try_get(11)?,
        output_tokens: r.try_get(12)?,
        error: r.try_get(13)?,
        source: r.try_get(14)?,
        conversation_id: r.try_get(15)?,
        process_id: r.try_get(16)?,
        change_id: r.try_get(17)?,
        step: r.try_get::<i32, _>(18)?.into(),
        action: r.try_get(19)?,
        agent: r.try_get(20)?,
        call_index: r.try_get::<i32, _>(21)?.into(),
        behaviors: if behaviors.is_empty() {
            Vec::new()
        } else {
            behaviors.split(',').map(str::to_string).collect()
        },
        behavior_tokens: r.try_get(23)?,
        behavior_tokens_estimated: r.try_get(24)?,
        has_exchange: has == 1,
    })
}

fn not_found(e: sqlx::Error) -> Error {
    match e {
        sqlx::Error::RowNotFound => Error::NotFound(String::new()),
        e => Error::Sql(e),
    }
}

impl UsageFilter {
    /// The WHERE clause of a filter (after_seq included) with its arguments.
    fn where_clause(&self) -> (String, Vec<Arg>) {
        let mut conds = Vec::new();
        let mut args = Vec::new();
        if self.after_seq > 0 {
            conds.push("seq > ?".to_string());
            args.push(Arg::I(self.after_seq));
        }
        if let Some(f) = self.from {
            conds.push("at_ms >= ?".to_string());
            args.push(Arg::I(f.timestamp_millis()));
        }
        if let Some(t) = self.to {
            conds.push("at_ms < ?".to_string());
            args.push(Arg::I(t.timestamp_millis()));
        }
        for (col, val) in [
            ("subject", &self.subject),
            ("end_user", &self.end_user),
            ("project", &self.project),
            ("alias", &self.alias),
            ("source", &self.source),
            ("process_id", &self.process_id),
            ("change_id", &self.change_id),
            ("conversation_id", &self.conversation_id),
        ] {
            if !val.is_empty() {
                conds.push(format!("{col} = ?"));
                args.push(Arg::S(val.clone()));
            }
        }
        if !self.model.is_empty() {
            conds.push("(model = ? OR provider || '/' || model = ?)".to_string());
            args.push(Arg::S(self.model.clone()));
            args.push(Arg::S(self.model.clone()));
        }
        if conds.is_empty() {
            return (String::new(), args);
        }
        (format!(" WHERE {}", conds.join(" AND ")), args)
    }
}

/// The SQL expressions of the groupings; a time grouping is the bucket number, formatted by bucket_key.
fn group_column(group: &str) -> Option<String> {
    Some(match group {
        GROUP_MODEL => "model".into(),
        GROUP_ALIAS => "alias".into(),
        GROUP_SOURCE => "source".into(),
        GROUP_SUBJECT => "subject".into(),
        GROUP_END_USER => "end_user".into(),
        GROUP_PROCESS => "process_id".into(),
        GROUP_ACTION => "action".into(),
        GROUP_AGENT => "agent".into(),
        GROUP_DAY => format!("at_ms / {DAY_MS}"),
        GROUP_HOUR => format!("at_ms / {HOUR_MS}"),
        _ => return None,
    })
}

#[async_trait::async_trait]
impl Store for SqlStore {
    async fn usage(&self, _ctx: &Ctx, model: &str, period: &str) -> Result<i64, Error> {
        on_pool!(self, pool, pg => {
            let r: Option<i64> = sqlx::query_scalar(AssertSqlSafe(dialect(pg,
                "SELECT tokens FROM llm_usage WHERE model_key = ? AND period = ?")))
                .bind(model)
                .bind(period)
                .fetch_optional(pool)
                .await?;
            Ok(r.unwrap_or(0))
        })
    }

    async fn add_usage(
        &self,
        _ctx: &Ctx,
        model: &str,
        period: &str,
        tokens: i64,
    ) -> Result<(), Error> {
        on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(dialect(pg, "INSERT INTO llm_usage (model_key, period, tokens) VALUES (?, ?, ?)
        ON CONFLICT (model_key, period) DO UPDATE SET tokens = llm_usage.tokens + excluded.tokens")))
                .bind(model)
                .bind(period)
                .bind(tokens)
                .execute(pool)
                .await?;
            Ok(())
        })
    }

    async fn append_call(&self, _ctx: &Ctx, c: Call, x: Option<Exchange>) -> Result<i64, Error> {
        on_pool!(self, pool, pg => {
            let mut tx = begin!(pool, pg);
            in_tx!(tx, {
                let seq: i64 = sqlx::query_scalar(AssertSqlSafe(dialect(pg, "INSERT INTO llm_call (at_ms, duration_ms, subject, end_user, project, org, alias, provider, model, kind,
		input_tokens, output_tokens, error, source, conversation_id, process_id, change_id, step, action, agent, call_index, behaviors, behavior_tokens, behavior_tokens_estimated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING seq")))
                    .bind(c.at.timestamp_millis())
                    .bind(c.duration_ms)
                    .bind(&c.subject)
                    .bind(&c.end_user)
                    .bind(&c.project)
                    .bind(&c.org)
                    .bind(&c.alias)
                    .bind(&c.provider)
                    .bind(&c.model)
                    .bind(&c.kind)
                    .bind(c.input_tokens)
                    .bind(c.output_tokens)
                    .bind(&c.error)
                    .bind(&c.source)
                    .bind(&c.conversation_id)
                    .bind(&c.process_id)
                    .bind(&c.change_id)
                    .bind(c.step as i32)
                    .bind(&c.action)
                    .bind(&c.agent)
                    .bind(c.call_index as i32)
                    .bind(c.behaviors.join(","))
                    .bind(c.behavior_tokens)
                    .bind(c.behavior_tokens_estimated)
                    .fetch_one(&mut *tx)
                    .await?;
                if let Some(x) = &x {
                    let msgs = crate::tools::go_json(&x.messages);
                    sqlx::query(AssertSqlSafe(dialect(pg,
                        "INSERT INTO llm_call_exchange (seq, system, messages, response, truncated) VALUES (?, ?, ?, ?, ?)")))
                        .bind(seq)
                        .bind(&x.system)
                        .bind(msgs)
                        .bind(&x.response)
                        .bind(i32::from(x.truncated))
                        .execute(&mut *tx)
                        .await?;
                }
                Ok(seq)
            })
        })
    }

    async fn call(&self, _ctx: &Ctx, seq: i64) -> Result<Call, Error> {
        on_pool!(self, pool, pg => {
            let r = sqlx::query(AssertSqlSafe(dialect(pg, &format!(
                "SELECT {CALL_COLUMNS} FROM llm_call WHERE seq = ?"))))
                .bind(seq)
                .fetch_one(pool)
                .await
                .map_err(not_found)?;
            scan_call(&r)
        })
    }

    async fn exchange(&self, _ctx: &Ctx, seq: i64) -> Result<Exchange, Error> {
        on_pool!(self, pool, pg => {
            let r = sqlx::query(AssertSqlSafe(dialect(pg,
                "SELECT system, messages, response, truncated FROM llm_call_exchange WHERE seq = ?")))
                .bind(seq)
                .fetch_one(pool)
                .await
                .map_err(not_found)?;
            let msgs: String = r.try_get(1)?;
            let trunc: i32 = r.try_get(3)?;
            let messages = serde_json::from_str(&msgs)
                .map_err(|e| Error::Message(format!("exchange {seq}: {e}")))?;
            Ok(Exchange {
                seq,
                system: r.try_get(0)?,
                messages,
                response: r.try_get(2)?,
                truncated: trunc == 1,
            })
        })
    }

    async fn calls(&self, _ctx: &Ctx, f: &UsageFilter) -> Result<(Vec<Call>, bool), Error> {
        let (wh, args) = f.where_clause();
        let n = f.limit();
        // without a cursor, the latest ones, put back in order below
        let order = if f.after_seq == 0 { "DESC" } else { "ASC" };
        let q = format!("SELECT {CALL_COLUMNS} FROM llm_call{wh} ORDER BY seq {order} LIMIT ?");
        let mut out = on_pool!(self, pool, pg => {
            let rows = bind_args!(sqlx::query(AssertSqlSafe(dialect(pg, &q))), &args)
                .bind(n as i64 + 1)
                .fetch_all(pool)
                .await?;
            rows.iter().map(scan_call).collect::<Result<Vec<_>, _>>()?
        });
        let more = out.len() > n;
        out.truncate(n);
        if order == "DESC" {
            out.reverse();
        }
        Ok((out, more))
    }

    async fn summary(
        &self,
        _ctx: &Ctx,
        f: &UsageFilter,
        group: &str,
    ) -> Result<Vec<SummaryRow>, Error> {
        let Some(col) = group_column(group) else {
            return Err(Error::Invalid(format!("group_by {}", quote(group))));
        };
        let (wh, args) = f.where_clause();
        let time = group == GROUP_DAY || group == GROUP_HOUR;
        let order = if time {
            "k"
        } else {
            "CAST(SUM(input_tokens + output_tokens) AS bigint) DESC, k"
        };
        let q = format!(
            "SELECT {col} AS k, COUNT(*), CAST(SUM(input_tokens) AS bigint), CAST(SUM(output_tokens) AS bigint),
		CAST(SUM(CASE WHEN error <> '' THEN 1 ELSE 0 END) AS bigint), CAST(SUM(duration_ms) AS bigint) FROM llm_call{wh} GROUP BY k ORDER BY {order}"
        );
        on_pool!(self, pool, pg => {
            let rows = bind_args!(sqlx::query(AssertSqlSafe(dialect(pg, &q))), &args)
                .fetch_all(pool)
                .await?;
            let mut out = Vec::with_capacity(rows.len());
            for r in &rows {
                let key = if time {
                    bucket_key(group, r.try_get::<i64, _>(0)?)
                } else {
                    r.try_get::<String, _>(0)?
                };
                out.push(SummaryRow {
                    key,
                    calls: r.try_get(1)?,
                    input: r.try_get(2)?,
                    output: r.try_get(3)?,
                    errors: r.try_get(4)?,
                    duration_ms: r.try_get(5)?,
                });
            }
            Ok(out)
        })
    }

    async fn purge_calls(&self, _ctx: &Ctx, before: DateTime<Utc>) -> Result<i64, Error> {
        let ms = before.timestamp_millis();
        on_pool!(self, pool, pg => {
            let mut tx = begin!(pool, pg);
            in_tx!(tx, {
                sqlx::query(AssertSqlSafe(dialect(pg,
                    "DELETE FROM llm_call_exchange WHERE seq IN (SELECT seq FROM llm_call WHERE at_ms < ?)")))
                    .bind(ms)
                    .execute(&mut *tx)
                    .await?;
                let res = sqlx::query(AssertSqlSafe(dialect(pg, "DELETE FROM llm_call WHERE at_ms < ?")))
                    .bind(ms)
                    .execute(&mut *tx)
                    .await?;
                Ok(res.rows_affected() as i64)
            })
        })
    }

    // ---- measured cost of the behaviours (ADR 0093) ---------------------------------

    async fn behavior_costs(&self, _ctx: &Ctx) -> Result<Vec<BehaviorCost>, Error> {
        on_pool!(self, pool, _pg => {
            let rows = sqlx::query(
                "SELECT behavior, instruction_hash, provider, model, tokens, baseline_tokens, measured_at, source FROM llm_behavior_cost")
                .fetch_all(pool)
                .await?;
            let mut out = Vec::with_capacity(rows.len());
            for r in &rows {
                out.push(BehaviorCost {
                    key: CostKey {
                        behavior: r.try_get(0)?,
                        hash: r.try_get(1)?,
                        provider: r.try_get(2)?,
                        model: r.try_get(3)?,
                    },
                    tokens: r.try_get(4)?,
                    baseline: r.try_get(5)?,
                    measured_at: from_ms(r.try_get(6)?),
                    source: r.try_get(7)?,
                });
            }
            Ok(out)
        })
    }

    async fn put_behavior_cost(&self, _ctx: &Ctx, c: BehaviorCost) -> Result<(), Error> {
        on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(dialect(pg, "INSERT INTO llm_behavior_cost (behavior, instruction_hash, provider, model, tokens, baseline_tokens, measured_at, source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (behavior, instruction_hash, provider, model) DO UPDATE SET tokens = excluded.tokens, baseline_tokens = excluded.baseline_tokens,
		measured_at = excluded.measured_at, source = excluded.source")))
                .bind(&c.key.behavior)
                .bind(&c.key.hash)
                .bind(&c.key.provider)
                .bind(&c.key.model)
                .bind(c.tokens)
                .bind(c.baseline)
                .bind(c.measured_at.timestamp_millis())
                .bind(&c.source)
                .execute(pool)
                .await?;
            Ok(())
        })
    }

    async fn delete_behavior_costs(&self, _ctx: &Ctx, keys: &[CostKey]) -> Result<(), Error> {
        on_pool!(self, pool, pg => {
            for k in keys {
                sqlx::query(AssertSqlSafe(dialect(pg,
                    "DELETE FROM llm_behavior_cost WHERE behavior = ? AND instruction_hash = ? AND provider = ? AND model = ?")))
                    .bind(&k.behavior)
                    .bind(&k.hash)
                    .bind(&k.provider)
                    .bind(&k.model)
                    .execute(pool)
                    .await?;
            }
            Ok(())
        })
    }

    async fn model_baselines(&self, _ctx: &Ctx) -> Result<Vec<ModelBaseline>, Error> {
        on_pool!(self, pool, _pg => {
            let rows = sqlx::query("SELECT provider, model, tokens, measured_at FROM llm_model_baseline")
                .fetch_all(pool)
                .await?;
            let mut out = Vec::with_capacity(rows.len());
            for r in &rows {
                out.push(ModelBaseline {
                    provider: r.try_get(0)?,
                    model: r.try_get(1)?,
                    tokens: r.try_get(2)?,
                    measured_at: from_ms(r.try_get(3)?),
                });
            }
            Ok(out)
        })
    }

    async fn put_model_baseline(&self, _ctx: &Ctx, b: ModelBaseline) -> Result<(), Error> {
        on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(dialect(pg, "INSERT INTO llm_model_baseline (provider, model, tokens, measured_at) VALUES (?, ?, ?, ?)
        ON CONFLICT (provider, model) DO UPDATE SET tokens = excluded.tokens, measured_at = excluded.measured_at")))
                .bind(&b.provider)
                .bind(&b.model)
                .bind(b.tokens)
                .bind(b.measured_at.timestamp_millis())
                .execute(pool)
                .await?;
            Ok(())
        })
    }

    async fn delete_model_baselines(
        &self,
        _ctx: &Ctx,
        keys: &[(String, String)],
    ) -> Result<(), Error> {
        on_pool!(self, pool, pg => {
            for k in keys {
                sqlx::query(AssertSqlSafe(dialect(pg, "DELETE FROM llm_model_baseline WHERE provider = ? AND model = ?")))
                    .bind(&k.0)
                    .bind(&k.1)
                    .execute(pool)
                    .await?;
            }
            Ok(())
        })
    }
}
