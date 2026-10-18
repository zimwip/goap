//! The part of `namespace_test.go` that is the storage's: one key per namespace, the same key in another one, the
//! default namespace and the namespace of a change. (Proposing across namespaces is a later slice's.)

use goap_domain::{DEFAULT_NAMESPACE, MAIN_BRANCH};

use crate::testsupport::*;
use crate::{ErrorKind, NewChange};

#[tokio::test]
async fn namespaces() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let g = r.graph();
        must(g.bootstrap(&c).await);
        let mk = async |ns: &str, typ: &str| {
            land(&g, ns, &[landing("X-1", typ)]).await?;
            g.node_by_key(&c, ns, "X-1").await
        };
        let sdlc = must(mk("", "Thing").await);
        assert_eq!(sdlc.namespace, DEFAULT_NAMESPACE);
        // the same key can live in another namespace
        let org = must(mk("notes", "notes@Note").await);
        assert_ne!(org.id, sdlc.id);
        // a second node of a key in one namespace is refused by the storage
        let dup = seed_node(
            &g,
            NewNode {
                key: "X-1".into(),
                r#type: "Thing".into(),
                ..Default::default()
            },
        )
        .await;
        assert_kind(dup, ErrorKind::Conflict, "duplicate key in one namespace");
        assert_eq!(must(g.node_by_key(&c, "notes", "X-1").await).id, org.id);
        assert_eq!(must(g.node_by_key(&c, "", "X-1").await).id, sdlc.id);
        let base = must(g.branch_head(&c, DEFAULT_NAMESPACE, MAIN_BRANCH).await);
        let ch = must(
            g.create_change(
                &c,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "t".into(),
                    baseline_id: base.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(ch.namespace, DEFAULT_NAMESPACE);
        // a change starts from a baseline of its own namespace
        let other = must(g.branch_head(&c, "notes", MAIN_BRANCH).await);
        let res = g
            .create_change(
                &c,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "t".into(),
                    baseline_id: other.id,
                    ..Default::default()
                },
            )
            .await;
        assert!(
            res.is_err(),
            "a change from a baseline of another namespace"
        );
    })
    .await;
}
