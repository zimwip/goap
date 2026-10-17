//! Port of `snapshot_test.go`: the state of a namespace without its history, exported and imported as one change
//! (ADR 0104).

use std::collections::BTreeMap;

use goap_domain::*;
use serde_json::json;

use crate::tests::helpers::accept_all;
use crate::testsupport::*;
use crate::{
    SNAPSHOT_FORMAT, Snapshot, SnapshotImport, SnapshotLink, SnapshotNode, parse_snapshot,
};

fn import(s: &Snapshot, title: &str) -> SnapshotImport {
    SnapshotImport {
        snapshot: s.clone(),
        project_id: "PROJ-ROOT".into(),
        title: title.into(),
        ..Default::default()
    }
}

// A snapshot is the state of a namespace without its history (ADR 0104): exported from the head of main, imported as
// one change that creates what the graph lacks, brings what differs to the snapshot and leaves the rest alone.
#[tokio::test]
async fn snapshot_export_import() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let s = must(
            g.export_snapshot(&ctx, "", "", &BaselineID::default())
                .await,
        );
        assert!(
            s.format == SNAPSHOT_FORMAT
                && s.namespace == DEFAULT_NAMESPACE
                && s.baseline == f.base.id.as_str()
                && s.nodes.len() == 3,
            "export of the head of main: {s:?}"
        );
        let keys: Vec<&str> = s.nodes.iter().map(|n| n.key.as_str()).collect();
        assert_eq!(keys, ["NEED-1", "REQ-1", "TST-1"], "nodes sorted by key");
        let l = &s.nodes[1].links;
        assert!(
            l.len() == 1 && l[0].r#type == "satisfies" && l[0].to == "NEED-1",
            "links by key: {l:?}"
        );
        // each node says where its version comes from; no model wrote the fixture
        let p = &s.nodes[1].provenance;
        assert!(
            p.as_ref()
                .is_some_and(|p| p.version >= 1 && !p.change.is_empty() && !p.model),
            "provenance: {p:?}"
        );
        // JSON round trip
        let data = serde_json::to_vec(&s).expect("marshal");
        let mut s = must(parse_snapshot(&data));

        // the graph already holds it: nothing to import, no change
        let res = must(g.import_snapshot(&ctx, import(&s, "")).await);
        assert!(
            res.change.is_empty() && res.unchanged == 3,
            "nothing to import: {res:?}"
        );

        // a title changed, a node added, a link removed, a link to nowhere
        s.nodes[1].properties = props(&[("title", json!("Use PSP v2"))]);
        s.nodes[2].links = Vec::new();
        s.nodes.push(SnapshotNode {
            key: "TST-2".into(),
            r#type: "TestCase".into(),
            links: vec![
                SnapshotLink {
                    r#type: "verifies".into(),
                    to: "REQ-1".into(),
                    ..Default::default()
                },
                SnapshotLink {
                    r#type: "verifies".into(),
                    to: "NOPE".into(),
                    ..Default::default()
                },
            ],
            ..Default::default()
        });
        let res = must(g.import_snapshot(&ctx, import(&s, "")).await);
        assert!(
            !res.change.is_empty()
                && res.created == 1
                && res.updated == 2
                && res.unchanged == 1
                && res.warnings.len() == 1,
            "import as a change: {res:?}"
        );
        let c = must(g.change(&ctx, &res.change).await);
        assert!(
            c.status == CHANGE_ACTIVE && c.project_id == "PROJ-ROOT",
            "the change stays open for review: {c:?}"
        );
        let latest = must(
            g.node(
                &ctx,
                &NodeRef {
                    id: f.req.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(
            latest.version, f.req.version,
            "nothing lands before the change is merged"
        );
        must(accept_all(g, &res.change).await);
        must(apply(g, &res.change, "").await);
        let after = must(
            g.export_snapshot(&ctx, "", "", &BaselineID::default())
                .await,
        );
        let by_key: BTreeMap<&str, &SnapshotNode> =
            after.nodes.iter().map(|n| (n.key.as_str(), n)).collect();
        assert!(
            after.nodes.len() == 4
                && by_key["REQ-1"].properties.get("title") == Some(&json!("Use PSP v2"))
                && by_key["TST-1"].links.is_empty()
                && by_key["TST-2"].links.len() == 1
                && by_key["TST-2"].links[0].to == "REQ-1",
            "the graph is the snapshot once merged: {:?}",
            after.nodes
        );
        // and importing the state it has is a no-op again
        let res = must(g.import_snapshot(&ctx, import(&after, "")).await);
        assert!(
            res.change.is_empty() && res.unchanged == 4,
            "nothing to import after the merge: {res:?}"
        );
    })
    .await;
}

// A snapshot imported into an empty graph creates every node, links between new nodes included.
#[tokio::test]
async fn snapshot_import_into_empty_graph() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let g = r.graph();
        let s = Snapshot {
            format: SNAPSHOT_FORMAT.into(),
            namespace: DEFAULT_NAMESPACE.into(),
            nodes: vec![
                SnapshotNode {
                    key: "A".into(),
                    r#type: "Need".into(),
                    properties: props(&[("title", json!("a"))]),
                    links: vec![SnapshotLink {
                        r#type: "refines".into(),
                        to: "B".into(),
                        properties: props(&[("why", json!("x"))]),
                    }],
                    ..Default::default()
                },
                SnapshotNode {
                    key: "B".into(),
                    r#type: "Need".into(),
                    links: vec![SnapshotLink {
                        r#type: "refines".into(),
                        to: "A".into(),
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            ],
            ..Default::default()
        };
        let res = must(g.import_snapshot(&ctx, import(&s, "seed")).await);
        assert!(
            !res.change.is_empty() && res.created == 2,
            "created: {res:?}"
        );
        must(accept_all(&g, &res.change).await);
        must(apply(&g, &res.change, "").await);
        let got = must(
            g.export_snapshot(&ctx, "", "", &BaselineID::default())
                .await,
        );
        assert!(
            got.nodes.len() == 2
                && got.nodes[0].links.len() == 1
                && got.nodes[0].links[0].properties.get("why") == Some(&json!("x"))
                && got.nodes[1].links.len() == 1,
            "round trip: {:?}",
            got.nodes
        );
        assert!(
            parse_snapshot(br#"{"format":"other","namespace":"x","nodes":[]}"#).is_err(),
            "an unknown format is refused"
        );
    })
    .await;
}
