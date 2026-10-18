//! Facts, journal records and impact events are entries of one log, in one order,
//! filtered on their columns (ADR 0030); an edit of a change header is an entry of its log.

use goap_domain::{
    ChangeImpact, ChangeImpactID, ChangeItem, IMPACT_PROPOSED, INTENT_CREATED, ImpactEvent,
    LOG_CHANGE, LOG_FACT, LOG_IMPACT, LogEntry, LogFilter, TYPE_ARTIFACT,
};
use goap_graphapi::ChangePatch;
use serde_json::json;

use super::graph::new_change;
use crate::testsupport::*;
use crate::{ErrorKind, Graph};

/// A change with a fact and two impact events of run e1 (Go's newFlowWorld, without the flow: a later slice).
async fn world(r: &TestRepo) -> (Graph, goap_domain::Change) {
    let c = ctx();
    let f = new_fixture(r).await;
    let g = f.g;
    let ch = must(g.create_change(&c, new_change(&f.base, "log")).await);
    must(
        g.add_items(
            &c,
            &ch.id,
            vec![ChangeItem {
                r#type: TYPE_ARTIFACT,
                target: "note".into(),
                ..Default::default()
            }],
        )
        .await,
    );
    let mut tx = must(g.change_tx(&c, &ch.id).await);
    let mut events = Vec::new();
    for key in ["NEW-1", "NEW-2"] {
        let id = ChangeImpactID::new(g.new_id());
        events.push(ImpactEvent {
            change: ch.id.clone(),
            impact: id.clone(),
            op: IMPACT_PROPOSED,
            execution: "e1".into(),
            state: Some(ChangeImpact {
                id,
                key: key.into(),
                r#type: "Design".into(),
                intent: INTENT_CREATED,
                rationale: "new".into(),
                execution: "e1".into(),
                created_at: g.now(),
                ..Default::default()
            }),
            ..Default::default()
        });
    }
    must(g.emit(&c, &mut tx, events).await);
    must(tx.commit(&c).await);
    (g, ch)
}

#[tokio::test]
async fn change_log() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let (g, ch) = world(r).await;
        // the graph knows no use case: the entries of the execution journal are entries of a stream of its own
        let rec = |kind: &str, process: &str, flow: &str, action: &str| {
            let id = uuid::Uuid::new_v4().to_string();
            LogEntry {
                id: id.clone(),
                change: ch.id.clone(),
                r#type: format!("journal.{kind}"),
                process: process.into(),
                flow: flow.into(),
                execution: id,
                subject: action.into(),
                at: Some(g.now()),
                payload: json!({}),
                ..Default::default()
            }
        };
        let on_flow = rec("action", "p2", "f1", "identify_scope");
        must(
            g.append_log(
                &c,
                vec![
                    rec("schedule", "p1", "", ""),
                    rec("action", "p1", "", "identify_scope"),
                    on_flow.clone(),
                ],
            )
            .await,
        );
        // the streams of the graph itself are written through its own operations only, and a change must exist
        let forged = |change: goap_domain::ChangeID, typ: String| LogEntry {
            change,
            r#type: typ,
            payload: json!({}),
            ..Default::default()
        };
        for (e, what) in [
            (
                forged(ch.id.clone(), format!("{LOG_FACT}.artifact")),
                "forged fact",
            ),
            (
                forged(ch.id.clone(), format!("{LOG_IMPACT}.written")),
                "forged impact event",
            ),
            (
                forged(goap_domain::ChangeID::new("unknown"), "journal.tick".into()),
                "unknown change",
            ),
            (forged(ch.id.clone(), "journal".into()), "no kind"),
        ] {
            assert_kind(g.append_log(&c, vec![e]).await, ErrorKind::Invalid, what);
        }
        assert_kind(
            g.change_log(&c, &LogFilter::default()).await,
            ErrorKind::Invalid,
            "a log read needs a change or processes",
        );
        let (by_process, _) = must(
            g.change_log(
                &c,
                &LogFilter {
                    processes: vec!["p2".into()],
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(by_process.len(), 1, "by process alone");
        let (all, counts) = must(
            g.change_log(
                &c,
                &LogFilter {
                    change: ch.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        let mut streams = std::collections::BTreeMap::<String, usize>::new();
        for (i, e) in all.iter().enumerate() {
            *streams.entry(e.stream().to_string()).or_default() += 1;
            assert!(
                i == 0 || e.seq > all[i - 1].seq,
                "the log is not in one order"
            );
        }
        assert!(
            streams.get(LOG_IMPACT).copied().unwrap_or(0) > 0
                && streams.get(LOG_FACT).copied().unwrap_or(0) > 0
                && streams.get("journal") == Some(&3),
            "streams = {streams:?}"
        );
        let total: i64 = counts.values().sum();
        assert_eq!(total as usize, all.len(), "counts = {counts:?}");
        assert_eq!(counts.get("journal.action"), Some(&2));
        let count = async |mut f: LogFilter| {
            f.change = ch.id.clone();
            let (out, _) = must(g.change_log(&c, &f).await);
            for e in &out {
                assert!(
                    f.matches(e),
                    "entry {} ({}) does not match {f:?}",
                    e.seq,
                    e.r#type
                );
            }
            out
        };
        let n = count(LogFilter {
            types: vec!["journal.".into()],
            ..Default::default()
        })
        .await
        .len();
        assert_eq!(n, 3, "journal stream");
        let n = count(LogFilter {
            types: vec![
                "journal.action".into(),
                format!("{LOG_IMPACT}.{IMPACT_PROPOSED}"),
            ],
            ..Default::default()
        })
        .await
        .len();
        assert_eq!(n, 4, "two types");
        let got = count(LogFilter {
            flows: Some(vec!["f1".into()]),
            ..Default::default()
        })
        .await;
        assert_eq!(got.len(), 1, "flow entries");
        let got = count(LogFilter {
            flows: Some(vec![String::new()]),
            types: vec!["journal.".into()],
            ..Default::default()
        })
        .await;
        assert_eq!(got.len(), 2, "main flow journal");
        let got = count(LogFilter {
            processes: vec!["p2".into()],
            ..Default::default()
        })
        .await;
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].id, on_flow.id, "by process");
        let got = count(LogFilter {
            execution: "e1".into(),
            ..Default::default()
        })
        .await;
        assert_eq!(got.len(), 2, "by action run");
        let got = count(LogFilter {
            after_seq: all[2].seq,
            limit: 2,
            ..Default::default()
        })
        .await;
        assert_eq!(got.len(), 2);
        assert_eq!(got[0].seq, all[3].seq, "following the log");
    })
    .await;
}

// An edit of the header of a change (title, intent, goal, status, data) is an entry of its log, in the same
// transaction; an edit that changes nothing writes none, and the stream is the graph's own.
#[tokio::test]
async fn update_change_is_logged() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let (g, ch) = world(r).await;
        let headers = async || {
            must(
                g.change_log(
                    &c,
                    &LogFilter {
                        change: ch.id.clone(),
                        types: vec![format!("{LOG_CHANGE}.")],
                        ..Default::default()
                    },
                )
                .await,
            )
            .0
        };
        let before = headers().await.len();
        let title = "A new title".to_string();
        must(
            g.update_change(
                &c,
                &ch.id,
                &ChangePatch {
                    title: Some(title.clone()),
                    data: props(&[("criticality", "high".into())]),
                    ..Default::default()
                },
            )
            .await,
        );
        let got = headers().await;
        assert_eq!(got.len(), before + 1, "header entries = {got:?}");
        let last = got.last().expect("an entry");
        assert_eq!(last.r#type, "change.updated");
        assert_eq!(last.subject, "data.criticality,title");
        must(
            g.update_change(
                &c,
                &ch.id,
                &ChangePatch {
                    title: Some(title),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(
            headers().await.len(),
            before + 1,
            "an edit that changes nothing is logged"
        );
        assert_kind(
            g.append_log(
                &c,
                vec![LogEntry {
                    change: ch.id.clone(),
                    r#type: format!("{LOG_CHANGE}.updated"),
                    payload: json!({}),
                    ..Default::default()
                }],
            )
            .await,
            ErrorKind::Invalid,
            "forged header entry",
        );
    })
    .await;
}
