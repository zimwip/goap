//! Only a modification is accepted and committed; a committed change is
//! iterated on.

use goap_domain::*;
use goap_graphapi::{NodeCheckout, NodeCreate, NodeUpdate};

use crate::ErrorKind;
use crate::flownodes::node_of;
use crate::tests::changeimpact::modified;
use crate::tests::draft::{own_change, title};
use crate::tests::helpers::*;
use crate::testsupport::*;

fn design(key: &str) -> NodeCreate {
    NodeCreate {
        key: key.into(),
        r#type: "Design".into(),
        rationale: "new".into(),
        ..Default::default()
    }
}

// ADR 0102: only a modification is accepted and committed. A candidate (an impact never checked out) and a draft left
// as it was checked out are refused at review, and a commit with nothing that modifies a node is refused.
#[tokio::test]
async fn only_modifications_are_accepted_and_committed() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(g.create_change(&ctx, own_change(&f.base, "touch")).await);
        let cand = must(
            g.propose_impact(&ctx, &c.id, vec![modified(&f.req, "maybe")])
                .await,
        )
        .remove(0);
        let review = async |id: &ChangeImpactID| {
            g.impact_node_review(&ctx, &c.id, id, REVIEW_ACCEPTED, "bob", "ok")
                .await
        };
        assert_kind(
            review(&cand.id).await,
            ErrorKind::Invalid,
            "a candidate is not accepted",
        );
        let err = commit_change(g, &c.id, "").await.unwrap_err();
        assert!(err.is_invalid(), "nothing to commit: {err}");
        must(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    impact: cand.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_kind(
            review(&cand.id).await,
            ErrorKind::Invalid,
            "an unchanged draft is not accepted",
        );
        let err = commit_change(g, &c.id, "").await.unwrap_err();
        assert!(
            err.is_invalid(),
            "an unchanged draft is nothing to commit: {err}"
        );
        // a candidate left as it is does not hold the commit of what is modified
        must(g.impact_node_create(&ctx, &c.id, design("D-9")).await);
        for cn in must(g.list_change_impacts(&ctx, &c.id).await) {
            if cn.key == "D-9" {
                must(review(&cn.id).await);
            }
        }
        let b = must(commit_change(g, &c.id, "").await);
        assert_eq!(
            b.nodes.get(&f.req.id),
            Some(&f.req.version),
            "REQ-1 was not modified: no version of it is written"
        );
        let ch = must(g.change(&ctx, &c.id).await);
        assert!(
            ch.nodes[0].review == REVIEW_PROPOSED && ch.status == CHANGE_ACTIVE,
            "the candidate stays in the open change: {ch:?}"
        );
    })
    .await;
}

// ADR 0102: a committed change is iterated on: its nodes are checked out again from the versions it committed, edited
// and committed again; it is merged once, which closes it.
#[tokio::test]
async fn iterate_on_the_branch_of_a_change() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(g.create_change(&ctx, own_change(&f.base, "iterate")).await);
        assert_kind(
            integrate_change(g, &c.id).await,
            ErrorKind::NothingToCommit,
            "nothing committed: nothing to merge",
        );
        let req = must(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    node: f.req.id.clone(),
                    rationale: "v2".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        let update = async |t: &str| {
            must(
                g.impact_node_update(
                    &ctx,
                    &c.id,
                    &req.id,
                    NodeUpdate {
                        properties: title(t),
                        ..Default::default()
                    },
                )
                .await,
            )
        };
        let review = async |id: &ChangeImpactID| {
            must(
                g.impact_node_review(&ctx, &c.id, id, REVIEW_ACCEPTED, "bob", "ok")
                    .await,
            )
        };
        let checkout = async |id: &ChangeImpactID| {
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    impact: id.clone(),
                    ..Default::default()
                },
            )
            .await
        };
        update("one").await;
        review(&req.id).await;
        let created = must(g.impact_node_create(&ctx, &c.id, design("D-1")).await);
        review(&created.id).await;
        let first = must(commit_change(g, &c.id, "").await);
        let v1 = first.nodes[&f.req.id];

        // edited again after the commit: checked out from the version the commit wrote
        must(checkout(&req.id).await);
        let d = must(draft_of(g, &c.id, "", &req.id).await);
        assert!(
            d.as_ref()
                .is_some_and(|d| d.base.as_ref().is_some_and(|b| b.version == v1)
                    && d.properties["title"] == "one"),
            "the draft starts from the committed version: {d:?}"
        );
        update("two").await;
        assert_kind(
            integrate_change(g, &c.id).await,
            ErrorKind::Conflict,
            "an uncommitted modification holds the merge",
        );
        review(&req.id).await;
        // a node it created is checked out again too, then left as it was
        must(checkout(&created.id).await);
        let second = must(commit_change(g, &c.id, "").await);
        let v2 = second.nodes[&f.req.id];
        assert!(
            second.parent_id == first.id && v2 != v1,
            "the second commit follows the first: {second:?}"
        );
        let n = must(
            g.node(
                &ctx,
                &NodeRef {
                    id: f.req.id.clone(),
                    version: v2,
                },
            )
            .await,
        );
        assert!(
            n.properties["title"] == "two" && n.parents == vec![v1],
            "the version of the second commit derives from the first: {n:?}"
        );
        must(g.impact_node_cancel(&ctx, &c.id, &created.id, "", "").await);
        let ch = must(integrate_change(g, &c.id).await);
        assert_eq!(ch.status, CHANGE_APPLIED, "merge");
        let head = must(g.baseline(&ctx, &ch.result_baseline_id).await);
        assert!(
            head.kind == BASELINE_FAST_FORWARD
                && head.nodes.get(&f.req.id) == Some(&v2)
                && head.nodes.get(&node_of(&created)).is_some_and(|v| *v != 0),
            "main fast-forwards to the last commit: {head:?}"
        );
        assert_kind(
            checkout(&req.id).await,
            ErrorKind::Conflict,
            "a merged change is closed",
        );
    })
    .await;
}

// ADR 0102: an impact committed then withdrawn is reverted by the next commit; the merge waits for it.
#[tokio::test]
async fn withdraw_a_committed_impact() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(g.create_change(&ctx, own_change(&f.base, "revert")).await);
        let req = must(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    node: f.req.id.clone(),
                    rationale: "v2".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        touch(g, &c.id, &req.id).await;
        must(
            g.impact_node_review(&ctx, &c.id, &req.id, REVIEW_ACCEPTED, "bob", "ok")
                .await,
        );
        let made = must(g.impact_node_create(&ctx, &c.id, design("D-2")).await);
        must(
            g.impact_node_review(&ctx, &c.id, &made.id, REVIEW_ACCEPTED, "bob", "ok")
                .await,
        );
        let st = must(g.commit_state(&ctx, &c.id).await);
        assert!(
            st.can_commit && !st.can_merge && st.to_commit.len() == 2 && st.unsettled.len() == 2,
            "two modifications to commit, nothing to merge: {st:?}"
        );
        let first = must(commit_change(g, &c.id, "").await);
        assert_ne!(
            first.nodes.get(&f.req.id),
            Some(&f.req.version),
            "REQ-1 committed: {:?}",
            first.nodes
        );

        must(g.withdraw_impact(&ctx, &c.id, &made.id, "", "").await);
        let err = integrate_change(g, &c.id).await.unwrap_err();
        assert!(
            err.is_conflict() && err.to_string().contains("withdrawn"),
            "the withdrawal waits for a commit: {err}"
        );
        let st = must(g.commit_state(&ctx, &c.id).await);
        assert!(
            st.can_commit
                && !st.can_merge
                && st.reverts == 1
                && st.commits
                && st.to_commit.is_empty(),
            "a revert to commit, nothing else: {st:?}"
        );
        let second = must(commit_change(g, &c.id, "").await);
        let st = must(g.commit_state(&ctx, &c.id).await);
        assert!(
            !st.can_commit && st.can_merge && st.unsettled.is_empty(),
            "committed, ready to merge: {st:?}"
        );
        assert!(
            !second.nodes.contains_key(&node_of(&made))
                && second.nodes.get(&f.req.id) == first.nodes.get(&f.req.id)
                && second.kind == BASELINE_REVERT,
            "D-2 reverted, REQ-1 kept: {second:?}"
        );
        let ch = must(integrate_change(g, &c.id).await);
        let head = must(g.baseline(&ctx, &ch.result_baseline_id).await);
        assert!(
            !head.nodes.contains_key(&node_of(&made))
                && head.nodes.get(&f.req.id) == first.nodes.get(&f.req.id),
            "main holds REQ-1's commit and no D-2: {:?}",
            head.nodes
        );
    })
    .await;
}
