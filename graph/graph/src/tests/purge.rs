//! A change that landed nothing goes with its log (ADR 0037, 0129).

use goap_domain::*;
use serde_json::json;

use crate::ErrorKind;
use crate::tests::changebranch::set_prop;
use crate::testsupport::*;

#[tokio::test]
async fn purge_change() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;

        // an unapplied change: its versions, log, impacts and branch go, the node it modified is back to v1
        let c = set_prop(
            g,
            &f,
            &f.base.id,
            "A",
            props(&[("title", json!("A title"))]),
        )
        .await;
        must(g.purge_change(&ctx, &c.id).await);
        assert_kind(
            g.change(&ctx, &c.id).await,
            ErrorKind::NotFound,
            "change after purge",
        );
        assert_kind(
            g.branch(&ctx, "", &c.branch).await,
            ErrorKind::NotFound,
            "branch after purge",
        );
        let vs = must(g.versions(&ctx, &f.req.id).await);
        assert_eq!(vs.len(), 1, "versions after purge");
        // the node is still writable: the next version follows v1
        let d = set_prop(
            g,
            &f,
            &f.base.id,
            "B",
            props(&[("title", json!("B title"))]),
        )
        .await;
        must(apply(g, &d.id, "").await);

        // an applied change landed in the graph: it stays
        assert_kind(
            g.purge_change(&ctx, &d.id).await,
            ErrorKind::Conflict,
            "purge of an applied change",
        );

        // a change whose log records an execution keeps it for audit: it can be abandoned, not purged
        let e = set_prop(
            g,
            &f,
            &f.base.id,
            "C",
            props(&[("title", json!("C title"))]),
        )
        .await;
        must(
            g.append_log(
                &ctx,
                vec![LogEntry {
                    change: e.id.clone(),
                    r#type: "model.call".into(),
                    ..Default::default()
                }],
            )
            .await,
        );
        assert_kind(
            g.purge_change(&ctx, &e.id).await,
            ErrorKind::Conflict,
            "purge of a change with a recorded execution",
        );
    })
    .await;
}
