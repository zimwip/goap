mod common;

use std::sync::Arc;

use common::{by, code, rpc, serve, subject, typed_graph};
use goap_access as access;
use goap_connect::{Code, Interceptors, Request};
use goap_ctx::Ctx;
use goap_domain::{MAIN_BRANCH, Node};
use goap_gen::graph::v1::{self as pb, GraphService};
use goap_graphsvc::Handler;
use goap_pbconv as pbconv;
use serde_json::{Value, json};

const NS: &str = "platform";
const TYP: &str = "platform@LlmProvider";

fn edit(key: &str, props: serde_json::Map<String, Value>) -> pb::SettingEdit {
    pb::SettingEdit {
        namespace: NS.into(),
        r#type: TYP.into(),
        key: key.into(),
        props: pbconv::to_struct(&props),
        ..Default::default()
    }
}

fn retire(key: &str) -> pb::SettingEdit {
    pb::SettingEdit {
        namespace: NS.into(),
        r#type: TYP.into(),
        key: key.into(),
        retire: true,
        ..Default::default()
    }
}

fn staged<'a>(p: &'a pb::PendingSettingsResponse, key: &str) -> Option<&'a pb::StagedSetting> {
    p.changes
        .iter()
        .flat_map(|c| c.nodes.iter())
        .find(|n| n.key == key)
}

fn prop(s: &pb::StagedSetting, k: &str) -> Option<Value> {
    pbconv::to_map(s.props.as_ref()).get(k).cloned()
}

/// The settings dialog stages its edits through the server (ADR 0122): one call per edit or batch of edits, the
/// pending state of the caller in every answer, Save and Discard in one call each.
#[tokio::test]
async fn staged_settings() {
    let ctx = &Ctx::background();
    let g = Arc::new(typed_graph());
    g.bootstrap(ctx).await.unwrap();
    let h = Handler::new(g.clone());
    let interceptors = Interceptors::new(vec![Arc::new(h.personal_scope())]);
    let cl = rpc(&serve(h, interceptors).await);

    let stage = async |s: &str, edits: Vec<pb::SettingEdit>| {
        Box::pin(cl.stage_settings(ctx, by(pb::StageSettingsRequest { edits }, &subject(s))))
            .await
            .unwrap()
            .msg
    };
    let pending_of = async |s: &str| {
        cl.get_pending_settings(
            ctx,
            by(pb::GetPendingSettingsRequest::default(), &subject(s)),
        )
        .await
        .unwrap()
        .msg
    };
    let save = async |s: &str| {
        Box::pin(cl.save_settings(ctx, by(pb::SaveSettingsRequest::default(), &subject(s)))).await
    };
    let on_main =
        async |key: &str| -> Node { g.node_by_key_on(ctx, NS, MAIN_BRANCH, key).await.unwrap() };

    // a creation then an edit of the same node, in one call: one change, one created node with both edits
    let p = stage(
        "alice",
        vec![
            edit(
                "LLP:p1",
                obj! {"name" => "p1", "kind" => "openai", "protocol" => "openai"},
            ),
            edit("LLP:p1", obj! {"enabled" => false}),
        ],
    )
    .await;
    assert!(
        p.changes.len() == 1 && p.changes[0].nodes.len() == 1,
        "pending = {p:?}"
    );
    let s = staged(&p, "LLP:p1");
    assert!(
        matches!(s, Some(s) if s.created && s.written && prop(s, "enabled") == Some(json!(false)) && prop(s, "name") == Some(json!("p1"))),
        "staged = {s:?}"
    );
    // nobody else sees it
    let p = pending_of("bob").await;
    assert!(p.changes.is_empty(), "bob sees {p:?}");
    let p = pending_of("alice").await;
    assert!(staged(&p, "LLP:p1").is_some(), "alice reads back {p:?}");

    let out = save("alice").await;
    assert!(
        matches!(&out, Ok(o) if o.msg.changes.is_empty()),
        "save = {out:?}"
    );
    let n = on_main("LLP:p1").await;
    assert!(
        n.properties.get("enabled") == Some(&json!(false)) && n.state == "active",
        "on main = {n:?}"
    );

    // a retirement, then an edit restores the node
    let p = stage("alice", vec![retire("LLP:p1")]).await;
    let s = staged(&p, "LLP:p1");
    assert!(
        matches!(s, Some(s) if s.retire && !s.created),
        "retired = {s:?}"
    );
    let p = stage("alice", vec![edit("LLP:p1", obj! {"enabled" => true})]).await;
    let s = staged(&p, "LLP:p1");
    assert!(
        matches!(s, Some(s) if !s.retire && prop(s, "enabled") == Some(json!(true))),
        "restored = {s:?}"
    );
    // a node created then retired is given up, the rest stays
    let p = stage(
        "alice",
        vec![edit("LLP:p2", obj! {"name" => "p2"}), retire("LLP:p2")],
    )
    .await;
    assert!(
        staged(&p, "LLP:p2").is_none() && staged(&p, "LLP:p1").is_some(),
        "pending = {p:?}"
    );

    // discard: nothing pending, main untouched
    let out = Box::pin(cl.discard_settings(
        ctx,
        by(pb::DiscardSettingsRequest::default(), &subject("alice")),
    ))
    .await;
    assert!(
        matches!(&out, Ok(o) if o.msg.changes.is_empty()),
        "discard = {out:?}"
    );
    let n = on_main("LLP:p1").await;
    assert!(
        n.properties.get("enabled") == Some(&json!(false)) && n.state != access::STATE_RETIRED,
        "on main after discard = {n:?}"
    );

    // an edit put back as it was is not saved: the change goes, landing nothing
    stage("alice", vec![edit("LLP:p1", obj! {"enabled" => true})]).await;
    stage("alice", vec![edit("LLP:p1", obj! {"enabled" => false})]).await;
    let before = g.branch_head(ctx, NS, MAIN_BRANCH).await.unwrap();
    let out = save("alice").await;
    assert!(
        matches!(&out, Ok(o) if o.msg.changes.is_empty()),
        "save of nothing = {out:?}"
    );
    let after = g.branch_head(ctx, NS, MAIN_BRANCH).await.unwrap();
    assert_eq!(after.id, before.id, "main moved");

    // an anonymous caller has no settings
    let r = cl
        .get_pending_settings(ctx, Request::new(pb::GetPendingSettingsRequest::default()))
        .await;
    assert_eq!(code(&r), Some(Code::Unauthenticated), "anonymous: {r:?}");
}
