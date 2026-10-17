//! The parts of `graph_test.go` that are G1's: the validation of the items added to a change, and the change headers
//! (creation, patch, listing, the changes acting on a node). The apply tests are a later slice's.

use goap_domain::{
    CHANGE_ABANDONED, CHANGE_ACTIVE, CHANGE_APPLIED, ChangeImpact, ChangeImpactID, ChangeItem,
    Decision, IMPACT_PROPOSED, INTENT_MODIFIED, ImpactEvent, ItemType, LINK_CONTAINS,
    TYPE_DECISION,
};
use goap_graphapi::{ChangePatch, ChangesFilter};

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
