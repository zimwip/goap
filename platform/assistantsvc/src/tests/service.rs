//! service_test.go

use std::sync::Arc;
use std::time::Instant;

use goap_authz::{Principal, system, with_principal};
use goap_connect::{Code, Request};
use goap_convsvc as convsvc;
use goap_ctx::Ctx;
use goap_gen::assistant::v1 as pb;
use goap_gen::assistant::v1::AssistantService as _;
use goap_identity::Extractor;
use goap_llm as llm;
use goap_modelgw::AliasEntry;

use super::*;
use crate::Error;
use crate::agents::TOOL_LIST_AGENTS;
use crate::context::{Element, Entity, Focus, Screen};
use crate::frame::Level;
use crate::handler::{Handler, rpc_err};
use crate::service::{
    MAX_HISTORY_BYTES, MAX_HISTORY_MESSAGES, MAX_ROUNDS, MAX_SELECTION_BYTES, MAX_TEXT_BYTES,
    MAX_TOOL_CALLS, STALE_AFTER,
};
use crate::turn::{
    TOOL_CREATE_CHANGE, TOOL_LIST_METHODOLOGIES, TOOL_OPEN_CHANGE, TOOL_SELECT_PROJECT,
};

fn level_project_tools() -> usize {
    crate::frame::Frame {
        level: Level::Project,
        ..Default::default()
    }
    .tools(true)
    .len()
}

#[tokio::test]
async fn send_answers_and_stores_the_turn() {
    let e = new_env(&[
        &call(TOOL_LIST_METHODOLOGIES, "{}"),
        r#"{"message":"Use sdlc."}"#,
    ])
    .await;
    let c = Context {
        app: App {
            project: "PROJ-A".into(),
            tab: Tab {
                kind: "node".into(),
                ..Default::default()
            },
        },
        focus: Focus {
            element: Some(Element {
                r#type: "node".into(),
                id: "NODE-1".into(),
                ..Default::default()
            }),
            ..Default::default()
        },
        ..Default::default()
    };
    let (user, pending) = e.send("what can I do here?", c).await.unwrap();
    assert!(
        user.role == convsvc::ROLE_USER
            && pending.role == convsvc::ROLE_ASSISTANT
            && pending.status == convsvc::STATUS_PENDING
            && pending.conversation_id == e.conv.id,
        "messages {user:?} {pending:?}"
    );
    let got = e.answer().await;
    assert!(
        got.id == pending.id
            && got.status == convsvc::STATUS_DONE
            && got.text == "Use sdlc."
            && got.actions.is_empty(),
        "answer {got:?}"
    );
    let reqs = e.got();
    assert_eq!(reqs.len(), 2);
    let (first, second) = (&reqs[0], &reqs[1]);
    assert!(
        first.model == "assistant"
            && !first.json
            && first.tools.len() == level_project_tools() - 1
            && first
                .system
                .contains(r#""element":{"type":"node","id":"NODE-1"}"#)
            && first.system.contains(r#""activeProject":"PROJ-A""#),
        "request {first:?}"
    );
    // the ledger of the gateway names the assistant and its conversation (ADR 0089)
    for m in &e.model.0.lock().metas {
        assert!(
            m.source == llm::SOURCE_ASSISTANT && m.conversation_id == e.conv.id,
            "meta {m:?}"
        );
    }
    assert_eq!(e.model.0.lock().who[0].subject, "u1");
    // the tool result went back: sdlc with its description and goal examples, and nothing of another project
    let last = second.messages.last().unwrap();
    assert!(
        last.role == "user"
            && last.content.contains(r#""name":"sdlc""#)
            && last.content.contains("Software lifecycle")
            && last.content.contains("ship v2")
            && !last.content.contains("risk"),
        "tool result {:?}",
        last.content
    );
}

#[tokio::test]
async fn context_is_described_not_stored() {
    let e = new_env(&[r#"{"message":"ok"}"#]).await;
    let secret = format!("my password is hunter2 {}", "x".repeat(500));
    let c = Context {
        app: App {
            project: "PROJ-A".into(),
            tab: Tab {
                kind: "node".into(),
                params: [(sv("k"), sv("v"))].into_iter().collect(),
            },
        },
        focus: Focus {
            element: Some(Element {
                r#type: "node".into(),
                id: "NODE-7".into(),
                ..Default::default()
            }),
            selection: secret,
            ..Default::default()
        },
        ..Default::default()
    };
    let (user, _) = e.send("help", c).await.unwrap();
    assert_eq!(user.text, "help");
    assert!(
        !user.context.is_empty()
            && user.context.len() <= convsvc::MAX_CONTEXT_BYTES
            && !user.context.contains("hunter2")
            && !(user.context.contains('k') && user.context.contains("v}")),
        "context {:?}",
        user.context
    );
    assert!(
        user.context.contains("tab node")
            && user.context.contains("on node NODE-7")
            && user.context.contains("selected characters"),
        "context {:?}",
        user.context
    );
    // the model saw the selection for this turn only
    assert!(e.got()[0].system.contains("hunter2"));
    for m in e.messages().await {
        assert!(
            !m.text.contains("hunter2") && !m.context.contains("hunter2"),
            "selection stored: {m:?}"
        );
    }
    // a second turn: the old snapshot is gone from the prompt
    let c = Context {
        app: App {
            tab: Tab {
                kind: "home".into(),
                ..Default::default()
            },
            ..Default::default()
        },
        ..Default::default()
    };
    e.send("again", c).await.unwrap();
    let last = e.got().pop().unwrap();
    assert!(
        !last.system.contains("hunter2") && !last.system.contains("NODE-7"),
        "an old context leaked: {}",
        last.system
    );
}

#[tokio::test]
async fn unknown_tool_is_an_error_and_never_run() {
    let e = new_env(&[
        &call("delete_everything", r#"{"x":1}"#),
        r#"{"message":"I cannot."}"#,
    ])
    .await;
    e.send("do it", Context::default()).await.unwrap();
    let last = e.tool_result(1);
    assert!(
        last.contains(r#"unknown tool \"delete_everything\""#),
        "result {last:?}"
    );
    let a = e.answer().await;
    assert!(
        a.status == convsvc::STATUS_DONE
            && a.text == "I cannot."
            && a.actions.is_empty()
            && e.graph.created().is_empty(),
        "{a:?}"
    );
}

#[tokio::test]
async fn select_project() {
    let e = new_env(&[
        &call(TOOL_SELECT_PROJECT, r#"{"project":"PROJ-SECRET"}"#), // exists, no access
        &call(TOOL_SELECT_PROJECT, r#"{"project":"PROJ-NOPE"}"#),   // does not exist
        &call(TOOL_SELECT_PROJECT, r#"{"project":"PROJ-B"}"#),
        r#"{"message":"Switched."}"#,
    ])
    .await;
    e.send("go to B", in_project("PROJ-A")).await.unwrap();
    for (i, want) in ["PROJ-SECRET", "PROJ-NOPE"].iter().enumerate() {
        let res = e.tool_result(i + 1);
        assert!(
            res.contains("does not exist or you may not work on it") && res.contains(want),
            "round {i} result {res:?}"
        );
    }
    let a = e.answer().await;
    assert!(
        a.actions.len() == 1
            && s(&a.actions[0], "type") == "select_project"
            && s(&obj(&a.actions[0], "args"), "project") == "PROJ-B",
        "actions {:?}",
        a.actions
    );
    // the project selected is the active one of the next round
    assert!(e.got()[3].system.contains(r#""activeProject":"PROJ-B""#));
}

#[tokio::test]
async fn create_change_acts_as_the_caller() {
    let e = new_env(&[
        &call(
            TOOL_CREATE_CHANGE,
            r#"{"title":"Ship v2","intent":"Customers need it","methodology":"risk"}"#,
        ), // not applicable to PROJ-A
        &call(
            TOOL_CREATE_CHANGE,
            r#"{"title":"Ship v2","intent":"Customers need it","methodology":"sdlc"}"#,
        ),
        r#"{"message":"Created."}"#,
    ])
    .await;
    e.searched().await;
    e.send("start a change", in_project("PROJ-A"))
        .await
        .unwrap();
    let res = e.tool_result(1);
    assert!(
        res.contains("does not apply to the active project"),
        "result {res:?}"
    );
    let created = e.graph.created();
    assert_eq!(created.len(), 1);
    let (in_, who) = (&created[0], e.graph.0.lock().who[0].clone());
    assert!(
        in_.title == "Ship v2"
            && in_.intent == "Customers need it"
            && in_.methodology == "sdlc"
            && in_.namespace == "alm"
            && in_.project_id == "PROJ-A",
        "new change {in_:?}"
    );
    assert!(
        who.subject == "u1"
            && who.project == "PROJ-A"
            && in_.data["createdBy"] == "u1"
            && in_.data["via"] == "assistant",
        "created as {who:?} data {:?}",
        in_.data
    );
    let a = e.answer().await;
    assert!(
        a.actions.len() == 1
            && s(&a.actions[0], "type") == "create_change"
            && s(&obj(&a.actions[0], "result"), "changeId") == "CHG-NEW",
        "actions {:?}",
        a.actions
    );
}

#[tokio::test]
async fn create_change_in_a_project_the_caller_cannot_use() {
    let mut e = new_env(&[
        &call(TOOL_CREATE_CHANGE, r#"{"title":"x","intent":"y"}"#),
        r#"{"message":"no"}"#,
    ])
    .await;
    // a stale claim; the project of the context is not usable either
    let mut u = e.user.clone();
    u.project = "PROJ-SECRET".into();
    e.set_user(u);
    e.searched().await;
    // the context names the principal's own project (not re-checked at Send): the tool refuses it
    e.send("hi", in_project("PROJ-SECRET"))
        .await
        .expect("the principal's own project is not re-checked");
    assert!(e.graph.created().is_empty());
    let res = e.tool_result(1);
    assert!(
        res.contains("you may not work on project"),
        "result {res:?}"
    );
}

#[tokio::test]
async fn create_change_refused_by_the_graph() {
    let e = new_env(&[
        &call(TOOL_CREATE_CHANGE, r#"{"title":"x","intent":"y"}"#),
        r#"{"message":"It failed."}"#,
    ])
    .await;
    e.graph.0.lock().err = Some("boom".into());
    e.searched().await;
    e.send("go", Context::default()).await.unwrap();
    let a = e.answer().await;
    assert!(
        a.status == convsvc::STATUS_DONE && a.actions.is_empty(),
        "{a:?}"
    );
}

#[tokio::test]
async fn send_refuses_a_project_the_caller_cannot_use() {
    let e = new_env(&[r#"{"message":"x"}"#]).await;
    let err = e.send("hi", in_project("PROJ-SECRET")).await.unwrap_err();
    assert!(err.is_forbidden(), "got {err}");
    assert!(e.messages().await.is_empty(), "something was appended");
}

#[tokio::test]
async fn open_change() {
    let e = new_env(&[
        &call(TOOL_OPEN_CHANGE, r#"{"changeId":"CHG-404"}"#),
        &call(TOOL_OPEN_CHANGE, r#"{"changeId":"CHG-HERS"}"#),
        &call(TOOL_OPEN_CHANGE, r#"{"changeId":"CHG-1"}"#),
        r#"{"message":"Opened."}"#,
    ])
    .await;
    e.send("open CHG-1", Context::default()).await.unwrap();
    for (i, id) in ["CHG-404", "CHG-HERS"].iter().enumerate() {
        let res = e.tool_result(i + 1);
        assert!(
            res.contains("does not exist or is not visible") && res.contains(id),
            "round {i} result {res:?}"
        );
    }
    let a = e.answer().await;
    assert!(
        a.actions.len() == 1
            && s(&a.actions[0], "type") == "open_change"
            && s(&obj(&a.actions[0], "args"), "changeId") == "CHG-1",
        "actions {:?}",
        a.actions
    );
    assert_eq!(s(&obj(&a.actions[0], "result"), "project"), "PROJ-A");
}

#[tokio::test]
async fn one_turn_at_a_time() {
    let mut e = new_env(&[r#"{"message":"first"}"#, r#"{"message":"second"}"#]).await;
    let later = e.later.clone();
    e.svc_mut().go = Runner::With(Arc::new(move |f| later.lock().push(f)));
    e.send("one", Context::default()).await.unwrap();
    let err = e.send("two", Context::default()).await.unwrap_err();
    assert!(err.is_busy(), "got {err}");
    assert_eq!(e.messages().await.len(), 2);
    // the busy mark of the process is gone (another replica sees the pending message instead)
    let f = e.later.lock().remove(0);
    f.await;
    let a = e.answer().await;
    assert!(
        a.text == "first" && a.status == convsvc::STATUS_DONE,
        "{a:?}"
    );
    e.send("two", Context::default()).await.unwrap();
    let f = e.later.lock().remove(0);
    f.await;
    assert_eq!(e.answer().await.text, "second");
}

#[tokio::test]
async fn pending_from_another_process_blocks_until_stale() {
    let mut e = new_env(&[r#"{"message":"ok"}"#]).await;
    e.convs
        .append(
            &e.ctx,
            &PRINCIPAL,
            &e.conv.id,
            convsvc::ROLE_ASSISTANT,
            convsvc::Content {
                status: convsvc::STATUS_PENDING.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let err = e.send("hi", Context::default()).await.unwrap_err();
    assert!(err.is_busy(), "got {err}");
    // a pending message that died with its process no longer blocks
    e.svc_mut().now = Some(Arc::new(|| {
        chrono::Utc::now()
            + chrono::TimeDelta::from_std(STALE_AFTER).unwrap()
            + chrono::TimeDelta::minutes(1)
    }));
    e.send("hi", Context::default()).await.unwrap();
}

#[tokio::test]
async fn alias_unavailable() {
    let e = new_env(&[r#"{"message":"x"}"#]).await;
    e.model.0.lock().aliases = vec![AliasEntry {
        alias: "helper".into(),
        target: "fake/echo".into(),
        ..Default::default()
    }];
    let err = e.send("hi", Context::default()).await.unwrap_err();
    assert!(err.is_unavailable(), "got {err}");
    assert!(e.messages().await.is_empty() && e.got().is_empty());
    assert_eq!(
        rpc_err(&Error::Unavailable(String::new())).code(),
        Code::FailedPrecondition
    );
}

#[tokio::test]
async fn history_is_capped() {
    let e = new_env(&[r#"{"message":"ok"}"#]).await;
    let big = "w".repeat(3000);
    for i in 0..20 {
        let (role, p) = if i % 2 == 1 {
            (convsvc::ROLE_ASSISTANT, PRINCIPAL.clone())
        } else {
            (convsvc::ROLE_USER, e.user.clone())
        };
        e.convs
            .append(
                &with_principal(&e.ctx, p.clone()),
                &p,
                &e.conv.id,
                role,
                convsvc::Content {
                    text: format!("{i:02}{big}"),
                    ..Default::default()
                },
            )
            .await
            .unwrap();
    }
    e.send("latest", Context::default()).await.unwrap();
    let msgs = e.got()[0].messages.clone();
    let total: usize = msgs.iter().map(|m| m.content.len()).sum();
    assert!(
        msgs.len() <= MAX_HISTORY_MESSAGES && total <= MAX_HISTORY_BYTES + MAX_TEXT_BYTES,
        "{} messages, {total} bytes sent",
        msgs.len()
    );
    let n = msgs.len();
    assert!(
        msgs[0].role == "user" && msgs[n - 1].content == "latest" && msgs[n - 1].role == "user",
        "shape {msgs:?}"
    );
    assert!(
        msgs[n - 2].content.starts_with("19") || msgs[n - 2].content.starts_with("18"),
        "the newest messages are kept"
    );
}

#[tokio::test]
async fn model_failure_writes_status_error() {
    let e = new_env(&["unused"]).await;
    e.model.0.lock().err = Some(|| goap_modelgw::Error::QuotaExceeded(String::new()));
    e.send("hi", Context::default()).await.unwrap();
    let a = e.answer().await;
    assert!(
        a.status == convsvc::STATUS_ERROR
            && a.error.contains("quota exceeded")
            && a.text.is_empty(),
        "{a:?}"
    );
    // the conversation is free again
    e.model.0.lock().err = None;
    e.set_answers(&[r#"{"message":"back"}"#]);
    e.send("again", Context::default()).await.unwrap();
    assert_eq!(e.answer().await.text, "back");
}

#[tokio::test]
async fn rounds_are_capped() {
    let e = new_env(&[&call(TOOL_OPEN_CHANGE, r#"{"changeId":"CHG-1"}"#)]).await;
    e.send("loop", Context::default()).await.unwrap();
    let got = e.got();
    assert_eq!(got.len(), MAX_ROUNDS);
    // the last round is offered no tool and told to answer
    let last = &got[MAX_ROUNDS - 1];
    assert!(
        last.tools.is_empty() && last.system.contains("last step") && !got[0].tools.is_empty(),
        "last round: {} tools",
        last.tools.len()
    );
    let a = e.answer().await;
    assert!(
        a.status == convsvc::STATUS_ERROR && a.error.contains("did not finish"),
        "{a:?}"
    );
}

#[tokio::test]
async fn last_round_answers_with_what_it_has() {
    let mut answers: Vec<String> = (0..MAX_ROUNDS - 1)
        .map(|_| call(TOOL_LIST_METHODOLOGIES, "{}"))
        .collect();
    answers.push(r#"{"message":"Here is what I found so far."}"#.into());
    let refs: Vec<&str> = answers.iter().map(String::as_str).collect();
    let e = new_env(&refs).await;
    e.send("dig", Context::default()).await.unwrap();
    let a = e.answer().await;
    assert!(
        a.status == convsvc::STATUS_DONE
            && a.text == "Here is what I found so far."
            && e.got().len() == MAX_ROUNDS,
        "{} calls, {a:?}",
        e.got().len()
    );
}

#[tokio::test]
async fn cancel_stops_the_turn_in_this_process() {
    let e = new_env(&[&call(TOOL_OPEN_CHANGE, r#"{"changeId":"CHG-1"}"#)]).await;
    let (svc, ctx, conv) = (e.svc.clone(), e.ctx.clone(), e.conv.id.clone());
    e.model.0.lock().on_call = Some(Arc::new(move |n| {
        let (svc, ctx, conv) = (svc.clone(), ctx.clone(), conv.clone());
        Box::pin(async move {
            if n == 2 {
                let m = svc.cancel(&ctx, &conv).await.expect("cancel");
                assert_eq!(m.status, convsvc::STATUS_CANCELLED, "cancel: {m:?}");
            }
        })
    }));
    e.send("loop", Context::default()).await.unwrap();
    assert_eq!(e.got().len(), 2, "model calls after the stop");
    // what the turn recorded stays, never an answer
    let a = e.answer().await;
    assert!(
        a.status == convsvc::STATUS_CANCELLED && a.text.is_empty() && !a.actions.is_empty(),
        "{a:?}"
    );
    // the conversation is free again, and the model is told the answer was stopped
    e.model.0.lock().on_call = None;
    e.set_answers(&[r#"{"message":"ok"}"#]);
    e.send("again", Context::default()).await.unwrap();
    let a = e.answer().await;
    assert!(a.status == convsvc::STATUS_DONE && a.text == "ok", "{a:?}");
    let sent: String = e.got()[0]
        .messages
        .iter()
        .map(|m| m.content.as_str())
        .collect();
    assert!(sent.contains("stopped this answer"), "history: {sent:?}");
}

#[tokio::test]
async fn cancel_from_another_process() {
    let e = new_env(&[&call(TOOL_OPEN_CHANGE, r#"{"changeId":"CHG-1"}"#)]).await;
    // a replica serving the Cancel: the turn is not its own
    let other = Arc::new(Service::new(
        e.convs.clone(),
        e.model.clone(),
        e.graph.clone(),
        Arc::new(FakeMethodologies::default()),
        e.projects.clone(),
    ));
    let (ctx, conv) = (e.ctx.clone(), e.conv.id.clone());
    e.model.0.lock().on_call = Some(Arc::new(move |n| {
        let (other, ctx, conv) = (other.clone(), ctx.clone(), conv.clone());
        Box::pin(async move {
            if n == 3 {
                other.cancel(&ctx, &conv).await.expect("cancel");
            }
        })
    }));
    e.send("loop", Context::default()).await.unwrap();
    // the turn sees its message cancelled before its next round
    assert_eq!(e.got().len(), 3);
    let a = e.answer().await;
    assert!(
        a.status == convsvc::STATUS_CANCELLED && !a.actions.is_empty(),
        "{a:?}"
    );
}

#[tokio::test]
async fn cancel_with_nothing_pending() {
    let e = new_env(&[r#"{"message":"done"}"#]).await;
    e.send("hi", Context::default()).await.unwrap();
    let m = e.svc.cancel(&e.ctx, &e.conv.id).await.unwrap();
    assert!(
        m.status == convsvc::STATUS_DONE && m.text == "done",
        "{m:?}"
    );
    // someone else's conversation
    let other = Principal {
        subject: "u2".into(),
        ..Default::default()
    };
    assert!(
        e.svc
            .cancel(&with_principal(&Ctx::background(), other), &e.conv.id)
            .await
            .is_err(),
        "cancelled the conversation of another"
    );
}

#[tokio::test]
async fn tool_calls_of_a_round_are_capped() {
    let calls = [r#"{"name":"open_change","arguments":{"changeId":"CHG-1"}}"#; MAX_TOOL_CALLS + 1]
        .join(",");
    let first = format!(r#"{{"message":"","tool_calls":[{calls}]}}"#);
    let e = new_env(&[&first, r#"{"message":"ok"}"#]).await;
    e.send("many", Context::default()).await.unwrap();
    assert_eq!(e.answer().await.actions.len(), MAX_TOOL_CALLS);
}

#[tokio::test]
async fn answer_that_is_not_json_is_the_answer() {
    let e = new_env(&["Just text, no JSON."]).await;
    e.send("hi", Context::default()).await.unwrap();
    let a = e.answer().await;
    assert!(
        a.text == "Just text, no JSON." && a.status == convsvc::STATUS_DONE,
        "{a:?}"
    );
}

#[tokio::test]
async fn send_validation() {
    let e = new_env(&[r#"{"message":"x"}"#]).await;
    assert!(
        e.send("  ", Context::default())
            .await
            .unwrap_err()
            .is_invalid()
    );
    assert!(
        e.send(&"x".repeat(MAX_TEXT_BYTES + 1), Context::default())
            .await
            .unwrap_err()
            .is_invalid()
    );
    let c = Context {
        focus: Focus {
            selection: "x".repeat(MAX_SELECTION_BYTES + 1),
            ..Default::default()
        },
        ..Default::default()
    };
    assert!(e.send("hi", c).await.unwrap_err().is_invalid());
    let send = |ctx: Ctx, id: &str| {
        let svc = e.svc.clone();
        let id = id.to_string();
        async move {
            svc.send(
                &ctx,
                SendInput {
                    conversation_id: id,
                    text: "hi".into(),
                    ..Default::default()
                },
            )
            .await
        }
    };
    let err = send(e.ctx.clone(), "CONV-nope").await.unwrap_err();
    assert!(err.is_not_found(), "unknown conversation: {err}");
    let other = with_principal(
        &Ctx::background(),
        Principal {
            subject: "u2".into(),
            ..Default::default()
        },
    );
    let err = send(other, &e.conv.id).await.unwrap_err();
    assert!(err.is_not_found(), "someone else's conversation: {err}");
    let err = send(Ctx::background(), &e.conv.id).await.unwrap_err();
    assert!(err.is_anonymous(), "anonymous: {err}");
    let err = send(
        with_principal(&Ctx::background(), system("x", &[])),
        &e.conv.id,
    )
    .await
    .unwrap_err();
    assert!(err.is_forbidden(), "system: {err}");
    assert!(
        e.messages().await.is_empty(),
        "a refused request appended something"
    );
}

#[tokio::test]
async fn handler() {
    let e = new_env(&[r#"{"message":"hello"}"#]).await;
    let h = Handler {
        service: e.svc.clone(),
        identity: Extractor {
            default: Some(e.user.clone()),
        },
    };
    let app = pb::assistant_service_router(Arc::new(h), goap_connect::Interceptors::none());
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", ln.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(ln, app).await });
    let cl = pb::AssistantServiceClient::connect(&url);
    let ctx = Ctx::background();
    let r = cl
        .send(
            &ctx,
            Request::new(pb::SendRequest {
                conversation_id: e.conv.id.clone(),
                text: "hi".into(),
                context: Some(pb::PageContext {
                    app: Some(pb::AppContext {
                        project: "PROJ-A".into(),
                        tab: Some(pb::PageTab {
                            kind: "change".into(),
                            params: [(sv("id"), sv("CHG-1"))].into_iter().collect(),
                        }),
                    }),
                    ..Default::default()
                }),
                ..Default::default()
            }),
        )
        .await
        .unwrap();
    let (u, a) = (
        r.msg.user_message.clone().unwrap(),
        r.msg.assistant_message.clone().unwrap(),
    );
    assert!(
        u.text == "hi" && a.status == "pending" && u.context.contains("tab change"),
        "{:?}",
        r.msg
    );
    assert_eq!(e.answer().await.text, "hello");
    e.model.0.lock().aliases.clear();
    let err = cl
        .send(
            &ctx,
            Request::new(pb::SendRequest {
                conversation_id: e.conv.id.clone(),
                text: "again".into(),
                ..Default::default()
            }),
        )
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::FailedPrecondition, "got {err}");
}

/// A change is never created without a project (ADR 0091): with no active project the assistant creates it in the
/// root one.
#[tokio::test]
async fn create_change_with_no_active_project_acts_in_the_root() {
    let mut e = new_env(&[
        &call(TOOL_CREATE_CHANGE, r#"{"title":"x","intent":"y"}"#),
        r#"{"message":"Created."}"#,
    ])
    .await;
    let mut u = e.user.clone();
    u.project = String::new();
    e.set_user(u);
    e.searched().await;
    e.send("go", Context::default()).await.unwrap();
    let created = e.graph.created();
    assert!(
        created.len() == 1 && created[0].project_id == "PROJ-ROOT",
        "created {created:?}"
    );
}

/// A round whose tools only hand something to the person (open a change) and all succeeded, with a message, is the
/// final answer: no second model call. Anything that returns data to build on, or fails, or comes without a message,
/// goes on.
#[tokio::test]
async fn settling_tools_with_a_message_save_the_last_model_call() {
    let with_msg = |name: &str, args: &str| {
        format!(
            r#"{{"message":"On it.","tool_calls":[{{"name":{},"arguments":{args}}}]}}"#,
            crate::util::q(name)
        )
    };
    let cases = [
        (
            "settling tool and message",
            with_msg(TOOL_OPEN_CHANGE, r#"{"changeId":"CHG-1"}"#),
            1,
            "On it.",
        ),
        (
            "no message",
            call(TOOL_OPEN_CHANGE, r#"{"changeId":"CHG-1"}"#),
            2,
            "after",
        ),
        (
            "data tool",
            with_msg(TOOL_LIST_METHODOLOGIES, "{}"),
            2,
            "after",
        ),
        (
            "failed settling tool",
            with_msg(TOOL_OPEN_CHANGE, r#"{"changeId":"CHG-NOPE"}"#),
            2,
            "after",
        ),
    ];
    for (name, first, calls, answer) in cases {
        let e = new_env(&[&first, r#"{"message":"after"}"#]).await;
        e.send("open it", in_project("PROJ-A")).await.unwrap();
        assert_eq!(e.got().len(), calls, "{name}: model calls");
        let a = e.answer().await;
        assert!(
            a.status == convsvc::STATUS_DONE && a.text == answer,
            "{name}: answer {a:?}, want {answer:?}"
        );
    }
}

/// The part of a turn that is ours (context sanitising, prompt, tools, conversation writes) must stay negligible next
/// to the model call: measured with a model answering at once, a 40-entity context and the tools of a real turn.
#[tokio::test]
async fn turn_overhead_is_negligible() {
    const TURNS: u32 = 50;
    let ent: Vec<Entity> = (0..40)
        .map(|i| Entity {
            r#type: "impact".into(),
            id: format!("IMP-{i}"),
            label: format!("key-{i}"),
            state: "proposed".into(),
            props: [
                (sv("type"), sv("alm@Requirement")),
                (sv("intent"), sv("created")),
            ]
            .into_iter()
            .collect(),
        })
        .collect();
    let mut c = change_ctx("CHG-D");
    c.screen = Screen {
        kind: "change".into(),
        title: "t".into(),
        summary: "s".into(),
        entities: ent,
    };
    let e = new_env(&[
        &call(TOOL_LIST_AGENTS, "{}"),
        &call(TOOL_LIST_METHODOLOGIES, "{}"),
        r#"{"message":"done"}"#,
    ])
    .await;
    let start = Instant::now();
    for i in 0..TURNS {
        e.model.0.lock().got.clear();
        e.send(&format!("message {i}"), c.clone()).await.unwrap();
    }
    let per = start.elapsed() / TURNS;
    // a debug build: a looser bound than Go's 20ms
    assert!(
        per.as_millis() < 200,
        "a turn costs {per:?} of our own time"
    );
}
