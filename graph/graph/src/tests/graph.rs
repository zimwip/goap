//! The parts of `graph_test.go` that are G1's: the validation of the items added to a change, and the change headers
//! (creation, patch, listing, the changes acting on a node), and the apply.

use goap_domain::{
    CHANGE_ABANDONED, CHANGE_ACTIVE, CHANGE_APPLIED, ChangeImpact, ChangeImpactID, ChangeItem,
    Decision, IMPACT_PROPOSED, INTENT_MODIFIED, ImpactEvent, ItemType, LINK_CONTAINS,
    TYPE_DECISION,
};
use goap_graphapi::{ChangePatch, ChangesFilter};

use crate::tests::helpers::*;
use crate::testsupport::*;
use crate::{ErrorKind, Graph, NewChange};

pub(super) fn new_change(base: &goap_domain::Baseline, title: &str) -> NewChange {
    NewChange {
        project_id: "PROJ-ROOT".into(),
        title: title.into(),
        baseline_id: base.id.clone(),
        ..Default::default()
    }
}

/// writeOrgUnit lands an organisational unit under a parent (the commit of a later slice checks the parenting).
pub(super) async fn write_org_unit(g: &Graph, key: &str, parent: &str) {
    must(
        land(
            g,
            NAMESPACE_ORGANISATION,
            &[Landing {
                links: vec![(LINK_PART_OF.into(), parent.into())],
                ..landing(key, NODE_TYPE_ORG_UNIT)
            }],
        )
        .await,
    );
}

#[tokio::test]
async fn add_items_validation() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let f = new_fixture(r).await;
        let ch = must(f.g.create_change(&c, new_change(&f.base, "c")).await);
        assert_kind(
            f.g.add_items(
                &c,
                &ch.id,
                vec![ChangeItem {
                    r#type: ItemType::new("impact"),
                    ..Default::default()
                }],
            )
            .await,
            ErrorKind::Invalid,
            "the impact kind is gone",
        );
        assert_kind(
            f.g.add_items(
                &c,
                &ch.id,
                vec![ChangeItem {
                    r#type: TYPE_DECISION,
                    decision: Some(Decision {
                        item: "nope".into(),
                        ..Default::default()
                    }),
                    ..Default::default()
                }],
            )
            .await,
            ErrorKind::Invalid,
            "a decision on no item",
        );
        // the items get their ids and land in the log of the change
        let out = must(
            f.g.add_items(
                &c,
                &ch.id,
                vec![ChangeItem {
                    r#type: goap_domain::TYPE_ARTIFACT,
                    target: "note".into(),
                    ..Default::default()
                }],
            )
            .await,
        );
        assert!(!out[0].id.is_empty());
        let got = must(f.g.change(&c, &ch.id).await);
        assert_eq!(got.items.len(), 1);
        assert_eq!(got.items[0].id, out[0].id);
        // an applied change takes no more items
        let applied = must(f.g.change(&c, &f.base.change_id).await);
        assert_eq!(applied.status, CHANGE_APPLIED);
        assert_kind(
            f.g.add_items(
                &c,
                &applied.id,
                vec![ChangeItem {
                    r#type: goap_domain::TYPE_ARTIFACT,
                    target: "note".into(),
                    ..Default::default()
                }],
            )
            .await,
            ErrorKind::Conflict,
            "an applied change",
        );
    })
    .await;
}

#[tokio::test]
async fn change_headers() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        assert_kind(
            g.create_change(
                &c,
                NewChange {
                    baseline_id: goap_domain::BaselineID::new(uuid::Uuid::new_v4().to_string()),
                    ..new_change(&f.base, "x")
                },
            )
            .await,
            ErrorKind::NotFound,
            "an unknown baseline",
        );
        let ch = must(g.create_change(&c, new_change(&f.base, "first")).await);
        assert_eq!(
            ch.status,
            goap_domain::CHANGE_DRAFT,
            "a change starts as a draft"
        );
        assert_eq!(
            ch.owner_org,
            root_org(g),
            "the owner defaults to the root unit"
        );
        assert_eq!(ch.baseline_id, f.base.id);
        let title = "renamed".to_string();
        let up = must(
            g.update_change(
                &c,
                &ch.id,
                &ChangePatch {
                    title: Some(title.clone()),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(up.title, title);
        assert_eq!(must(g.change(&c, &ch.id).await).title, title);
        // a change abandoned cannot be taken back
        must(
            g.update_change(
                &c,
                &ch.id,
                &ChangePatch {
                    status: Some(CHANGE_ABANDONED),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_kind(
            g.update_change(
                &c,
                &ch.id,
                &ChangePatch {
                    status: Some(CHANGE_ACTIVE),
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::Conflict,
            "abandoned to active",
        );
        let abandoned = must(
            g.list_changes(
                &c,
                &ChangesFilter {
                    status: vec![CHANGE_ABANDONED],
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(abandoned.len(), 1);
        assert_eq!(abandoned[0].id, ch.id);
        let org = must(
            g.list_changes(
                &c,
                &ChangesFilter {
                    namespace: NAMESPACE_ORGANISATION.into(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert!(org.iter().all(|x| x.namespace == NAMESPACE_ORGANISATION) && !org.is_empty());
    })
    .await;
}

// The changes acting on a node: those that landed it (their impacts name it) and the open ones holding an impact
// on it, oldest first.
#[tokio::test]
async fn node_changes() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let ch = must(g.create_change(&c, new_change(&f.base, "open")).await);
        let mut tx = must(g.change_tx(&c, &ch.id).await);
        let id = ChangeImpactID::new(g.new_id());
        must(
            g.emit(
                &c,
                &mut tx,
                vec![ImpactEvent {
                    change: ch.id.clone(),
                    impact: id.clone(),
                    op: IMPACT_PROPOSED,
                    state: Some(ChangeImpact {
                        id,
                        key: f.req.key.clone(),
                        r#type: f.req.r#type.clone(),
                        intent: INTENT_MODIFIED,
                        rationale: "x".into(),
                        pre: Some(f.req.r#ref()),
                        created_at: g.now(),
                        ..Default::default()
                    }),
                    ..Default::default()
                }],
            )
            .await,
        );
        must(tx.commit(&c).await);
        let got = must(g.node_changes(&c, &f.req.id).await);
        let ids: Vec<_> = got.iter().map(|x| x.id.clone()).collect();
        assert_eq!(ids, vec![f.req.change_id.clone(), ch.id.clone()]);
        assert!(got.iter().all(|x| x.items.is_empty() && x.nodes.is_empty()));
        let refs = must(g.change_impacts(&c, &ch.id).await);
        assert_eq!(refs, vec![f.req.r#ref()]);
        // the neighbourhood of a version
        let v = must(g.view(&c, &f.req.r#ref()).await);
        assert_eq!(v.node.key, "REQ-1");
        let _ = LINK_CONTAINS;
    })
    .await;
}

// ---- apply ----------------------------------------------------------------------------------------------------

#[tokio::test]
async fn apply_update_creates_suspect_links() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(g.create_change(&ctx, new_change(&f.base, "PSP v2")).await);
        let req_ref = f.req.r#ref();
        let cns = must(
            propose_or_create(
                g,
                &c.id,
                vec![
                    ChangeImpact {
                        intent: INTENT_MODIFIED,
                        pre: Some(req_ref.clone()),
                        rationale: "PSP v2".into(),
                        ..Default::default()
                    },
                    ChangeImpact {
                        intent: goap_domain::INTENT_CREATED,
                        key: "TST-2".into(),
                        r#type: "TestCase".into(),
                        rationale: "cover REQ-1".into(),
                        ..Default::default()
                    },
                ],
            )
            .await,
        );
        must(
            edit(
                g,
                &c.id,
                &cns[0].id,
                Edit {
                    properties: props(&[("title", serde_json::json!("Use PSP v2"))]),
                    ..Default::default()
                },
            )
            .await,
        );
        must(
            edit(
                g,
                &c.id,
                &cns[1].id,
                Edit {
                    add_links: vec![goap_graphapi::LinkWrite {
                        r#type: "verifies".into(),
                        to: req_ref.clone(),
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            )
            .await,
        );
        for n in &cns {
            must(accept(g, &c.id, &n.id, "u", "ok").await);
        }

        let bb = must(g.blackboard(&ctx, &c.id).await);
        let v = &bb.nodes[&req_ref];
        assert!(
            v.key == "REQ-1" && v.r#in.len() == 1 && v.out.len() == 1,
            "unexpected hydration: {v:?}"
        );

        let b2 = must(apply(g, &c.id, "").await);
        assert_eq!(
            b2.nodes.get(&f.req.id),
            Some(&2),
            "REQ-1 should be at v2 in the target baseline"
        );
        assert!(
            b2.nodes.get(&f.need.id) == Some(&1) && b2.nodes.get(&f.test.id) == Some(&1),
            "untouched nodes must keep their version: {:?}",
            b2.nodes
        );
        let req2 = must(
            g.node(
                &ctx,
                &goap_domain::NodeRef {
                    id: f.req.id.clone(),
                    version: 0,
                },
            )
            .await,
        );
        assert_eq!(req2.properties["title"], "Use PSP v2", "props not updated");

        let (nodes, links) = must(g.baseline_graph(&ctx, &b2.id).await);
        assert_eq!(nodes.len(), 4, "expected 4 nodes");
        // carried: REQ-1@v2 satisfies NEED-1@v1 ; added: TST-2@v1 verifies REQ-1@v2
        assert_eq!(links.len(), 2, "expected 2 links in B2, got {links:?}");
        let suspects = must(g.suspect_links(&ctx, &b2.id).await);
        assert!(
            suspects.len() == 1 && suspects[0].from == f.test.r#ref(),
            "TST-1 verifies REQ-1@v1 must be suspect, got {suspects:?}"
        );
        // The reference baseline is untouched.
        let (_, links1) = must(g.baseline_graph(&ctx, &f.base.id).await);
        assert_eq!(links1.len(), 2, "reference baseline changed: {links1:?}");
        let sus1 = must(g.suspect_links(&ctx, &f.base.id).await);
        assert!(sus1.is_empty(), "reference baseline has suspects: {sus1:?}");
    })
    .await;
}

#[tokio::test]
async fn apply_remove_link_bumps_source() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let v = must(g.view(&ctx, &f.test.r#ref()).await);
        let c = must(
            g.create_change(&ctx, new_change(&f.base, "drop test"))
                .await,
        );
        let ns = must(
            g.propose_impact(
                &ctx,
                &c.id,
                vec![ChangeImpact {
                    intent: INTENT_MODIFIED,
                    pre: Some(f.test.r#ref()),
                    rationale: "the test no longer verifies".into(),
                    ..Default::default()
                }],
            )
            .await,
        );
        must(
            edit(
                g,
                &c.id,
                &ns[0].id,
                Edit {
                    remove_links: vec![v.out[0].id.clone()],
                    ..Default::default()
                },
            )
            .await,
        );
        must(accept(g, &c.id, &ns[0].id, "u", "ok").await);
        let b2 = must(apply(g, &c.id, "B2").await);
        assert_eq!(b2.nodes.get(&f.test.id), Some(&2), "TST-1 should be bumped");
        let (_, links) = must(g.baseline_graph(&ctx, &b2.id).await);
        assert!(
            links.len() == 1 && links[0].r#type == "satisfies",
            "unexpected links {links:?}"
        );
    })
    .await;
}

#[tokio::test]
async fn apply_rejected_and_conflicts() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let req_ref = f.req.r#ref();
        // each change writes the same property with its own value, and accepts (or rejects) it
        let change = async |title: &str, value: i64, ok: bool| {
            let c = must(g.create_change(&ctx, new_change(&f.base, title)).await);
            let ns = must(
                g.propose_impact(
                    &ctx,
                    &c.id,
                    vec![ChangeImpact {
                        intent: INTENT_MODIFIED,
                        pre: Some(req_ref.clone()),
                        rationale: title.into(),
                        ..Default::default()
                    }],
                )
                .await,
            );
            must(
                edit(
                    g,
                    &c.id,
                    &ns[0].id,
                    Edit {
                        properties: props(&[("x", serde_json::json!(value))]),
                        ..Default::default()
                    },
                )
                .await,
            );
            let status = if ok {
                goap_domain::REVIEW_ACCEPTED
            } else {
                goap_domain::REVIEW_REJECTED
            };
            must(
                g.impact_node_review(&ctx, &c.id, &ns[0].id, status, "u", "decided")
                    .await,
            );
            c
        };
        let c1 = change("c1", 1, false).await;
        let c2 = change("c2", 1, true).await;
        // the only change impact of c1 is rejected: reworked or withdrawn before anything merges (ADR 0102)
        assert_kind(
            apply(g, &c1.id, "").await,
            ErrorKind::Conflict,
            "rejected change impact applied",
        );
        must(apply(g, &c2.id, "").await);
        // a third change from B1 writes another value of the same property: the apply rebases it and stops on the
        // conflict
        let c3 = change("c3", 2, true).await;
        assert_kind(
            apply(g, &c3.id, "").await,
            ErrorKind::Conflict,
            "expected a conflict",
        );
        let c3b = must(g.change(&ctx, &c3.id).await);
        assert_eq!(c3b.status, CHANGE_ACTIVE, "expected active");
    })
    .await;
}
