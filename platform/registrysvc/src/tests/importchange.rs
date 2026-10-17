//! importchange_test.go, over the graph in memory of the tests.

use std::sync::Arc;

use goap_authz::{Principal, with_principal};
use goap_ctx::Ctx;

use super::{FakeGraph, registry};
use crate::graphstore::{GraphStore, StoreGraph};
use crate::store::STATUS_PUBLISHED;

// Importing a methodology and publishing it is one change, and the aliases it needs are proposed in one change.
#[tokio::test]
async fn import_published_is_one_change() {
    super::setup();
    let ctx = with_principal(
        &Ctx::background(),
        Principal {
            subject: "system:registry".into(),
            roles: vec!["admin".into()],
            ..Principal::default()
        },
    );
    let g = FakeGraph::new();
    let (reg, d) = registry(Arc::new(GraphStore::new(g.clone())), false, |_| {});
    d.seed_domains(&ctx, "../../domains").await.expect("seed");
    let src = br#"
name: imp
version: "1"
namespace: alm
agents:
  - {name: a1, planner: goap, model: alias-one, actions: [x]}
  - {name: a2, planner: goap, model: alias-two, actions: [x]}
conditions: [{name: c, expr: "true"}]
actions:
  - {name: x, kind: human, effects: {c: true}}
goals: [{name: g, pre: {c: true}}]
"#;
    let (r, _) = reg.import(&ctx, src, true).await.expect("import");
    assert!(
        r.status == STATUS_PUBLISHED && r.published_at.is_some(),
        "imported as published: {r:?}"
    );
    let cs = g.changes(&ctx).await.expect("changes");
    let (mut methodologies, mut aliases) = (0, 0);
    for c in &cs {
        if c.title.starts_with("Methodology imp") {
            methodologies += 1;
        } else if c.title.starts_with("Model aliases needed by imp") {
            aliases += 1;
            assert_eq!(c.nodes.len(), 2, "both aliases in the one change");
        }
    }
    assert!(
        methodologies == 1 && aliases == 1,
        "one change for the import ({methodologies}) and one for its aliases ({aliases})"
    );
}
