//! Port of `tag_test.go`: names on the states changes leave, and baselines computed from the log (ADR 0056).

use goap_domain::*;
use goap_graphapi::{Commit, NodeEdit};
use serde_json::json;

use crate::tests::branches::commit_on;
use crate::testsupport::*;
use crate::{ErrorKind, Graph, NewChange, NodeQuery};

/// landed is what a commit of the test leaves: its change and the baseline it produced.
struct Landed {
    change: ChangeID,
    baseline: Baseline,
}

#[tokio::test]
async fn tags() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let commit = async |title: &str, key: &str, name: &str| {
            let head = must(g.branch_head(&ctx, DEFAULT_NAMESPACE, MAIN_BRANCH).await);
            let res = must(
                g.commit(
                    &ctx,
                    Commit {
                        project_id: "PROJ-ROOT".into(),
                        title: title.into(),
                        baseline: head.id,
                        by: "test".into(),
                        baseline_name: name.into(),
                        edits: vec![NodeEdit {
                            key: key.into(),
                            r#type: "Doc".into(),
                            props: props(&[("status", json!("draft"))]),
                            ..Default::default()
                        }],
                        ..Default::default()
                    },
                )
                .await,
            );
            Landed {
                change: res.change,
                baseline: res.baseline,
            }
        };
        // a name other than the title tags the state the change leaves; the title itself is no tag
        let a = commit("First", "T-1", "release").await;
        commit("Second", "T-2", "Second").await;
        let tags = must(g.tags(&ctx, &TagFilter::default()).await);
        assert!(
            tags.len() == 1
                && tags[0].name == "release"
                && tags[0].change_id == a.change
                && tags[0].baseline_id == a.baseline.id,
            "tags = {tags:?}"
        );
        // not unique: the same name on another change, and several names on one change
        let b = commit("Third", "T-3", "").await;
        for name in ["release", "stable"] {
            must(g.tag_change(&ctx, &b.change, name, "me").await);
        }
        let again = must(g.tag_change(&ctx, &b.change, "release", "me").await);
        assert_eq!(
            again.change_id, b.change,
            "repeating a name on the same change"
        );
        let got = must(
            g.tags(
                &ctx,
                &TagFilter {
                    name: "release".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(got.len(), 2, "release labels two changes, got {got:?}");
        let got = must(
            g.tags(
                &ctx,
                &TagFilter {
                    change: b.change.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(got.len(), 2, "two names on the third change, got {got:?}");
        // the state named is the one the change left
        let s = must(g.state_after(&ctx, &b.change).await);
        assert_eq!(s.id, b.baseline.id, "state after");
        // a draft has no state to name
        let draft = must(
            g.create_change(
                &ctx,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "Draft".into(),
                    baseline_id: b.baseline.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_kind(
            g.tag_change(&ctx, &draft.id, "x", "").await,
            ErrorKind::Conflict,
            "tagging a draft",
        );
        assert_kind(
            g.state_after(&ctx, &draft.id).await,
            ErrorKind::NotFound,
            "state after a draft",
        );
        assert_kind(
            g.tag_change(&ctx, &b.change, "", "").await,
            ErrorKind::Invalid,
            "a tag needs a name",
        );
        // deleting a tag leaves the change
        must(g.delete_tag(&ctx, &tags[0].id).await);
        assert_kind(
            g.delete_tag(&ctx, &tags[0].id).await,
            ErrorKind::NotFound,
            "deleting twice",
        );
        must(g.change(&ctx, &a.change).await);
    })
    .await;
}

// A state is computed from the log of the changes; materialising one only stores it (ADR 0056).
#[tokio::test]
async fn virtual_baselines() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut f = new_fixture(r).await;
        f.g.materialize_every = 3;
        let g = &f.g;
        let mut head = must(g.branch_head(&ctx, "", MAIN_BRANCH).await);
        let mut written = Vec::new();
        for i in 0..7 {
            let mut edits = vec![NodeEdit {
                key: format!("DES-{i}"),
                r#type: "Design".into(),
                ..Default::default()
            }];
            if i == 4 {
                edits.push(NodeEdit {
                    pre: Some(NodeRef {
                        id: f.test.id.clone(),
                        version: head.nodes[&f.test.id],
                    }),
                    props: props(&[("obsolete", json!(true))]),
                    ..Default::default()
                });
            }
            head = commit_on(g, "", &head.id, edits).await;
            written.push(head.clone());
        }
        // stored: a header only between two snapshots
        let mut raw = Vec::new();
        {
            let mut tx = must(r.repo.begin(&ctx).await);
            for b in &written {
                raw.push(must(tx.baseline(&ctx, &b.id).await));
            }
            must(tx.commit(&ctx).await);
        }
        let gaps: Vec<i64> = raw.iter().map(|b| b.gap).collect();
        for (i, b) in raw.iter().enumerate() {
            assert!(
                b.gap == 0 || b.nodes.is_empty(),
                "baseline {i} is a header only, yet holds {} nodes",
                b.nodes.len()
            );
        }
        assert!(
            gaps.iter().all(|g| *g < 3),
            "gaps {gaps:?}: a snapshot at least every 3 baselines"
        );
        assert!(
            gaps.iter().any(|g| *g > 0),
            "gaps {gaps:?}: no baseline is computed"
        );
        // read: every one holds what was written, computed or stored, through a graph with no cache
        let fresh = Graph::new(r.repo.clone());
        for (i, want) in written.iter().enumerate() {
            let got = must(fresh.baseline(&ctx, &want.id).await);
            assert!(
                got.nodes == want.nodes && got.gap == gaps[i],
                "baseline {i} (gap {}): read {:?}, wrote {:?}",
                gaps[i],
                got.nodes,
                want.nodes
            );
            let st = must(fresh.state_after(&ctx, &want.change_id).await);
            assert_eq!(
                st.nodes, want.nodes,
                "state after the change of baseline {i}"
            );
            let (nodes, _) = must(fresh.baseline_graph(&ctx, &want.id).await);
            assert_eq!(nodes.len(), want.nodes.len(), "baseline {i}: graph");
        }
        // browsing a computed baseline by type materialises it
        if let Some((i, b)) = written.iter().enumerate().find(|(i, _)| gaps[*i] != 0) {
            let page = must(
                fresh
                    .baseline_nodes(
                        &ctx,
                        &b.id,
                        NodeQuery {
                            r#type: "Design".into(),
                            ..Default::default()
                        },
                    )
                    .await,
            );
            assert!(page.total > 0, "baseline {i}: no Design node listed");
            let got = must(fresh.baseline(&ctx, &b.id).await);
            assert_eq!(
                got.gap, 0,
                "baseline {i} was browsed by type, it is stored now"
            );
        }
        // a snapshot on demand
        for (i, b) in written.iter().enumerate() {
            if gaps[i] == 0 {
                continue;
            }
            must(fresh.materialize(&ctx, &b.id).await);
            let got = must(Graph::new(r.repo.clone()).baseline(&ctx, &b.id).await);
            assert!(
                got.gap == 0 && got.nodes == b.nodes,
                "baseline {i} after Materialize: gap {}, {:?}",
                got.gap,
                got.nodes
            );
        }
    })
    .await;
}
