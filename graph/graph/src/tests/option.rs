//! Port of `option_test.go`: options (ADR 0009 §3, ADR 0032 §6): two hypotheses explored at once on the same node, the
//! change working on the active one, compared, then one selected.

use goap_domain::*;
use goap_graphapi::OpenOptionRequest;
use serde_json::Value;

use crate::changeview::{VIEW_ACCEPTED, VIEW_WRITTEN};
use crate::tests::draft::title;
use crate::tests::helpers::*;
use crate::testsupport::*;
use crate::{ErrorKind, NewChange};

async fn own_change(f: &Fixture) -> Change {
    must(
        f.g.create_change(
            &ctx(),
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "PSP".into(),
                baseline_id: f.base.id.clone(),
                own_branch: true,
                ..Default::default()
            },
        )
        .await,
    )
}

// Two hypotheses explored at once on the same node, the change working on the active one, compared, then one
// selected: its draft becomes the change's, the other is rejected.
#[tokio::test]
async fn options() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = own_change(&f).await;
        let pre = f.req.r#ref();

        let a = must(
            g.open_option(
                &ctx,
                &c.id,
                OpenOptionRequest {
                    name: "stripe".into(),
                    hypothesis: "Stripe covers every market".into(),
                    activate: true,
                    by: "u".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert!(
            a.option.is_some() && a.active && a.option_status() == OPTION_EXPLORING,
            "option A: {a:?}"
        );
        assert_kind(
            g.open_option(
                &ctx,
                &c.id,
                OpenOptionRequest {
                    name: "Stripe".into(),
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::Conflict,
            "two open options with one name",
        );
        // with A active, a call that names no flow works on A
        let write = async |t: &str| -> ChangeImpact {
            let added = must(
                g.propose_impact(
                    &ctx,
                    &c.id,
                    vec![ChangeImpact {
                        intent: INTENT_MODIFIED,
                        pre: Some(pre.clone()),
                        rationale: t.into(),
                        ..Default::default()
                    }],
                )
                .await,
            );
            must(
                edit(
                    g,
                    &c.id,
                    &added[0].id,
                    Edit {
                        properties: title(t),
                        ..Default::default()
                    },
                )
                .await,
            )
        };
        let ia = write("Use Stripe").await;
        assert_eq!(ia.flow, a.id, "declared on the active option");
        let b = must(
            g.open_option(
                &ctx,
                &c.id,
                OpenOptionRequest {
                    name: "adyen".into(),
                    hypothesis: "Adyen is cheaper".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        let active = must(g.activate_option(&ctx, &c.id, &b.id, "u").await);
        assert_eq!(active, b.id, "active");
        let ib = write("Use Adyen").await;
        assert_eq!(ib.flow, b.id, "declared on option B");
        // the blackboard of the change is the active option's; "main" names the main flow
        let bb = must(g.blackboard(&ctx, &c.id).await);
        assert!(
            bb.change.nodes.len() == 1 && bb.change.nodes[0].id == ib.id,
            "blackboard on the active option: {:?}",
            bb.change.nodes
        );
        let bb = must(g.blackboard_in(&ctx, &c.id, MAIN_FLOW).await);
        assert!(
            bb.change.nodes.is_empty(),
            "main flow: {:?}",
            bb.change.nodes
        );
        // the graph the change reads is the active option's
        let (nodes, _) = must(g.change_graph(&ctx, &c.id, "").await);
        let t = nodes
            .iter()
            .find(|n| n.id == pre.id)
            .and_then(|n| n.properties.get("title").cloned());
        assert_eq!(
            t,
            Some(Value::from("Use Adyen")),
            "change graph on option B: REQ-1 title"
        );
        // two versions of REQ-1 at once, each on its option, both children of v1
        let cmp = must(g.compare_options(&ctx, &c.id, VIEW_WRITTEN, false).await);
        assert!(
            cmp.options.len() == 2
                && cmp.nodes.len() == 1
                && cmp.nodes[0].key == "REQ-1"
                && cmp.nodes[0].main.as_ref() == Some(&pre),
            "comparison: {cmp:?}"
        );
        let n0 = &cmp.nodes[0];
        let va = n0.options.get(&a.id).cloned().flatten();
        let vb = n0.options.get(&b.id).cloned().flatten();
        let prop = |id: &str| n0.props.get(id).and_then(|p| p.get("title")).cloned();
        assert!(
            va.as_ref().is_some_and(|v| v.is_draft())
                && vb.as_ref().is_some_and(|v| v.is_draft())
                && prop(&a.id) == Some(Value::from("Use Stripe"))
                && prop(&b.id) == Some(Value::from("Use Adyen")),
            "option versions: {n0:?}"
        );
        // nothing is accepted yet
        let acc = must(g.compare_options(&ctx, &c.id, VIEW_ACCEPTED, false).await);
        assert!(acc.nodes.is_empty(), "accepted comparison: {:?}", acc.nodes);
        assert_kind(
            apply(g, &c.id, "").await,
            ErrorKind::Conflict,
            "apply with open options",
        );
        // the decision: A is evaluated, accepted on its own flow and selected
        must(
            g.evaluate_option(&ctx, &c.id, &a.id, "u", "covers every market")
                .await,
        );
        must(g.activate_option(&ctx, &c.id, &a.id, "u").await);
        must(accept(g, &c.id, &ia.id, "u", "ok").await);
        let sel = must(g.select_option(&ctx, &c.id, &a.id, "u").await);
        assert!(
            sel.option_status() == OPTION_SELECTED && !sel.active,
            "selected: {sel:?}"
        );
        let opts = must(g.options(&ctx, &c.id).await);
        assert!(
            opts.len() == 2 && opts[1].option_status() == OPTION_REJECTED,
            "options: {opts:?}"
        );
        // option A's draft is the change's now: no version was written for the options
        let cur = must(g.change(&ctx, &c.id).await);
        assert_eq!(
            cur.active_option(),
            "",
            "no option is active after the decision"
        );
        assert_eq!(
            version_count(g, &pre.id).await,
            pre.version as usize,
            "no option wrote a version"
        );
        let res = must(apply(g, &c.id, "").await);
        let head = must(
            g.node(
                &ctx,
                &NodeRef {
                    id: pre.id.clone(),
                    version: 0,
                },
            )
            .await,
        );
        assert!(
            res.nodes.get(&pre.id) == Some(&head.version)
                && head.version == pre.version + 1
                && head.properties.get("title") == Some(&Value::from("Use Stripe")),
            "the selected option lands as one version: {:?}, main {head:?}",
            res.nodes
        );
    })
    .await;
}

// Option intent (derive/revise/refine): refine is a sub-branch of another open option, not of the main flow.
#[tokio::test]
async fn option_refine() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = own_change(&f).await;
        let req = |name: &str, parent: &str, intent: OptionIntent| OpenOptionRequest {
            name: name.into(),
            parent: parent.into(),
            intent,
            by: "u".into(),
            ..Default::default()
        };

        assert_kind(
            g.open_option(&ctx, &c.id, req("stripe", "", OptionIntent::from("bogus")))
                .await,
            ErrorKind::Invalid,
            "unknown intent",
        );
        let a = must(
            g.open_option(
                &ctx,
                &c.id,
                OpenOptionRequest {
                    hypothesis: "Stripe covers every market".into(),
                    ..req("stripe", "", INTENT_DERIVE)
                },
            )
            .await,
        );
        assert!(
            a.option.as_ref().is_some_and(|o| o.intent == INTENT_DERIVE) && a.parent.is_empty(),
            "derive option: {a:?}"
        );

        // refine: a sub-branch of the open option A, not of main
        let sub = must(
            g.open_option(
                &ctx,
                &c.id,
                OpenOptionRequest {
                    hypothesis: "Stripe, EU entities only".into(),
                    ..req("stripe-eu", &a.id, INTENT_REFINE)
                },
            )
            .await,
        );
        assert!(
            sub.parent == a.id
                && sub
                    .option
                    .as_ref()
                    .is_some_and(|o| o.intent == INTENT_REFINE),
            "refine option: {sub:?}"
        );

        // a parent that isn't an open option of this change is refused
        assert_kind(
            g.open_option(
                &ctx,
                &c.id,
                req("other", "no-such-flow", OptionIntent::default()),
            )
            .await,
            ErrorKind::NotFound,
            "unknown parent",
        );
        must(g.reject_option(&ctx, &c.id, &a.id, "u").await);
        assert_kind(
            g.open_option(&ctx, &c.id, req("other", &a.id, OptionIntent::default()))
                .await,
            ErrorKind::Conflict,
            "discarded parent",
        );
    })
    .await;
}
