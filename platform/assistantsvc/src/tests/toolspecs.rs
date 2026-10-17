//! toolspecs_test.go

use goap_convsvc as convsvc;
use serde_json::{Map, Value, json};

use super::uitools::{filter_tool, set_field_tool};
use super::*;
use crate::frame::{Level, level_tools};
use crate::toolspecs::ui_tool_spec;
use crate::turn::{TOOL_LIST_METHODOLOGIES, TOOL_OPEN_CHANGE};
use crate::util::{go_json, map_of};

#[test]
fn screen_tools_are_declared_with_their_schema() {
    let spec = ui_tool_spec(&filter_tool());
    let b = go_json(&spec.input_schema);
    for want in [
        r#""status":{"enum":["open","closed"],"type":"string"}"#,
        r#""tags":{"items":{"type":"string"},"type":"array"}"#,
        r#""limit":{"type":"number"}"#,
        r#""rationale":{"#,
        r#""required":["status"]"#,
        r#""additionalProperties":false"#,
    ] {
        assert!(b.contains(want), "schema lacks {want}: {b}");
    }
    assert!(
        spec.name == "ui.filter_list"
            && spec
                .description
                .contains("Required to feed it: give the status")
            && spec.description.contains("effect"),
        "{spec:?}"
    );
    let w = ui_tool_spec(&set_field_tool());
    assert!(w.description.contains("only proposed"), "{}", w.description);
}

fn args(v: Value) -> Map<String, Value> {
    v.as_object().cloned().unwrap_or_default()
}

#[tokio::test]
async fn native_tool_calls_and_results() {
    let e = new_env(&[]).await;
    e.model.0.lock().responses = vec![
        llm::Response {
            text: "Let me look.".into(),
            stop_reason: llm::STOP_TOOL_USE.into(),
            tool_calls: vec![
                llm::ToolCall {
                    id: "c1".into(),
                    name: TOOL_LIST_METHODOLOGIES.into(),
                    arguments: Map::new(),
                },
                llm::ToolCall {
                    id: "c2".into(),
                    name: "ui.set_field".into(),
                    arguments: args(json!({"value": "New title", "rationale": "you asked for it"})),
                },
                llm::ToolCall {
                    id: "c3".into(),
                    name: "delete_everything".into(),
                    ..Default::default()
                },
            ],
            ..Default::default()
        },
        llm::Response {
            text: "I propose the new title.".into(),
            ..Default::default()
        },
    ];
    e.send_tools("rename it", Context::default(), vec![set_field_tool()])
        .await
        .unwrap();
    let got = e.got();
    let (first, second) = (&got[0], &got[1]);
    assert!(
        !first.json
            && first.tools.len() == level_tools(Level::Project).len() - 1 + 1
            && first.tools.last().is_some_and(|t| t.name == "ui.set_field")
            && !first.system.contains(r#""args""#),
        "first request: json {}, {} tools",
        first.json,
        first.tools.len()
    );
    let n = second.messages.len();
    let (call, res) = (&second.messages[n - 2], &second.messages[n - 1]);
    assert!(
        call.role == "assistant" && call.tool_calls.len() == 3 && call.content == "Let me look.",
        "assistant turn {call:?}"
    );
    let r = &res.tool_results;
    assert!(
        r.len() == 3
            && r[0].call_id == "c1"
            && !r[0].is_error
            && r[0].content.contains("sdlc")
            && r[1].call_id == "c2"
            && !r[1].is_error
            && r[2].call_id == "c3"
            && r[2].is_error
            && r[2].content.contains("unknown tool"),
        "results {r:?}"
    );
    let a = e.answer().await;
    assert!(
        a.status == convsvc::STATUS_DONE
            && a.text == "I propose the new title."
            && a.actions.len() == 1,
        "answer {a:?}"
    );
    // the rationale travelled as an argument: it is the proposal's, not an argument of the screen tool
    let act = &a.actions[0];
    assert!(
        s(act, "rationale") == "you asked for it" && s(act, "status") == "proposed",
        "action {act:?}"
    );
    let ar = map_of(act, "args");
    assert!(
        ar.get("value").and_then(Value::as_str) == Some("New title")
            && !ar.contains_key("rationale"),
        "args {:?}",
        act.get("args")
    );
}

#[tokio::test]
async fn native_settling_calls_end_the_turn() {
    let e = new_env(&[]).await;
    e.model.0.lock().responses = vec![llm::Response {
        text: "Opening it.".into(),
        tool_calls: vec![llm::ToolCall {
            id: "c1".into(),
            name: TOOL_OPEN_CHANGE.into(),
            arguments: args(json!({"changeId": "CHG-1"})),
        }],
        ..Default::default()
    }];
    e.send("open CHG-1", Context::default()).await.unwrap();
    let a = e.answer().await;
    let calls = e.got().len();
    assert!(
        calls == 1 && a.text == "Opening it." && a.actions.len() == 1,
        "{calls} calls, answer {a:?}"
    );
}
