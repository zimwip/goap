//! compact_test.go

use goap_authz::with_principal;
use goap_convsvc as convsvc;
use serde_json::json;

use super::*;
use crate::agents::{ACTION_START_AGENT, STATUS_STARTED};
use crate::compact::{SUMMARY_SYSTEM, join_summary, keep_facts, split_summary};

#[tokio::test]
async fn old_messages_are_folded_into_the_summary() {
    let e = new_env(&[
        r#"{"message":"ok"}"#,
        "Durable facts:\n- the person works on CHG-7\nLanguage: English",
        r#"{"message":"still ok"}"#,
    ])
    .await;
    let big = "w".repeat(3000);
    for i in 0..20 {
        let (role, p) = if i % 2 == 1 {
            (convsvc::ROLE_ASSISTANT, PRINCIPAL.clone())
        } else {
            (convsvc::ROLE_USER, e.user.clone())
        };
        let mut c = convsvc::Content {
            text: format!("{i:02}{big}"),
            ..Default::default()
        };
        if i == 1 {
            let acts = json!([
                {"type": ACTION_START_AGENT, "status": STATUS_STARTED,
                 "args": {"methodology": "sdlc", "agent": "analyst"}, "result": {"processId": "PRC-9", "changeId": "CHG-7"}},
                {"type": "select_project", "args": {"project": "PROJ-B"}}
            ]);
            c.actions = serde_json::from_value(acts).unwrap();
        }
        e.convs
            .append(&with_principal(&e.ctx, p.clone()), &p, &e.conv.id, role, c)
            .await
            .unwrap();
    }
    e.send("latest", Context::default()).await.unwrap();
    // the turn, then the summary of what fell out of its window
    let got = e.got();
    assert_eq!(got.len(), 2, "model calls");
    let sum_req = &got[1];
    let window = got[0].messages.len() - 1; // the history, without the new message
    let content = &sum_req.messages[0].content;
    assert!(
        sum_req.system == SUMMARY_SYSTEM
            && content.contains("[1 user] 00www")
            && !content.contains(&format!("[{} ", 20 - window + 1)),
        "summary request {:.300} (window {window})",
        content
    );
    // the facts of the actions are recorded by code, kept before the model's text
    let facts = "Recorded facts:\n- [2] the person accepted the proposal to start agent analyst of sdlc: it was started (process PRC-9 on change CHG-7)\n".to_string()
        + r#"- [2] select_project project="PROJ-B""# + "\n\n";
    assert!(content.contains(&facts), "summary request {:.600}", content);
    let sum = e
        .convs
        .summary(&e.ctx, &PRINCIPAL, &e.conv.id)
        .await
        .unwrap();
    assert!(
        sum.text
            .starts_with(&(facts.clone() + "Durable facts:\n- the person works on CHG-7"))
            && sum.up_to == (20 - window) as i64,
        "summary {sum:?} (window {window})"
    );
    // the next turn gets it with its rules; two more messages out of the window are not enough to write it again
    e.send("and now?", Context::default()).await.unwrap();
    let got = e.got();
    assert!(
        got.len() == 3
            && got[2]
                .system
                .contains("Summary of the earlier part of this conversation")
            && got[2].system.contains("the person works on CHG-7"),
        "{} calls",
        got.len()
    );
}

#[tokio::test]
async fn a_short_conversation_has_no_summary() {
    let e = new_env(&[r#"{"message":"ok"}"#]).await;
    e.send("hello", Context::default()).await.unwrap();
    let got = e.got();
    assert!(
        got.len() == 1 && !got[0].system.contains("Summary of the earlier part"),
        "{} calls",
        got.len()
    );
}

#[test]
fn recorded_facts_are_kept() {
    let stored = join_summary(
        &[sv("- [2] a"), sv("- [3] b")],
        "Durable facts:\n- x\n\nConversation so far:\nnothing",
    );
    let (facts, text) = split_summary(&stored);
    assert!(
        facts.join("|") == "- [2] a|- [3] b"
            && text.starts_with("Durable facts:")
            && text.ends_with("nothing"),
        "{facts:?} / {text:?}"
    );
    let (facts, text) = split_summary("an older summary");
    assert!(
        facts.is_empty() && text == "an older summary",
        "a summary without recorded facts"
    );
    // repeats go, then the oldest past the budget
    let got = keep_facts(
        &[sv("- [1] old"), sv("- [2] b"), sv("- [2] b"), sv("- [3] c")],
        18,
    );
    assert_eq!(got.join("|"), "- [2] b|- [3] c");
    let long = join_summary(&[sv("- [1] a")], &"z".repeat(convsvc::MAX_SUMMARY_BYTES));
    assert!(
        long.len() <= convsvc::MAX_SUMMARY_BYTES,
        "{} bytes",
        long.len()
    );
}
