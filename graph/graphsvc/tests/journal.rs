mod common;

use std::sync::Arc;

use common::{client, code, graph, rpc, serve};
use goap_connect::{Code, Interceptors, Request};
use goap_ctx::Ctx;
use goap_domain::{LOG_FACT, LogEntry, LogFilter, MAIN_BRANCH};
use goap_gen::graph::v1::{self as pb, GraphService};
use goap_graph::NewChange;
use goap_graphapi::Port;
use goap_graphsvc::Handler;
use goap_journal as journal;

/// The execution journal through the service: the engine's client appends the entries of its records to the log of
/// the change (journal::append over the graph client) and reads them back as entries, the web reads them as records.
#[tokio::test]
async fn journal_through_the_service() {
    let ctx = &Ctx::background();
    let g = Arc::new(graph());
    let url = serve(Handler::new(g.clone()), Interceptors::none()).await;
    let cl = client(&url);

    let base = g.branch_head(ctx, "", MAIN_BRANCH).await.unwrap();
    let c = g
        .create_change(
            ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "t".into(),
                baseline_id: base.id,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let rec = journal::Record {
        change_id: c.id.clone(),
        process_id: "p1".into(),
        kind: journal::KIND_ACTION.into(),
        action: "a".into(),
        step: 2,
        model_calls: vec![journal::ModelCall {
            model: "fast".into(),
            input_tokens: 3,
            exchange: Some(journal::ModelExchange {
                response: "ok".into(),
                ..Default::default()
            }),
            ..Default::default()
        }],
        ..Default::default()
    };
    journal::append(ctx, &cl, &[rec]).await.unwrap();
    let got = journal::read(
        ctx,
        &cl,
        &journal::Filter {
            process_ids: vec!["p1".into()],
            ..Default::default()
        },
    )
    .await;
    assert!(
        matches!(&got, Ok(got) if got.len() == 1
            && got[0].action == "a"
            && got[0].model_calls[0].input_tokens == 3
            && got[0].model_calls[0].exchange.is_none()),
        "records: {got:?}"
    );
    // the prompts are entries of their own, read through the log with the change named
    let entries = cl
        .change_log(
            ctx,
            LogFilter {
                change: c.id.clone(),
                types: vec![format!("{}.", journal::STREAM_MODEL)],
                ..Default::default()
            },
        )
        .await;
    assert!(
        matches!(&entries, Ok((e, _)) if e.len() == 1),
        "model entries: {entries:?}"
    );
    // the streams of the graph are not written through the service
    let r = cl
        .append_log(
            ctx,
            vec![LogEntry {
                change: c.id.clone(),
                r#type: format!("{LOG_FACT}.artifact"),
                ..Default::default()
            }],
        )
        .await;
    assert!(r.is_err(), "a fact was appended through the log");
    // the web reads records
    let web = rpc(&url);
    let r = web
        .list_executions(
            ctx,
            Request::new(pb::ListExecutionsRequest {
                change_id: c.id.to_string(),
                ..Default::default()
            }),
        )
        .await;
    assert!(
        matches!(&r, Ok(r) if r.msg.records.len() == 1 && r.msg.records[0].action == "a"),
        "ListExecutions: {r:?}"
    );
    let r = web
        .list_executions(ctx, Request::new(pb::ListExecutionsRequest::default()))
        .await;
    assert_eq!(
        code(&r),
        Some(Code::InvalidArgument),
        "an empty filter: {r:?}"
    );
}
