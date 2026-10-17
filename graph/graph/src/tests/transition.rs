//! Port of `transition_test.go`: the lifecycle of a change (ADR 0058): its transitions, their gates (a decided
//! decision point, the world, vetos and objectives with derogations, ADR 0075 §3), the freeze of the impacts of a
//! state left, and the default goal of a change (ADR 0096).

use std::collections::BTreeMap;
use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::*;
use goap_graphapi::{ChangePatch, OpenDecisionRequest, RuleRequest, TransitionRequest};
use parking_lot::Mutex;
use serde_json::{Value, json};

use crate::tests::helpers::*;
use crate::tests::lifecycle::new_lifecycle_world;
use crate::testsupport::*;
use crate::{ChangeLifecycles, Error, ErrorKind, Graph, NewChange, Result};

/// Lifecycles is a ChangeLifecycles whose world state the test sets (Go's lifecycles).
#[derive(Default)]
pub(crate) struct Lifecycles {
    pub(crate) lc: Mutex<Lifecycle>,
    pub(crate) world: Mutex<BTreeMap<String, bool>>,
    pub(crate) goal: String,
}

impl Lifecycles {
    fn set(&self, k: &str, v: bool) {
        self.world.lock().insert(k.into(), v);
    }
}

fn guard_err(e: impl std::fmt::Display) -> Error {
    Error::wrap(ErrorKind::Invalid, e.to_string())
}

#[async_trait]
impl ChangeLifecycles for Lifecycles {
    async fn methodology_version(&self, _: &Ctx, _: &str) -> Result<String> {
        Ok(String::new())
    }
    async fn lifecycle(&self, _: &Ctx, _: &str) -> Result<Option<Lifecycle>> {
        Ok(Some(self.lc.lock().clone()))
    }
    async fn default_goal(&self, _: &Ctx, _: &str) -> Result<String> {
        Ok(self.goal.clone())
    }
    /// Guard evaluates expr over the world, the change (with the transition and the decision) and its decision
    /// points.
    async fn guard(
        &self,
        _: &Ctx,
        bb: &Blackboard,
        expr: &str,
        transition: &str,
        decision: &str,
    ) -> Result<bool> {
        let gd = goap_guard::compile(expr).map_err(guard_err)?;
        let mut f = goap_guard::Facts::default();
        for d in decision_points_of(bb) {
            f.decision_points
                .push(json!({"id": d.id, "status": d.status, "option": d.option}));
        }
        let change =
            json!({"id": bb.change.id.to_string(), "transition": transition, "decision": decision});
        let change = change.as_object().cloned().unwrap_or_default();
        let world = self.world.lock().clone();
        gd.check_change(&change, &f, &world).map_err(guard_err)
    }
    /// Gate stands for the guardian of the methodology (the registry's condition.CheckGate, which the graph does not
    /// import, ADR 0125): the vetos and objectives over the world and the decision points, an unmet objective covered
    /// by a derogation in force naming it.
    async fn gate(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        t: &Transition,
        decision: &str,
    ) -> Result<GateResult> {
        let mut res = GateResult::default();
        for c in &t.vetos {
            if !self.guard(ctx, bb, &c.expr, &t.name, decision).await? {
                res.vetoed.push(c.name.clone());
            }
        }
        for c in &t.objectives {
            if self.guard(ctx, bb, &c.expr, &t.name, decision).await? {
                continue;
            }
            res.unmet.push(c.name.clone());
            for d in goap_risk::derogations(&bb.change) {
                if d.rule == c.name && d.in_force(chrono::Utc::now()) {
                    res.reserve = BTreeMap::from([(c.name.clone(), d.key.clone())]);
                    break;
                }
            }
        }
        Ok(res)
    }
    async fn merge(&self, _: &Ctx, _: &Change, _: &Blackboard) -> Result<(bool, bool)> {
        Ok((false, false))
    }
    async fn guardians(&self, _: &Ctx, _: &str, _: &str) -> Result<Vec<GuardianRef>> {
        Ok(Vec::new())
    }
    async fn integrates(&self, _: &Ctx, _: &str, _: &str) -> Result<bool> {
        Ok(false)
    }
    async fn check_guardians(
        &self,
        _: &Ctx,
        _: &Blackboard,
        _: &str,
        _: &[Change],
    ) -> Result<String> {
        Ok(String::new())
    }
}

const GATE_GO: &str = r#"decisionPoints.exists(d, d.id == change.decision && d.status == "decided" && d.option == "go") && world["analysed"]"#;
const GATE_REWORK: &str = r#"decisionPoints.exists(d, d.id == change.decision && d.status == "decided" && d.option == "rework")"#;

fn tr(name: &str, from: &str, to: &str, guard: &str) -> Transition {
    Transition {
        name: name.into(),
        from: from.into(),
        to: to.into(),
        guard: guard.into(),
        ..Default::default()
    }
}

fn st(name: &str, fin: bool) -> LifecycleState {
    LifecycleState {
        name: name.into(),
        r#final: fin,
        ..Default::default()
    }
}

async fn move_change(g: &Graph, id: &ChangeID, name: &str, decision: &str) -> Result<Change> {
    g.transition_change(
        &ctx(),
        id,
        TransitionRequest {
            transition: name.into(),
            decision: decision.into(),
            by: "u".into(),
        },
    )
    .await
}

#[tokio::test]
async fn change_lifecycle() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut w = new_lifecycle_world(r).await;
        let lcs = Arc::new(Lifecycles {
            lc: Mutex::new(Lifecycle {
                name: "maturity".into(),
                initial: "proposed".into(),
                states: vec![
                    st("proposed", false),
                    st("analysing", false),
                    st("implementing", false),
                    st("done", true),
                ],
                transitions: vec![
                    tr("analyse", "proposed", "analysing", ""),
                    tr("implement", "analysing", "implementing", GATE_GO),
                    tr("rework", "implementing", "analysing", GATE_REWORK),
                    tr("finish", "implementing", "done", ""),
                ],
                ..Default::default()
            }),
            ..Default::default()
        });
        lcs.set("analysed", false);
        w.g.lifecycles = Some(lcs.clone());
        w.g.decision_policy = Some(Arc::new(goap_decision::Policy));
        let g = &w.g;

        let c = must(
            g.create_change(
                &ctx,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "lifecycle".into(),
                    methodology: "m".into(),
                    baseline_id: w.base.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert!(
            c.lifecycle == "maturity" && c.state == "proposed",
            "a change starts in the initial state: {:?} {:?}",
            c.lifecycle,
            c.state
        );
        let mut data = serde_json::Map::new();
        data.insert("to".into(), "done".into());
        assert_kind(
            g.add_items(
                &ctx,
                &c.id,
                vec![ChangeItem {
                    r#type: TYPE_TRANSITION,
                    data,
                    ..Default::default()
                }],
            )
            .await,
            ErrorKind::Invalid,
            "a transition is written by TransitionChange only",
        );
        assert_kind(
            move_change(g, &c.id, "finish", "").await,
            ErrorKind::Invalid,
            "no such transition out of proposed",
        );
        must(move_change(g, &c.id, "analyse", "").await);
        let create = async |key: &str| -> ChangeImpactID {
            let ns = must(
                propose_or_create(
                    g,
                    &c.id,
                    vec![ChangeImpact {
                        intent: INTENT_CREATED,
                        key: key.into(),
                        r#type: "Note".into(),
                        rationale: "new".into(),
                        ..Default::default()
                    }],
                )
                .await,
            );
            must(w.write(&c, &ns[0].id, set_props(key)).await);
            ns[0].id.clone()
        };
        let analysis = create("N-1").await;

        let decide = async |option: &str| -> String {
            let mut policy = serde_json::Map::new();
            policy.insert(
                goap_decision::KEY_DECIDER.into(),
                goap_decision::DECIDER_HUMAN.into(),
            );
            let p = must(
                g.open_decision(
                    &ctx,
                    &c.id,
                    OpenDecisionRequest {
                        question: "gate?".into(),
                        policy,
                        ..Default::default()
                    },
                )
                .await,
            );
            must(
                g.rule_decision(
                    &ctx,
                    &c.id,
                    RuleRequest {
                        point: p.id.clone(),
                        outcome: OUTCOME_DECIDED.into(),
                        option: option.into(),
                        confidence: 1.0,
                        justification: "ok".into(),
                        human: true,
                        by: "u".into(),
                        ..Default::default()
                    },
                )
                .await,
            );
            p.id
        };
        let first = decide("go").await;
        assert_kind(
            move_change(g, &c.id, "implement", "").await,
            ErrorKind::Conflict,
            "a gate needs its decision",
        );
        assert_kind(
            move_change(g, &c.id, "implement", &first).await,
            ErrorKind::Conflict,
            "a gate needs the expected world state",
        );
        lcs.set("analysed", true);
        must(move_change(g, &c.id, "implement", &first).await);
        let got = must(g.change(&ctx, &c.id).await);
        assert!(
            got.state == "implementing" && got.state_moves().len() == 2,
            "state {:?}, moves {:?}",
            got.state,
            got.state_moves()
        );

        // the analysis is frozen, what is new is not
        assert_kind(
            w.write(&c, &analysis, set_props("late")).await,
            ErrorKind::Conflict,
            "an impact of a state the change left is frozen",
        );
        let imp = create("N-2").await;
        w.accept(&c).await;
        // the merge guards hold a change that is not in a final state (ADR 0102: FinalStateGuard)
        assert_err(
            g.check_merge(&ctx, &c.id).await,
            ErrorKind::Conflict,
            &["final state"],
            "a change is merged in a final state only",
        );

        // going back needs a new decision: a point gates one transition only
        assert_kind(
            move_change(g, &c.id, "rework", &first).await,
            ErrorKind::Conflict,
            "a decision point is consumed by the transition it gated",
        );
        let go = decide("go").await;
        assert_kind(
            move_change(g, &c.id, "rework", &go).await,
            ErrorKind::Conflict,
            "the decision must say rework",
        );
        let rework = decide("rework").await;
        must(move_change(g, &c.id, "rework", &rework).await);
        must(w.write(&c, &analysis, set_props("revised")).await);
        assert_kind(
            w.write(&c, &imp, set_props("stale")).await,
            ErrorKind::Conflict,
            "the implementation is frozen in turn",
        );
        for n in must(g.list_change_impacts(&ctx, &c.id).await) {
            if n.id == imp {
                assert_eq!(
                    n.review, REVIEW_PROPOSED,
                    "what was written after the phase must be reviewed again"
                );
            }
            if n.id == analysis {
                assert_eq!(
                    n.review, REVIEW_PROPOSED,
                    "the analysis checked out again is reviewed again (ADR 0076)"
                );
            }
        }

        // the lifecycle ends in a final state, then the change applies
        let go = decide("go").await;
        must(move_change(g, &c.id, "implement", &go).await);
        must(move_change(g, &c.id, "finish", "").await);
        w.accept(&c).await;
        must(apply(g, &c.id, "").await);
    })
    .await;
}

fn set_props(t: &str) -> Edit {
    Edit {
        properties: crate::tests::draft::title(t),
        ..Default::default()
    }
}

// A gate splits its criteria in vetos (one unmet blocks) and objectives (an unmet one blocks too unless a derogation
// in force names it, then the transition goes with reserve and says so, ADR 0075 §3).
#[tokio::test]
async fn gate_vetos_and_objectives() {
    goap_risk::register();
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut w = new_lifecycle_world(r).await;
        let lcs = Arc::new(Lifecycles {
            lc: Mutex::new(Lifecycle {
                name: "maturity".into(),
                initial: "proposed".into(),
                states: vec![st("proposed", false), st("done", true)],
                transitions: vec![Transition {
                    vetos: vec![Criterion {
                        name: "no_blocker".into(),
                        expr: r#"!world["blocked"]"#.into(),
                        ..Default::default()
                    }],
                    objectives: vec![Criterion {
                        name: "docs".into(),
                        expr: r#"world["docs"]"#.into(),
                        ..Default::default()
                    }],
                    ..tr("ship", "proposed", "done", "")
                }],
                ..Default::default()
            }),
            ..Default::default()
        });
        lcs.set("blocked", true);
        lcs.set("docs", false);
        w.g.lifecycles = Some(lcs.clone());
        let g = &w.g;
        let c = must(
            g.create_change(
                &ctx,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "gate".into(),
                    methodology: "m".into(),
                    baseline_id: w.base.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        let ship = async || move_change(g, &c.id, "ship", "").await;
        assert_err(
            ship().await,
            ErrorKind::Conflict,
            &["no_blocker"],
            "a veto not met blocks",
        );
        lcs.set("blocked", false);
        assert_err(
            ship().await,
            ErrorKind::Conflict,
            &["docs"],
            "an objective not met blocks",
        );
        let drg = |rule: &str| ChangeItem {
            r#type: goap_risk::TYPE_DEROGATION,
            data: json!({
                "key": "DRG-1", "rule": rule, "target": c.id.to_string(), "reason": "deadline",
                "signatory": "alice",
                "expires": (chrono::Utc::now() + chrono::Duration::hours(1)).to_rfc3339_opts(chrono::SecondsFormat::Secs, true),
            })
            .as_object()
            .cloned()
            .unwrap_or_default(),
            ..Default::default()
        };
        must(g.add_items(&ctx, &c.id, vec![drg("something else")]).await);
        assert_kind(
            ship().await,
            ErrorKind::Conflict,
            "a derogation of another rule covers nothing",
        );
        // a veto is never compensated
        lcs.set("blocked", true);
        must(g.add_items(&ctx, &c.id, vec![drg("docs")]).await);
        assert_kind(
            ship().await,
            ErrorKind::Conflict,
            "a derogation does not waive a veto",
        );
        lcs.set("blocked", false);
        must(ship().await);
        let got = must(g.change(&ctx, &c.id).await);
        let moves = got.items_of_type(&TYPE_TRANSITION);
        assert!(
            got.state == "done" && moves.len() == 1,
            "state {:?}, moves {}",
            got.state,
            moves.len()
        );
        assert!(
            moves[0].data.get("reserve") == Some(&json!({"docs": "DRG-1"}))
                && moves[0].data.get("unmet") == Some(&json!(["docs"])),
            "the move says what it goes with reserve of: {:?}",
            moves[0].data
        );
    })
    .await;
}

// A change starts with the main goal of its methodology, unless it names one; a sub-change works towards its parent's
// goal (ADR 0096). The creation default is not a header edit: nothing is logged.
#[tokio::test]
async fn change_default_goal() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut g = r.graph();
        must(g.bootstrap(&ctx).await);
        // the units of Go's orgWorld the test names (ORG-ACME > ORG-DIGITAL)
        let unit = async |g: &Graph, key: &str, parent: &Node| -> Node {
            must(
                import_node(
                    g,
                    ImportNode {
                        namespace: "organisation".into(),
                        key: key.into(),
                        r#type: NODE_TYPE_ORG_UNIT.into(),
                        properties: props(&[("name", Value::from(key))]),
                        links: vec![goap_graphapi::LinkWrite {
                            r#type: LINK_PART_OF.into(),
                            to: parent.r#ref(),
                            ..Default::default()
                        }],
                        ..Default::default()
                    },
                )
                .await,
            )
        };
        let root = must(g.node_by_key(&ctx, "organisation", &root_org(&g)).await);
        let acme = unit(&g, "ORG-ACME", &root).await;
        unit(&g, "ORG-DIGITAL", &acme).await;
        let base = must(g.branch_head(&ctx, "", MAIN_BRANCH).await);

        let new = async |g: &Graph, mut input: NewChange| -> Change {
            input.project_id = "PROJ-ROOT".into();
            input.baseline_id = base.id.clone();
            must(g.create_change(&ctx, input).await)
        };
        let nc = |title: &str, methodology: &str| NewChange {
            title: title.into(),
            methodology: methodology.into(),
            ..Default::default()
        };
        // no registry: no default
        let c = new(&g, nc("a", "m")).await;
        assert_eq!(c.goal, "", "without a registry");
        g.lifecycles = Some(Arc::new(Lifecycles {
            goal: "deliver".into(),
            ..Default::default()
        }));
        let c = new(
            &g,
            NewChange {
                own_branch: true,
                owner_org: "ORG-DIGITAL".into(),
                ..nc("b", "m")
            },
        )
        .await;
        assert_eq!(c.goal, "deliver", "default goal");
        assert_eq!(
            must(g.change(&ctx, &c.id).await).goal,
            "deliver",
            "stored goal"
        );
        let o = new(
            &g,
            NewChange {
                goal: "other".into(),
                ..nc("c", "m")
            },
        )
        .await;
        assert_eq!(o.goal, "other", "override");
        assert_eq!(new(&g, nc("d", "")).await.goal, "", "no methodology");
        let sub = |title: &str, goal: &str| NewChange {
            title: title.into(),
            parent_id: c.id.clone(),
            goal: goal.into(),
            ..Default::default()
        };
        let s = must(g.create_change(&ctx, sub("s", "")).await);
        assert_eq!(s.goal, "deliver", "sub-change goal");
        must(
            g.update_change(
                &ctx,
                &c.id,
                &ChangePatch {
                    goal: Some("edited".into()),
                    ..Default::default()
                },
            )
            .await,
        );
        let s2 = must(g.create_change(&ctx, sub("s2", "")).await);
        assert_eq!(s2.goal, "edited", "sub-change of an edited parent");
        let s3 = must(g.create_change(&ctx, sub("s3", "own")).await);
        assert_eq!(s3.goal, "own", "sub-change override");
    })
    .await;
}
