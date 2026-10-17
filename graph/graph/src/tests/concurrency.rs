//! Port of `concurrency_test.go`: concurrent writers of one change and concurrent merges into one branch (ADR 0127).
//! Go runs goroutines; the writers here are futures polled together (`join_all`), which interleave at every await of
//! a transaction, on a multi-threaded runtime. Meaningful under PostgreSQL (`GOAP_TEST_PG_DSN`).

use futures::future::join_all;
use goap_domain::*;
use goap_graphapi::{NodeCheckout, NodeCreate, NodeUpdate};
use serde_json::json;

use crate::NewChange;
use crate::tests::helpers::accept;
use crate::testsupport::*;

fn new_change(base: &Baseline, title: &str) -> NewChange {
    NewChange {
        project_id: "PROJ-ROOT".into(),
        title: title.into(),
        baseline_id: base.id.clone(),
        ..Default::default()
    }
}

fn create(key: String) -> NodeCreate {
    NodeCreate {
        key,
        r#type: "Requirement".into(),
        rationale: "new".into(),
        ..Default::default()
    }
}

// Several people (or a person and an agent) write the same change at once (ADR 0127): the writers of a change are
// serialized, so the projection of its impacts stays the fold of its log, the drafts read after the writes hold every
// one of them, and a check made before a write (a second checkout) still holds when it is written.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn concurrent_writers_of_a_change() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(g.create_change(&ctx, new_change(&f.base, "together")).await);
        const N: usize = 8;

        // creations of distinct nodes: every one is an impact of the change
        let errs = join_all(
            (0..N)
                .map(|i| Box::pin(g.impact_node_create(&ctx, &c.id, create(format!("REQ-C{i}"))))),
        )
        .await;
        for (i, r) in errs.into_iter().enumerate() {
            assert!(r.is_ok(), "creation {i}: {:?}", r.err());
        }
        let got = must(g.change(&ctx, &c.id).await).nodes.len();
        assert_eq!(got, N, "{got} impacts after {N} concurrent creations");

        // one checkout of a node, whoever asks first; the others are told it is checked out
        let tries = join_all((0..N).map(|_| {
            Box::pin(g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    node: f.req.id.clone(),
                    rationale: "edit".into(),
                    ..Default::default()
                },
            ))
        }))
        .await;
        let mut won = Vec::new();
        for r in tries {
            match r {
                Ok(cn) => won.push(cn),
                Err(e) => assert!(
                    e.is_conflict(),
                    "a concurrent checkout fails with a conflict, not {e}"
                ),
            }
        }
        assert_eq!(
            won.len(),
            1,
            "{} concurrent checkouts of one node succeeded, want 1",
            won.len()
        );

        // edits of distinct properties of one draft: the draft holds them all
        let cn = &won[0];
        let edits = join_all((0..N).map(|i| {
            Box::pin(g.impact_node_update(
                &ctx,
                &c.id,
                &cn.id,
                NodeUpdate {
                    properties: props(&[(format!("p{i}").as_str(), json!(i))]),
                    ..Default::default()
                },
            ))
        }))
        .await;
        for (i, r) in edits.into_iter().enumerate() {
            assert!(r.is_ok(), "edit {i}: {:?}", r.err());
        }
        let post = cn.post.clone().expect("a draft");
        let view = must(g.change_node_view(&ctx, &c.id, "", &post).await);
        for i in 0..N {
            assert!(
                view.properties.contains_key(&format!("p{i}")),
                "edit p{i} is lost from the draft: {:?}",
                view.properties
            );
        }
    })
    .await;
}

// Changes merged into one branch at once all land (ADR 0127): a merge reads the head of the target under the branch's
// lock, so the later one sees the earlier one landed and merges after it, and the head moves only from the baseline a
// merge built on: no merge leaves the work of another out of the branch.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn concurrent_merges_into_a_branch() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        const N: usize = 4;
        let mut ids = Vec::new();
        for i in 0..N {
            let c = must(
                g.create_change(&ctx, new_change(&f.base, &format!("add {i}")))
                    .await,
            );
            let cn = must(
                g.impact_node_create(&ctx, &c.id, create(format!("REQ-M{i}")))
                    .await,
            );
            must(accept(g, &c.id, &cn.id, "bob", "fine").await);
            ids.push(c.id);
        }
        let errs = join_all(ids.iter().map(|id| Box::pin(apply(g, id, "")))).await;
        for (i, r) in errs.into_iter().enumerate() {
            assert!(r.is_ok(), "merge {i}: {:?}", r.err());
        }
        let head = must(g.branch_head(&ctx, "", MAIN_BRANCH).await);
        for i in 0..N {
            let key = format!("REQ-M{i}");
            let n = g.node_by_key_on(&ctx, "", MAIN_BRANCH, &key).await;
            assert!(
                n.as_ref()
                    .is_ok_and(|n| head.nodes.get(&n.id) == Some(&n.version)),
                "{key} is not on main after the concurrent merges: {:?}",
                n.err()
            );
        }
    })
    .await;
}
