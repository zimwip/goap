//! Helpers shared by the access tests (the helpers of the Go test files).
#![allow(dead_code)]

use std::sync::Arc;
use std::time::Duration;

use goap_access::{Authorizer, Directory};
use goap_authz::{Principal, Request, Resource};
use goap_ctx::Ctx;
use goap_graph::{Graph, Memory};

/// The type catalogue of the domains of the repository (ADR 0012).
pub fn domains_catalog() -> Arc<goap_typecat::Catalog> {
    let ds = goap_def::load_domains("../../../domains").expect("load the domains");
    Arc::new(goap_typecat::new(&ds).expect("catalogue of the domains"))
}

/// A graph over a memory store judged by the domains of the repository.
pub fn typed_graph() -> Graph {
    let mut g = Graph::new(Arc::new(Memory::new()));
    let cat = domains_catalog();
    g.set_types(move || Some(cat.clone() as Arc<dyn goap_graph::TypeCatalog>));
    g
}

/// A directory over a graph with a TTL of one nanosecond (Go's `TTL: 1`).
pub fn directory(g: &Arc<Graph>) -> Arc<Directory> {
    Arc::new(Directory::new(g.clone()).with_ttl(Duration::from_nanos(1)))
}

/// setup returns a graph judged by the domains of the repository (ADR 0012), so a User node seeded here goes
/// through the same user lifecycle (ADR 0048) a real deployment enforces.
pub fn setup() -> (Arc<Graph>, Authorizer) {
    let g = Arc::new(typed_graph());
    let a = Authorizer::new(directory(&g)).expect("authorizer");
    (g, a)
}

pub fn ctx() -> Ctx {
    Ctx::background()
}

pub fn principal(subject: &str, org: &str, roles: &[&str]) -> Principal {
    Principal {
        subject: subject.into(),
        org: org.into(),
        roles: roles.iter().map(|r| r.to_string()).collect(),
        ..Default::default()
    }
}

pub async fn can(a: &dyn goap_authz::Authorizer, p: &Principal, res: &str, act: &str) -> bool {
    a.authorize(
        &ctx(),
        &Request {
            subject: p.clone(),
            action: act.into(),
            resource: Resource {
                r#type: res.into(),
                org: p.org.clone(),
                ..Default::default()
            },
        },
    )
    .await
    .expect("authorize")
}

pub fn strings(ss: &[&str]) -> Vec<String> {
    ss.iter().map(|s| s.to_string()).collect()
}
