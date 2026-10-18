//! The execution journal over the log of a change (ADR 0011, 0030, 0059). Go runs it on
//! the memory and SQLite repositories; it runs on every repository here.

use chrono::{TimeDelta, TimeZone, Utc};
use goap_domain::*;
use goap_journal as journal;

use crate::testsupport::*;
use crate::{Graph, NewChange};

async fn change_of(g: &Graph) -> Change {
    let ctx = ctx();
    let base = must(g.branch_head(&ctx, DEFAULT_NAMESPACE, MAIN_BRANCH).await);
    must(
        g.create_change(
            &ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "c".into(),
                baseline_id: base.id,
                ..Default::default()
            },
        )
        .await,
    )
}

fn uuid() -> String {
    uuid::Uuid::new_v4().to_string()
}

#[tokio::test]
async fn journal() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let g = r.graph();
        let c = change_of(&g).await;
        let t0 = Utc.with_ymd_and_hms(2026, 9, 1, 10, 0, 0).unwrap();
        let recs = vec![
            journal::Record {
                id: uuid(),
                change_id: c.id.clone(),
                process_id: "p1".into(),
                seq: 2,
                kind: journal::KIND_ACTION.into(),
                action: "a".into(),
                effects_met: Some(true),
                started_at: Some(t0 + TimeDelta::seconds(1)),
                model_calls: vec![journal::ModelCall {
                    model: "fast".into(),
                    input_tokens: 10,
                    output_tokens: 5,
                    ..Default::default()
                }],
                ..Default::default()
            },
            journal::Record {
                id: uuid(),
                change_id: c.id.clone(),
                process_id: "p1".into(),
                seq: 1,
                kind: journal::KIND_TICK.into(),
                plan: vec!["a".into(), "b".into()],
                started_at: Some(t0),
                ..Default::default()
            },
            journal::Record {
                id: uuid(),
                change_id: c.id.clone(),
                process_id: "p2".into(),
                seq: 1,
                kind: journal::KIND_TICK.into(),
                started_at: Some(t0 + TimeDelta::seconds(2)),
                ..Default::default()
            },
        ];
        journal::append(&ctx, &g, &recs).await.expect("append");
        let all = journal::read(
            &ctx,
            &g,
            &journal::Filter {
                change_id: c.id.clone(),
                ..Default::default()
            },
        )
        .await
        .expect("read");
        assert!(
            all.len() == 3
                && all[1].kind == journal::KIND_TICK
                && all[0].model_calls[0].input_tokens == 10
                && all[0].effects_met == Some(true),
            "journal: {all:?}"
        );
        let p2 = journal::read(
            &ctx,
            &g,
            &journal::Filter {
                process_ids: vec!["p2".into()],
                ..Default::default()
            },
        )
        .await
        .expect("read");
        assert!(
            p2.len() == 1 && p2[0].process_id == "p2",
            "by process: {p2:?}"
        );
        let err = journal::append(
            &ctx,
            &g,
            &[journal::Record {
                id: uuid(),
                change_id: ChangeID::new(uuid()),
                process_id: "p".into(),
                kind: "tick".into(),
                started_at: Some(t0),
                ..Default::default()
            }],
        )
        .await
        .unwrap_err();
        assert!(
            matches!(&err, journal::Error::Log(e) if e.is_invalid()),
            "unknown change: {err}"
        );
        let err = journal::append(
            &ctx,
            &g,
            &[journal::Record {
                change_id: c.id.clone(),
                process_id: "p".into(),
                ..Default::default()
            }],
        )
        .await
        .unwrap_err();
        assert!(err.is_invalid(), "no kind: {err}");
        let err = journal::read(&ctx, &g, &journal::Filter::default())
            .await
            .unwrap_err();
        assert!(err.is_invalid(), "empty filter: {err}");
    })
    .await;
}

// The prompts of a record's model calls are entries of the log of the change, not part of the record.
#[tokio::test]
async fn model_exchange() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let g = r.graph();
        let c = change_of(&g).await;
        let long = "x".repeat(journal::MAX_EXCHANGE_TEXT + 10);
        let id = uuid();
        let rec = journal::Record {
            id: id.clone(),
            change_id: c.id.clone(),
            process_id: "p1".into(),
            seq: 1,
            step: 4,
            kind: journal::KIND_ACTION.into(),
            action: "a".into(),
            started_at: Some(Utc::now()),
            model_calls: vec![
                journal::ModelCall {
                    model: "fast".into(),
                    input_tokens: 10,
                    output_tokens: 5,
                    exchange: Some(journal::ModelExchange {
                        system: "sys".into(),
                        response: "ok".into(),
                        messages: vec![journal::ModelMessage {
                            role: "user".into(),
                            content: "hello".into(),
                        }],
                        ..Default::default()
                    }),
                    ..Default::default()
                },
                journal::ModelCall {
                    model: "fast".into(),
                    input_tokens: 1,
                    ..Default::default()
                },
                journal::ModelCall {
                    model: "fast".into(),
                    exchange: Some(journal::ModelExchange {
                        response: long,
                        ..Default::default()
                    }),
                    ..Default::default()
                },
            ],
            ..Default::default()
        };
        journal::append(&ctx, &g, std::slice::from_ref(&rec))
            .await
            .expect("append");
        assert!(
            rec.model_calls[0].exchange.is_some(),
            "Append cleared the caller's exchange"
        );
        let got = journal::read(
            &ctx,
            &g,
            &journal::Filter {
                change_id: c.id.clone(),
                ..Default::default()
            },
        )
        .await
        .expect("read");
        assert!(
            got.len() == 1
                && got[0].model_calls.len() == 3
                && got[0].model_calls[0].input_tokens == 10,
            "journal: {got:?}"
        );
        for m in &got[0].model_calls {
            assert!(m.exchange.is_none(), "the record holds a prompt: {m:?}");
        }
        let (entries, _) = must(
            g.change_log(
                &ctx,
                &LogFilter {
                    change: c.id.clone(),
                    types: vec![format!("{}.", journal::STREAM_MODEL)],
                    execution: id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(entries.len(), 2, "model entries: {entries:?}");
        let first = journal::decode_exchange(&entries[0]).expect("first");
        let third = journal::decode_exchange(&entries[1]).expect("third");
        assert!(
            entries[0].process == "p1"
                && entries[0].subject == "0"
                && first.step == 4
                && first.system == "sys"
                && first.messages[0].content == "hello"
                && first.response == "ok"
                && !first.truncated,
            "first exchange: {:?} {first:?}",
            entries[0]
        );
        assert!(
            entries[1].subject == "2"
                && third.call == 2
                && third.truncated
                && third.response.len() <= journal::MAX_EXCHANGE_TEXT,
            "third exchange: {third:?} ({})",
            third.response.len()
        );
        // the journal stream stays the records
        let (j, _) = must(
            g.change_log(
                &ctx,
                &LogFilter {
                    change: c.id.clone(),
                    types: vec![format!("{}.", journal::STREAM_JOURNAL)],
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(j.len(), 1, "journal entries: {j:?}");
    })
    .await;
}
