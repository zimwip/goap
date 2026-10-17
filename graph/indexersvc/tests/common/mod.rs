//! Shared fakes of the tests (the Go package's test helpers).
#![allow(dead_code)]

use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use goap_authz::{Authorizer, Principal, Request};
use goap_ctx::Ctx;
use goap_indexersvc::{Access, BoxError, Service};
use goap_llm as llm;

/// A bag-of-words embedder: one dimension per hashed token.
#[derive(Default)]
pub struct BagEmbedder {
    pub fail: bool,
}

pub fn bag(texts: &[String]) -> llm::EmbedResponse {
    let mut out = llm::EmbedResponse::default();
    for t in texts {
        let mut v = vec![0f32; 256];
        for w in goap_index::tokens(t) {
            let mut h: i64 = 0;
            for c in w.chars() {
                h = h.wrapping_mul(31).wrapping_add(c as i64);
            }
            v[h.rem_euclid(256) as usize] += 1.0;
        }
        out.vectors.push(v);
    }
    out
}

#[async_trait]
impl llm::Embedder for BagEmbedder {
    async fn embed(
        &self,
        _ctx: &Ctx,
        req: llm::EmbedRequest,
    ) -> Result<llm::EmbedResponse, llm::Error> {
        if self.fail {
            return Err(llm::Error::Message("unexpected EOF".into()));
        }
        Ok(bag(&req.texts))
    }
}

/// Denies everything on one type.
pub struct DenyType(pub &'static str);

#[async_trait]
impl Authorizer for DenyType {
    async fn authorize(&self, _ctx: &Ctx, r: &Request) -> Result<bool, goap_authz::Error> {
        Ok(r.resource.r#type != self.0)
    }
}

/// subject -> projects (and below) it holds; "*": every project.
pub struct FakeAccess {
    pub projects: HashMap<&'static str, Vec<&'static str>>,
    pub subs: HashMap<&'static str, Vec<&'static str>>,
}

#[async_trait]
impl Access for FakeAccess {
    async fn may_access_project(
        &self,
        _ctx: &Ctx,
        p: &Principal,
        project: &str,
    ) -> Result<bool, BoxError> {
        let l = self
            .projects
            .get(p.subject.as_str())
            .cloned()
            .unwrap_or_default();
        Ok(l.contains(&"*") || l.contains(&project))
    }

    async fn sub_projects(&self, _ctx: &Ctx, project: &str) -> Result<Vec<String>, BoxError> {
        Ok(match self.subs.get(project) {
            Some(s) => s.iter().map(|s| s.to_string()).collect(),
            None => vec![project.to_string()],
        })
    }
}

pub fn service(
    embedder: Option<Arc<dyn llm::Embedder>>,
    authz: Option<Arc<dyn Authorizer>>,
) -> Service {
    Service::new(Arc::new(goap_index::Memory::new()), embedder, authz)
}

pub fn as_(subject: &str) -> Ctx {
    goap_authz::with_principal(
        &Ctx::background(),
        Principal {
            subject: subject.into(),
            roles: vec!["contributor".into()],
            ..Principal::default()
        },
    )
}

/// The sink of the indexer as the graph's event sink (Go's `Sink` satisfies `graph.EventSink` structurally; the crate
/// does not depend on the graph, so the tests adapt it).
pub struct GraphSink(pub goap_indexersvc::Sink);

#[async_trait]
impl goap_graph::EventSink for GraphSink {
    async fn publish(
        &self,
        ctx: &Ctx,
        subject: &str,
        ev: &goap_graph::GraphEvent,
    ) -> goap_graph::Result<()> {
        self.0
            .publish(ctx, subject, ev)
            .await
            .map_err(goap_graph::Error::from)
    }
}

/// A graph over memory typed by one domain, its events going to `svc`.
pub fn docs_graph(ctx: &Ctx, svc: &Arc<Service>, domain: &str) -> goap_graph::Graph {
    let d = goap_def::parse_domain(domain.as_bytes()).unwrap();
    let cat: Arc<dyn goap_graph::TypeCatalog> = Arc::new(goap_typecat::Catalog::new(&[d]).unwrap());
    let mut g = goap_graph::Graph::new(Arc::new(goap_graph::Memory::new()));
    g.set_types(move || Some(cat.clone()));
    g.observe(Arc::new(GraphSink(goap_indexersvc::Sink::new(
        ctx,
        svc.clone(),
    ))));
    g
}

/// Makes the service reindex by republishing the graph into a new sink.
pub fn republish_from(svc: &Arc<Service>, g: &Arc<goap_graph::Graph>) {
    let (svc2, g) = (svc.clone(), g.clone());
    svc.set_republish(Some(goap_indexersvc::republish_fn(move |ctx: Ctx| {
        let (svc, g) = (svc2.clone(), g.clone());
        async move {
            let sink = GraphSink(goap_indexersvc::Sink::new(&ctx, svc));
            g.republish(&ctx, &sink)
                .await
                .map_err(|e| Box::new(e) as goap_indexersvc::BoxError)
        }
    })));
}

/// Polls until the search returns want hits (the sink is asynchronous).
pub async fn wait_for(
    svc: &Service,
    ctx: &Ctx,
    q: &goap_index::Query,
    want: usize,
) -> goap_index::SearchResult {
    let mut res = goap_index::SearchResult::default();
    for _ in 0..200 {
        res = svc.searcher.search(ctx, q.clone()).await.unwrap();
        if res.total == want {
            return res;
        }
        tokio::time::sleep(std::time::Duration::from_millis(10)).await;
    }
    panic!("want {want} hits, got {} ({:?})", res.total, res.hits);
}
