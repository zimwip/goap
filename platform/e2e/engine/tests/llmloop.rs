//! Go: llmloop_test.go.

use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::Blackboard;
use goap_engine::{
    ActionContext, Internals, LLMExecutor, MAX_REPAIRS, MAX_TOOL_FAILURES, Process,
    STATUS_COMPLETED, ToolPort,
};
use goap_llm as llm;
use goap_mcp::ToolInfo;
use goap_methodology::{self as methodology, Action};
use parking_lot::Mutex;
use serde_json::{Map, Value, json};
use tokio::sync::Barrier;

use crate::common::*;
use crate::mcp::{FakeHub, docs_as_llm, mcp_engine, run_docs};

pub const DOC_ITEMS: &str =
    r#"{"items":[{"type":"artifact","target":"doc","data":{"seen":"ok"}}]}"#;

/// recording is a client that records the requests and answers with answer(n, request), n counting from 1.
pub fn recording(
    reqs: &Arc<Mutex<Vec<llm::Request>>>,
    answer: impl Fn(usize, &llm::Request) -> llm::Response + Send + Sync + 'static,
) -> Arc<dyn llm::Client> {
    let reqs = reqs.clone();
    let answer = Arc::new(answer);
    llm::ClientFunc::shared(move |_ctx: Ctx, r: llm::Request| {
        let (reqs, answer) = (reqs.clone(), answer.clone());
        async move {
            let mut rs = reqs.lock();
            rs.push(r.clone());
            Ok(answer(rs.len(), &r))
        }
    })
}

pub fn text(t: &str) -> llm::Response {
    llm::Response {
        text: t.into(),
        ..Default::default()
    }
}

fn last(r: &llm::Request) -> String {
    r.messages.last().unwrap().content.clone()
}

#[tokio::test]
async fn llm_action_repairs_an_unreadable_answer() {
    let hub = Arc::new(FakeHub::bound("acme", &["document-repository"]));
    let reqs: Arc<Mutex<Vec<llm::Request>>> = Arc::default();
    let client = recording(&reqs, |n, _| match n {
        1 => text("Sure, here is what I found."),
        2 => llm::Response {
            text: r#"{"items":[{"type":"artifact","target":"doc","da"#.into(),
            stop_reason: llm::STOP_MAX_TOKENS.into(),
            ..Default::default()
        },
        _ => text(DOC_ITEMS),
    });
    let (e, base, _errs) = mcp_engine(hub, Some(client), Some(&docs_as_llm)).await;
    let p = run_docs(&e, &base, "acme").await;
    assert_eq!(p.status, STATUS_COMPLETED, "{p:?}");
    let reqs = reqs.lock();
    assert_eq!(reqs.len(), 3, "model calls");
    let s = last(&reqs[1]);
    assert!(
        s.contains("could not be read") && s.contains(r#"{"tool_calls":[...]}"#),
        "first repair: {s:?}"
    );
    let s = last(&reqs[2]);
    assert!(
        s.contains("cut by the output limit"),
        "second repair: {s:?}"
    );
    // the unreadable answers stay in the conversation, and a tool loop asks for the prompt cache
    assert!(
        reqs[2].messages.len() == 5
            && reqs[2].messages[1].content == "Sure, here is what I found."
            && reqs[2].cache,
        "messages = {:?} cache={}",
        reqs[2].messages,
        reqs[2].cache
    );
}

#[tokio::test]
async fn llm_action_fails_after_max_repairs() {
    let reqs: Arc<Mutex<Vec<llm::Request>>> = Arc::default();
    let client = recording(&reqs, |_, _| text("no JSON here"));
    let mut a = Action::default();
    a.name = "a".into();
    a.kind = methodology::KIND_LLM.into();
    let ac = ActionContext {
        process: Arc::new(Process::default()),
        action: a,
        blackboard: Blackboard::default(),
        graph: Arc::new(crate::common::stub::StubGraph(NoStub)),
        host: None,
        step: None,
        form: None,
    };
    let x = LLMExecutor {
        client: Some(client),
    };
    let r = Internals::run_loop(&x, &ctx(), &ac, "do it".into(), "system".into(), vec![]).await;
    let err = r.unwrap_err().error;
    let calls = reqs.lock().len();
    assert!(
        err.to_string()
            .contains("unreadable answer after 2 repairs")
            && calls == MAX_REPAIRS + 1,
        "err = {err} after {calls} calls"
    );
}

struct NoStub;
impl crate::common::stub::Stub for NoStub {}

#[tokio::test]
async fn llm_action_guides_then_stops_a_failing_tool() {
    let hub = Arc::new(FakeHub::bound("acme", &["document-repository"]));
    hub.st.lock().fail = Some("path not found".into());
    let reqs: Arc<Mutex<Vec<llm::Request>>> = Arc::default();
    let client = recording(&reqs, |n, _| {
        if n as i64 <= MAX_TOOL_FAILURES + 1 {
            text(
                r#"{"tool_calls":[{"name":"document-repository/read","arguments":{"path":"spec.txt"}}]}"#,
            )
        } else {
            text(DOC_ITEMS)
        }
    });
    let (e, base, _errs) = mcp_engine(hub.clone(), Some(client), Some(&docs_as_llm)).await;
    let p = run_docs(&e, &base, "acme").await;
    assert_eq!(p.status, STATUS_COMPLETED, "{p:?}");
    let lasts: Vec<String> = reqs.lock().iter().map(last).collect();
    assert!(
        lasts[1].contains("failure 1 of 3 allowed; do not repeat the same call"),
        "first failure: {:?}",
        lasts[1]
    );
    assert!(
        lasts[3].contains("this was the last try"),
        "last failure: {:?}",
        lasts[3]
    );
    // the fourth call never reaches the tool
    assert!(
        lasts[4].contains("failed 3 times: do not call it again")
            && hub.calls().len() as i64 == MAX_TOOL_FAILURES,
        "refusal {:?}, hub calls {:?}",
        lasts[4],
        hub.calls()
    );
}

/// BlockingHub holds every call until both calls of the round are in flight.
pub struct BlockingHub {
    pub hub: FakeHub,
    pub barrier: Barrier,
}

#[async_trait]
impl ToolPort for BlockingHub {
    async fn call_tool(
        &self,
        ctx: &Ctx,
        org: &str,
        name: &str,
        args: Map<String, Value>,
    ) -> goap_engine::Result<Value> {
        // both calls must be in flight before either may end
        self.barrier.wait().await;
        self.hub.call_tool(ctx, org, name, args).await
    }

    async fn tools(
        &self,
        ctx: &Ctx,
        org: &str,
    ) -> goap_engine::Result<(Vec<ToolInfo>, Vec<String>)> {
        self.hub.tools(ctx, org).await
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn llm_action_runs_the_calls_of_a_round_together() {
    let hub = Arc::new(BlockingHub {
        hub: FakeHub::bound("acme", &["document-repository"]),
        barrier: Barrier::new(2),
    });
    let reqs: Arc<Mutex<Vec<llm::Request>>> = Arc::default();
    let client = recording(&reqs, |n, _| {
        if n == 1 {
            text(
                r#"{"tool_calls":[{"name":"document-repository/read","arguments":{"path":"a"}},{"name":"document-repository/read","arguments":{"path":"b"}}]}"#,
            )
        } else {
            text(DOC_ITEMS)
        }
    });
    let (e, base, _errs) = mcp_engine(hub, Some(client), Some(&docs_as_llm)).await;
    let p = tokio::time::timeout(
        std::time::Duration::from_secs(10),
        run_docs(&e, &base, "acme"),
    )
    .await
    .expect("the calls of a round run together");
    assert_eq!(p.status, STATUS_COMPLETED, "{p:?}");
}

#[tokio::test]
async fn llm_action_calls_tools_natively() {
    let hub = Arc::new(FakeHub::bound("acme", &["document-repository"]));
    let reqs: Arc<Mutex<Vec<llm::Request>>> = Arc::default();
    let client = recording(&reqs, |n, _| {
        if n == 1 {
            llm::Response {
                text: "Reading it.".into(),
                stop_reason: llm::STOP_TOOL_USE.into(),
                tool_calls: vec![
                    llm::ToolCall {
                        id: "c1".into(),
                        name: "document-repository/read".into(),
                        arguments: obj(json!({"path": "spec.txt"})),
                    },
                    llm::ToolCall {
                        id: "c2".into(),
                        name: "ticketing/create".into(),
                        arguments: obj(json!({"path": "x"})),
                    },
                ],
                ..Default::default()
            }
        } else {
            text(DOC_ITEMS)
        }
    });
    let (e, base, _errs) = mcp_engine(hub.clone(), Some(client), Some(&docs_as_llm)).await;
    let p = run_docs(&e, &base, "acme").await;
    let reqs = reqs.lock();
    assert!(
        p.status == STATUS_COMPLETED && reqs.len() == 2,
        "{p:?} after {} calls",
        reqs.len()
    );
    assert!(
        reqs[0].tools.len() == 1
            && reqs[0].tools[0].name == "document-repository/read"
            && !reqs[0].system.contains("Available tools"),
        "tools {:?}",
        reqs[0].tools
    );
    let msgs = &reqs[1].messages;
    let (call, results) = (&msgs[msgs.len() - 2], &msgs[msgs.len() - 1]);
    assert!(
        call.role == "assistant" && call.tool_calls.len() == 2 && call.content == "Reading it.",
        "assistant turn {call:?}"
    );
    let r = &results.tool_results;
    assert!(
        r.len() == 2
            && r[0].call_id == "c1"
            && !r[0].is_error
            && r[0].content.contains("contents of spec.txt")
            && r[1].call_id == "c2"
            && r[1].is_error
            && r[1]
                .content
                .contains("the tools you can call are document-repository/read"),
        "results {r:?}"
    );
    assert_eq!(hub.calls().len(), 1, "hub calls {:?}", hub.calls());
}
