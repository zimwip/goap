//! Port of `lifecycle_test.go`: the lifecycles of the nodes inside a change (ADR 0076, 0078): transitions, their
//! rules, guards and authorizer, the landable states at the merge, the Activity goal replacing them.

use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::*;
use goap_graphapi::{ChangePatch, NodeCheckout, NodeTransition};
use parking_lot::Mutex;
use serde_json::Value;

use crate::tests::draft::title;
use crate::tests::helpers::*;
use crate::testsupport::*;
use crate::{ChangeLifecycles, Error, ErrorKind, Graph, NewChange, Result, TransitionAuthorizer};

/// A world with lifecycles (Go's lcWorld): Requirement proposed → draft (not landable) → approved → draft…; a Spec
/// document contains requirements and can only be released when they are all approved.
pub(crate) struct LcWorld {
    pub(crate) g: Graph,
    pub(crate) req1: Node,
    pub(crate) req2: Node,
    pub(crate) spec: Node,
    pub(crate) base: Baseline,
}

pub(crate) async fn new_lifecycle_world(r: &TestRepo) -> LcWorld {
    new_lifecycle_world_g(r, "").await
}

pub(crate) fn req_lifecycle(approve_guard: &str) -> Lifecycle {
    let tr = |name: &str, from: &str, to: &str| Transition {
        name: name.into(),
        from: from.into(),
        to: to.into(),
        ..Default::default()
    };
    let st = |name: &str, not_landable: bool| LifecycleState {
        name: name.into(),
        not_landable,
        ..Default::default()
    };
    Lifecycle {
        initial: "proposed".into(),
        states: vec![
            st("proposed", false),
            st("draft", true),
            st("approved", false),
        ],
        transitions: vec![
            tr("start", "proposed", "draft"),
            Transition {
                guard: approve_guard.into(),
                requires: TransitionRequires {
                    attributes: vec!["title".into()],
                    ..Default::default()
                },
                ..tr("approve", "draft", "approved")
            },
            tr("reopen", "approved", "draft"),
        ],
        ..Default::default()
    }
}

pub(crate) fn spec_lifecycle() -> Lifecycle {
    Lifecycle {
        initial: "released".into(),
        states: vec![
            LifecycleState {
                name: "draft".into(),
                not_landable: true,
                ..Default::default()
            },
            LifecycleState {
                name: "released".into(),
                ..Default::default()
            },
        ],
        transitions: vec![
            Transition {
                name: "reopen".into(),
                from: "released".into(),
                to: "draft".into(),
                ..Default::default()
            },
            Transition {
                name: "release".into(),
                from: "draft".into(),
                to: "released".into(),
                children: Some(ChildrenRule {
                    states: vec!["approved".into()],
                }),
                ..Default::default()
            },
        ],
        ..Default::default()
    }
}

pub(crate) async fn new_lifecycle_world_g(r: &TestRepo, approve_guard: &str) -> LcWorld {
    let mut g = r.graph();
    let types = TestTypes::new(&[
        (
            "Requirement",
            TestType {
                lifecycle: Some(req_lifecycle(approve_guard)),
                ..Default::default()
            },
        ),
        (
            "Spec",
            TestType {
                lifecycle: Some(spec_lifecycle()),
                ..Default::default()
            },
        ),
        ("Note", TestType::default()),
    ]);
    use_types(&mut g, Arc::new(types));
    let mk =
        async |g: &Graph, key: &str, typ: &str, p: serde_json::Map<String, Value>, state: &str| {
            must(
                seed_node(
                    g,
                    NewNode {
                        key: key.into(),
                        r#type: typ.into(),
                        properties: p,
                        state: state.into(),
                        ..Default::default()
                    },
                )
                .await,
            )
        };
    must(g.bootstrap(&ctx()).await);
    let req1 = mk(&g, "REQ-1", "Requirement", title("one"), "approved").await;
    let req2 = mk(&g, "REQ-2", "Requirement", Default::default(), "proposed").await;
    let spec = must(
        seed_node(
            &g,
            NewNode {
                key: "SPEC-1".into(),
                r#type: "Spec".into(),
                properties: title("spec"),
                state: "released".into(),
                links: vec![
                    (LINK_CONTAINS.into(), req1.r#ref()),
                    (LINK_CONTAINS.into(), req2.r#ref()),
                ],
                ..Default::default()
            },
        )
        .await,
    );
    // the spec is version 1 with its links
    let base = must(pin_baseline(&g, "", &[req1.r#ref(), req2.r#ref(), spec.r#ref()]).await);
    LcWorld {
        g,
        req1,
        req2,
        spec,
        base,
    }
}

impl LcWorld {
    pub(crate) async fn change(&self, t: &str) -> Change {
        must(
            self.g
                .create_change(
                    &ctx(),
                    NewChange {
                        project_id: "PROJ-ROOT".into(),
                        title: t.into(),
                        baseline_id: self.base.id.clone(),
                        ..Default::default()
                    },
                )
                .await,
        )
    }

    /// declare adds the change impact of a node the change modifies.
    pub(crate) async fn declare(&self, c: &Change, n: &Node) -> ChangeImpactID {
        let ns = must(
            self.g
                .propose_impact(
                    &ctx(),
                    &c.id,
                    vec![ChangeImpact {
                        intent: INTENT_MODIFIED,
                        pre: Some(n.r#ref()),
                        rationale: format!("test {}", n.key),
                        ..Default::default()
                    }],
                )
                .await,
        );
        ns[0].id.clone()
    }

    /// write edits a change impact.
    pub(crate) async fn write(&self, c: &Change, id: &ChangeImpactID, e: Edit) -> Result<()> {
        edit(&self.g, &c.id, id, e).await.map(|_| ())
    }

    pub(crate) async fn steps(&self, c: &Change, id: &ChangeImpactID, steps: Vec<Edit>) {
        for e in steps {
            must(self.write(c, id, e).await);
        }
    }

    /// accept accepts the change impacts of a change.
    pub(crate) async fn accept(&self, c: &Change) {
        must(accept_all(&self.g, &c.id).await);
    }
}

pub(crate) fn to_state(s: &str) -> Edit {
    Edit {
        state: s.into(),
        ..Default::default()
    }
}

pub(crate) fn set_title(t: &str) -> Edit {
    Edit {
        properties: title(t),
        ..Default::default()
    }
}

/// mergeGate is a ChangeLifecycles whose only say is on the merge (Go's mergeGate, transition_test.go).
pub(crate) struct MergeGate(pub(crate) Box<MergeFn>);

/// The say of a MergeGate on a merge: (decided, ok).
pub(crate) type MergeFn = dyn Fn(&Change, &Blackboard) -> Result<(bool, bool)> + Send + Sync;

#[async_trait]
impl ChangeLifecycles for MergeGate {
    async fn methodology_version(&self, _: &Ctx, _: &str) -> Result<String> {
        Ok(String::new())
    }
    async fn lifecycle(&self, _: &Ctx, _: &str) -> Result<Option<Lifecycle>> {
        Ok(None)
    }
    async fn default_goal(&self, _: &Ctx, _: &str) -> Result<String> {
        Ok(String::new())
    }
    async fn guard(&self, _: &Ctx, _: &Blackboard, _: &str, _: &str, _: &str) -> Result<bool> {
        Ok(true)
    }
    async fn gate(&self, _: &Ctx, _: &Blackboard, _: &Transition, _: &str) -> Result<GateResult> {
        Ok(GateResult::default())
    }
    async fn merge(&self, _: &Ctx, c: &Change, bb: &Blackboard) -> Result<(bool, bool)> {
        (self.0)(c, bb)
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

// A change scoped to an Activity is gated by its own goal condition (the guardian of the merge,
// ChangeLifecycles::merge), not the node-type lifecycle's landable-state floor: the activity's call replaces the
// blanket "state cannot land" check, rather than adding to it.
#[tokio::test]
async fn activity_goals_gate_replaces_landable_floor() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut w = new_lifecycle_world(r).await;
        let mut data = serde_json::Map::new();
        data.insert("scope".into(), "deliver/draft-requirement".into());
        let c = must(
            w.g.create_change(
                &ctx,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "edit REQ-2".into(),
                    baseline_id: w.base.id.clone(),
                    data,
                    ..Default::default()
                },
            )
            .await,
        );
        let id = w.declare(&c, &w.req2).await;
        w.steps(&c, &id, vec![to_state("draft"), set_title("two")])
            .await;
        w.accept(&c).await;

        // no hook registered: the landable-state floor still applies
        assert_err(
            apply(&w.g, &c.id, "").await,
            ErrorKind::Invalid,
            &["cannot land"],
            "no hook: landable floor must still apply",
        );

        let got: Arc<Mutex<Option<(String, Blackboard)>>> = Arc::new(Mutex::new(None));
        let met = Arc::new(std::sync::atomic::AtomicBool::new(false));
        let (got2, met2) = (got.clone(), met.clone());
        w.g.lifecycles = Some(Arc::new(MergeGate(Box::new(move |c, bb| {
            let r = c.data.get("scope").and_then(|v| v.as_str()).unwrap_or("");
            if r.is_empty() {
                return Ok((false, false));
            }
            *got2.lock() = Some((r.to_string(), bb.clone()));
            Ok((true, met2.load(std::sync::atomic::Ordering::SeqCst)))
        }))));

        // the hook says no: refused, by the activity's own message, not the landable-state one
        assert_err(
            apply(&w.g, &c.id, "").await,
            ErrorKind::Invalid,
            &["does not satisfy the goal"],
            "hook unmet",
        );
        let (got_ref, got_bb) = got.lock().clone().expect("the hook was asked");
        assert!(
            got_ref == "deliver/draft-requirement"
                && got_bb.change.nodes.len() == 1
                && got_bb.change.nodes[0].id == id,
            "hook arguments: ref={got_ref:?} change={:?}",
            got_bb.change
        );
        // the blackboard is hydrated with the pending write this merge is about to land, including its draft state
        let post = got_bb.change.nodes[0]
            .post
            .clone()
            .expect("a post version on the impact");
        let v = got_bb.nodes.get(&post);
        assert!(
            v.is_some_and(|v| v.node.state == "draft" && v.node.properties["title"] == "two"),
            "blackboard must see this change's own pending write: {v:?}"
        );

        // the hook says yes: applies even though REQ-2 is left in the not landable "draft" state
        met.store(true, std::sync::atomic::Ordering::SeqCst);
        must(apply(&w.g, &c.id, "").await);
        let n = must(state_of(&w.g, &w.req2.id).await);
        assert_eq!(n.state, "draft", "REQ-2 must land in draft");
    })
    .await;
}

#[tokio::test]
async fn lifecycle_reopen_edit_and_approve() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_lifecycle_world(r).await;
        let c = w.change("edit REQ-1").await;
        let id = w.declare(&c, &w.req1).await;
        // an approved node is edited in the change like any other: edits are allowed in any state (ADR 0078)
        w.steps(
            &c,
            &id,
            vec![set_title("x"), to_state("draft"), set_title("one v2")],
        )
        .await;
        w.accept(&c).await;
        // the change acts on the node
        let refs = must(w.g.change_impacts(&ctx, &c.id).await);
        assert_eq!(refs, vec![w.req1.r#ref()], "pre versions");
        let cs = must(w.g.node_changes(&ctx, &w.req1.id).await);
        assert!(cs.len() == 1 && cs[0].id == c.id, "node changes: {cs:?}");
        // leaving the node in draft (not landable) is refused at the merge, the node listed with its state
        assert_err(
            apply(&w.g, &c.id, "").await,
            ErrorKind::Invalid,
            &[
                "move them to a landable state first",
                "REQ-1 (Requirement) in draft",
            ],
            "apply must refuse a node left in a state that cannot land, naming it",
        );
        // the refusal leaves the change as it was: the node is still edited in its not landable state, then moved
        must(w.write(&c, &id, set_title("one v3")).await);
        must(w.write(&c, &id, to_state("approved")).await);
        w.accept(&c).await; // the edit sent the review back to proposed (ADR 0077)
        must(apply(&w.g, &c.id, "").await);
        let n = must(state_of(&w.g, &w.req1.id).await);
        assert!(
            n.state == "approved" && n.properties["title"] == "one v3" && n.change_id == c.id,
            "REQ-1 after apply: {n:?}"
        );
        // persisted versions keep their state
        let v1 = must(w.g.node(&ctx, &w.req1.r#ref()).await);
        assert_eq!(v1.state, "approved", "v1 state");
    })
    .await;
}

#[tokio::test]
async fn lifecycle_transition_rules() {
    for_each_repo(async |r: &TestRepo| {
        let w = new_lifecycle_world(r).await;
        // no such transition
        let c = w.change("bad move").await;
        let id = w.declare(&c, &w.req1).await;
        assert_err(
            w.write(&c, &id, to_state("proposed")).await,
            ErrorKind::Invalid,
            &["cannot go from approved to proposed"],
            "unknown transition",
        );
        // a node type without lifecycle has no transitions
        let c = w.change("no lifecycle").await;
        let notes = must(
            propose_or_create(
                &w.g,
                &c.id,
                vec![ChangeImpact {
                    intent: INTENT_CREATED,
                    key: "NOTE-1".into(),
                    r#type: "Note".into(),
                    rationale: "a note".into(),
                    ..Default::default()
                }],
            )
            .await,
        );
        assert!(
            w.write(&c, &notes[0].id, to_state("x")).await.is_err(),
            "a Note has no lifecycle: a state must be refused"
        );
        // required attribute: REQ-2 has no title, the transition is refused when it is taken (ADR 0076)
        let c = w.change("approve without title").await;
        let id = w.declare(&c, &w.req2).await;
        must(w.write(&c, &id, to_state("draft")).await);
        assert_err(
            w.write(&c, &id, to_state("approved")).await,
            ErrorKind::Invalid,
            &[r#"attribute "title" is required"#],
            "requires.attributes",
        );
        // with the attribute set before the approval it goes through
        let c = w.change("approve with title").await;
        let id = w.declare(&c, &w.req2).await;
        w.steps(
            &c,
            &id,
            vec![to_state("draft"), set_title("two"), to_state("approved")],
        )
        .await;
        w.accept(&c).await;
        must(apply(&w.g, &c.id, "").await);
        // REQ-2 is approved now
        let n = must(state_of(&w.g, &w.req2.id).await);
        assert!(
            n.state == "approved" && n.properties["title"] == "two" && n.change_id == c.id,
            "REQ-2: {n:?}"
        );
    })
    .await;
}

// The second change is rebased onto the first at its merge and stops on the conflict (ADR 0101, 0102).
#[tokio::test]
async fn lifecycle_parallel_changes_conflict() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_lifecycle_world(r).await;
        let mut cs = Vec::new();
        for t in ["first", "second"] {
            let c = w.change(t).await;
            let id = w.declare(&c, &w.req1).await;
            w.steps(
                &c,
                &id,
                vec![to_state("draft"), set_title(t), to_state("approved")],
            )
            .await;
            w.accept(&c).await;
            cs.push(c);
        }
        let att = must(w.g.node_changes(&ctx, &w.req1.id).await);
        assert_eq!(att.len(), 2, "both changes act on the node: {att:?}");
        must(apply(&w.g, &cs[0].id, "").await);
        assert_kind(
            apply(&w.g, &cs[1].id, "").await,
            ErrorKind::Conflict,
            "the second change must stop on the conflict",
        );
        let got = must(w.g.change(&ctx, &cs[1].id).await);
        assert_eq!(
            got.status, CHANGE_ACTIVE,
            "the second change must wait for its conflict"
        );
    })
    .await;
}

#[tokio::test]
async fn lifecycle_impact_node_create() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_lifecycle_world(r).await;
        let create = async |c: &Change, key: &str, state: &str| {
            let ns = must(
                propose_or_create(
                    &w.g,
                    &c.id,
                    vec![ChangeImpact {
                        intent: INTENT_CREATED,
                        key: key.into(),
                        r#type: "Requirement".into(),
                        rationale: "new".into(),
                        ..Default::default()
                    }],
                )
                .await,
            );
            w.write(
                c,
                &ns[0].id,
                Edit {
                    properties: title("nine"),
                    state: state.into(),
                    ..Default::default()
                },
            )
            .await
        };
        let c = w.change("create approved").await;
        assert_kind(
            create(&c, "REQ-8", "approved").await,
            ErrorKind::Invalid,
            "no transition proposed → approved",
        );
        let c = w.change("create").await;
        // created in the (landable) initial state
        must(create(&c, "REQ-9", "").await);
        w.accept(&c).await;
        must(apply(&w.g, &c.id, "").await);
        let n = must(w.g.node_by_key(&ctx, "", "REQ-9").await);
        assert_eq!(n.state, "proposed", "created node");
        // created directly in a not landable state: refused when applied
        let c = w.change("create draft").await;
        must(create(&c, "REQ-10", "draft").await);
        w.accept(&c).await;
        assert_err(
            apply(&w.g, &c.id, "").await,
            ErrorKind::Invalid,
            &["REQ-10 (Requirement) in draft"],
            "a created node in draft must be refused",
        );
    })
    .await;
}

#[tokio::test]
async fn lifecycle_document_validates_children() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_lifecycle_world(r).await;
        // reopen the spec and release it again while REQ-2 is only proposed
        let c = w.change("release the spec").await;
        let spec = w.declare(&c, &w.spec).await;
        must(w.write(&c, &spec, to_state("draft")).await);
        assert_err(
            w.write(&c, &spec, to_state("released")).await,
            ErrorKind::Invalid,
            &["REQ-2", "proposed"],
            "children validation",
        );
        // the change approves REQ-2 first: children are judged on the states the change gives them
        let req2 = w.declare(&c, &w.req2).await;
        w.steps(
            &c,
            &req2,
            vec![to_state("draft"), set_title("two"), to_state("approved")],
        )
        .await;
        must(w.write(&c, &spec, to_state("released")).await);
        // the spec went back to the state it was in: not a modification (ADR 0102), the author withdraws it
        must(w.g.withdraw_impact(&ctx, &c.id, &spec, "", "").await);
        w.accept(&c).await;
        must(apply(&w.g, &c.id, "").await);
        // no version of the spec; REQ-2 lands
        let n = must(state_of(&w.g, &w.spec.id).await);
        assert!(n.state == "released" && n.change_id != c.id, "spec: {n:?}");
        let n = must(state_of(&w.g, &w.req2.id).await);
        assert!(n.state == "approved" && n.change_id == c.id, "req2: {n:?}");
    })
    .await;
}

struct DenyReopen;

#[async_trait]
impl TransitionAuthorizer for DenyReopen {
    async fn authorize(&self, _: &Ctx, _: &Node, t: &Transition) -> Result<()> {
        if t.name == "reopen" {
            return Err(Error::msg("reopen denied by the test"));
        }
        Ok(())
    }
}

#[tokio::test]
async fn lifecycle_authorizer_and_guard() {
    for_each_repo(async |r: &TestRepo| {
        let mut w = new_lifecycle_world(r).await;
        w.g.authorizer = Some(Arc::new(DenyReopen));
        let c = w.change("denied").await;
        let id = w.declare(&c, &w.req1).await;
        let err = w.write(&c, &id, to_state("draft")).await.unwrap_err();
        assert!(
            err.to_string().contains("reopen denied by the test"),
            "a transition is authorized when it is taken (ADR 0076): {err}"
        );
        let n = must(state_of(&w.g, &w.req1.id).await);
        assert_eq!(n.state, "approved", "a refused transition writes nothing");
    })
    .await;
}

/// An authorizer that reads the graph (another graph on the same store: the access policies are graph data).
struct Reads {
    g: Graph,
    asked: Arc<Mutex<Vec<String>>>,
}

#[async_trait]
impl TransitionAuthorizer for Reads {
    async fn authorize(&self, ctx: &Ctx, n: &Node, t: &Transition) -> Result<()> {
        self.g.baselines(ctx, &n.namespace).await?;
        self.asked.lock().push(t.name.clone());
        Ok(())
    }
}

// The authorizer reads the graph: it runs outside the transaction of the transition, which would otherwise wait for
// itself.
#[tokio::test]
async fn lifecycle_authorizer_reads_the_graph() {
    for_each_repo(async |r: &TestRepo| {
        let mut w = new_lifecycle_world(r).await;
        let asked = Arc::new(Mutex::new(Vec::new()));
        w.g.authorizer = Some(Arc::new(Reads {
            g: r.graph(),
            asked: asked.clone(),
        }));
        let c = w.change("reads").await;
        let id = w.declare(&c, &w.req1).await;
        let done =
            tokio::time::timeout(Duration::from_secs(10), w.write(&c, &id, to_state("draft")))
                .await;
        match done {
            Ok(r) => must(r),
            Err(_) => panic!(
                "the transition waits for itself: the authorizer runs inside its transaction"
            ),
        }
        assert!(
            !asked.lock().is_empty(),
            "the transition was not authorized"
        );
    })
    .await;
}

#[tokio::test]
async fn lifecycle_guard() {
    for_each_repo(async |r: &TestRepo| {
        // guard: a CEL predicate over the node
        let w = new_lifecycle_world_g(r, r#"node.props.title.startsWith("ok")"#).await;
        for (t, ok) in [("nope", false), ("ok!", true)] {
            let c = w.change(&format!("guard {t}")).await;
            let id = w.declare(&c, &w.req1).await;
            w.steps(&c, &id, vec![to_state("draft"), set_title(t)])
                .await;
            let res = w.write(&c, &id, to_state("approved")).await;
            if ok {
                must(res);
                w.accept(&c).await;
                must(apply(&w.g, &c.id, "").await);
            } else {
                assert_err(res, ErrorKind::Invalid, &["guard not satisfied"], "guard");
            }
        }
    })
    .await;
}

// A transition may require a review (ADR 0076): the review is the change's, its guard reads it from the impact of
// the node.
#[tokio::test]
async fn lifecycle_guard_requires_a_review() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_lifecycle_world_g(r, r#"impact.review == "accepted""#).await;
        let c = w.change("approve after review").await;
        let id = w.declare(&c, &w.req1).await;
        let mv = |to: &str| NodeTransition {
            checkout: NodeCheckout {
                impact: id.clone(),
                ..Default::default()
            },
            to: to.into(),
        };
        must(w.g.impact_node_transition(&ctx, &c.id, mv("draft")).await);
        assert_err(
            w.g.impact_node_transition(&ctx, &c.id, mv("approved"))
                .await,
            ErrorKind::Invalid,
            &["guard not satisfied"],
            "approving before the review",
        );
        must(
            w.g.impact_node_review(&ctx, &c.id, &id, REVIEW_ACCEPTED, "reviewer", "fine")
                .await,
        );
        let cn = must(
            w.g.impact_node_transition(&ctx, &c.id, mv("approved"))
                .await,
        );
        let n = must(
            w.g.change_node(&ctx, &c.id, "", cn.post.as_ref().unwrap())
                .await,
        );
        assert!(
            n.state == "approved" && n.is_draft(),
            "the transition moves the draft: {n:?}"
        );
        // back in the state it was checked out in: not a modification, to withdraw (ADR 0102)
        assert_err(
            apply(&w.g, &c.id, "").await,
            ErrorKind::Conflict,
            &["modifies nothing"],
            "a node back where it was is not a modification",
        );
    })
    .await;
}

#[tokio::test]
async fn change_status_machine() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_lifecycle_world(r).await;
        let c = w.change("status").await;
        let patch = |s: ChangeStatus| ChangePatch {
            status: Some(s),
            ..Default::default()
        };
        must(
            w.g.update_change(&ctx, &c.id, &patch(CHANGE_ABANDONED))
                .await,
        );
        assert_kind(
            w.g.update_change(&ctx, &c.id, &patch(CHANGE_ACTIVE)).await,
            ErrorKind::Conflict,
            "abandoned is final",
        );
        let c2 = w.change("other").await;
        assert_kind(
            w.g.update_change(&ctx, &c2.id, &patch(CHANGE_APPLIED))
                .await,
            ErrorKind::Conflict,
            "only the merge applies a change",
        );
    })
    .await;
}
