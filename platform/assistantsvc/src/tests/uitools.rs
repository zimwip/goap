//! uitools_test.go

use std::collections::BTreeMap;

use goap_authz::{system, with_principal};
use goap_convsvc as convsvc;
use goap_gen::assistant::v1 as pb;

use super::*;
use crate::agents::{
    DECISION_ACCEPT, DECISION_REJECT, STATUS_FAILED, STATUS_PROPOSED, STATUS_REJECTED,
};
use crate::context::{
    Dialog, Element, Entity, Focus, MAX_ENTITIES, MAX_ERROR_BYTES, MAX_ERRORS, MAX_PROPS,
    MAX_SUMMARY_BYTES, Screen,
};
use crate::handler::tool_from_pb;
use crate::service::{MAX_CONTEXT_BYTES, MAX_SELECTION_BYTES};
use crate::turn::TOOL_OPEN_CHANGE;
use crate::uitools::{
    ACTION_UI_TOOL, LEVEL_EFFECT, LEVEL_WRITE, MAX_ENUM_VALUES, MAX_PROPOSALS_PER_ANSWER,
    MAX_TOOL_DESCRIPTION, MAX_TOOL_PROPS, MAX_UI_TOOLS, ReportInput, STATUS_ACCEPTED, STATUS_DONE,
    STATUS_REQUESTED, UIArgs, UIParam, check_tools,
};
use crate::util::map_of;

fn param(t: &str) -> UIParam {
    UIParam {
        r#type: t.into(),
        ..Default::default()
    }
}

pub(crate) fn filter_tool() -> UITool {
    UITool {
        name: "filter_list".into(),
        description: "Filter the list".into(),
        guidance: "give the status".into(),
        level: LEVEL_EFFECT.into(),
        args: UIArgs {
            properties: [
                (
                    sv("status"),
                    UIParam {
                        r#type: "enum".into(),
                        r#enum: vec![sv("open"), sv("closed")],
                        ..Default::default()
                    },
                ),
                (
                    sv("tags"),
                    UIParam {
                        r#type: "array".into(),
                        items: Some(Box::new(param("string"))),
                        ..Default::default()
                    },
                ),
                (sv("limit"), param("number")),
            ]
            .into_iter()
            .collect(),
            required: vec![sv("status")],
        },
        ..Default::default()
    }
}

pub(crate) fn set_field_tool() -> UITool {
    UITool {
        name: "set_field".into(),
        description: "Set the title of the change".into(),
        guidance: "give the new title".into(),
        level: LEVEL_WRITE.into(),
        target: "field:title".into(),
        args: UIArgs {
            properties: [
                (sv("value"), param("string")),
                (sv("force"), param("boolean")),
            ]
            .into_iter()
            .collect(),
            required: vec![sv("value")],
        },
    }
}

#[test]
fn context_caps_and_sanitising() {
    let mut c = Context {
        app: App {
            project: "PROJ-A\x00".into(),
            tab: Tab {
                kind: "change".into(),
                params: [(sv("id"), sv("CHG-1"))].into_iter().collect(),
            },
        },
        screen: Screen {
            kind: "change".into(),
            title: "A\x1b[31mtitle\nline".into(),
            summary: "s".repeat(5000),
            ..Default::default()
        },
        focus: Focus {
            pending_action: "typing\x07".into(),
            selection: "a\x00b\nc".into(),
            ..Default::default()
        },
    };
    for _ in 0..80 {
        let mut props: BTreeMap<String, String> = ["a", "b", "c", "d", "e", "f", "g"]
            .iter()
            .enumerate()
            .map(|(i, k)| (sv(k), (i + 1).to_string()))
            .collect();
        props.insert(sv("h"), "v".repeat(500));
        c.screen.entities.push(Entity {
            r#type: "impact".into(),
            id: "i".repeat(90),
            label: "l".repeat(400),
            state: "draft".into(),
            props,
        });
    }
    c.focus.element = Some(Element {
        r#type: "impact".into(),
        id: "FOCUSED".into(),
        label: "the one".into(),
    });
    c.screen.entities.push(Entity {
        r#type: "impact".into(),
        id: "FOCUSED".into(),
        label: "the one".into(),
        ..Default::default()
    });
    for _ in 0..20 {
        c.focus.errors.push("e".repeat(900));
    }
    let o = c.normalise().expect("normalise");
    let n = o.render("").len();
    assert!(n <= MAX_CONTEXT_BYTES, "{n} bytes");
    assert!(
        o.screen.entities.len() <= MAX_ENTITIES
            && o.focus.errors.len() == MAX_ERRORS
            && o.focus.errors[0].len() <= MAX_ERROR_BYTES
            && o.screen.summary.len() <= MAX_SUMMARY_BYTES,
        "caps: {} entities, {} errors",
        o.screen.entities.len(),
        o.focus.errors.len()
    );
    assert_eq!(
        o.screen.entities[0].id, "FOCUSED",
        "the focused entity is not first"
    );
    assert!(
        o.screen.entities[1].props.len() <= MAX_PROPS,
        "props not capped"
    );
    let r = o.render("");
    for bad in ["\x00", "\x1b", "\x07", "\\u0000"] {
        assert!(!r.contains(bad), "control character {bad:?} in {:.200}", r);
    }
    if o.focus.selection != "a b\nc" && o.focus.selection != "ab\nc" {
        // control characters are blanked, the newline of a selection is kept
        assert!(
            o.focus.selection.contains('\n') && !o.focus.selection.contains('\x00'),
            "selection {:?}",
            o.focus.selection
        );
    }
    assert!(!o.screen.title.contains('\n'), "title {:?}", o.screen.title);
    // the selection itself is refused beyond its cap
    let err = Context {
        focus: Focus {
            selection: "x".repeat(MAX_SELECTION_BYTES + 1),
            ..Default::default()
        },
        ..Default::default()
    }
    .normalise()
    .unwrap_err();
    assert!(err.is_invalid(), "got {err}");
}

#[tokio::test]
async fn context_is_rendered_focus_first() {
    let e = new_env(&[r#"{"message":"ok"}"#]).await;
    let c = Context {
        app: App {
            project: "PROJ-A".into(),
            tab: Tab {
                kind: "change".into(),
                params: [(sv("id"), sv("CHG-1"))].into_iter().collect(),
            },
        },
        screen: Screen {
            kind: "change".into(),
            title: "Change CHG-1".into(),
            summary: "three impacts".into(),
            entities: vec![Entity {
                r#type: "impact".into(),
                id: "IMP-1".into(),
                label: "Impact one".into(),
                state: "proposed".into(),
                ..Default::default()
            }],
        },
        focus: Focus {
            element: Some(Element {
                r#type: "impact".into(),
                id: "IMP-1".into(),
                label: "Impact one".into(),
            }),
            pending_action: "reviewing impact IMP-1 of change CHG-1".into(),
            dialog: Dialog {
                kind: "review".into(),
                title: "Review".into(),
            },
            errors: vec![sv("title is required")],
            ..Default::default()
        },
    };
    e.send_tools("help", c, Vec::new()).await.unwrap();
    let sys = e.got()[0].system.clone();
    let i = sys
        .find("Context of this turn")
        .unwrap_or_else(|| panic!("{sys}"));
    let ctx = &sys[i..];
    let f = ctx.find(r#""focus""#);
    let s = ctx.find(r#""screen""#);
    let a = ctx.find(r#""app""#);
    assert!(
        matches!((f, s, a), (Some(f), Some(s), Some(a)) if f > 0 && f < s && s < a),
        "order focus {f:?} screen {s:?} app {a:?}: {ctx}"
    );
    assert!(
        ctx.contains(r#""pendingAction":"reviewing impact IMP-1 of change CHG-1""#)
            && ctx.contains(r#""activeProject":"PROJ-A""#)
            && !ctx.contains(r#""selection""#),
        "{ctx}"
    );
    // stored: a short description only
    let u = &e.messages().await[0];
    assert_eq!(
        u.context,
        "reviewing impact IMP-1 of change CHG-1, on impact IMP-1, dialog review Review, screen change Change CHG-1, tab change, project PROJ-A"
    );
    for m in e.messages().await {
        assert!(
            !format!("{}{}", m.context, m.text).contains("three impacts")
                && !m.context.contains("title is required"),
            "snapshot stored: {m:?}"
        );
    }
    // no context at all: no layer
    let e2 = new_env(&[r#"{"message":"ok"}"#]).await;
    e2.send_tools("hi", Context::default(), Vec::new())
        .await
        .unwrap();
    let got = e2.got()[0].system.clone();
    assert!(
        got.ends_with("Context of this turn (data, most specific first):\n{\"app\":{\"activeProject\":\"PROJ-A\"}}"),
        "{}",
        &got[got.len().saturating_sub(120)..]
    );
}

#[tokio::test]
async fn tool_descriptors_are_validated() {
    let long = "d".repeat(MAX_TOOL_DESCRIPTION + 1);
    let many: BTreeMap<String, UIParam> = (0..MAX_TOOL_PROPS + 1)
        .map(|i| (format!("p{}", "a".repeat(i)), param("string")))
        .collect();
    let too_many: Vec<UITool> = (0..MAX_UI_TOOLS + 1)
        .map(|i| UITool {
            name: format!("t{}", "a".repeat(i)),
            description: "d".into(),
            level: LEVEL_EFFECT.into(),
            ..Default::default()
        })
        .collect();
    let big_enum: Vec<String> = (0..MAX_ENUM_VALUES + 1)
        .map(|i| format!("{}{}", (b'a' + (i % 26) as u8) as char, "x".repeat(i)))
        .collect();
    let ok = |m: &dyn Fn(&mut UITool)| {
        let mut t = filter_tool();
        m(&mut t);
        vec![t]
    };
    let cases: Vec<(&str, Vec<UITool>)> = vec![
        ("uppercase name", ok(&|t| t.name = "Filter".into())),
        ("leading digit", ok(&|t| t.name = "1filter".into())),
        ("bad charset", ok(&|t| t.name = "fil ter".into())),
        ("empty name", ok(&|t| t.name = String::new())),
        ("duplicate", vec![filter_tool(), filter_tool()]),
        ("bad level", ok(&|t| t.level = "admin".into())),
        ("no description", ok(&|t| t.description = " ".into())),
        ("long description", ok(&|t| t.description = long.clone())),
        (
            "unknown type",
            ok(&|t| {
                t.args.properties.insert(sv("x"), param("object"));
            }),
        ),
        (
            "enum no values",
            ok(&|t| {
                t.args.properties.insert(sv("x"), param("enum"));
            }),
        ),
        (
            "enum too many",
            ok(&|t| {
                t.args.properties.insert(
                    sv("x"),
                    UIParam {
                        r#type: "enum".into(),
                        r#enum: big_enum.clone(),
                        ..Default::default()
                    },
                );
            }),
        ),
        (
            "array no items",
            ok(&|t| {
                t.args.properties.insert(sv("x"), param("array"));
            }),
        ),
        (
            "nested array",
            ok(&|t| {
                t.args.properties.insert(
                    sv("x"),
                    UIParam {
                        r#type: "array".into(),
                        items: Some(Box::new(UIParam {
                            r#type: "array".into(),
                            items: Some(Box::new(param("string"))),
                            ..Default::default()
                        })),
                        ..Default::default()
                    },
                );
            }),
        ),
        (
            "required unknown",
            ok(&|t| t.args.required = vec![sv("nope")]),
        ),
        (
            "bad property",
            ok(&|t| {
                t.args.properties.insert(sv("bad name"), param("string"));
            }),
        ),
        ("too many props", ok(&|t| t.args.properties = many.clone())),
        ("too many tools", too_many),
    ];
    for (name, tools) in cases {
        let e = new_env(&[r#"{"message":"ok"}"#]).await;
        let res = e.send_tools("hi", Context::default(), tools).await;
        assert!(
            res.as_ref().is_err_and(|e| e.is_invalid()),
            "{name}: got {res:?}"
        );
        assert!(
            e.messages().await.is_empty(),
            "{name}: a refused request appended something"
        );
    }
    check_tools(&[filter_tool(), set_field_tool()]).expect("valid tools");
}

#[tokio::test]
async fn prompt_lists_the_screen_tools_with_their_prefix() {
    let e = new_env(&[r#"{"message":"ok"}"#]).await;
    e.send_tools(
        "hi",
        Context::default(),
        vec![filter_tool(), set_field_tool()],
    )
    .await
    .unwrap();
    let sys = e.got()[0].system.clone();
    for want in [
        r#""name":"ui.filter_list""#,
        r#""name":"ui.set_field""#,
        r#""level":"write""#,
        r#""guidance":"give the status""#,
        r#""target":"field:title""#,
        "most specific first",
        "ask one short question",
    ] {
        assert!(sys.contains(want), "missing {want}");
    }
    assert!(
        !sys.contains(r#""name":"filter_list""#),
        "an unprefixed name is offered"
    );
}

#[tokio::test]
async fn ui_tool_not_in_the_descriptors_is_refused() {
    let e = new_env(&[
        &call("ui.delete_everything", "{}"),
        r#"{"message":"sorry"}"#,
    ])
    .await;
    e.send_tools("do it", Context::default(), vec![filter_tool()])
        .await
        .unwrap();
    let r = e.tool_result(1);
    assert!(
        r.contains("offers no tool") && r.contains("ui.filter_list"),
        "{r}"
    );
    // a server tool name cannot be shadowed: a screen tool called list_agents is only ever ui.list_agents
    let e = new_env(&[&call("ui.list_agents", "{}"), r#"{"message":"x"}"#]).await;
    e.send_tools(
        "do it",
        Context::default(),
        vec![UITool {
            name: "list_agents".into(),
            description: "d".into(),
            level: LEVEL_EFFECT.into(),
            ..Default::default()
        }],
    )
    .await
    .unwrap();
    let a = e.answer().await;
    assert!(
        a.actions.len() == 1 && s(&a.actions[0], "type") == ACTION_UI_TOOL,
        "{:?}",
        a.actions
    );
    // without descriptors, none exists; and the turn emitted nothing
    let e = new_env(&[
        &call("ui.filter_list", r#"{"status":"open"}"#),
        r#"{"message":"x"}"#,
    ])
    .await;
    e.send_tools("do it", Context::default(), Vec::new())
        .await
        .unwrap();
    let a = e.answer().await;
    assert!(
        a.actions.is_empty() && e.tool_result(1).contains("none"),
        "{:?} {}",
        a.actions,
        e.tool_result(1)
    );
}

#[tokio::test]
async fn ui_tool_arguments_failing_the_schema_are_refused() {
    let too_big = format!(
        r#"{{"status":"open","tags":["{}","{}","{}"]}}"#,
        "x".repeat(2000),
        "y".repeat(2000),
        "z".repeat(2000)
    );
    let bad = [
        ("missing required", "{}".to_string()),
        ("enum", r#"{"status":"later"}"#.to_string()),
        ("type", r#"{"status":"open","limit":"ten"}"#.to_string()),
        (
            "unknown property",
            r#"{"status":"open","extra":1}"#.to_string(),
        ),
        (
            "array element",
            r#"{"status":"open","tags":["a",3]}"#.to_string(),
        ),
        (
            "not an array",
            r#"{"status":"open","tags":"a"}"#.to_string(),
        ),
        ("too big", too_big),
    ];
    for (name, a) in bad {
        let e = new_env(&[
            &call(&format!("ui.{}", filter_tool().name), &a),
            r#"{"message":"x"}"#,
        ])
        .await;
        e.send_tools("go", Context::default(), vec![filter_tool()])
            .await
            .unwrap();
        let got = e.answer().await;
        assert!(
            got.actions.is_empty() && e.tool_result(1).contains(r#""error""#),
            "{name}: {:?} {}",
            got.actions,
            e.tool_result(1)
        );
    }
}

#[tokio::test]
async fn effect_tool_is_emitted_requested() {
    let e = new_env(&[
        &call("ui.filter_list", r#"{"status":"open","tags":["a","b"]}"#),
        r#"{"message":"Filtered."}"#,
    ])
    .await;
    e.send_tools("show open ones", in_project("PROJ-A"), vec![filter_tool()])
        .await
        .unwrap();
    let a = e.answer().await;
    assert_eq!(a.actions.len(), 1, "{a:?}");
    let act = &a.actions[0];
    assert!(
        s(act, "type") == ACTION_UI_TOOL
            && s(act, "status") == STATUS_REQUESTED
            && s(act, "tool") == "filter_list"
            && s(act, "level") == LEVEL_EFFECT
            && s(act, "label").contains("Filter the list"),
        "{act:?}"
    );
    assert_eq!(
        map_of(act, "args")["status"],
        "open",
        "{:?}",
        act.get("args")
    );
    let r = e.tool_result(1);
    assert!(r.contains("no result is available"), "{r}");
    assert!(
        e.graph.created().is_empty() && e.engine.starts().is_empty(),
        "a screen tool ran something on the server"
    );
}

#[tokio::test]
async fn write_tool_is_recorded_proposed_with_no_side_effect() {
    let e = new_env(&[
        r#"{"message":"","tool_calls":[{"name":"ui.set_field","arguments":{"value":"New title"},"rationale":"you asked"}]}"#,
        r#"{"message":"I propose it."}"#,
    ])
    .await;
    e.send_tools("rename it", in_project("PROJ-A"), vec![set_field_tool()])
        .await
        .unwrap();
    let a = e.answer().await;
    assert_eq!(a.actions.len(), 1, "{a:?}");
    let act = &a.actions[0];
    assert!(
        s(act, "status") == STATUS_PROPOSED
            && s(act, "level") == LEVEL_WRITE
            && s(act, "rationale") == "you asked"
            && s(act, "project") == "PROJ-A"
            && s(act, "target") == "field:title",
        "{act:?}"
    );
    assert!(
        e.tool_result(1).contains("nothing was changed"),
        "{}",
        e.tool_result(1)
    );
    assert!(
        e.graph.created().is_empty() && e.engine.starts().is_empty(),
        "a proposal ran something on the server"
    );
}

#[tokio::test]
async fn at_most_three_proposals_per_answer() {
    let calls = [r#"{"name":"ui.set_field","arguments":{"value":"v"}}"#; 4].join(",");
    let first = format!(r#"{{"message":"","tool_calls":[{calls}]}}"#);
    let e = new_env(&[&first, r#"{"message":"x"}"#]).await;
    e.send_tools("rename", Context::default(), vec![set_field_tool()])
        .await
        .unwrap();
    let n = e.answer().await.actions.len();
    assert_eq!(n, MAX_PROPOSALS_PER_ANSWER, "{n} proposals");
}

impl Env {
    /// uiProposal runs a turn that ends with a write proposal and returns its message.
    pub(crate) async fn ui_proposal(&self, c: Context) -> convsvc::Message {
        self.set_answers(&[
            &call("ui.set_field", r#"{"value":"New title"}"#),
            r#"{"message":"I propose it."}"#,
        ]);
        self.send_tools("rename", c, vec![set_field_tool()])
            .await
            .unwrap();
        let m = self.answer().await;
        assert_eq!(m.actions.len(), 1, "{m:?}");
        m
    }

    pub(crate) async fn report(
        &self,
        m: &convsvc::Message,
        status: &str,
        msg: &str,
    ) -> Result<convsvc::Message, crate::Error> {
        self.report_as(&self.ctx, m, status, msg).await
    }

    async fn report_as(
        &self,
        ctx: &Ctx,
        m: &convsvc::Message,
        status: &str,
        msg: &str,
    ) -> Result<convsvc::Message, crate::Error> {
        self.svc
            .report(
                ctx,
                ReportInput {
                    conversation_id: self.conv.id.clone(),
                    message_id: m.id.clone(),
                    status: status.into(),
                    error: msg.into(),
                    ..Default::default()
                },
            )
            .await
    }
}

#[tokio::test]
async fn confirm_ui_tool_accept_only_records_the_decision() {
    let e = new_env(&[]).await;
    let m = e.ui_proposal(in_project("PROJ-A")).await;
    let got = e.confirm(&m, DECISION_ACCEPT).await.unwrap();
    let act = &got.actions[0];
    let decided = map_of(act, "decided");
    assert!(
        s(act, "status") == STATUS_ACCEPTED
            && decided["by"] == "u1"
            && decided["decision"] == DECISION_ACCEPT,
        "{act:?}"
    );
    assert!(
        e.engine.starts().is_empty() && e.graph.created().is_empty(),
        "the server did something on accept"
    );
    let err = e.confirm(&m, DECISION_ACCEPT).await.unwrap_err();
    assert!(err.is_decided(), "double accept: {err}");
    let err = e.confirm(&m, DECISION_REJECT).await.unwrap_err();
    assert!(err.is_decided(), "reject after accept: {err}");
}

#[tokio::test]
async fn confirm_ui_tool_reject() {
    let e = new_env(&[]).await;
    let m = e.ui_proposal(in_project("PROJ-A")).await;
    let got = e.confirm(&m, DECISION_REJECT).await.unwrap();
    assert_eq!(
        s(&got.actions[0], "status"),
        STATUS_REJECTED,
        "{:?}",
        got.actions
    );
    let err = e.confirm(&m, DECISION_ACCEPT).await.unwrap_err();
    assert!(err.is_decided(), "got {err}");
    let err = e.report(&m, STATUS_DONE, "").await.unwrap_err();
    assert!(err.is_state(), "report on a rejected one: {err}");
}

#[tokio::test]
async fn confirm_ui_tool_stale_on_another_project() {
    let e = new_env(&[]).await;
    let m = e.ui_proposal(in_project("PROJ-A")).await;
    let err = e
        .confirm_in(&m, DECISION_ACCEPT, "PROJ-B")
        .await
        .unwrap_err();
    assert!(err.is_stale(), "got {err}");
    let got = e.answer().await;
    assert_eq!(
        s(&got.actions[0], "status"),
        STATUS_PROPOSED,
        "not left proposed: {:?}",
        got.actions
    );
    // still rejectable
    let got = e.confirm_in(&m, DECISION_REJECT, "PROJ-B").await.unwrap();
    assert_eq!(
        s(&got.actions[0], "status"),
        STATUS_REJECTED,
        "{:?}",
        got.actions
    );
}

#[tokio::test]
async fn report_action_owner_only_and_only_from_accepted() {
    let e = new_env(&[]).await;
    let m = e.ui_proposal(in_project("PROJ-A")).await;
    let err = e.report(&m, STATUS_DONE, "").await.unwrap_err();
    assert!(err.is_state(), "report before accept: {err}");
    let err = e.report(&m, "maybe", "").await.unwrap_err();
    assert!(err.is_invalid(), "bad status: {err}");
    e.confirm(&m, DECISION_ACCEPT).await.unwrap();
    // another user, and a service
    let other = with_principal(
        &Ctx::background(),
        Principal {
            subject: "u2".into(),
            ..Default::default()
        },
    );
    let err = e.report_as(&other, &m, STATUS_DONE, "").await.unwrap_err();
    assert!(err.is_not_found(), "another user: {err}");
    let svc = with_principal(&Ctx::background(), system("x", &[]));
    let err = e.report_as(&svc, &m, STATUS_DONE, "").await.unwrap_err();
    assert!(err.is_forbidden(), "a service: {err}");
    let got = e
        .report(&m, STATUS_FAILED, "the field is read-only\x00")
        .await
        .unwrap();
    let a = &got.actions[0];
    assert!(
        s(a, "status") == STATUS_FAILED
            && s(a, "error") == "the field is read-only"
            && map_of(a, "reported")["by"] == "u1",
        "{a:?}"
    );
    let err = e.report(&m, STATUS_DONE, "").await.unwrap_err();
    assert!(err.is_decided(), "second report: {err}");
    // not a screen tool action
    e.set_answers(&[
        &call(TOOL_OPEN_CHANGE, r#"{"changeId":"CHG-1"}"#),
        r#"{"message":"ok"}"#,
    ]);
    e.send("open", Context::default()).await.unwrap();
    let err = e
        .report(&e.answer().await, STATUS_DONE, "")
        .await
        .unwrap_err();
    assert!(err.is_invalid(), "report on open_change: {err}");
}

#[tokio::test]
async fn report_action_on_an_effect() {
    let e = new_env(&[
        &call("ui.filter_list", r#"{"status":"open"}"#),
        r#"{"message":"ok"}"#,
    ])
    .await;
    e.send_tools("filter", Context::default(), vec![filter_tool()])
        .await
        .unwrap();
    let m = e.answer().await;
    if let Err(err) = e.confirm(&m, DECISION_ACCEPT).await {
        assert!(err.is_decided(), "accept on an effect: {err}");
    }
    let got = e.report(&m, STATUS_DONE, "").await.unwrap();
    assert_eq!(
        s(&got.actions[0], "status"),
        STATUS_DONE,
        "{:?}",
        got.actions
    );
}

#[tokio::test]
async fn history_carries_decisions_and_outcomes() {
    let steps: [(&str, u8, &str); 5] = [
        ("undecided", 0, "has not decided yet, nothing was changed"),
        ("rejected", 1, "rejected the proposal ui.set_field"),
        (
            "accepted",
            2,
            "accepted the proposal ui.set_field value=\"New title\": the interface is applying it",
        ),
        ("done", 3, "ui.set_field value=\"New title\" was done"),
        ("failed", 4, "failed in the interface: locked"),
    ];
    for (name, what, want) in steps {
        let e = new_env(&[]).await;
        let m = e.ui_proposal(in_project("PROJ-A")).await;
        match what {
            1 => {
                let _ = e.confirm(&m, DECISION_REJECT).await;
            }
            2 => {
                let _ = e.confirm(&m, DECISION_ACCEPT).await;
            }
            3 => {
                let _ = e.confirm(&m, DECISION_ACCEPT).await;
                let _ = e.report(&m, STATUS_DONE, "").await;
            }
            4 => {
                let _ = e.confirm(&m, DECISION_ACCEPT).await;
                let _ = e.report(&m, STATUS_FAILED, "locked").await;
            }
            _ => {}
        }
        e.set_answers(&[r#"{"message":"ok"}"#]);
        e.send("and then?", in_project("PROJ-A")).await.unwrap();
        let hist: String = e.got()[0]
            .messages
            .iter()
            .map(|m| m.content.clone() + "\n")
            .collect();
        assert!(
            hist.contains(want),
            "{name}: history lacks {want:?}: {hist}"
        );
    }
    // an effect
    let e = new_env(&[
        &call("ui.filter_list", r#"{"status":"open"}"#),
        r#"{"message":"ok"}"#,
    ])
    .await;
    e.send_tools("filter", Context::default(), vec![filter_tool()])
        .await
        .unwrap();
    e.set_answers(&[r#"{"message":"ok"}"#]);
    e.send("and then?", Context::default()).await.unwrap();
    let h = e.got()[0].messages[1].content.clone();
    assert!(h.contains("asked to run ui.filter_list"), "{h}");
}

#[tokio::test]
async fn screen_tools_keep_the_ledger_source_assistant() {
    let e = new_env(&[
        &call("ui.filter_list", r#"{"status":"open"}"#),
        r#"{"message":"ok"}"#,
    ])
    .await;
    e.send_tools("filter", Context::default(), vec![filter_tool()])
        .await
        .unwrap();
    for m in e.model.0.lock().metas.iter() {
        assert_eq!(m.source, llm::SOURCE_ASSISTANT, "{m:?}");
    }
}

#[test]
fn tools_from_the_protocol_message() {
    let items = pb::UiParam {
        r#type: "enum".into(),
        r#enum: vec![sv("u"), sv("v")],
        ..Default::default()
    };
    let a = pb::UiParam {
        r#type: "array".into(),
        items: Some(Box::new(items)),
        ..Default::default()
    };
    let t = pb::UiTool {
        name: "x".into(),
        description: "d".into(),
        level: "write".into(),
        target: "t".into(),
        args: Some(pb::UiArgs {
            required: vec![sv("a")],
            properties: [(sv("a"), a)].into_iter().collect(),
        }),
        ..Default::default()
    };
    let got = tool_from_pb(&t);
    assert!(
        got.name == "x"
            && got.args.properties["a"]
                .items
                .as_ref()
                .is_some_and(|i| i.r#enum[1] == "v"),
        "{got:?}"
    );
    check_tools(&[got]).expect("valid");
}
