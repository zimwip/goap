//! Tests of the registry: ports of the `*_test.go` files. They run over a [`FakeGraph`], and the ones the graph
//! decides over the real graph too (`realgraph`, goap-graph as a dev-dependency).

// the definition structs keep private fields, so the tests build them by assignment over a default
#![allow(
    clippy::field_reassign_with_default,
    clippy::type_complexity,
    clippy::err_expect
)]

mod activate;
mod activitygoals;
mod attributes;
mod builtins;
mod changelifecycle;
mod compilecache;
mod convert;
mod fakegraph;
mod graphstore;
mod guardians;
mod importchange;
mod preview;
mod realgraph;
mod refs;
mod scopes;
mod service;
mod subactivity;
mod suspend;
mod versions;

use std::sync::{Arc, Once, OnceLock, Weak};

use chrono::{DateTime, TimeZone, Utc};
use goap_authz::{Casbin, Principal, with_principal};
use goap_ctx::Ctx;
use goap_methodology::Methodology;
use serde_json::Value;

pub(crate) use fakegraph::FakeGraph;

use crate::graphstore::GraphStore;
use crate::service::Service;
use crate::store::{MemoryStore, Store};

/// The kinds of item of the risk register, as the services register them at start (ADR 0065): TestMain in Go.
pub(crate) fn setup() {
    static ONCE: Once = Once::new();
    ONCE.call_once(|| {
        goap_risk::register();
        goap_assess::register();
        goap_verify::register();
    });
}

/// A caller with roles (mia of acme).
pub(crate) fn as_(roles: &[&str]) -> Ctx {
    setup();
    with_principal(
        &Ctx::background(),
        Principal {
            subject: "mia".into(),
            org: "acme".into(),
            roles: roles.iter().map(|r| r.to_string()).collect(),
            ..Principal::default()
        },
    )
}

/// The background context of a test.
pub(crate) fn bg() -> Ctx {
    setup();
    Ctx::background()
}

/// The example methodology the tests act on (its types resolved by the registry from its own domains).
pub(crate) fn example() -> Methodology {
    let mut m = goap_methodology::load_file("../../methodologies/examples/impact-analysis.yaml")
        .expect("impact-analysis");
    m.types = None;
    m
}

/// A store of each kind: in memory, and over a graph.
pub(crate) fn stores() -> Vec<(&'static str, Arc<dyn Store>)> {
    vec![
        ("memory", Arc::new(MemoryStore::new())),
        ("graph", Arc::new(GraphStore::new(FakeGraph::new()))),
        ("real graph", Arc::new(GraphStore::new(realgraph::real()))),
    ]
}

/// The domain registry in process, as the compositions wrap it (this crate does not depend on goap-domainsvc,
/// ADR 0125 §3).
pub(crate) struct InProcess(pub Arc<goap_domainsvc::Service>);

#[async_trait::async_trait]
impl crate::Domains for InProcess {
    async fn domain_versions(&self, ctx: &Ctx, all: bool) -> crate::Result<Vec<goap_def::Record>> {
        self.0
            .domain_versions(ctx, all)
            .await
            .map_err(crate::Error::domains)
    }
    async fn get_domain(
        &self,
        ctx: &Ctx,
        name: &str,
        version: &str,
    ) -> crate::Result<goap_def::Record> {
        self.0
            .get_domain(ctx, name, version)
            .await
            .map_err(crate::Error::domains)
    }
}

/// A registry under test with its domain registry (ADR 0125 §3): one in memory, that tells it of each publication
/// as goap-dev does (domainsOf in Go). `configure` sets the fields of the service before it is shared.
pub(crate) fn registry(
    store: Arc<dyn Store>,
    authz: bool,
    configure: impl FnOnce(&mut Service),
) -> (Arc<Service>, Arc<goap_domainsvc::Service>) {
    setup();
    let enf: Option<Arc<dyn goap_authz::Authorizer>> = if authz {
        Some(Arc::new(Casbin::new().expect("casbin")))
    } else {
        None
    };
    let cell: Arc<OnceLock<Weak<Service>>> = Arc::new(OnceLock::new());
    let mut d = goap_domainsvc::Service::new(Arc::new(goap_domainsvc::MemoryStore::new()));
    d.authz = enf.clone();
    let c = cell.clone();
    d.changed = Some(goap_domainsvc::changed_fn(move |ctx: Ctx, _| {
        let c = c.clone();
        async move {
            if let Some(s) = c.get().and_then(Weak::upgrade) {
                let _ = s.domains_changed(&ctx).await;
            }
        }
    }));
    let d = Arc::new(d);
    let mut s = Service::new(store);
    s.authz = enf;
    s.domains = Some(Arc::new(InProcess(d.clone())));
    configure(&mut s);
    let s = Arc::new(s);
    let _ = cell.set(Arc::downgrade(&s));
    (s, d)
}

/// withALM publishes the alm domain of the repository, which the example methodologies act on.
pub(crate) async fn with_alm(d: &goap_domainsvc::Service) {
    let src = std::fs::read("../../domains/alm.yaml").expect("alm.yaml");
    let (_, issues) = d
        .import_domain(&as_(&["admin"]), &src, true)
        .await
        .expect("alm domain");
    assert!(issues.0.is_empty(), "alm domain: {issues}");
}

/// The JSON of a value with sorted keys and integral numbers written as integers (canon in Go: Go's JSON writes
/// 0 for a float64 0, Rust's 0.0).
pub(crate) fn canon<T: serde::Serialize>(v: &T) -> String {
    fn norm(v: Value) -> Value {
        match v {
            Value::Object(m) => {
                let mut keys: Vec<(String, Value)> = m.into_iter().collect();
                keys.sort_by(|a, b| a.0.cmp(&b.0));
                Value::Object(keys.into_iter().map(|(k, v)| (k, norm(v))).collect())
            }
            Value::Array(l) => Value::Array(l.into_iter().map(norm).collect()),
            Value::Number(n) => match n.as_f64() {
                Some(f) if n.is_f64() && f.fract() == 0.0 && f.abs() < 1e15 => {
                    Value::from(f as i64)
                }
                _ => Value::Number(n),
            },
            v => v,
        }
    }
    norm(serde_json::to_value(v).expect("json")).to_string()
}

/// A fixed instant.
pub(crate) fn at(y: i32, mo: u32, d: u32, h: u32, mi: u32) -> DateTime<Utc> {
    Utc.with_ymd_and_hms(y, mo, d, h, mi, 0).unwrap()
}

/// tickingClock gives every write its own instant, so that "published last" is never a tie.
pub(crate) fn ticking_clock() -> crate::service::Clock {
    let t = parking_lot::Mutex::new(at(2026, 1, 1, 0, 0));
    Arc::new(move || {
        let mut g = t.lock();
        *g += chrono::Duration::seconds(1);
        *g
    })
}

/// The YAML of a domain.
pub(crate) fn must_yaml(d: &goap_def::Domain) -> Vec<u8> {
    d.yaml().expect("yaml").into_bytes()
}

/// A store in memory.
pub(crate) fn memory() -> Arc<dyn Store> {
    Arc::new(MemoryStore::new())
}
