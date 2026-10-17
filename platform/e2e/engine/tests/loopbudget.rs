//! Go: loopbudget_test.go.

use std::sync::Arc;

use goap_engine::{MAX_TOOL_ROUNDS, STATUS_COMPLETED};
use goap_llm as llm;
use goap_methodology::{Loop, Methodology};
use parking_lot::Mutex;

use crate::llmloop::{DOC_ITEMS, recording, text};
use crate::mcp::{FakeHub, docs_as_llm, mcp_engine, run_docs};

/// docs_with_loop is docs_as_llm with a loop budget on the fetch action (ADR 0118).
fn docs_with_loop(l: Loop) -> impl Fn(&mut Methodology) {
    move |m: &mut Methodology| {
        docs_as_llm(m);
        for a in &mut m.actions {
            if a.name == "fetch_doc" {
                a.r#loop = Some(l.clone());
            }
        }
    }
}

const ASK_READ: &str =
    r#"{"tool_calls":[{"name":"document-repository/read","arguments":{"path":"spec.txt"}}]}"#;

/// A budget allows more rounds than the default; once spent, the model is asked once for its final answer, with no
/// tool, instead of the action failing.
#[tokio::test]
async fn loop_budget_rounds_then_final_answer() {
    let hub = Arc::new(FakeHub::bound("acme", &["document-repository"]));
    let reqs: Arc<Mutex<Vec<llm::Request>>> = Arc::default();
    let client = recording(&reqs, |n, r| {
        if r.tools.is_empty() && n > 1 {
            text(DOC_ITEMS)
        } else {
            text(ASK_READ)
        }
    });
    let edit = docs_with_loop(Loop {
        max_rounds: 10,
        ..Default::default()
    });
    let (e, base, _errs) = mcp_engine(hub.clone(), Some(client), Some(&edit)).await;
    let p = run_docs(&e, &base, "acme").await;
    assert_eq!(p.status, STATUS_COMPLETED, "{p:?}");
    let reqs = reqs.lock();
    assert!(
        hub.calls().len() == 10 && reqs.len() == 12,
        "{} tool calls, {} model calls",
        hub.calls().len(),
        reqs.len()
    );
    let last = reqs.last().unwrap();
    let content = &last.messages.last().unwrap().content;
    assert!(
        last.tools.is_empty() && content.contains("budget of this task is spent (10 tool rounds)"),
        "final request: tools {}, {content:?}",
        last.tools.len()
    );
}

#[tokio::test]
async fn loop_budget_tokens() {
    let hub = Arc::new(FakeHub::bound("acme", &["document-repository"]));
    let reqs: Arc<Mutex<Vec<llm::Request>>> = Arc::default();
    let client = recording(&reqs, |_, r| {
        if r.tools.is_empty() {
            text(DOC_ITEMS)
        } else {
            llm::Response {
                text: ASK_READ.into(),
                usage: llm::Usage {
                    input_tokens: 20,
                    output_tokens: 10,
                    ..Default::default()
                },
                ..Default::default()
            }
        }
    });
    let edit = docs_with_loop(Loop {
        max_rounds: 100,
        max_tokens: 50,
        ..Default::default()
    });
    let (e, base, _errs) = mcp_engine(hub.clone(), Some(client), Some(&edit)).await;
    let p = run_docs(&e, &base, "acme").await;
    assert_eq!(p.status, STATUS_COMPLETED, "{p:?}");
    // 30 tokens a call: the second call spends the budget, its tools are not run
    let n = reqs.lock().len();
    assert!(
        hub.calls().len() == 1 && n == 3,
        "{} tool calls, {n} model calls",
        hub.calls().len()
    );
}

/// Without a budget an action keeps the default rounds and fails past them.
#[tokio::test]
async fn no_loop_keeps_the_default() {
    let hub = Arc::new(FakeHub::bound("acme", &["document-repository"]));
    let reqs: Arc<Mutex<Vec<llm::Request>>> = Arc::default();
    let client = recording(&reqs, |_, _| text(ASK_READ));
    let (e, base, _errs) = mcp_engine(hub.clone(), Some(client), Some(&docs_as_llm)).await;
    let p = run_docs(&e, &base, "acme").await;
    assert!(
        p.status != STATUS_COMPLETED && hub.calls().len() as i64 >= MAX_TOOL_ROUNDS,
        "status {}, {} tool calls",
        p.status,
        hub.calls().len()
    );
}
