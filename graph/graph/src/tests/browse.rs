//! The pages of the nodes of a baseline, the neighbourhood of a node (its suspect links)
//! and the namespaces of the graph.

use goap_domain::*;
use serde_json::{Map, Value};

use crate::browse::{NodeQuery, TypeCount};
use crate::tests::draft::title;
use crate::tests::helpers::*;
use crate::testsupport::*;

#[tokio::test]
async fn browse_baseline() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let g = r.graph();
        let node = async |key: &str, typ: &str, p: Map<String, Value>| {
            must(
                import_node(
                    &g,
                    ImportNode {
                        key: key.into(),
                        r#type: typ.into(),
                        properties: p,
                        ..Default::default()
                    },
                )
                .await,
            )
        };
        let mut refs = Vec::new();
        for i in 0..12 {
            refs.push(
                node(&format!("REQ-{i:02}"), "Req", title(&format!("req {i}")))
                    .await
                    .r#ref(),
            );
        }
        let test = node("TEST-1", "Test", title("login works")).await;
        node("TEST-2", "Test", Map::new()).await;
        let verifies = must(import_link(&g, "verifies", &test.r#ref(), &refs[0], Map::new()).await);
        // req 0 changes after the link: the incoming link now targets an earlier version (suspect)
        let req0 = must(import_props(&g, &refs[0], title("req 0 bis")).await);
        must(import_link(&g, "depends", &req0.r#ref(), &refs[1], Map::new()).await);
        let req0 = must(
            g.node(
                &ctx,
                &NodeRef {
                    id: req0.id.clone(),
                    version: 0,
                },
            )
            .await,
        );
        let b = must(g.branch_head(&ctx, DEFAULT_NAMESPACE, MAIN_BRANCH).await);

        let page = must(
            g.baseline_nodes(
                &ctx,
                &b.id,
                NodeQuery {
                    r#type: "Req".into(),
                    offset: 10,
                    limit: 5,
                    ..Default::default()
                },
            )
            .await,
        );
        assert!(
            page.total == 12 && page.nodes.len() == 2 && page.nodes[0].key == "REQ-10",
            "page = total {}, {} nodes",
            page.total,
            page.nodes.len()
        );
        let tc = |t: &str, count: usize| TypeCount {
            r#type: t.into(),
            count,
        };
        assert_eq!(page.types, vec![tc("Req", 12), tc("Test", 2)], "types");
        let page = must(
            g.baseline_nodes(
                &ctx,
                &b.id,
                NodeQuery {
                    text: "LOGIN".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert!(
            page.total == 1 && page.nodes[0].key == "TEST-1" && page.types.len() == 1,
            "text filter = {page:?}"
        );

        let nb = must(g.node_neighbourhood(&ctx, &b.id, &req0.id).await);
        assert!(
            nb.node.version == req0.version && nb.nodes.len() == 2 && nb.links.len() == 2,
            "neighbourhood = v{}, {} nodes, {} links",
            nb.node.version,
            nb.nodes.len(),
            nb.links.len()
        );
        let keys: Vec<&str> = nb.nodes.iter().map(|n| n.key.as_str()).collect();
        assert!(
            keys.contains(&"TEST-1") && keys.contains(&"REQ-01") && !keys.contains(&"TEST-2"),
            "neighbours = {keys:?}"
        );
        assert_eq!(nb.suspect, vec![verifies.id.clone()], "suspect");
        // seen from the test, the suspect link leads to the baseline's version of req 0
        let nb = must(g.node_neighbourhood(&ctx, &b.id, &test.id).await);
        assert!(
            nb.nodes.len() == 1 && nb.nodes[0].version == req0.version && nb.suspect.len() == 1,
            "neighbourhood of the test = {nb:?}"
        );
        assert!(
            g.node_neighbourhood(&ctx, &b.id, &NodeID::from("missing"))
                .await
                .is_err(),
            "unknown node: no error"
        );

        // the namespace of the test, and the one of the roots of the organisation and the projects (ADR 0054)
        let ns = must(g.namespaces(&ctx).await);
        assert_eq!(
            ns,
            vec![DEFAULT_NAMESPACE.to_string(), "organisation".to_string()],
            "namespaces"
        );
    })
    .await;
}
