//! The graph publishes a NodeEvent per version written and a BaselineEvent per baseline,
//! once the transaction committed (ADR 0026), with the search properties of the type (inherited ones included).

use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::SearchProperty;
use parking_lot::Mutex;

use crate::testsupport::*;
use crate::{EventSink, GraphEvent, Result};

#[derive(Default)]
pub(crate) struct RecSink(pub(crate) Mutex<Vec<(String, GraphEvent)>>);

#[async_trait]
impl EventSink for RecSink {
    async fn publish(&self, _: &Ctx, subject: &str, ev: &GraphEvent) -> Result<()> {
        self.0.lock().push((subject.to_string(), ev.clone()));
        Ok(())
    }
}

fn search(p: &str, text: bool, facet: bool) -> SearchProperty {
    SearchProperty {
        property: p.into(),
        text,
        facet,
    }
}

#[tokio::test]
async fn observe_node_and_baseline_events() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let mut g = r.graph();
        must(g.bootstrap(&c).await);
        use_types(
            &mut g,
            Arc::new(TestTypes::new(&[
                (
                    "Item",
                    TestType {
                        search: vec![search("title", true, true), search("prio", false, true)],
                        ..Default::default()
                    },
                ),
                (
                    "SubItem",
                    TestType {
                        extends: "Item".into(),
                        search: vec![search("notes", true, false)],
                        ..Default::default()
                    },
                ),
            ])),
        );
        let sink = Arc::new(RecSink::default());
        g.observe(sink.clone());
        must(
            land(
                &g,
                "",
                &[Landing {
                    properties: props(&[
                        ("title", "hello".into()),
                        ("prio", "high".into()),
                        ("notes", "long text".into()),
                        ("other", 1.into()),
                    ]),
                    ..landing("ITEM-1", "SubItem")
                }],
            )
            .await,
        );
        let n = must(g.node_by_key(&c, "", "ITEM-1").await);
        let got = sink.0.lock().clone();
        let evs: Vec<_> = got
            .iter()
            .filter_map(|(s, e)| match e {
                GraphEvent::Node(ev) if ev.id == n.id => Some((s.clone(), ev.clone())),
                _ => None,
            })
            .collect();
        assert_eq!(evs.len(), 1, "one NodeEvent for the node among {got:?}");
        let (subject, ev) = &evs[0];
        assert!(
            subject.ends_with(&format!(".{}.written", n.id)),
            "bad subject {subject}"
        );
        assert_eq!(ev.r#type, "SubItem");
        assert_eq!(ev.text.len(), 2, "text = {:?}", ev.text);
        assert_eq!(ev.text["title"], "hello");
        assert_eq!(ev.text["notes"], "long text");
        assert_eq!(ev.facets.len(), 2, "facets = {:?}", ev.facets);
        assert_eq!(ev.facets["title"], "hello");
        assert_eq!(ev.facets["prio"], "high");
        let advanced = got
            .iter()
            .filter(|(s, e)| {
                matches!(e, GraphEvent::Baseline(_)) && s == "goap.baseline.main.advanced"
            })
            .count();
        assert!(advanced > 0, "no baseline event: {got:?}");
        assert!(
            got.iter().any(|(_, e)| matches!(e, GraphEvent::Change(_))),
            "no change event"
        );
        // nothing is published for a transaction rolled back
        let before = sink.0.lock().len();
        let tx = must(g.begin(&c).await);
        must(tx.rollback().await);
        assert_eq!(sink.0.lock().len(), before);
        // republishing emits every node and change again
        let rec = RecSink::default();
        let count = must(g.republish(&c, &rec).await);
        assert!(count > 0);
        assert!(
            rec.0
                .lock()
                .iter()
                .any(|(_, e)| matches!(e, GraphEvent::Node(ev) if ev.id == n.id))
        );
    })
    .await;
}
