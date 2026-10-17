//! Go: startbaseline_test.go.

use std::sync::Arc;

use goap_engine::{Engine, MemoryStore, StartRequest, StaticMethodologies};
use goap_graph::{Graph, Memory, TypeCatalog};
use goap_graphtest as graphtest;
use goap_methodology as methodology;

use crate::common::*;
use crate::sdlc::dev_ctx;

/// A request without a baseline starts from the latest baseline of its methodology's namespace (the assistant only
/// knows the methodology once the intent is identified).
#[tokio::test]
async fn start_without_baseline() {
    init();
    let ctx = dev_ctx();
    let m = methodology::load_file(path("methodologies/sdlc.yaml")).unwrap();
    let cm = Arc::new(m.compile().unwrap());
    let ns = cm.definition().namespace.clone();
    let ds = goap_def::load_domains(path("domains")).unwrap();
    let cat = Arc::new(goap_typecat::new(&ds).unwrap());
    let mut g = Graph::new(Arc::new(Memory::new()));
    {
        let cat: Arc<dyn TypeCatalog> = cat.clone();
        g.types = Some(Arc::new(move || Some(cat.clone())));
    }
    let g = Arc::new(g);
    let mut e = Engine::new(
        g.clone(),
        Arc::new(StaticMethodologies::new().with(cm)),
        Arc::new(MemoryStore::new()),
    );
    e.intent = Some(goap_intent::Resolver::new(Arc::new(goap_intent::Lexical)));
    {
        let cat: Arc<dyn goap_def::TypeSet> = cat;
        e.types = Some(Arc::new(move || Some(cat.clone())));
    }
    let e = Arc::new(e);
    let req = StartRequest {
        methodology: "sdlc".into(),
        agent: "delivery".into(),
        goal: "deliver".into(),
        intent: "Allow payment in 3 installments".into(),
        project_id: TEST_PROJECT.into(),
        ..Default::default()
    };

    goap_devseed::demo(&ctx, &g).await.unwrap();
    graphtest::project(&ctx, &g, TEST_PROJECT, "Test")
        .await
        .unwrap();
    let bs = g.baselines(&ctx, &ns).await.unwrap();
    let latest = bs.last().unwrap_or_else(|| panic!("no baseline of {ns}"));
    let p = e.start(&ctx, req).await.unwrap();
    let c = g.change(&ctx, &p.change_id).await.unwrap();
    assert!(
        p.baseline_id == latest.id && c.baseline_id == latest.id && c.namespace == ns,
        "process baseline {}, change baseline {} ({}), want {} ({ns})",
        p.baseline_id,
        c.baseline_id,
        c.namespace,
        latest.id
    );
}
