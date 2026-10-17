//! Port of `typecat_test.go`: a graph judged by a type catalogue built from a domain (ADR 0012). Go's `testTypes` is
//! [`crate::testsupport::TestTypes`]; `TestAdminOnlyFollowsTheDomain` is in `tests/structure.rs`.

use std::sync::Arc;

use goap_domain::*;
use goap_graphapi::{Commit, LinkEdit, NodeEdit};
use serde_json::json;

use crate::tests::helpers::{ImportNode, import_node};
use crate::testsupport::*;
use crate::{ErrorKind, Graph, Result};

/// catalogGraph is a graph judged by a type catalogue built from a domain (ADR 0012).
async fn catalog_graph() -> (Graph, Baseline) {
    let d = goap_def::parse_domain(
        br#"
name: docs
version: 1.0.0
lifecycles:
  - name: req
    initial: proposed
    states: [{name: proposed}, {name: draft, notLandable: true}, {name: approved}]
    transitions: [{name: start, from: proposed, to: draft}, {name: approve, from: draft, to: approved}]
algorithms:
  - {name: not-blank, type: property_validator, language: javascript, code: 'return String(ctx.value() || "").trim() !== ""'}
algorithmInstances:
  - {name: title-required, algorithm: not-blank}
nodeTypes:
  - name: Req
    lifecycle: req
    attributes: [{name: title, validators: [title-required]}]
    search: [{property: title, text: true}]
  - {name: SubReq, extends: Req}
  - {name: Test, attributes: [title]}
linkTypes:
  - {name: verifies, from: Test, to: Req}
"#,
    )
    .expect("domain");
    let cat = goap_typecat::new(&[d]).expect("catalogue");
    let mut g = memory_repo().graph();
    use_types(&mut g, Arc::new(cat));
    let b = must(g.branch_head(&ctx(), "docs", MAIN_BRANCH).await);
    (g, b)
}

fn edit(key: &str, typ: &str, title: &str) -> NodeEdit {
    NodeEdit {
        key: key.into(),
        r#type: typ.into(),
        props: if title.is_empty() {
            Default::default()
        } else {
            props(&[("title", json!(title))])
        },
        ..Default::default()
    }
}

fn verifies(typ: &str, to_key: &str) -> Vec<LinkEdit> {
    vec![LinkEdit {
        r#type: typ.into(),
        to_key: to_key.into(),
        ..Default::default()
    }]
}

#[tokio::test]
async fn catalog_judges_the_nodes() {
    let ctx = ctx();
    let (g, b) = catalog_graph().await;
    let commit_in = async |namespace: &str, edits: Vec<NodeEdit>| -> Result<()> {
        g.commit(
            &ctx,
            Commit {
                project_id: "PROJ-ROOT".into(),
                namespace: namespace.into(),
                title: "t".into(),
                baseline: b.id.clone(),
                by: "test".into(),
                edits,
                ..Default::default()
            },
        )
        .await
        .map(|_| ())
    };
    let commit = async |edits: Vec<NodeEdit>| commit_in("docs", edits).await;
    // the existence rule
    assert_kind(
        commit(vec![edit("X", "docs@Nope", "")]).await,
        ErrorKind::Invalid,
        "unknown type",
    );
    assert_kind(
        commit(vec![edit("X", "Req", "x")]).await,
        ErrorKind::Invalid,
        "a bare type is unknown",
    );
    assert_kind(
        commit_in("other", vec![edit("X", "docs@Req", "x")]).await,
        ErrorKind::Invalid,
        "a node lives in the namespace of its type",
    );
    // validators come from the catalogue, inherited by the subtype
    assert_kind(
        commit(vec![edit("S1", "docs@SubReq", " ")]).await,
        ErrorKind::Invalid,
        "the inherited validator rejects a blank title",
    );
    // the lifecycle comes from the catalogue: a created node starts in the initial state
    must(
        commit(vec![
            edit("S1", "docs@SubReq", "one"),
            NodeEdit {
                links: verifies("docs@verifies", "S1"),
                ..edit("T1", "docs@Test", "t")
            },
        ])
        .await,
    );
    let s1 = must(g.node_by_key(&ctx, "docs", "S1").await);
    assert_eq!(
        s1.state, "proposed",
        "initial state of the inherited lifecycle"
    );
    assert!(
        g.lifecycle_controlled("docs@SubReq"),
        "a type with a lifecycle is controlled"
    );
    // link types are checked, ends included
    assert_kind(
        commit(vec![NodeEdit {
            links: verifies("docs@nope", "S1"),
            ..edit("T2", "docs@Test", "t")
        }])
        .await,
        ErrorKind::Invalid,
        "unknown link type",
    );
    assert_kind(
        commit(vec![NodeEdit {
            links: verifies("docs@verifies", "S1"),
            ..edit("S2", "docs@Req", "t")
        }])
        .await,
        ErrorKind::Invalid,
        "the source of verifies must be a Test",
    );
    // direct writes too
    assert_kind(
        import_node(
            &g,
            ImportNode {
                namespace: "docs".into(),
                key: "N".into(),
                r#type: "docs@Nope".into(),
                ..Default::default()
            },
        )
        .await,
        ErrorKind::Invalid,
        "direct write of an unknown type",
    );
    // the built-in domains are always known
    must(
        import_node(
            &g,
            ImportNode {
                namespace: "methodology".into(),
                key: "MV:m@1".into(),
                r#type: "methodology@MethodologyVersion".into(),
                ..Default::default()
            },
        )
        .await,
    );
}
