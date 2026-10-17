use std::sync::Arc;

use async_trait::async_trait;
use goap_connect::{Code, Request};
use goap_ctx::Ctx;
use goap_gen::model::v1 as pb;
use goap_gen::model::v1::ModelService;
use goap_llm as llm;
use parking_lot::Mutex;

use super::helpers::{alias, fake_provider, memory, model, new_service, user_msg, with_roles};
use crate::error::Error;
use crate::handler::{Handler, rpc_err};
use crate::store::{AliasEntry, ModelEntry};
use crate::suggest::{
    HELPER_ALIAS, MAX_SUGGEST_CONTEXT_BYTES, MAX_SUGGEST_FIELDS, MAX_SUGGEST_MESSAGE_BYTES,
    MAX_SUGGEST_MESSAGES, SuggestContext, SuggestField, SuggestInput, SuggestMessage, SuggestModel,
    SuggestResult, suggest,
};

/// stubModel answers every call with a fixed text and records the request.
#[derive(Default)]
struct StubModel {
    text: Mutex<String>,
    aliases: Vec<AliasEntry>,
    got: Mutex<Vec<llm::Request>>,
    metas: Mutex<Vec<llm::CallMeta>>,
}

#[async_trait]
impl SuggestModel for StubModel {
    async fn complete(&self, ctx: &Ctx, req: llm::Request) -> Result<llm::Response, Error> {
        self.got.lock().push(req);
        self.metas.lock().push(llm::meta_from(ctx));
        Ok(llm::Response {
            text: self.text.lock().clone(),
            usage: llm::Usage {
                input_tokens: 7,
                output_tokens: 3,
                ..Default::default()
            },
            ..Default::default()
        })
    }

    async fn available(&self, _: &Ctx) -> Result<(Vec<ModelEntry>, Vec<AliasEntry>), Error> {
        Ok((Vec::new(), self.aliases.clone()))
    }
}

fn stub(text: &str) -> StubModel {
    StubModel {
        text: Mutex::new(text.into()),
        aliases: vec![alias(HELPER_ALIAS, "fake/echo")],
        ..Default::default()
    }
}

fn field(id: &str, typ: &str) -> SuggestField {
    SuggestField {
        id: id.into(),
        r#type: typ.into(),
        ..Default::default()
    }
}

fn fields() -> Vec<SuggestField> {
    vec![
        field("title", "string"),
        field("size", "number"),
        field("done", "boolean"),
        field("due", "date"),
        SuggestField {
            enum_values: vec!["open".into(), "closed".into()],
            ..field("status", "enum")
        },
        field("extra", "json"),
        SuggestField {
            read_only: true,
            ..field("key", "string")
        },
    ]
}

fn input() -> SuggestInput {
    SuggestInput {
        context: SuggestContext {
            tab_kind: "node".into(),
            fields: fields(),
            ..Default::default()
        },
        ..Default::default()
    }
}

fn ids(r: &SuggestResult) -> String {
    r.proposals
        .iter()
        .map(|p| p.field_id.as_str())
        .collect::<Vec<_>>()
        .join(",")
}

#[tokio::test]
async fn test_suggest_parses_and_filters() {
    let answer = "Here you go:\n```json\n".to_string()
        + r#"{"message":"ok","proposals":[
	 {"fieldId":"title","value":"A title","rationale":"r"},
	 {"fieldId":"title","value":"duplicate"},
	 {"fieldId":"nope","value":"x"},
	 {"fieldId":"key","value":"readonly"},
	 {"fieldId":"size","value":"12"},
	 {"fieldId":"done","value":true},
	 {"fieldId":"due","value":"2026-10-07"},
	 {"fieldId":"status","value":"pending"},
	 {"fieldId":"extra","value":{"a":[1]}},
	 {"fieldId":"size"}
	]}"# + "\n```";
    let m = stub(&answer);
    let r = suggest(&Ctx::background(), &m, input()).await.unwrap();
    assert_eq!(ids(&r), "title,done,due,extra", "kept");
    assert!(
        r.proposals[0].value == r#""A title""#
            && r.proposals[0].rationale == "r"
            && r.message == "ok",
        "unexpected {r:?}"
    );
    assert_eq!(r.usage.input_tokens, 7, "usage {:?}", r.usage);
    let req = m.got.lock()[0].clone();
    assert!(
        req.model == HELPER_ALIAS
            && req.json
            && req.messages.len() == 1
            && req.messages[0].role == "user"
            && req.system.contains(r#""id":"title""#),
        "request {req:?}"
    );
}

#[tokio::test]
async fn test_suggest_enum_and_non_json_answer() {
    let m = stub(r#"{"proposals":[{"fieldId":"status","value":"closed"}]}"#);
    let r = suggest(&Ctx::background(), &m, input()).await.unwrap();
    assert_eq!(r.proposals.len(), 1, "{r:?}");
    *m.text.lock() = "I cannot help with that.".into();
    let r = suggest(&Ctx::background(), &m, input()).await.unwrap();
    assert!(
        r.proposals.is_empty() && r.message == "I cannot help with that.",
        "{r:?}"
    );
}

fn smsg(role: &str, text: &str) -> SuggestMessage {
    SuggestMessage {
        role: role.into(),
        text: text.into(),
    }
}

#[tokio::test]
async fn test_suggest_messages() {
    let mut inp = input();
    inp.messages = vec![
        smsg("assistant", "dropped: first turn is not the user's"),
        smsg("user", "a"),
        smsg("assistant", "b"),
    ];
    inp.instruction = "shorter".into();
    let m = stub("{}");
    suggest(&Ctx::background(), &m, inp).await.unwrap();
    let msgs = m.got.lock()[0].messages.clone();
    assert!(
        msgs.len() == 3
            && msgs[0].content == "a"
            && msgs[2].role == "user"
            && msgs[2].content == "shorter",
        "messages {msgs:?}"
    );
}

#[tokio::test]
async fn test_suggest_limits() {
    let m = stub("{}");
    type Mutate = fn(&mut SuggestInput);
    let cases: Vec<(&str, Mutate)> = vec![
        ("no field", |i| i.context.fields.clear()),
        ("too many", |i| {
            i.context.fields = (0..=MAX_SUGGEST_FIELDS)
                .map(|n| SuggestField {
                    id: format!("{}{}", (b'a' + (n % 26) as u8) as char, "x".repeat(n)),
                    ..Default::default()
                })
                .collect();
        }),
        ("duplicate id", |i| {
            i.context.fields.push(field("title", ""))
        }),
        ("context bytes", |i| {
            i.context.selection = "x".repeat(MAX_SUGGEST_CONTEXT_BYTES + 1)
        }),
        ("many messages", |i| {
            i.messages = vec![smsg("user", "x"); MAX_SUGGEST_MESSAGES + 1];
        }),
        ("bad role", |i| i.messages = vec![smsg("system", "x")]),
        ("long message", |i| {
            i.messages = vec![smsg("user", &"x".repeat(MAX_SUGGEST_MESSAGE_BYTES + 1))]
        }),
        ("instruction", |i| {
            i.instruction = "x".repeat(MAX_SUGGEST_MESSAGE_BYTES + 1)
        }),
    ];
    for (name, mutate) in cases {
        let mut inp = input();
        mutate(&mut inp);
        match suggest(&Ctx::background(), &m, inp).await {
            Err(e) if e.is_invalid() => {}
            other => panic!("{name}: want invalid, got {other:?}"),
        }
    }
    assert!(
        m.got.lock().is_empty(),
        "the model must not be called for an invalid request"
    );
}

#[tokio::test]
async fn test_suggest_refused_without_helper_alias() {
    let m = StubModel {
        text: Mutex::new("{}".into()),
        aliases: vec![alias("default", "fake/echo")],
        ..Default::default()
    };
    let err = suggest(&Ctx::background(), &m, input()).await.unwrap_err();
    assert!(err.is_model_disabled() && m.got.lock().is_empty(), "{err}");
    assert_eq!(rpc_err(err).code(), Code::FailedPrecondition);
}

/// The model call goes through the gateway as the caller: the role allow-list of the model applies.
#[tokio::test]
async fn test_suggest_authorization() {
    let ctx = Ctx::background();
    let (svc, g) = new_service(memory());
    let svc = svc.shared();
    let echo = ModelEntry {
        roles: vec!["methodologist".into()],
        ..model("fake", "echo")
    };
    g.seed(
        &[fake_provider()],
        &[echo],
        &[alias(HELPER_ALIAS, "fake/echo")],
    );
    let h = Handler::new(svc.clone());
    let call = |roles: &'static str| {
        let h = h.clone();
        let ctx = ctx.clone();
        async move {
            let mut req = Request::new(pb::SuggestRequest {
                context: Some(pb::SuggestContext {
                    fields: vec![pb::SuggestField {
                        id: "title".into(),
                        r#type: "string".into(),
                        ..Default::default()
                    }],
                    ..Default::default()
                }),
                ..Default::default()
            });
            req.headers
                .insert(goap_identity::HEADER_SUBJECT, "u".parse().unwrap());
            req.headers
                .insert(goap_identity::HEADER_ROLES, roles.parse().unwrap());
            h.suggest(&ctx, req).await
        }
    };
    let r = call("contributor").await;
    assert_eq!(
        r.as_ref().err().map(|e| e.code()),
        Some(Code::FailedPrecondition),
        "a caller without the role cannot use the helper model"
    );
    let resp = call("methodologist").await.unwrap().msg;
    // the fake model answers JSON with no proposal, and reports usage
    assert!(
        resp.message.is_empty()
            && resp.proposals.is_empty()
            && resp.usage.is_some_and(|u| u.input_tokens != 0),
        "{resp:?}"
    );
    // the service itself applies the catalog policy too
    let user = with_roles("u", &["contributor"]);
    let err = svc
        .complete(
            &user,
            llm::Request {
                model: HELPER_ALIAS.into(),
                messages: user_msg("x"),
                ..Default::default()
            },
        )
        .await
        .unwrap_err();
    assert!(err.is_forbidden(), "{err}");
}

/// The helper declares itself to the ledger of the gateway (ADR 0089).
#[tokio::test]
async fn test_suggest_stamps_its_source() {
    let m = Arc::new(stub(r#"{"message":"ok"}"#));
    suggest(&Ctx::background(), m.as_ref(), input())
        .await
        .unwrap();
    let metas = m.metas.lock();
    assert!(
        metas.len() == 1 && metas[0].source == llm::SOURCE_HELPER,
        "{metas:?}"
    );
}
