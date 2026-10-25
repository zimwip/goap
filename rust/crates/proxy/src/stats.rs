//! What the proxy counts: the calls per procedure and who served them, the verdicts of a comparison, the time of
//! each side; read by `/metrics`, `/proxy/coverage` and `/proxy/report`.
use std::{collections::BTreeMap, fmt::Write as _, time::Duration};

use serde::Serialize;

use crate::compare::Verdict;

/// Who served a call.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, PartialOrd, Ord, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ServedBy {
    Go,
    Rust,
}

impl ServedBy {
    fn name(self) -> &'static str {
        match self {
            ServedBy::Go => "go",
            ServedBy::Rust => "rust",
        }
    }
}

#[derive(Debug, Default, Clone, Serialize)]
pub struct Counts {
    pub served: BTreeMap<ServedBy, u64>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub verdicts: BTreeMap<Verdict, u64>,
    #[serde(skip)]
    pub seconds: BTreeMap<&'static str, (f64, u64)>,
}

#[derive(Debug, Default)]
pub struct Stats {
    /// Per procedure (`/goap.graph.v1.GraphService/GetNode`), or `other` for a path of no RPC.
    pub calls: BTreeMap<String, Counts>,
}

impl Stats {
    pub fn served(&mut self, procedure: &str, by: ServedBy) {
        *self
            .calls
            .entry(procedure.to_string())
            .or_default()
            .served
            .entry(by)
            .or_default() += 1;
    }

    pub fn time(&mut self, procedure: &str, side: &'static str, d: Duration) {
        let e = self
            .calls
            .entry(procedure.to_string())
            .or_default()
            .seconds
            .entry(side)
            .or_default();
        e.0 += d.as_secs_f64();
        e.1 += 1;
    }

    pub fn verdict(&mut self, procedure: &str, v: Verdict) {
        *self
            .calls
            .entry(procedure.to_string())
            .or_default()
            .verdicts
            .entry(v)
            .or_default() += 1;
    }

    /// The verdicts over every procedure.
    pub fn verdicts(&self) -> BTreeMap<Verdict, u64> {
        let mut out = BTreeMap::new();
        for c in self.calls.values() {
            for (v, n) in &c.verdicts {
                *out.entry(*v).or_default() += n;
            }
        }
        out
    }

    /// The Prometheus text exposition.
    pub fn metrics(&self, mode: &str) -> String {
        let mut s = String::new();
        let split = |p: &str| -> (String, String) {
            let p = p.trim_start_matches('/');
            match p.split_once('/') {
                Some((svc, m)) => (svc.rsplit('.').next().unwrap_or(svc).to_string(), m.to_string()),
                None => (p.to_string(), String::new()),
            }
        };
        let _ = writeln!(
            s,
            "# HELP goap_proxy_info The mode of the proxy.\n# TYPE goap_proxy_info gauge\ngoap_proxy_info{{mode=\"{mode}\"}} 1"
        );
        let _ = writeln!(
            s,
            "# HELP goap_proxy_calls_total The calls the proxy forwarded, by who served them.\n# TYPE goap_proxy_calls_total counter"
        );
        for (p, c) in &self.calls {
            let (svc, m) = split(p);
            for (by, n) in &c.served {
                let _ = writeln!(
                    s,
                    "goap_proxy_calls_total{{service=\"{svc}\",method=\"{m}\",served_by=\"{}\"}} {n}",
                    by.name()
                );
            }
        }
        let _ = writeln!(
            s,
            "# HELP goap_proxy_compare_total The compared calls, by verdict.\n# TYPE goap_proxy_compare_total counter"
        );
        for (p, c) in &self.calls {
            let (svc, m) = split(p);
            for (v, n) in &c.verdicts {
                let _ = writeln!(
                    s,
                    "goap_proxy_compare_total{{service=\"{svc}\",method=\"{m}\",verdict=\"{}\"}} {n}",
                    v.name()
                );
            }
        }
        let _ = writeln!(
            s,
            "# HELP goap_proxy_upstream_seconds The time of the services behind the proxy.\n# TYPE goap_proxy_upstream_seconds summary"
        );
        for (p, c) in &self.calls {
            let (svc, m) = split(p);
            for (side, (sum, n)) in &c.seconds {
                let _ = writeln!(
                    s,
                    "goap_proxy_upstream_seconds_sum{{service=\"{svc}\",method=\"{m}\",side=\"{side}\"}} {sum}"
                );
                let _ = writeln!(
                    s,
                    "goap_proxy_upstream_seconds_count{{service=\"{svc}\",method=\"{m}\",side=\"{side}\"}} {n}"
                );
            }
        }
        s
    }
}
