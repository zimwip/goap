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
    /// Per side, the calls whose time is at most each of [`BUCKETS`] (not cumulated: one bucket per call).
    #[serde(skip)]
    pub buckets: BTreeMap<&'static str, [u64; BUCKETS.len()]>,
}

/// The upper bounds, in seconds, of the histogram of the time of the services behind the proxy (the calls above the last
/// are counted in `+Inf` only).
pub const BUCKETS: [f64; 14] = [
    0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0,
];

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
        let secs = d.as_secs_f64();
        if let Some(i) = BUCKETS.iter().position(|b| secs <= *b) {
            self.calls
                .get_mut(procedure)
                .expect("entered above")
                .buckets
                .entry(side)
                .or_default()[i] += 1;
        }
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
        let _ = writeln!(
            s,
            "# HELP goap_proxy_upstream_duration_seconds The time of the services behind the proxy.\n# TYPE goap_proxy_upstream_duration_seconds histogram"
        );
        for (p, c) in &self.calls {
            let (svc, m) = split(p);
            for (side, (sum, n)) in &c.seconds {
                let labels = format!("service=\"{svc}\",method=\"{m}\",side=\"{side}\"");
                let mut acc = 0;
                let none = [0; BUCKETS.len()];
                let counts = c.buckets.get(side).unwrap_or(&none);
                for (b, k) in BUCKETS.iter().zip(counts) {
                    acc += k;
                    let _ = writeln!(
                        s,
                        "goap_proxy_upstream_duration_seconds_bucket{{{labels},le=\"{b}\"}} {acc}"
                    );
                }
                let _ = writeln!(
                    s,
                    "goap_proxy_upstream_duration_seconds_bucket{{{labels},le=\"+Inf\"}} {n}"
                );
                let _ = writeln!(s, "goap_proxy_upstream_duration_seconds_sum{{{labels}}} {sum}");
                let _ = writeln!(s, "goap_proxy_upstream_duration_seconds_count{{{labels}}} {n}");
            }
        }
        s
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_histogram_cumulates_the_buckets() {
        let mut st = Stats::default();
        let p = "/goap.graph.v1.GraphService/GetNode";
        st.time(p, "rust", Duration::from_micros(800));
        st.time(p, "rust", Duration::from_millis(20));
        st.time(p, "rust", Duration::from_secs(60));
        let m = st.metrics("rust");
        let at = |le: &str| {
            format!(
                "goap_proxy_upstream_duration_seconds_bucket{{service=\"GraphService\",method=\"GetNode\",side=\"rust\",le=\"{le}\"}}"
            )
        };
        for (le, n) in [("0.001", 1), ("0.01", 1), ("0.025", 2), ("30", 2), ("+Inf", 3)] {
            assert!(m.contains(&format!("{} {n}\n", at(le))), "{le}: {m}");
        }
        assert!(m.contains("goap_proxy_upstream_duration_seconds_count{service=\"GraphService\",method=\"GetNode\",side=\"rust\"} 3\n"), "{m}");
    }
}
