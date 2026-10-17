//! Port of `subchange_nested_test.go`: the recursive model of sub-changes (ADR 0103). A sub-change owns a branch
//! forked from the branch of its parent, recursively, and the levels only meet through a commit, a rebase and a
//! merge: a sub-change of a sub-change, the guardian of the change being merged, and a parent holding an uncommitted
//! draft.

use std::collections::BTreeSet;
use std::sync::Arc;

use goap_domain::*;
use goap_graphapi::NodeCheckout;
use parking_lot::Mutex;
use serde_json::{Map, Value, json};

use crate::tests::helpers::*;
use crate::tests::lifecycle::MergeGate;
use crate::tests::subchange::{OrgWorld, new_org_world, root_change, sub_change};
use crate::testsupport::*;
use crate::{ErrorKind, Graph};

/// nested is a root change with a sub-change and a sub-sub-change, each on its own branch.
struct Nested {
    w: OrgWorld,
    root: Change,
    mid: Change,
    leaf: Change,
}

async fn new_nested(r: &TestRepo) -> Nested {
    let ctx = ctx();
    let w = new_org_world(r).await;
    let root = must(w.g.create_change(&ctx, root_change(&w.base, "root")).await);
    let mid = must(w.g.create_change(&ctx, sub_change(&root.id, "mid")).await);
    let leaf = must(w.g.create_change(&ctx, sub_change(&mid.id, "leaf")).await);
    Nested { w, root, mid, leaf }
}

/// draftProps proposes a modification of node (as the change reads it) and edits its properties, leaving the draft
/// unreviewed and uncommitted.
async fn draft_props(g: &Graph, c: &Change, node: &Node, p: Map<String, Value>) -> ChangeImpactID {
    let added = must(
        g.propose_impact(
            &ctx(),
            &c.id,
            vec![ChangeImpact {
                intent: INTENT_MODIFIED,
                pre: Some(node.r#ref()),
                rationale: c.title.clone(),
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
                properties: p,
                ..Default::default()
            },
        )
        .await,
    );
    added[0].id.clone()
}

/// commitProps drafts a modification, accepts it and commits it on the change's branch.
async fn commit_props(g: &Graph, c: &Change, node: &Node, p: Map<String, Value>) -> ChangeImpactID {
    let id = draft_props(g, c, node, p).await;
    must(accept(g, &c.id, &id, "u", "ok").await);
    must(commit_change(g, &c.id, "").await);
    id
}

/// seen is a property of a node as a change reads it (its drafts, else its branch).
async fn seen(g: &Graph, c: &Change, key: &str, prop: &str) -> Value {
    let v = must(
        g.change_node_view_by_key(&ctx(), &c.id, "", DEFAULT_NAMESPACE, key)
            .await,
    );
    v.properties.get(prop).cloned().unwrap_or(Value::Null)
}

async fn head_of(g: &Graph, c: &Change) -> BaselineID {
    must(g.branch_head(&ctx(), &c.namespace, &c.branch).await).id
}

fn title(t: &str) -> Map<String, Value> {
    props(&[("title", json!(t))])
}

// A sub-sub-change forks from the branch of its parent as that branch is when it is created: it sees what its parent
// committed, never its parent's drafts; its merge lands on its parent's branch only, and climbs one level per merge.
#[tokio::test]
async fn sub_sub_change_branches() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_org_world(r).await;
        let g = &w.g;
        let root = must(g.create_change(&ctx, root_change(&w.base, "root")).await);
        commit_props(g, &root, &w.cmp1, title("root's")).await;
        let mid = must(g.create_change(&ctx, sub_change(&root.id, "mid")).await);
        let rh = head_of(g, &root).await;
        assert_eq!(
            mid.baseline_id, rh,
            "the middle change starts from the head of the root's branch"
        );
        // a draft of the middle change is its own: its sub-change, created now, does not see it
        draft_props(g, &mid, &w.cmp2, title("mid's draft")).await;
        let leaf = must(g.create_change(&ctx, sub_change(&mid.id, "leaf")).await);
        let b = must(g.branch(&ctx, &leaf.namespace, &leaf.branch).await);
        let mh = head_of(g, &mid).await;
        assert!(
            b.parent == mid.branch && b.fork_baseline == mh,
            "the leaf forks from the head of its parent's branch {}: {b:?}",
            mid.branch
        );
        assert_eq!(
            seen(g, &leaf, "CMP-1", "title").await,
            "root's",
            "the leaf sees what the root committed"
        );
        assert_eq!(
            seen(g, &leaf, "CMP-2", "title").await,
            "two",
            "the leaf does not see the draft of its parent"
        );
        assert_eq!(
            seen(g, &mid, "CMP-2", "title").await,
            "mid's draft",
            "the middle change sees its own draft"
        );

        // the leaf works on another node, merges into the middle change and nowhere else
        let cmp3 = must(g.node_by_key(&ctx, DEFAULT_NAMESPACE, "CMP-3").await);
        commit_props(g, &leaf, &cmp3, title("leaf's")).await;
        let root_head = head_of(g, &root).await;
        let main_head = must(g.branch_head(&ctx, DEFAULT_NAMESPACE, MAIN_BRANCH).await).id;
        must(integrate_change(g, &leaf.id).await);
        assert_eq!(
            seen(g, &mid, "CMP-3", "title").await,
            "leaf's",
            "the middle change holds the leaf's work"
        );
        assert_eq!(
            seen(g, &mid, "CMP-2", "title").await,
            "mid's draft",
            "the merge keeps the middle change's own draft"
        );
        assert!(
            head_of(g, &root).await == root_head
                && seen(g, &root, "CMP-3", "title").await == "three",
            "the leaf's merge reached the root's branch"
        );
        assert_eq!(
            must(g.branch_head(&ctx, DEFAULT_NAMESPACE, MAIN_BRANCH).await).id,
            main_head,
            "the leaf's merge reached main"
        );
        // the middle change records the leaf's work as its own: accepted, landed
        let recorded = must(g.change(&ctx, &mid.id).await)
            .nodes
            .into_iter()
            .rfind(|cn| cn.key == "CMP-3");
        assert!(
            recorded
                .as_ref()
                .is_some_and(|cn| cn.review == REVIEW_ACCEPTED && cn.landed.is_some()),
            "the middle change records the merged node, accepted and landed: {recorded:?}"
        );
        let c = must(g.change(&ctx, &leaf.id).await);
        assert_eq!(c.status, CHANGE_APPLIED, "the merged leaf");
        // a merged sub-change takes no sub-change
        assert!(
            g.create_change(&ctx, sub_change(&leaf.id, "late"))
                .await
                .is_err(),
            "a sub-change of a merged change was created"
        );

        // the middle change commits its own draft, merges into the root, the root lands everything on main
        for cn in must(g.change(&ctx, &mid.id).await).nodes {
            if cn.key == "CMP-2" {
                must(accept(g, &mid.id, &cn.id, "u", "ok").await);
            }
        }
        must(apply(g, &mid.id, "").await);
        must(apply(g, &root.id, "").await);
        for (key, want) in [
            ("CMP-1", "root's"),
            ("CMP-2", "mid's draft"),
            ("CMP-3", "leaf's"),
        ] {
            let n = must(g.node_by_key(&ctx, DEFAULT_NAMESPACE, key).await);
            assert_eq!(n.properties["title"], want, "{key} on main");
        }
    })
    .await;
}

/// What the guardian of the test saw and says.
#[derive(Default)]
struct Judge {
    asked: Vec<ChangeID>,
    judged: Vec<String>,
    refuse: BTreeSet<ChangeID>,
}

// The guardian of a change holds its merge into its parent like a root change's into main (ADR 0102): it is asked
// for the change being merged, with the versions it lands; a refusal leaves the parent's branch as it was.
#[tokio::test]
async fn sub_change_merge_guardian() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut n = new_nested(r).await;
        commit_props(&n.w.g, &n.leaf, &n.w.cmp3, title("leaf's")).await;
        let judge = Arc::new(Mutex::new(Judge::default()));
        judge
            .lock()
            .refuse
            .extend([n.leaf.id.clone(), n.mid.id.clone()]);
        {
            let judge = judge.clone();
            n.w.g.lifecycles = Some(Arc::new(MergeGate(Box::new(move |c, bb| {
                let mut j = judge.lock();
                j.asked.push(c.id.clone());
                j.judged = bb
                    .nodes
                    .values()
                    .filter_map(|v| v.properties.get("title").and_then(Value::as_str))
                    .filter(|t| !t.is_empty())
                    .map(str::to_string)
                    .collect();
                Ok((true, !j.refuse.contains(&c.id)))
            }))));
        }
        let g = &n.w.g;
        let mid_head = head_of(g, &n.mid).await;
        assert_kind(
            integrate_change(g, &n.leaf.id).await,
            ErrorKind::Invalid,
            "the guardian refuses the leaf's merge",
        );
        {
            let j = judge.lock();
            assert!(
                j.asked == [n.leaf.id.clone()],
                "the guardian is asked for the change being merged only: {:?}",
                j.asked
            );
            assert!(
                j.judged.iter().any(|s| s == "leaf's"),
                "the guardian judges the version the leaf lands: {:?}",
                j.judged
            );
        }
        assert!(
            head_of(g, &n.mid).await == mid_head
                && seen(g, &n.mid, "CMP-3", "title").await == "three",
            "a refused merge moved the parent's branch"
        );
        assert!(
            must(g.change(&ctx, &n.mid.id).await).nodes.is_empty(),
            "a refused merge recorded impacts in the parent"
        );
        let c = must(g.change(&ctx, &n.leaf.id).await);
        assert_ne!(c.status, CHANGE_APPLIED, "a refused merge applied the leaf");

        // let the leaf go: the middle change holds its work; its own guardian then holds the merge into the root
        judge.lock().refuse.remove(&n.leaf.id);
        must(integrate_change(g, &n.leaf.id).await);
        judge.lock().asked.clear();
        let root_head = head_of(g, &n.root).await;
        assert_kind(
            integrate_change(g, &n.mid.id).await,
            ErrorKind::Invalid,
            "the guardian refuses the middle change's merge",
        );
        {
            let j = judge.lock();
            assert!(
                j.asked == [n.mid.id.clone()],
                "the guardian is asked for the middle change: {:?}",
                j.asked
            );
        }
        assert_eq!(
            head_of(g, &n.root).await,
            root_head,
            "a refused merge moved the root's branch"
        );
        assert_eq!(
            seen(g, &n.mid, "CMP-3", "title").await,
            "leaf's",
            "refusing the middle change's merge leaves the leaf's merge in it"
        );
        judge.lock().refuse.remove(&n.mid.id);
        must(integrate_change(g, &n.mid.id).await);
        assert_eq!(
            seen(g, &n.root, "CMP-3", "title").await,
            "leaf's",
            "the root holds the work of both levels"
        );
    })
    .await;
}

// A parent holding an uncommitted draft (ADR 0103): of the node its sub-change merges, it holds the merge until it
// commits or cancels it; of another node, or with no modification, it holds nothing and is kept as it is. What the
// parent committed meanwhile is rebased into the sub-change at its merge: merged when the two touched different
// properties, a conflict to settle in the sub-change when they touched the same.
#[tokio::test]
async fn sub_change_under_parent_draft() {
    // same node
    Box::pin(for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let n = new_nested(r).await;
        let g = &n.w.g;
        commit_props(g, &n.leaf, &n.w.cmp3, title("leaf's")).await;
        let held = draft_props(g, &n.mid, &n.w.cmp3, title("mid's draft")).await;
        let mid_head = head_of(g, &n.mid).await;
        assert_kind(
            integrate_change(g, &n.leaf.id).await,
            ErrorKind::Conflict,
            "merge under an uncommitted draft of the parent",
        );
        assert!(
            head_of(g, &n.mid).await == mid_head
                && seen(g, &n.mid, "CMP-3", "title").await == "mid's draft",
            "the refused merge touched the parent's branch or its draft"
        );
        let c = must(g.change(&ctx, &n.leaf.id).await);
        assert_ne!(c.status, CHANGE_APPLIED, "the leaf was applied");
        // the parent cancels its draft: the leaf merges
        must(g.impact_node_cancel(&ctx, &n.mid.id, &held, "", "").await);
        must(integrate_change(g, &n.leaf.id).await);
        assert_eq!(
            seen(g, &n.mid, "CMP-3", "title").await,
            "leaf's",
            "the parent holds the leaf's work"
        );
    }))
    .await;

    // checkout without modification
    Box::pin(for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let n = new_nested(r).await;
        let g = &n.w.g;
        commit_props(g, &n.leaf, &n.w.cmp3, title("leaf's")).await;
        let added = must(
            g.propose_impact(
                &ctx,
                &n.mid.id,
                vec![ChangeImpact {
                    intent: INTENT_MODIFIED,
                    pre: Some(n.w.cmp3.r#ref()),
                    rationale: "look".into(),
                    ..Default::default()
                }],
            )
            .await,
        );
        must(
            g.impact_node_checkout(
                &ctx,
                &n.mid.id,
                NodeCheckout {
                    impact: added[0].id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        let r = integrate_change(g, &n.leaf.id).await;
        assert!(
            r.is_ok(),
            "a draft that modifies nothing holds no merge: {r:?}"
        );
        assert_eq!(
            seen(g, &n.mid, "CMP-3", "title").await,
            "leaf's",
            "the parent holds the leaf's work"
        );
    }))
    .await;

    // another node
    Box::pin(for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let n = new_nested(r).await;
        let g = &n.w.g;
        commit_props(g, &n.leaf, &n.w.cmp3, title("leaf's")).await;
        let other = draft_props(g, &n.mid, &n.w.cmp2, title("mid's draft")).await;
        let r = integrate_change(g, &n.leaf.id).await;
        assert!(r.is_ok(), "a draft of another node holds no merge: {r:?}");
        assert_eq!(
            seen(g, &n.mid, "CMP-3", "title").await,
            "leaf's",
            "the parent holds the leaf's work"
        );
        assert_eq!(
            seen(g, &n.mid, "CMP-2", "title").await,
            "mid's draft",
            "the parent keeps its draft"
        );
        // and commits it afterwards, on top of the leaf's work
        must(accept(g, &n.mid.id, &other, "u", "ok").await);
        must(commit_change(g, &n.mid.id, "").await);
        let mid = must(g.change(&ctx, &n.mid.id).await);
        let head = must(g.branch_head(&ctx, &mid.namespace, &mid.branch).await);
        assert!(
            head.nodes.get(&n.w.cmp2.id) != Some(&n.w.cmp2.version)
                && head.nodes.get(&n.w.cmp3.id) != Some(&n.w.cmp3.version),
            "the parent's branch holds both nodes changed: {:?}",
            head.nodes
        );
    }))
    .await;

    // committed meanwhile, other property
    Box::pin(for_each_repo(async |r: &TestRepo| {
        let n = new_nested(r).await;
        let g = &n.w.g;
        commit_props(g, &n.leaf, &n.w.cmp3, title("leaf's")).await;
        commit_props(g, &n.mid, &n.w.cmp3, props(&[("note", json!("mid's"))])).await;
        let r = integrate_change(g, &n.leaf.id).await;
        assert!(
            r.is_ok(),
            "a clean rebase onto the parent's commit merges: {r:?}"
        );
        let (t, note) = (
            seen(g, &n.mid, "CMP-3", "title").await,
            seen(g, &n.mid, "CMP-3", "note").await,
        );
        assert!(
            t == "leaf's" && note == "mid's",
            "the parent holds both: {t} / {note}"
        );
    }))
    .await;

    // committed meanwhile, same property
    Box::pin(for_each_repo(async |r: &TestRepo| {
        let n = new_nested(r).await;
        let g = &n.w.g;
        let leaf_impact = commit_props(g, &n.leaf, &n.w.cmp3, title("leaf's")).await;
        commit_props(g, &n.mid, &n.w.cmp3, title("mid's")).await;
        let mid_head = head_of(g, &n.mid).await;
        assert_kind(
            integrate_change(g, &n.leaf.id).await,
            ErrorKind::Conflict,
            "the rebase meets the parent's commit",
        );
        assert!(
            head_of(g, &n.mid).await == mid_head
                && seen(g, &n.mid, "CMP-3", "title").await == "mid's",
            "a conflicting merge touched the parent's branch"
        );
        let cn = must(seen_impact(g, &n.leaf.id, &leaf_impact, "").await);
        assert_eq!(
            cn.review, REVIEW_PROPOSED,
            "the conflict sends the leaf's impact back to review"
        );
        // settled in the leaf, reviewed, committed: it merges
        must(
            edit(
                g,
                &n.leaf.id,
                &leaf_impact,
                Edit {
                    properties: title("both"),
                    ..Default::default()
                },
            )
            .await,
        );
        must(accept(g, &n.leaf.id, &leaf_impact, "u", "settled").await);
        must(commit_change(g, &n.leaf.id, "").await);
        must(integrate_change(g, &n.leaf.id).await);
        assert_eq!(
            seen(g, &n.mid, "CMP-3", "title").await,
            "both",
            "the parent holds the settled title"
        );
    }))
    .await;
}
