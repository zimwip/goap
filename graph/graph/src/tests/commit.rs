//! Port of `commit_test.go`, `commitcycle_test.go` and `parenting_test.go`: a commit of node edits, applied as one
//! change (ADR 0024, 0040, 0054).

use goap_domain::*;
use goap_graphapi::{Commit, LinkEdit, LinkWrite, NodeEdit};
use serde_json::{Value, json};

use crate::tests::helpers::*;
use crate::testsupport::*;
use crate::{ErrorKind, Graph};

fn commit(title: &str, base: &BaselineID, edits: Vec<NodeEdit>) -> Commit {
    Commit {
        project_id: "PROJ-ROOT".into(),
        title: title.into(),
        baseline: base.clone(),
        by: "registry".into(),
        edits,
        ..Default::default()
    }
}

fn link_key(typ: &str, key: &str) -> LinkEdit {
    LinkEdit {
        r#type: typ.into(),
        to_key: key.into(),
        ..Default::default()
    }
}

fn link_to(typ: &str, to: NodeRef) -> LinkEdit {
    LinkEdit {
        r#type: typ.into(),
        to: Some(to),
        ..Default::default()
    }
}

async fn by_key(g: &Graph, key: &str) -> Node {
    must(g.node_by_key(&ctx(), DEFAULT_NAMESPACE, key).await)
}

// Go: TestCommit
#[tokio::test]
async fn commit_edits() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;

        // a header created with the element it defines, in the order that suits the links
        let mut c = commit(
            "Define DOC-1",
            &f.base.id,
            vec![
                NodeEdit {
                    key: "DOC-1".into(),
                    r#type: "Doc".into(),
                    props: props(&[("status", json!("draft"))]),
                    rationale: "new document".into(),
                    links: vec![
                        link_key("defines", "DOC-1/el"),
                        link_to("about", f.need.r#ref()),
                    ],
                    ..Default::default()
                },
                NodeEdit {
                    key: "DOC-1/el".into(),
                    r#type: "Element".into(),
                    props: props(&[("n", json!(1))]),
                    ..Default::default()
                },
            ],
        );
        c.baseline_name = "def".into();
        let res = must(g.commit(&ctx, c).await);
        assert!(!res.baseline.id.is_empty(), "no baseline");
        let doc = by_key(g, "DOC-1").await;
        let el = by_key(g, "DOC-1/el").await;
        let v = must(g.view(&ctx, &doc.r#ref()).await);
        assert_eq!(v.out.len(), 2, "header links: {v:?}");
        let defines = v
            .out
            .iter()
            .find(|l| l.r#type == "defines")
            .map(|l| l.to.clone())
            .unwrap_or_default();
        assert_eq!(defines.id, el.id, "defines must point at the element");
        assert!(
            doc.change_id == res.change
                && doc.comment == "new document"
                && !doc.change_impact.is_empty(),
            "the version must record its origin: {doc:?}"
        );

        // both modified, the header linking to the element: the header follows the element's new version
        let head = must(g.branch_head(&ctx, "", MAIN_BRANCH).await);
        let (doc_ref, el_ref) = (doc.r#ref(), el.r#ref());
        must(
            g.commit(
                &ctx,
                commit(
                    "Edit DOC-1",
                    &head.id,
                    vec![
                        NodeEdit {
                            pre: Some(doc_ref.clone()),
                            props: props(&[("status", json!("published"))]),
                            ..Default::default()
                        },
                        NodeEdit {
                            pre: Some(el_ref.clone()),
                            props: props(&[("n", json!(2))]),
                            ..Default::default()
                        },
                    ],
                ),
            )
            .await,
        );
        let doc2 = by_key(g, "DOC-1").await;
        let el2 = by_key(g, "DOC-1/el").await;
        let v2 = must(g.view(&ctx, &doc2.r#ref()).await);
        for l in &v2.out {
            assert!(
                l.r#type != "defines" || l.to == el2.r#ref(),
                "the carried link must follow the new element version {:?}, got {:?}",
                el2.r#ref(),
                l.to
            );
        }
        assert!(
            doc2.properties["status"] == "published"
                && el2.properties.get("n").and_then(Value::as_f64) == Some(2.0),
            "edits lost: {:?} {:?}",
            doc2.properties,
            el2.properties
        );

        // a link by key may target a node the graph already holds; one the version already carries is not added
        // again
        let head = must(g.branch_head(&ctx, "", MAIN_BRANCH).await);
        let doc2_ref = doc2.r#ref();
        let r = g
            .commit(
                &ctx,
                commit(
                    "Link stored",
                    &head.id,
                    vec![
                        NodeEdit {
                            key: "DOC-2".into(),
                            r#type: "Doc".into(),
                            links: vec![link_key("defines", "DOC-1/el")],
                            ..Default::default()
                        },
                        NodeEdit {
                            pre: Some(doc2_ref),
                            links: vec![link_key("defines", "DOC-1/el")],
                            ..Default::default()
                        },
                    ],
                ),
            )
            .await;
        assert!(r.is_ok(), "a link to a stored node: {r:?}");
        let other = by_key(g, "DOC-2").await;
        let v = must(g.view(&ctx, &other.r#ref()).await);
        assert!(
            v.out.len() == 1 && v.out[0].to.id == el2.id,
            "DOC-2 must define the stored element: {:?}",
            v.out
        );
        let doc3 = by_key(g, "DOC-1").await;
        let v = must(g.view(&ctx, &doc3.r#ref()).await);
        assert_eq!(
            v.out.len(),
            v2.out.len(),
            "the link DOC-1 carries must not be added twice"
        );
        let head = must(g.branch_head(&ctx, "", MAIN_BRANCH).await);
        assert_kind(
            g.commit(
                &ctx,
                commit(
                    "Link nowhere",
                    &head.id,
                    vec![NodeEdit {
                        key: "DOC-3".into(),
                        r#type: "Doc".into(),
                        links: vec![link_key("defines", "NOPE")],
                        ..Default::default()
                    }],
                ),
            )
            .await,
            ErrorKind::Invalid,
            "a link to a node nowhere must be invalid",
        );

        // the element is removed from the document: a modification of its parent, which loses the link (ADR 0024
        // §4); the element keeps its versions. A stale edit conflicts and leaves no open change.
        let head = must(g.branch_head(&ctx, "", MAIN_BRANCH).await);
        let doc3_ref = doc3.r#ref();
        let v = must(g.view(&ctx, &doc3_ref).await);
        let defines_id = v
            .out
            .iter()
            .filter(|l| l.r#type == "defines")
            .map(|l| l.id.clone())
            .next_back()
            .unwrap_or_default();
        must(
            g.commit(
                &ctx,
                commit(
                    "Remove the element",
                    &head.id,
                    vec![NodeEdit {
                        pre: Some(doc3_ref),
                        remove_links: vec![defines_id],
                        ..Default::default()
                    }],
                ),
            )
            .await,
        );
        let doc4 = by_key(g, "DOC-1").await;
        let v = must(g.view(&ctx, &doc4.r#ref()).await);
        assert!(
            !v.out.iter().any(|l| l.r#type == "defines"),
            "the document no longer defines the element: {:?}",
            v.out
        );
        let n = g.node_by_key(&ctx, DEFAULT_NAMESPACE, "DOC-1/el").await;
        assert!(
            matches!(&n, Ok(n) if !n.deleted),
            "the element keeps its versions: {n:?}"
        );
        let err = g
            .commit(
                &ctx,
                commit(
                    "Stale",
                    &head.id,
                    vec![NodeEdit {
                        pre: Some(doc_ref),
                        props: props(&[("status", json!("x"))]),
                        ..Default::default()
                    }],
                ),
            )
            .await
            .unwrap_err();
        assert!(
            err.is_conflict() || err.is_invalid(),
            "an edit based on a stale version must fail, got {err}"
        );
        for c in must(g.changes(&ctx).await) {
            assert!(
                c.title != "Stale" || c.status == CHANGE_ABANDONED,
                "a failed commit must be abandoned, is {}",
                c.status
            );
        }
    })
    .await;
}

// The nodes a commit creates link each other in any direction: a cycle is one change too.
#[tokio::test]
async fn commit_links_created_nodes_in_a_cycle() {
    let ctx = ctx();
    let g = memory_repo().graph();
    let node = |k: &str, next: &str| NodeEdit {
        key: k.into(),
        r#type: "T".into(),
        links: vec![link_key("next", next)],
        ..Default::default()
    };
    let res = must(
        g.commit(
            &ctx,
            Commit {
                project_id: "PROJ-ROOT".into(),
                title: "cycle".into(),
                by: "test".into(),
                edits: vec![node("A", "B"), node("B", "C"), node("C", "A")],
                ..Default::default()
            },
        )
        .await,
    );
    assert!(!res.change.is_empty(), "one change");
    for k in ["A", "B", "C"] {
        let n = must(g.node_by_key(&ctx, "default", k).await);
        let out = must(g.out_links_of(&ctx, &n.r#ref()).await);
        assert_eq!(out.len(), 1, "{k} links: {out:?}");
    }
    let n = must(g.changes(&ctx).await)
        .iter()
        .filter(|c| c.title == "cycle")
        .count();
    assert_eq!(n, 1, "one change for the whole cycle");
    let r = g
        .commit(
            &ctx,
            Commit {
                project_id: "PROJ-ROOT".into(),
                title: "bad".into(),
                by: "test".into(),
                edits: vec![node("X", "nope")],
                ..Default::default()
            },
        )
        .await;
    assert!(
        r.is_err(),
        "a link to a node the commit does not create is refused"
    );
}

fn org_commit(title: &str, base: &BaselineID, edits: Vec<NodeEdit>) -> Commit {
    Commit {
        project_id: "PROJ-ROOT".into(),
        namespace: NAMESPACE_ORGANISATION.into(),
        title: title.into(),
        baseline: base.clone(),
        by: "t".into(),
        edits,
        ..Default::default()
    }
}

// A created OrgUnit/ProjectUnit/User must name exactly one parent/membership link, except the two roots (ADR 0040,
// created by the bootstrap, ADR 0054). Checked before anything is written (checkRequiredParent).
#[tokio::test]
async fn commit_requires_a_parent() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let g = r.graph();
        let ns = NAMESPACE_ORGANISATION;
        // the root unit is the bootstrap's (ADR 0054)
        must(g.bootstrap(&ctx).await);
        let org = must(g.node_by_key(&ctx, ns, &root_org(&g)).await);
        let base = must(g.branch_head(&ctx, ns, MAIN_BRANCH).await);
        let team = |links: Vec<LinkEdit>| NodeEdit {
            key: "TEAM-A".into(),
            r#type: NODE_TYPE_ORG_UNIT.into(),
            props: props(&[("name", json!("A"))]),
            links,
            ..Default::default()
        };

        // no parent at all: refused
        assert_kind(
            g.commit(&ctx, org_commit("team", &base.id, vec![team(vec![])]))
                .await,
            ErrorKind::Invalid,
            "OrgUnit with no part_of",
        );
        // exactly one parent: accepted
        must(
            g.commit(
                &ctx,
                org_commit(
                    "team",
                    &base.id,
                    vec![team(vec![link_to(LINK_PART_OF, org.r#ref())])],
                ),
            )
            .await,
        );
        // the root itself needs none
        let head = must(g.branch_head(&ctx, ns, MAIN_BRANCH).await);
        must(
            g.commit(
                &ctx,
                org_commit(
                    "rename root",
                    &head.id,
                    vec![NodeEdit {
                        pre: Some(org.r#ref()),
                        props: props(&[("name", json!("Default org"))]),
                        ..Default::default()
                    }],
                ),
            )
            .await,
        );
        // a User needs exactly one member_of
        let head = must(g.branch_head(&ctx, ns, MAIN_BRANCH).await);
        assert_kind(
            g.commit(
                &ctx,
                org_commit(
                    "user",
                    &head.id,
                    vec![NodeEdit {
                        key: "USR:alice".into(),
                        r#type: NODE_TYPE_USER.into(),
                        props: props(&[("subject", json!("alice"))]),
                        ..Default::default()
                    }],
                ),
            )
            .await,
            ErrorKind::Invalid,
            "User with no member_of",
        );
    })
    .await;
}

// A modify edit that adds a second member_of without removing the first is rejected after the write, not silently
// accepted: the safety net for a client that built its edit without knowing about the existing link (checked when
// the version is accepted, checkRequiredLinks, ADR 0040).
#[tokio::test]
async fn commit_rejects_a_second_membership() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let g = r.graph();
        let ns = NAMESPACE_ORGANISATION;
        must(g.bootstrap(&ctx).await);
        let root = must(g.node_by_key(&ctx, ns, &root_org(&g)).await);
        let under = vec![LinkWrite {
            r#type: LINK_PART_OF.into(),
            to: root.r#ref(),
            ..Default::default()
        }];
        let unit = |key: &str, name: &str| ImportNode {
            namespace: ns.into(),
            key: key.into(),
            r#type: NODE_TYPE_ORG_UNIT.into(),
            properties: props(&[("name", json!(name))]),
            links: under.clone(),
            ..Default::default()
        };
        let org_a = must(import_node(&g, unit("ORG-A", "A")).await);
        let org_b = must(import_node(&g, unit("ORG-B", "B")).await);
        // member_of ORG-A
        let user = must(
            import_node(
                &g,
                ImportNode {
                    namespace: ns.into(),
                    key: "USR:alice".into(),
                    r#type: NODE_TYPE_USER.into(),
                    properties: props(&[("subject", json!("alice"))]),
                    links: vec![LinkWrite {
                        r#type: LINK_MEMBER_OF.into(),
                        to: org_a.r#ref(),
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            )
            .await,
        );
        let base = must(g.branch_head(&ctx, ns, MAIN_BRANCH).await);

        // a client that never saw the existing member_of ORG-A adds member_of ORG-B without removing it: refused, not
        // leaving alice with two organisations
        assert_kind(
            g.commit(
                &ctx,
                org_commit(
                    "move",
                    &base.id,
                    vec![NodeEdit {
                        pre: Some(user.r#ref()),
                        links: vec![link_to(LINK_MEMBER_OF, org_b.r#ref())],
                        ..Default::default()
                    }],
                ),
            )
            .await,
            ErrorKind::Invalid,
            "adding a second member_of",
        );
        // the correct move (remove the old, add the new) succeeds
        let links = must(g.out_links_of(&ctx, &user.r#ref()).await);
        let old = links
            .iter()
            .filter(|l| l.r#type == LINK_MEMBER_OF)
            .map(|l| l.id.clone())
            .next_back()
            .expect("alice must already be member_of ORG-A");
        must(
            g.commit(
                &ctx,
                org_commit(
                    "move",
                    &base.id,
                    vec![NodeEdit {
                        pre: Some(user.r#ref()),
                        remove_links: vec![old],
                        links: vec![link_to(LINK_MEMBER_OF, org_b.r#ref())],
                        ..Default::default()
                    }],
                ),
            )
            .await,
        );
    })
    .await;
}
