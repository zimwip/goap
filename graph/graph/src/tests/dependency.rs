//! Dependencies between changes (ADR 0108).

use goap_domain::*;
use goap_graphapi::ChangePatch;
use serde_json::json;

use crate::tests::helpers::*;
use crate::tests::subchange::new_org_world;
use crate::testsupport::*;
use crate::{ErrorKind, Graph, HEADER_DEPENDS_ON, NewChange};

fn ids(ds: &[Dependency]) -> Vec<ChangeID> {
    ds.iter().map(|d| d.id.clone()).collect()
}

async fn modify(g: &Graph, c: &Change, n: &Node, title: &str) {
    let added = must(
        g.propose_impact(
            &ctx(),
            &c.id,
            vec![ChangeImpact {
                intent: INTENT_MODIFIED,
                pre: Some(n.r#ref()),
                rationale: title.into(),
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
                properties: props(&[("title", json!(title))]),
                ..Default::default()
            },
        )
        .await,
    );
    must(accept(g, &c.id, &added[0].id, "u", "ok").await);
    must(commit_change(g, &c.id, "").await);
}

#[tokio::test]
async fn change_dependencies() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_org_world(r).await;
        let g = &w.g;
        let create = async |title: &str, parent: Option<&ChangeID>| {
            must(
                g.create_change(
                    &ctx,
                    NewChange {
                        project_id: "PROJ-ROOT".into(),
                        title: title.into(),
                        methodology: "sdlc".into(),
                        baseline_id: w.base.id.clone(),
                        own_branch: parent.is_none(),
                        parent_id: parent.cloned().unwrap_or_default(),
                        ..Default::default()
                    },
                )
                .await,
            )
        };
        let release = create("R1", None).await;
        let b = create("B", Some(&release.id)).await; // created first, merged second
        let a = create("A", Some(&release.id)).await;
        must(
            g.set_change_dependencies(&ctx, &b.id, &[a.id.clone(), a.id.clone()])
                .await,
        );
        let c = must(g.change(&ctx, &b.id).await);
        assert_eq!(c.depends_on, std::slice::from_ref(&a.id), "stored once");
        for (name, id, deps) in [
            ("cycle", &a.id, vec![b.id.clone()]),
            ("itself", &a.id, vec![a.id.clone()]),
            ("ancestor", &a.id, vec![release.id.clone()]),
            ("sub-change", &release.id, vec![a.id.clone()]),
        ] {
            assert_kind(
                g.set_change_dependencies(&ctx, id, &deps).await,
                ErrorKind::Invalid,
                name,
            );
        }

        // the sub-changes of the release merge in their dependency order
        let deps = must(g.change_dependencies(&ctx, &release.id).await);
        assert!(
            ids(&deps.order) == [a.id.clone(), b.id.clone()]
                && deps.order[0].satisfied
                && !deps.order[1].satisfied,
            "merge order: {:?}",
            deps.order
        );
        let got = ids(&must(g.change_dependencies(&ctx, &a.id).await).dependents);
        assert_eq!(got, std::slice::from_ref(&b.id), "dependents of A");
        let bb = must(g.blackboard(&ctx, &b.id).await);
        let fd: Vec<Dependency> = facet(&bb, FACET_DEPENDENCIES);
        assert!(
            fd.len() == 1 && fd[0].id == a.id && !fd[0].satisfied,
            "the guards see the dependencies: {fd:?}"
        );

        modify(g, &b, &w.cmp1, "one by B").await;
        let err = integrate_change(g, &b.id).await.unwrap_err();
        assert!(
            err.is_conflict() && err.to_string().contains(a.id.as_str()),
            "B merges before A: {err}"
        );
        modify(g, &a, &w.cmp2, "two by A").await;
        must(integrate_change(g, &a.id).await);
        let r = integrate_change(g, &b.id).await;
        assert!(r.is_ok(), "B merges once A merged into the release: {r:?}");
        assert_kind(
            g.set_change_dependencies(&ctx, &b.id, &[]).await,
            ErrorKind::Conflict,
            "the dependencies of a merged change no longer change",
        );

        // a root change depending on A waits for the release to land: A's work is on the release's branch only
        let x = create("X", None).await;
        must(
            g.set_change_dependencies(&ctx, &x.id, std::slice::from_ref(&a.id))
                .await,
        );
        modify(g, &x, &w.cmp3, "three by X").await;
        let err = integrate_change(g, &x.id).await.unwrap_err();
        assert!(
            err.is_conflict() && err.to_string().contains("has not landed"),
            "X merges before the release landed: {err}"
        );
        must(integrate_change(g, &release.id).await);
        let r = integrate_change(g, &x.id).await;
        assert!(r.is_ok(), "X merges once the release landed: {r:?}");

        // the edits are journaled; an abandoned change is no dependency
        let y = create("Y", None).await;
        let z = create("Z", None).await;
        must(
            g.update_change(
                &ctx,
                &z.id,
                &ChangePatch {
                    status: Some(CHANGE_ABANDONED),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_kind(
            g.set_change_dependencies(&ctx, &y.id, std::slice::from_ref(&z.id))
                .await,
            ErrorKind::Conflict,
            "depend on an abandoned change",
        );
        let (log, _) = must(
            g.change_log(
                &ctx,
                &LogFilter {
                    change: x.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert!(
            log.iter()
                .any(|e| e.payload.to_string().contains(HEADER_DEPENDS_ON)),
            "the dependencies are journaled: {log:?}"
        );
    })
    .await;
}

// Go: TestDependencyOrder
#[test]
fn dependency_order_follows_the_dependencies() {
    let c = |id: &str, deps: &[&str]| Change {
        id: ChangeID::new(id),
        depends_on: deps.iter().map(|d| ChangeID::new(*d)).collect(),
        ..Default::default()
    };
    let got = goap_domain::dependency_order(&[
        c("d", &["c", "x"]),
        c("c", &["a", "b"]),
        c("b", &[]),
        c("a", &["b"]),
    ]);
    let order: Vec<String> = got.iter().map(|x| x.id.to_string()).collect();
    assert_eq!(order, ["b", "a", "c", "d"], "order");
}
