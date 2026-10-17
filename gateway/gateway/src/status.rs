use std::{sync::Arc, time::Duration};

use axum::{Json, routing::MethodRouter};
use chrono::{DateTime, Utc};
use serde::{Deserialize, Deserializer, Serialize, Serializer};
use tokio::sync::Mutex;

use crate::gateway::Route;

/// ServiceStatus is the health of one upstream service.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct ServiceStatus {
    pub name: String,
    /// up | down
    pub status: String,
    #[serde(rename = "latencyMs")]
    pub latency_ms: i64,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub error: String,
}

/// PlatformStatus aggregates the services health.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct PlatformStatus {
    /// ok | degraded | down
    pub status: String,
    pub services: Vec<ServiceStatus>,
    #[serde(serialize_with = "go_time", deserialize_with = "parse_time")]
    pub time: DateTime<Utc>,
}

/// Go's RFC 3339 with nanoseconds, trailing zeros of the fraction dropped.
fn go_time<S: Serializer>(t: &DateTime<Utc>, s: S) -> Result<S::Ok, S::Error> {
    let base = t.format("%Y-%m-%dT%H:%M:%S").to_string();
    let nanos = t.timestamp_subsec_nanos();
    let text = if nanos == 0 {
        format!("{base}Z")
    } else {
        format!("{base}.{}Z", format!("{nanos:09}").trim_end_matches('0'))
    };
    s.serialize_str(&text)
}

fn parse_time<'de, D: Deserializer<'de>>(d: D) -> Result<DateTime<Utc>, D::Error> {
    let s = String::deserialize(d)?;
    DateTime::parse_from_rfc3339(&s)
        .map(|t| t.with_timezone(&Utc))
        .map_err(serde::de::Error::custom)
}

/// service_name derives "graph" from "/goap.graph.v1.GraphService/".
fn service_name(prefix: &str) -> String {
    let parts: Vec<&str> = prefix.trim_matches('/').split('.').collect();
    if parts.len() >= 2 {
        return parts[1].to_string();
    }
    prefix.to_string()
}

/// check_status probes the readiness endpoint of every upstream in parallel.
async fn check_status(routes: &[Route], hc: &reqwest::Client) -> PlatformStatus {
    let probes = routes.iter().map(|r| async move {
        let mut s = ServiceStatus {
            name: service_name(&r.prefix),
            status: "down".into(),
            ..Default::default()
        };
        let start = std::time::Instant::now();
        let url = format!("{}/readyz", r.upstream.trim_end_matches('/'));
        let resp = hc.get(url).send().await;
        s.latency_ms = start.elapsed().as_millis() as i64;
        match resp {
            Err(_) => s.error = "unreachable".into(),
            Ok(resp) if resp.status() != reqwest::StatusCode::OK => {
                let st = resp.status();
                s.error = format!("{} {}", st.as_u16(), st.canonical_reason().unwrap_or(""))
                    .trim_end()
                    .to_string();
            }
            Ok(_) => s.status = "up".into(),
        }
        s
    });
    let mut services = futures::future::join_all(probes).await;
    services.sort_by(|a, b| a.name.cmp(&b.name));
    let up = services.iter().filter(|s| s.status == "up").count();
    let status = if up == services.len() {
        "ok"
    } else if up == 0 {
        "down"
    } else {
        "degraded"
    };
    PlatformStatus {
        status: status.into(),
        services,
        time: Utc::now(),
    }
}

/// status_handler serves GET /api/status (cached a few seconds). Errors are kept generic: the endpoint does not
/// reveal internal addresses.
pub(crate) fn status_handler(routes: Vec<Route>) -> MethodRouter {
    let hc = reqwest::Client::builder()
        .timeout(Duration::from_secs(2))
        .build()
        .unwrap_or_default();
    let routes = Arc::new(routes);
    let cached: Arc<Mutex<Option<PlatformStatus>>> = Arc::new(Mutex::new(None));
    axum::routing::get(move || {
        let (hc, routes, cached) = (hc.clone(), routes.clone(), cached.clone());
        async move {
            let mut c = cached.lock().await;
            let stale = match &*c {
                Some(s) => {
                    (Utc::now() - s.time).to_std().unwrap_or_default() > Duration::from_secs(3)
                }
                None => true,
            };
            if stale {
                *c = Some(check_status(&routes, &hc).await);
            }
            Json(c.clone().unwrap_or_default())
        }
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn names_of_services() {
        assert_eq!(service_name("/goap.graph.v1.GraphService/"), "graph");
        assert_eq!(service_name("/plain/"), "/plain/");
    }
}
