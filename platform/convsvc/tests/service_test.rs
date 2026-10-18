use std::sync::Arc;

use chrono::{DateTime, TimeZone, Utc};
use goap_authz::Principal;
use goap_connect::{Code, Ctx, Request};
use goap_convsvc::{
    Action, Content, Conversation, DEFAULT_TITLE, Error, MAX_ACTIONS_BYTES, MAX_CONTEXT_BYTES,
    MAX_SUMMARY_BYTES, MAX_TEXT_BYTES, MAX_TITLE_BYTES, MIGRATIONS, MemoryStore, Message,
    ROLE_ASSISTANT, ROLE_USER, SQLITE_MIGRATIONS, STATUS_DONE, STATUS_PENDING, Service, SqlStore,
    Store,
};
use goap_gen::conversations::v1::{self as pb, ConversationService};
use parking_lot::Mutex;
use serde_json::json;

struct Stores {
    all: Vec<(&'static str, Arc<dyn Store>)>,
    _pg: Option<goap_pgtest::PgTest>,
}

async fn stores(name: &str) -> Stores {
    let ctx = Ctx::background();
    let dir = std::env::temp_dir().join(format!(
        "goap-convsvc-{name}-{}-{}",
        std::process::id(),
        Utc::now().timestamp_nanos_opt().unwrap_or_default()
    ));
    let db = goap_platform::open_sqlite(&ctx, dir.join("c.db").to_str().unwrap())
        .await
        .unwrap();
    goap_platform::migrate_sqlite(&ctx, &db, "conversations", &SQLITE_MIGRATIONS, ".")
        .await
        .unwrap();
    let mut all: Vec<(&'static str, Arc<dyn Store>)> = vec![
        ("memory", Arc::new(MemoryStore::new())),
        ("sqlite", Arc::new(SqlStore::Sqlite(db))),
    ];
    // PostgreSQL when GOAP_TEST_PG_DSN is set
    let mut pg = None;
    if let Ok(dsn) = std::env::var(goap_pgtest::DSN_VAR)
        && !dsn.is_empty()
    {
        let t = goap_pgtest::pool_with(&dsn, None).await;
        goap_platform::migrate(&ctx, t.pool(), &MIGRATIONS, ".")
            .await
            .unwrap();
        all.push(("postgres", Arc::new(SqlStore::Postgres(t.pool().clone()))));
        pg = Some(t);
    }
    Stores { all, _pg: pg }
}

/// clock is a service clock that moves one second per reading, so that the order of the writes is the order of the
/// times.
fn new_service(st: Arc<dyn Store>) -> Service {
    let t = Mutex::new(Utc.with_ymd_and_hms(2026, 1, 1, 0, 0, 0).unwrap());
    let n = Mutex::new(0);
    Service {
        store: st,
        now: Some(Arc::new(move || {
            let mut t = t.lock();
            *t += chrono::TimeDelta::seconds(1);
            *t
        })),
        new_id: Some(Arc::new(move || {
            let mut n = n.lock();
            *n += 1;
            format!("ID-{:04}", *n)
        })),
    }
}

fn alice() -> Principal {
    Principal {
        subject: "alice".into(),
        ..Default::default()
    }
}

fn bob() -> Principal {
    Principal {
        subject: "bob".into(),
        ..Default::default()
    }
}

fn system() -> Principal {
    goap_authz::system("assistant", &[])
}

fn text(t: &str) -> Content {
    Content {
        text: t.into(),
        ..Default::default()
    }
}

fn action(v: serde_json::Value) -> Action {
    v.as_object().unwrap().clone()
}

#[tokio::test]
async fn ownership_and_system_append() {
    let ctx = Ctx::background();
    let st = stores("ownership").await;
    for (name, st) in &st.all {
        let s = new_service(st.clone());
        let c = s.create(&ctx, &alice(), "").await.unwrap();
        assert!(
            c.title == DEFAULT_TITLE && c.subject == "alice",
            "{name}: create = {c:?}"
        );
        let u = s
            .append(&ctx, &alice(), &c.id, "", text("open the change CHG-1"))
            .await
            .unwrap();
        assert!(
            u.seq == 1 && u.role == ROLE_USER && u.status == STATUS_DONE,
            "{name}: user message = {u:?}"
        );
        // a person does not write the assistant's words, a platform service does not write the user's
        let e = s
            .append(&ctx, &alice(), &c.id, ROLE_ASSISTANT, text("x"))
            .await
            .unwrap_err();
        assert!(e.is_forbidden(), "{name}: assistant by owner: {e}");
        let e = s
            .append(&ctx, &system(), &c.id, ROLE_USER, text("x"))
            .await
            .unwrap_err();
        assert!(e.is_not_found(), "{name}: user message by system: {e}");
        let a = s
            .append(
                &ctx,
                &system(),
                &c.id,
                ROLE_ASSISTANT,
                Content {
                    status: STATUS_PENDING.into(),
                    process_id: "P1".into(),
                    ..Default::default()
                },
            )
            .await
            .unwrap();
        assert!(
            a.seq == 2 && a.status == STATUS_PENDING && a.process_id == "P1",
            "{name}: assistant message = {a:?}"
        );
        let actions = vec![action(
            json!({"type": "open_change", "args": {"id": "CHG-1"}}),
        )];
        let up = s
            .update_message(
                &ctx,
                &system(),
                &a.id,
                Content {
                    text: "Opening it.".into(),
                    actions,
                    ..Default::default()
                },
            )
            .await
            .unwrap();
        assert!(
            up.text == "Opening it." && up.status == STATUS_DONE && up.process_id == "P1",
            "{name}: update = {up:?}"
        );
        let e = s
            .update_message(&ctx, &alice(), &a.id, text("forged"))
            .await
            .unwrap_err();
        assert!(e.is_forbidden(), "{name}: update by owner: {e}");
        let e = s
            .update_message(&ctx, &system(), &u.id, text("forged"))
            .await
            .unwrap_err();
        assert!(e.is_invalid(), "{name}: update of a user message: {e}");
        let e = s
            .update_message(&ctx, &system(), "nope", Content::default())
            .await
            .unwrap_err();
        assert!(e.is_not_found(), "{name}: update of nothing: {e}");

        let (got, ms) = s.get(&ctx, &alice(), &c.id).await.unwrap();
        assert!(
            ms.len() == 2
                && ms[0].id == u.id
                && ms[1].text == "Opening it."
                && ms[1].actions.len() == 1
                && ms[1].actions[0]["type"] == "open_change",
            "{name}: get = {got:?}, {ms:?}"
        );
        assert!(
            got.updated_at > c.updated_at,
            "{name}: a message did not touch the conversation: {} then {}",
            c.updated_at,
            got.updated_at
        );

        // bob and the platform see nothing of alice's conversation, and cannot change it
        for (who, p) in [("bob", bob()), ("system", system())] {
            assert!(
                s.get(&ctx, &p, &c.id).await.unwrap_err().is_not_found(),
                "{name}: {who} get"
            );
            assert!(
                s.rename(&ctx, &p, &c.id, "mine")
                    .await
                    .unwrap_err()
                    .is_not_found(),
                "{name}: {who} rename"
            );
            assert!(
                s.delete(&ctx, &p, &c.id).await.unwrap_err().is_not_found(),
                "{name}: {who} delete"
            );
        }
        assert!(
            s.append(&ctx, &bob(), &c.id, "", text("hi"))
                .await
                .unwrap_err()
                .is_not_found(),
            "{name}: bob append"
        );
        let (cs, _) = s.list(&ctx, &bob(), 0, "").await.unwrap();
        assert!(cs.is_empty(), "{name}: bob list = {cs:?}");
        assert!(
            s.list(&ctx, &system(), 0, "")
                .await
                .unwrap_err()
                .is_forbidden(),
            "{name}: system list"
        );
        assert!(
            s.create(&ctx, &system(), "x")
                .await
                .unwrap_err()
                .is_forbidden(),
            "{name}: system create"
        );
        assert!(
            s.create(&ctx, &Principal::default(), "x")
                .await
                .unwrap_err()
                .is_anonymous(),
            "{name}: anonymous create"
        );

        let r = s
            .rename(&ctx, &alice(), &c.id, " Release 2 ")
            .await
            .unwrap();
        assert_eq!(r.title, "Release 2", "{name}: rename");
        assert!(
            s.rename(&ctx, &alice(), &c.id, " ")
                .await
                .unwrap_err()
                .is_invalid(),
            "{name}: empty title"
        );
        s.delete(&ctx, &alice(), &c.id).await.unwrap();
        assert!(
            s.get(&ctx, &alice(), &c.id)
                .await
                .unwrap_err()
                .is_not_found(),
            "{name}: after delete"
        );
        assert!(
            st.get_message(&ctx, &a.id)
                .await
                .unwrap_err()
                .is_not_found(),
            "{name}: messages survive the conversation"
        );
    }
}

#[tokio::test]
async fn list_paging() {
    let ctx = Ctx::background();
    let st = stores("paging").await;
    for (name, st) in &st.all {
        let s = new_service(st.clone());
        let mut ids: Vec<String> = Vec::new();
        for i in 0..5 {
            let c = s.create(&ctx, &alice(), &format!("c{i}")).await.unwrap();
            ids.insert(0, c.id); // newest first
        }
        s.create(&ctx, &bob(), "bobs").await.unwrap();
        // a message moves its conversation to the top
        s.append(&ctx, &alice(), &ids[4], "", text("bump"))
            .await
            .unwrap();
        let mut want = vec![ids[4].clone()];
        want.extend(ids[..4].iter().cloned());
        let mut got = Vec::new();
        let mut token = String::new();
        let mut pages = 0;
        loop {
            let (cs, next) = s.list(&ctx, &alice(), 2, &token).await.unwrap();
            got.extend(cs.into_iter().map(|c| c.id));
            if next.is_empty() {
                assert_eq!(pages, 2, "{name}: {} pages, want 3", pages + 1);
                break;
            }
            token = next;
            pages += 1;
        }
        assert_eq!(got, want, "{name}: listing");
        assert!(
            s.list(&ctx, &alice(), 2, "garbage")
                .await
                .unwrap_err()
                .is_invalid(),
            "{name}: bad token"
        );
    }
}

fn now() -> DateTime<Utc> {
    Utc::now()
}

#[tokio::test]
async fn limits() {
    let ctx = Ctx::background();
    let st = stores("limits").await;
    for (name, st) in &st.all {
        let s = new_service(st.clone());
        let c = s.create(&ctx, &alice(), "t").await.unwrap();
        let assistant = |content: Content| {
            let s = &s;
            let id = c.id.clone();
            let ctx = &ctx;
            async move {
                s.append(ctx, &system(), &id, ROLE_ASSISTANT, content)
                    .await
                    .map(|_| ())
            }
        };
        let user = |role: &'static str, content: Content| {
            let s = &s;
            let id = c.id.clone();
            let ctx = &ctx;
            async move {
                s.append(ctx, &alice(), &id, role, content)
                    .await
                    .map(|_| ())
            }
        };
        let bad: Vec<(&str, Result<(), Error>)> = vec![
            (
                "long title",
                s.create(&ctx, &alice(), &"x".repeat(MAX_TITLE_BYTES + 1))
                    .await
                    .map(|_| ()),
            ),
            (
                "long text",
                user("", text(&"x".repeat(MAX_TEXT_BYTES + 1))).await,
            ),
            ("empty user text", user("", text(" ")).await),
            ("bad role", user("robot", text("x")).await),
            (
                "bad status",
                assistant(Content {
                    status: "maybe".into(),
                    ..Default::default()
                })
                .await,
            ),
            (
                "long context",
                user(
                    "",
                    Content {
                        text: "x".into(),
                        context: "x".repeat(MAX_CONTEXT_BYTES + 1),
                        ..Default::default()
                    },
                )
                .await,
            ),
            (
                "context on an assistant message",
                assistant(Content {
                    context: "tab change".into(),
                    ..Default::default()
                })
                .await,
            ),
            (
                "action without type",
                assistant(Content {
                    actions: vec![action(json!({"args": 1}))],
                    ..Default::default()
                })
                .await,
            ),
            (
                "large actions",
                assistant(Content {
                    actions: vec![action(
                        json!({"type": "x", "args": "x".repeat(MAX_ACTIONS_BYTES)}),
                    )],
                    ..Default::default()
                })
                .await,
            ),
            (
                "user message with actions",
                user(
                    "",
                    Content {
                        text: "x".into(),
                        actions: vec![action(json!({"type": "x"}))],
                        ..Default::default()
                    },
                )
                .await,
            ),
        ];
        for (what, r) in bad {
            match r {
                Err(e) if e.is_invalid() => {}
                r => panic!("{name}: {what}: {r:?}"),
            }
        }
        let (_, ms) = s.get(&ctx, &alice(), &c.id).await.unwrap();
        assert!(ms.is_empty(), "{name}: refused messages were kept: {ms:?}");

        // messages per conversation: the limit is on the store, exercised with a small one
        let msg = |id: &str, conversation: &str| Message {
            id: id.into(),
            conversation_id: conversation.into(),
            role: ROLE_USER.into(),
            status: STATUS_DONE.into(),
            created_at: now(),
            ..Default::default()
        };
        for i in 1..=3 {
            st.append(&ctx, msg(&format!("M{i}"), &c.id), 3)
                .await
                .unwrap();
        }
        let e = st.append(&ctx, msg("M4", &c.id), 3).await.unwrap_err();
        assert!(e.is_full(), "{name}: fourth message: {e}");
        let e = st.append(&ctx, msg("M5", "nope"), 3).await.unwrap_err();
        assert!(e.is_not_found(), "{name}: message of nothing: {e}");
        // conversations per subject
        let conv = |id: &str| Conversation {
            id: id.into(),
            subject: "carol".into(),
            created_at: now(),
            updated_at: now(),
            ..Default::default()
        };
        for i in 0..2 {
            st.create(&ctx, conv(&format!("X{i}")), 2).await.unwrap();
        }
        let e = st.create(&ctx, conv("X2"), 2).await.unwrap_err();
        assert!(e.is_full(), "{name}: third conversation: {e}");
    }
}

fn as_user<T>(p: &Principal, msg: T) -> Request<T> {
    let mut r = Request::new(msg);
    goap_identity::set_headers(p, &mut r.headers);
    r
}

fn to_struct(v: serde_json::Value) -> pbjson_types::Struct {
    serde_json::from_value(v).unwrap()
}

/// Over the wire: the caller is the subject of the request, the platform acts under its system subject.
#[tokio::test]
async fn handler() {
    let ctx = Ctx::background();
    let h = goap_convsvc::Handler::new(Arc::new(new_service(Arc::new(MemoryStore::new()))));
    let app = pb::conversation_service_router(Arc::new(h), goap_connect::Interceptors::none());
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", ln.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(ln, app).await });
    let cl = pb::ConversationServiceClient::connect(&url);

    let created = cl
        .create_conversation(
            &ctx,
            as_user(
                &alice(),
                pb::CreateConversationRequest {
                    title: "First".into(),
                },
            ),
        )
        .await
        .unwrap();
    let id = created.msg.conversation.unwrap().id;
    cl.append_message(
        &ctx,
        as_user(
            &alice(),
            pb::AppendMessageRequest {
                conversation_id: id.clone(),
                text: "hello".into(),
                ..Default::default()
            },
        ),
    )
    .await
    .unwrap();
    let act = to_struct(json!({"type": "select_project", "args": {"id": "PROJ-1"}}));
    let am = cl
        .append_message(
            &ctx,
            as_user(
                &system(),
                pb::AppendMessageRequest {
                    conversation_id: id.clone(),
                    role: "assistant".into(),
                    status: "pending".into(),
                    process_id: "P1".into(),
                    ..Default::default()
                },
            ),
        )
        .await
        .unwrap();
    cl.update_message(
        &ctx,
        as_user(
            &system(),
            pb::UpdateMessageRequest {
                id: am.msg.message.unwrap().id,
                text: "Done.".into(),
                actions: vec![act],
                status: "done".into(),
                ..Default::default()
            },
        ),
    )
    .await
    .unwrap();
    let got = cl
        .get_conversation(
            &ctx,
            as_user(&alice(), pb::GetConversationRequest { id: id.clone() }),
        )
        .await
        .unwrap();
    let ms = &got.msg.messages;
    assert!(
        ms.len() == 2
            && serde_json::to_value(&ms[1].actions[0]).unwrap()["type"] == "select_project"
            && ms[1].seq == 2,
        "get = {:?}",
        got.msg
    );
    let e = cl
        .get_conversation(
            &ctx,
            as_user(&bob(), pb::GetConversationRequest { id: id.clone() }),
        )
        .await
        .unwrap_err();
    assert_eq!(e.code(), Code::NotFound, "bob: {e}");
    let e = cl
        .get_conversation(
            &ctx,
            Request::new(pb::GetConversationRequest { id: id.clone() }),
        )
        .await
        .unwrap_err();
    assert_eq!(e.code(), Code::Unauthenticated, "anonymous: {e}");
    let e = cl
        .append_message(
            &ctx,
            as_user(
                &alice(),
                pb::AppendMessageRequest {
                    conversation_id: id.clone(),
                    role: "assistant".into(),
                    text: "forged".into(),
                    ..Default::default()
                },
            ),
        )
        .await
        .unwrap_err();
    assert_eq!(e.code(), Code::PermissionDenied, "forged: {e}");
    let e = cl
        .append_message(
            &ctx,
            as_user(
                &alice(),
                pb::AppendMessageRequest {
                    conversation_id: id.clone(),
                    text: "x".repeat(MAX_TEXT_BYTES + 1),
                    ..Default::default()
                },
            ),
        )
        .await
        .unwrap_err();
    assert_eq!(e.code(), Code::InvalidArgument, "large: {e}");
    let l = cl
        .list_conversations(
            &ctx,
            as_user(&alice(), pb::ListConversationsRequest::default()),
        )
        .await
        .unwrap();
    assert!(
        l.msg.conversations.len() == 1 && l.msg.conversations[0].title == "First",
        "list = {:?}",
        l.msg
    );
}

/// The short description of the context of a user message is stored with it.
#[tokio::test]
async fn message_context() {
    let ctx = Ctx::background();
    let st = stores("context").await;
    for (name, st) in &st.all {
        let s = new_service(st.clone());
        let c = s.create(&ctx, &alice(), "t").await.unwrap();
        s.append(
            &ctx,
            &alice(),
            &c.id,
            "",
            Content {
                text: "hi".into(),
                context: "tab change, project PROJ-A".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
        let (_, ms) = s.get(&ctx, &alice(), &c.id).await.unwrap();
        assert!(
            ms.len() == 1 && ms[0].context == "tab change, project PROJ-A",
            "{name}: {ms:?}"
        );
    }
}

#[tokio::test]
async fn rolling_summary() {
    let ctx = Ctx::background();
    let st = stores("summary").await;
    for (name, st) in &st.all {
        let s = new_service(st.clone());
        let c = s.create(&ctx, &alice(), "").await.unwrap();
        let sum = s.summary(&ctx, &system(), &c.id).await.unwrap();
        assert!(
            sum.text.is_empty() && sum.up_to == 0,
            "{name}: none yet: {sum:?}"
        );
        // only a platform service writes it
        assert!(
            s.set_summary(&ctx, &alice(), &c.id, "x", 2)
                .await
                .unwrap_err()
                .is_forbidden(),
            "{name}: owner writes"
        );
        assert!(
            s.set_summary(
                &ctx,
                &system(),
                &c.id,
                &"x".repeat(MAX_SUMMARY_BYTES + 1),
                2
            )
            .await
            .unwrap_err()
            .is_invalid(),
            "{name}: too long"
        );
        s.set_summary(&ctx, &system(), &c.id, "first four", 4)
            .await
            .unwrap();
        // a summary covering fewer messages does not replace it; one covering more does
        s.set_summary(&ctx, &system(), &c.id, "first two", 2)
            .await
            .unwrap();
        let sum = s.summary(&ctx, &alice(), &c.id).await.unwrap();
        assert!(
            sum.text == "first four" && sum.up_to == 4,
            "{name}: kept {sum:?}"
        );
        s.set_summary(&ctx, &system(), &c.id, "first six", 6)
            .await
            .unwrap();
        let sum = s.summary(&ctx, &system(), &c.id).await.unwrap();
        assert!(
            sum.text == "first six" && sum.up_to == 6,
            "{name}: grown {sum:?}"
        );
        // someone else's conversation is not found; deleting the conversation drops its summary
        assert!(
            s.summary(&ctx, &bob(), &c.id)
                .await
                .unwrap_err()
                .is_not_found(),
            "{name}: other subject"
        );
        s.delete(&ctx, &alice(), &c.id).await.unwrap();
        assert!(
            st.summary(&ctx, &c.id).await.unwrap_err().is_not_found(),
            "{name}: after delete"
        );
    }
}

/// update_action is the compare-and-set of one action (ADR 0090): a refusal of the function leaves the message as
/// it is and comes back as it was given.
#[tokio::test]
async fn update_action() {
    #[derive(Debug, thiserror::Error)]
    #[error("decided")]
    struct Decided;

    let ctx = Ctx::background();
    let st = stores("action").await;
    for (name, st) in &st.all {
        let s = new_service(st.clone());
        let c = s.create(&ctx, &alice(), "").await.unwrap();
        let m = s
            .append(
                &ctx,
                &system(),
                &c.id,
                ROLE_ASSISTANT,
                Content {
                    actions: vec![action(json!({"type": "start_agent", "status": "proposed"}))],
                    ..Default::default()
                },
            )
            .await
            .unwrap();
        let out = s
            .update_action(
                &ctx,
                &system(),
                &m.id,
                0,
                Box::new(|mut a| {
                    a.insert("status".into(), json!("starting"));
                    Ok(a)
                }),
            )
            .await
            .unwrap();
        assert_eq!(out.actions[0]["status"], "starting", "{name}");
        let e = s
            .update_action(
                &ctx,
                &system(),
                &m.id,
                0,
                Box::new(|_| Err(Error::other(Decided))),
            )
            .await
            .unwrap_err();
        assert!(e.downcast_other::<Decided>().is_some(), "{name}: {e}");
        let e = s
            .update_action(&ctx, &system(), &m.id, 3, Box::new(Ok))
            .await
            .unwrap_err();
        assert!(e.is_action_missing() && e.is_invalid(), "{name}: {e}");
        let e = s
            .update_action(&ctx, &alice(), &m.id, 0, Box::new(Ok))
            .await
            .unwrap_err();
        assert!(e.is_forbidden(), "{name}: {e}");
        let (_, ms) = s.get(&ctx, &alice(), &c.id).await.unwrap();
        assert_eq!(ms[0].actions[0]["status"], "starting", "{name}");
    }
}
