//! The facets of a blackboard (ADR 0066, 0109).

use std::sync::Arc;

use goap_ctx::Ctx;
use goap_domain::*;
use parking_lot::Mutex;
use serde_json::json;

use crate::tests::flownodes::new_flow_world;
use crate::testsupport::*;
use crate::{BlackboardFacet, LogFacet, LogFacetFold, NewChange, Result};

// The blackboard holds the built-in facets (options, active option, decision points) and the ones the providers of
// Graph.Facets compute from the change; WithFacet leaves the original untouched.
#[tokio::test]
async fn blackboard_facets() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut f = new_fixture(r).await;
        let title: BlackboardFacet = Arc::new(|c: &Change, _| {
            let v: FacetValue = Arc::new(format!("facet of {}", c.title));
            v
        });
        f.g.facets.insert("title".into(), title);
        let g = &f.g;
        let c = must(
            g.create_change(
                &ctx,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "c".into(),
                    baseline_id: f.base.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        let bb = must(g.blackboard(&ctx, &c.id).await);
        assert_eq!(
            facet::<String>(&bb, "title"),
            "facet of c",
            "provider facet"
        );
        for name in [FACET_OPTIONS, FACET_ACTIVE_OPTION, FACET_DECISION_POINTS] {
            assert!(
                bb.facets.contains_key(name),
                "no built-in facet {name}: {:?}",
                bb.facets.keys()
            );
        }
        assert!(
            options_of(&bb).is_empty()
                && active_option_of(&bb).is_empty()
                && decision_points_of(&bb).is_empty(),
            "a fresh change has no option or decision point"
        );
        let other = bb.with_facet(FACET_ACTIVE_OPTION, "x".to_string());
        assert!(
            active_option_of(&other) == "x" && active_option_of(&bb).is_empty(),
            "WithFacet must not change the original"
        );
        assert_eq!(
            facet::<i64>(&bb, "title"),
            0,
            "a facet of another type reads as the zero value"
        );
    })
    .await;
}

/// A fold of the test: counts the entries, and keeps them.
struct Seen {
    change: ChangeID,
    folded: Arc<Mutex<Vec<LogEntry>>>,
}

#[async_trait::async_trait]
impl LogFacetFold for Seen {
    async fn fold(&self, _: &Ctx, c: &Change, es: &[LogEntry]) -> Result<FacetValue> {
        assert_eq!(c.id, self.change, "folded for {}", c.id);
        *self.folded.lock() = es.to_vec();
        Ok(Arc::new(es.len()))
    }
}

// A log facet is folded from the entries of its types for the blackboard of a change, and for the one its merge reads
// (the history of ADR 0109 is one).
#[tokio::test]
async fn log_facets() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut w = new_flow_world(r).await;
        let c = w.change.clone();
        let folded = Arc::new(Mutex::new(Vec::new()));
        w.f.g.log_facets.insert(
            "seen".into(),
            LogFacet {
                types: vec!["journal.step.ended".into()],
                fold: Arc::new(Seen {
                    change: c.id.clone(),
                    folded: folded.clone(),
                }),
            },
        );
        let g = &w.f.g;
        let entry = |kind: &str| {
            let id = uuid::Uuid::new_v4().to_string();
            LogEntry {
                id: id.clone(),
                change: c.id.clone(),
                r#type: format!("journal.{kind}"),
                execution: id,
                at: Some(g.now()),
                payload: json!({}),
                ..Default::default()
            }
        };
        must(
            g.append_log(
                &ctx,
                vec![entry("tick"), entry("step.ended"), entry("step.ended")],
            )
            .await,
        );
        let bb = must(g.blackboard(&ctx, &c.id).await);
        assert!(
            facet::<usize>(&bb, "seen") == 2 && folded.lock().len() == 2,
            "the facet folds the entries of its types: {}",
            folded.lock().len()
        );
    })
    .await;
}
