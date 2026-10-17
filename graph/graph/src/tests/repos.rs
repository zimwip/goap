//! The fixtures of `repos_test.go` (seedNode, pinBaseline, testChange) and the round trips they rely on, against
//! every repository.

use goap_domain::{MAIN_BRANCH, NodeRef};

use crate::testsupport::*;

// A node seeded directly is a version on main outside any baseline; pinned, it is part of the head of main.
#[tokio::test]
async fn seed_and_pin() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let g = r.graph();
        must(g.bootstrap(&c).await);
        let n = must(
            seed_node(
                &g,
                NewNode {
                    key: "X-1".into(),
                    r#type: "Thing".into(),
                    properties: props(&[("a", 1.into())]),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(
            (n.version, n.key.as_str(), n.branch.as_str()),
            (1, "X-1", MAIN_BRANCH)
        );
        assert_eq!(n.properties["a"], 1);
        let head = must(g.branch_head(&c, "", MAIN_BRANCH).await);
        assert!(
            !head.contains(&n.r#ref()),
            "a seeded node is in no baseline"
        );
        let b = must(pin_baseline(&g, "", &[n.r#ref()]).await);
        let head = must(g.branch_head(&c, "", MAIN_BRANCH).await);
        assert_eq!(head.id, b.id);
        assert!(head.contains(&n.r#ref()));
        let got = must(g.node_by_key(&c, "", "X-1").await);
        assert_eq!(got.id, n.id);
        let ch = test_change(&g, "").await;
        assert_eq!(
            ch.baseline_id, b.id,
            "a change starts from the head of main"
        );
        let (nodes, _) = must(g.baseline_graph(&c, &b.id).await);
        assert!(nodes.iter().any(|x| x.r#ref() == n.r#ref()));
    })
    .await;
}

// land writes a version per node, the links between them, and a commit baseline the head moves to.
#[tokio::test]
async fn land_writes_versions_and_links() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        assert_eq!(f.base.nodes.len(), 3);
        let out = must(g.out_links_of(&c, &f.req.r#ref()).await);
        assert_eq!(out.len(), 1);
        assert_eq!(out[0].r#type, "satisfies");
        assert_eq!(out[0].to, f.need.r#ref());
        let b2 = must(
            land(
                g,
                "",
                &[Landing {
                    properties: props(&[("title", "Use PSP v2".into())]),
                    ..landing("REQ-1", "Requirement")
                }],
            )
            .await,
        );
        assert_eq!(b2.nodes[&f.req.id], 2);
        assert_eq!(b2.parent_id, f.base.id);
        let v2 = must(
            g.node(
                &c,
                &NodeRef {
                    id: f.req.id.clone(),
                    version: 2,
                },
            )
            .await,
        );
        assert_eq!(v2.parents, vec![1]);
        let vs = must(g.versions(&c, &f.req.id).await);
        assert_eq!(vs.len(), 2);
    })
    .await;
}
