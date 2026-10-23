//! The log of a change read through a filter (Go's `Tx.Log` / `LogCounts` over `logWhere`, ADR 0030).
use std::collections::HashMap;

use graph_core::{GraphError, impact::LogEntry};

use super::{Session, log_entry_of, value::Arg};

/// What a read of the log selects (`domain.LogFilter`).
#[derive(Debug, Clone, Default)]
pub struct LogFilter {
    pub change: String,
    /// types, a type ending in `.` a prefix
    pub types: Vec<String>,
    /// the flows (the main flow ""), None: every flow
    pub flows: Option<Vec<String>>,
    pub processes: Vec<String>,
    pub execution: String,
    pub after_seq: i64,
    /// at most (0: no limit)
    pub limit: i64,
}

impl Session {
    /// The conditions of a filter and their arguments: one statement text for both dialects (`logWhere`).
    fn log_where(&self, f: &LogFilter, after: bool) -> (String, Vec<Arg>) {
        let mut conds = Vec::new();
        let mut args: Vec<Arg> = Vec::new();
        let arg = |args: &mut Vec<Arg>, v: Arg| {
            args.push(v);
            self.sql.ph(args.len())
        };
        if !f.change.is_empty() {
            conds.push(format!("change_id = {}", arg(&mut args, f.change.as_str().into())));
        }
        if !f.types.is_empty() {
            let ors: Vec<String> = f
                .types
                .iter()
                .map(|t| match t.ends_with('.') {
                    true => format!("type LIKE {}", arg(&mut args, Arg::Text(format!("{t}%")))),
                    false => format!("type = {}", arg(&mut args, t.as_str().into())),
                })
                .collect();
            conds.push(format!("({})", ors.join(" OR ")));
        }
        let within = |args: &mut Vec<Arg>, col: &str, vals: &[String]| {
            let ps: Vec<String> = vals.iter().map(|v| arg(args, v.as_str().into())).collect();
            format!("{col} IN ({})", ps.join(", "))
        };
        match &f.flows {
            Some(fl) if fl.is_empty() => conds.push("1 = 0".into()),
            Some(fl) => conds.push(within(&mut args, "flow", fl)),
            None => {}
        }
        if !f.processes.is_empty() {
            conds.push(within(&mut args, "process_id", &f.processes));
        }
        if !f.execution.is_empty() {
            conds.push(format!("execution = {}", arg(&mut args, f.execution.as_str().into())));
        }
        if after && f.after_seq > 0 {
            conds.push(format!("seq > {}", arg(&mut args, Arg::Int(f.after_seq))));
        }
        if conds.is_empty() {
            return (String::new(), args);
        }
        (format!(" WHERE {}", conds.join(" AND ")), args)
    }

    /// The entries matching a filter, in order, with the number of entries per type the filter selects without its
    /// types nor its start (`Tx.Log`, `Tx.LogCounts`).
    pub async fn change_log(&self, f: &LogFilter) -> Result<(Vec<LogEntry>, HashMap<String, i64>), GraphError> {
        let s = self.sql;
        let what = format!("log of change {}", f.change);
        if s.pg && !f.change.is_empty() && uuid::Uuid::try_parse(&f.change).is_err() {
            return Err(GraphError::unimplemented(
                "a change id PostgreSQL does not read: the error Go words is not served here yet",
            ));
        }
        let (w, args) = self.log_where(f, true);
        let mut q = format!(
            "SELECT seq, id, {}, type, flow, process_id, execution, subject, by_whom, causation, at, {} FROM change_log{w} ORDER BY seq",
            s.id("change_id"),
            // the text PostgreSQL gives of its jsonb, as Go reads it
            s.id("payload")
        );
        if f.limit > 0 {
            q.push_str(&format!(" LIMIT {}", f.limit));
        }
        let entries = self.select((q, args), &what).await?.iter().map(log_entry_of).collect();
        let all = LogFilter {
            types: Vec::new(),
            ..f.clone()
        };
        let (w, args) = self.log_where(&all, false);
        let counts = self
            .select(
                (format!("SELECT type, count(*) FROM change_log{w} GROUP BY type"), args),
                &what,
            )
            .await?
            .iter()
            .map(|r| (r.text(0), r.int(1)))
            .collect();
        Ok((entries, counts))
    }
}
