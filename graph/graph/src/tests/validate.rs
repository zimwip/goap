//! Port of `validate_test.go`: the consistency of the blackboard as a process sees it. (The flow view of a relaunched
//! run is a later slice's: flows.)

use std::collections::BTreeMap;

use goap_domain::{
    BoardIssue, ChangeID, ChangeImpact, ChangeImpactID, ChangeItem, Decision, IMPACT_PROPOSED,
    INTENT_MODIFIED, ISSUE_WARNING, ImpactEvent, ItemID, NodeRef, TYPE_ARTIFACT, TYPE_DECISION,
};

use super::graph::new_change;
use crate::Graph;
use crate::testsupport::*;

fn by_code(is: &[BoardIssue]) -> BTreeMap<String, Vec<BoardIssue>> {
    let mut out: BTreeMap<String, Vec<BoardIssue>> = BTreeMap::new();
    for i in is {
        out.entry(i.code.clone()).or_default().push(i.clone());
    }
    out
}

fn note(id: &str, derived: &[&str]) -> ChangeItem {
    ChangeItem {
        id: ItemID::new(id),
        r#type: TYPE_ARTIFACT,
        target: "note".into(),
        derived_from: derived.iter().map(|d| ItemID::new(*d)).collect(),
        ..Default::default()
    }
}

async fn issues(g: &Graph, id: &ChangeID) -> Vec<BoardIssue> {
    must(g.validate_board(&ctx(), id, "").await)
}

#[tokio::test]
async fn validate_board() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let ch = must(g.create_change(&c, new_change(&f.base, "v")).await);
        must(
            g.add_items(&c, &ch.id, vec![note("p1", &[]), note("p2", &["p1"])])
                .await,
        );
        assert!(issues(g, &ch.id).await.is_empty(), "clean board");
        // a decision rejects p1: p2 builds on an invalid item, the culprit is p1
        must(
            g.add_items(
                &c,
                &ch.id,
                vec![ChangeItem {
                    id: ItemID::new("d1"),
                    r#type: TYPE_DECISION,
                    decision: Some(Decision {
                        item: ItemID::new("p1"),
                        accept: false,
                        ..Default::default()
                    }),
                    ..Default::default()
                }],
            )
            .await,
        );
        let is = issues(g, &ch.id).await;
        let got = by_code(&is)
            .remove("derived_from_invalid")
            .unwrap_or_default();
        assert!(
            got.len() == 1 && got[0].item.as_str() == "p2" && got[0].culprit.as_str() == "p1",
            "issues = {is:?}"
        );
        // dangling provenance
        must(
            g.add_items(
                &c,
                &ch.id,
                vec![note("n1", &["ghost"]), note("n2", &["ghost"])],
            )
            .await,
        );
        let by = by_code(&issues(g, &ch.id).await);
        assert_eq!(
            by.get("dangling").map(Vec::len),
            Some(2),
            "dangling = {by:?}"
        );
    })
    .await;
}

// An impact written against a version that is no longer the head is outdated (a warning: it is re-checked); one whose
// pre version is not in the reference baseline is an error.
#[tokio::test]
async fn validate_board_impacts() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let ch = must(g.create_change(&c, new_change(&f.base, "b")).await);
        let impact = |pre: NodeRef, recheck: bool| {
            let id = ChangeImpactID::new(g.new_id());
            ImpactEvent {
                change: ch.id.clone(),
                impact: id.clone(),
                op: IMPACT_PROPOSED,
                state: Some(ChangeImpact {
                    id,
                    key: "REQ-1".into(),
                    r#type: "Requirement".into(),
                    intent: INTENT_MODIFIED,
                    rationale: "from b".into(),
                    pre: Some(pre),
                    recheck,
                    created_at: g.now(),
                    ..Default::default()
                }),
                ..Default::default()
            }
        };
        let outdated = impact(f.req.r#ref(), true);
        let wrong = impact(
            NodeRef {
                id: f.test.id.clone(),
                version: 9,
            },
            false,
        );
        let (oid, wid) = (outdated.impact.clone(), wrong.impact.clone());
        let mut tx = must(g.change_tx(&c, &ch.id).await);
        must(g.emit(&c, &mut tx, vec![outdated, wrong]).await);
        must(tx.commit(&c).await);
        let by = by_code(&issues(g, &ch.id).await);
        let o = &by["outdated"];
        assert!(
            o.len() == 1
                && o[0].item.as_str() == oid.as_str()
                && o[0].culprit.as_str() == oid.as_str()
                && o[0].severity == ISSUE_WARNING,
            "outdated = {by:?}"
        );
        let w = &by["reference"];
        assert!(
            w.len() == 1 && w[0].item.as_str() == wid.as_str(),
            "reference = {by:?}"
        );
    })
    .await;
}
