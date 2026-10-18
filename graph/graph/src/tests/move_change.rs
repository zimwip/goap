//! A change moves to another project with its open sub-changes (ADR 0091, 0106).

use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::*;
use goap_graphapi::{ChangePatch, Commit, LinkEdit, NodeCreate, NodeEdit};
use serde_json::json;

use crate::tests::attach::{Stub, guardian, names};
use crate::testsupport::*;
use crate::{Error, ErrorKind, Graph, NewChange, ProjectMoveGate, Result};

const PROJECT_UNIT: &str = "organisation@ProjectUnit";
const PROJECT_PART_OF: &str = "organisation@project_part_of";

/// moveWorld holds two sibling projects under the root one, and a node landed in the first.
struct MoveWorld {
    g: Graph,
    base: Baseline,
}

async fn new_move_world(r: &TestRepo) -> MoveWorld {
    let ctx = ctx();
    let mut g = r.graph();
    g.caller = Some(Arc::new(|_: &Ctx| "mover".to_string()));
    must(g.bootstrap(&ctx).await);
    let root = must(g.node_by_key(&ctx, "organisation", &root_project(&g)).await);
    for key in ["PROJ-A", "PROJ-B"] {
        must(
            g.commit(
                &ctx,
                Commit {
                    project_id: root_project(&g),
                    namespace: "organisation".into(),
                    title: key.into(),
                    by: "t".into(),
                    edits: vec![NodeEdit {
                        key: key.into(),
                        r#type: PROJECT_UNIT.into(),
                        props: props(&[("name", json!(key))]),
                        links: vec![LinkEdit {
                            r#type: PROJECT_PART_OF.into(),
                            to: Some(root.r#ref()),
                            ..Default::default()
                        }],
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            )
            .await,
        );
    }
    must(
        g.commit(
            &ctx,
            Commit {
                project_id: "PROJ-A".into(),
                title: "old".into(),
                by: "t".into(),
                edits: vec![NodeEdit {
                    key: "OLD-1".into(),
                    r#type: "Design".into(),
                    ..Default::default()
                }],
                ..Default::default()
            },
        )
        .await,
    );
    let base = must(g.branch_head(&ctx, DEFAULT_NAMESPACE, MAIN_BRANCH).await);
    MoveWorld { g, base }
}

impl MoveWorld {
    async fn open(&self, project: &str) -> Change {
        must(
            self.g
                .create_change(
                    &ctx(),
                    NewChange {
                        title: "c".into(),
                        baseline_id: self.base.id.clone(),
                        own_branch: true,
                        project_id: project.into(),
                        ..Default::default()
                    },
                )
                .await,
        )
    }
}

fn sub_of(parent: &ChangeID, title: &str) -> NewChange {
    NewChange {
        title: title.into(),
        parent_id: parent.clone(),
        ..Default::default()
    }
}

fn create(key: &str, rationale: &str) -> NodeCreate {
    NodeCreate {
        key: key.into(),
        r#type: "Design".into(),
        rationale: rationale.into(),
        ..Default::default()
    }
}

/// The header edits of the change.updated entries of a change.
async fn header_edits(g: &Graph, id: &ChangeID) -> Vec<(HeaderEdit, LogEntry)> {
    let (log, _) = must(
        g.change_log(
            &ctx(),
            &LogFilter {
                change: id.clone(),
                types: vec![format!("{LOG_CHANGE}.updated")],
                ..Default::default()
            },
        )
        .await,
    );
    log.into_iter()
        .map(|e| {
            let h: HeaderEdit = serde_json::from_value(e.payload.clone()).expect("header edit");
            (h, e)
        })
        .collect()
}

/// projectMoves are the projectId fields of the change.updated entries of a change.
async fn project_moves(g: &Graph, id: &ChangeID) -> Vec<(HeaderValue, LogEntry)> {
    header_edits(g, id)
        .await
        .into_iter()
        .filter_map(|(h, e)| {
            h.fields
                .get(crate::HEADER_PROJECT_ID)
                .cloned()
                .map(|v| (v, e))
        })
        .collect()
}

// A move changes the project of the root change and of its open sub-changes, in the log of each; the nodes keep their
// project, and the node the change creates afterwards takes the new one when it lands (ADR 0091).
#[tokio::test]
async fn move_change() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_move_world(r).await;
        let g = &w.g;
        let c = w.open("PROJ-A").await;
        let sub = must(g.create_change(&ctx, sub_of(&c.id, "s")).await);
        // a node drafted before the move
        let pre = must(
            g.impact_node_create(&ctx, &c.id, create("PRE-1", "before"))
                .await,
        );

        let moved = must(g.move_change(&ctx, &c.id, "PROJ-B").await);
        assert!(
            moved.id == c.id && moved.project_id == "PROJ-B",
            "moved = {moved:?}"
        );
        assert_eq!(
            must(g.change(&ctx, &sub.id).await).project_id,
            "PROJ-B",
            "the sub-change follows its parent"
        );
        for id in [&c.id, &sub.id] {
            let moves = project_moves(g, id).await;
            assert!(
                moves.len() == 1
                    && moves[0].0.from == json!("PROJ-A")
                    && moves[0].0.to == json!("PROJ-B")
                    && moves[0].1.subject == crate::HEADER_PROJECT_ID
                    && moves[0].1.by == "mover",
                "log of {id}: {moves:?}"
            );
        }
        // the draft made before the move and the one made after take the project of the change at landing
        let post = must(
            g.impact_node_create(&ctx, &c.id, create("POST-1", "after"))
                .await,
        );
        for cn in [&pre, &post] {
            must(
                g.impact_node_review_on(
                    &ctx,
                    &c.id,
                    "",
                    "",
                    &cn.id,
                    REVIEW_ACCEPTED,
                    "reviewer",
                    "ok",
                )
                .await,
            );
        }
        // the sub-change, which wrote nothing, is closed first: it has nothing to merge into its parent (ADR 0102)
        let r = apply(g, &sub.id, "").await;
        assert!(
            r.as_ref().is_err_and(|e| e.is_nothing_to_commit()),
            "an empty sub-change: {r:?}"
        );
        must(
            g.update_change(
                &ctx,
                &sub.id,
                &ChangePatch {
                    status: Some(CHANGE_ABANDONED),
                    ..Default::default()
                },
            )
            .await,
        );
        must(apply(g, &c.id, "").await);
        let b = must(g.node_by_key(&ctx, "organisation", "PROJ-B").await);
        let a = must(g.node_by_key(&ctx, "organisation", "PROJ-A").await);
        for key in ["PRE-1", "POST-1"] {
            let n = must(g.node_by_key(&ctx, "", key).await);
            assert_eq!(n.project, b.id, "{key} lands in the project of the change");
        }
        let n = must(g.node_by_key(&ctx, "", "OLD-1").await);
        assert_eq!(n.project, a.id, "a node keeps its project");
        // a landed change moves no more
        assert_kind(
            g.move_change(&ctx, &c.id, "PROJ-A").await,
            ErrorKind::Conflict,
            "an applied change",
        );
    })
    .await;
}

/// A gate refusing every move, checking what it is asked.
struct RefuseGate {
    family: Vec<ChangeID>,
}

#[async_trait]
impl ProjectMoveGate for RefuseGate {
    async fn check(&self, _: &Ctx, family: &[Change], to: &str) -> Result<()> {
        let ids: Vec<ChangeID> = family.iter().map(|c| c.id.clone()).collect();
        assert!(
            ids == self.family && to == "PROJ-B",
            "gate asked about {ids:?} -> {to}"
        );
        Err(Error::msg("no"))
    }
}

#[tokio::test]
async fn move_change_refusals() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut w = new_move_world(r).await;
        let c = w.open("PROJ-A").await;
        let sub = must(w.g.create_change(&ctx, sub_of(&c.id, "s")).await);
        let refuse =
            async |g: &Graph, what: &str, id: &ChangeID, project: &str, want: ErrorKind| {
                assert_kind(g.move_change(&ctx, id, project).await, want, what);
                let got = must(g.change(&ctx, id).await);
                assert!(
                    !(got.project_id == project && project != "PROJ-A"),
                    "{what}: the change moved"
                );
            };
        {
            let g = &w.g;
            refuse(g, "a sub-change", &sub.id, "PROJ-B", ErrorKind::Invalid).await;
            refuse(g, "the same project", &c.id, "PROJ-A", ErrorKind::Invalid).await;
            refuse(g, "no project", &c.id, "", ErrorKind::Invalid).await;
            refuse(
                g,
                "an unknown project",
                &c.id,
                "PROJ-NOPE",
                ErrorKind::Invalid,
            )
            .await;
            assert_kind(
                g.move_change(
                    &ctx,
                    &"00000000-0000-4000-8000-000000000000".into(),
                    "PROJ-B",
                )
                .await,
                ErrorKind::NotFound,
                "an unknown change",
            );
            // a node that is not a project
            refuse(g, "a unit", &c.id, &root_org(g), ErrorKind::Invalid).await;
        }

        // the gate refuses before anything is written
        w.g.project_move_gate = Some(Arc::new(RefuseGate {
            family: vec![c.id.clone(), sub.id.clone()],
        }));
        {
            let g = &w.g;
            let err = g.move_change(&ctx, &c.id, "PROJ-B").await;
            assert!(
                err.as_ref().is_err_and(|e| e.to_string() == "no"),
                "gate: {err:?}"
            );
            assert!(
                project_moves(g, &c.id).await.is_empty(),
                "a refused move is not logged"
            );
        }
        w.g.project_move_gate = None;

        // committed (its sub-change is abandoned first: a change commits with none open), abandoned
        let g = &w.g;
        let gone = ChangePatch {
            status: Some(CHANGE_ABANDONED),
            ..Default::default()
        };
        must(g.update_change(&ctx, &sub.id, &gone).await);
        let n = must(g.impact_node_create(&ctx, &c.id, create("C-1", "x")).await);
        must(
            g.impact_node_review_on(
                &ctx,
                &c.id,
                "",
                "",
                &n.id,
                REVIEW_ACCEPTED,
                "reviewer",
                "ok",
            )
            .await,
        );
        must(commit_change(g, &c.id, "").await);
        refuse(
            g,
            "a committed change",
            &c.id,
            "PROJ-B",
            ErrorKind::Conflict,
        )
        .await;
        let ab = w.open("PROJ-A").await;
        must(g.update_change(&ctx, &ab.id, &gone).await);
        refuse(
            g,
            "an abandoned change",
            &ab.id,
            "PROJ-B",
            ErrorKind::Conflict,
        )
        .await;
    })
    .await;
}

// A move computes the guardian chain of the change again for its new project, and of its sub-changes from it, in
// the same change.updated entry (ADR 0106).
#[tokio::test]
async fn move_change_guardians() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut w = new_move_world(r).await;
        // projectChains: "release" frames "sdlc" in PROJ-B only
        w.g.lifecycles = Some(Arc::new(Stub {
            guardians: Some(Box::new(|m, project| {
                if m == "sdlc" && project == "PROJ-B" {
                    vec![guardian("release"), guardian("sdlc")]
                } else {
                    vec![guardian(m)]
                }
            })),
            ..Default::default()
        }));
        let g = &w.g;
        let c = must(
            g.create_change(
                &ctx,
                NewChange {
                    title: "c".into(),
                    methodology: "sdlc".into(),
                    baseline_id: w.base.id.clone(),
                    own_branch: true,
                    project_id: "PROJ-A".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        let sub = must(
            g.create_change(
                &ctx,
                NewChange {
                    methodology: "review".into(),
                    ..sub_of(&c.id, "s")
                },
            )
            .await,
        );
        assert!(
            names(&c) == ["sdlc"] && names(&sub) == ["sdlc", "review"],
            "before the move: {:?} / {:?}",
            names(&c),
            names(&sub)
        );
        let moved = must(g.move_change(&ctx, &c.id, "PROJ-B").await);
        assert_eq!(
            names(&moved),
            ["release", "sdlc"],
            "the root takes the chain of its new project"
        );
        assert_eq!(
            names(&must(g.change(&ctx, &sub.id).await)),
            ["release", "sdlc", "review"],
            "the sub-change takes its chain from its parent's"
        );
        let edits = header_edits(g, &c.id).await;
        let last = &edits.last().expect("an entry").0;
        assert!(
            last.fields.contains_key(crate::HEADER_GUARDIANS),
            "the chain is journaled with the move: {:?}",
            last.fields
        );
    })
    .await;
}
