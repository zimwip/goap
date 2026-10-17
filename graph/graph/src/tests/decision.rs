//! Port of `decision_test.go`: the decision loop (ADR 0009 §4), its safeguards (rounds, deadline: escalation), the
//! mechanism without a policy (ADR 0067), and a selection settling the points that chose among the options.

use std::sync::Arc;

use chrono::{TimeZone, Utc};
use goap_decision as decision;
use goap_domain::*;
use goap_graphapi::{OpenDecisionRequest, OpenOptionRequest, RuleRequest};
use parking_lot::Mutex;
use serde_json::{Map, Value, json};

use crate::tests::draft::title;
use crate::tests::helpers::*;
use crate::testsupport::*;
use crate::{ErrorKind, Graph, NewChange};

async fn change(f: &Fixture, t: &str, own_branch: bool) -> Change {
    must(
        f.g.create_change(
            &ctx(),
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: t.into(),
                baseline_id: f.base.id.clone(),
                own_branch,
                ..Default::default()
            },
        )
        .await,
    )
}

fn option(name: &str) -> OpenOptionRequest {
    OpenOptionRequest {
        name: name.into(),
        ..Default::default()
    }
}

fn question(q: &str, options: Option<Vec<String>>, policy: Value) -> OpenDecisionRequest {
    OpenDecisionRequest {
        question: q.into(),
        options,
        policy: policy.as_object().cloned().unwrap_or_default(),
        ..Default::default()
    }
}

fn rule(
    point: &str,
    outcome: &str,
    opt: &str,
    confidence: f64,
    justification: &str,
) -> RuleRequest {
    RuleRequest {
        point: point.into(),
        outcome: outcome.into(),
        option: opt.into(),
        confidence,
        justification: justification.into(),
        ..Default::default()
    }
}

// The decision loop (ADR 0009 §4): a point on two options, ruled undecidable with a question, blocked until the
// question is answered, ruled by an agent below the threshold, the ruling ratified by a person: the option is
// selected, the other one rejected.
#[tokio::test]
async fn decision_loop() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut f = new_fixture(r).await;
        f.g.decision_policy = Some(Arc::new(decision::Policy));
        let c = change(&f, "PSP", true).await;
        let g = &f.g;
        let pre = f.req.r#ref();
        let write = async |opt: &str, t: &str| -> NodeRef {
            let added = must(
                g.propose_impact(
                    &ctx,
                    &c.id,
                    vec![ChangeImpact {
                        intent: INTENT_MODIFIED,
                        pre: Some(pre.clone()),
                        rationale: t.into(),
                        flow: opt.into(),
                        ..Default::default()
                    }],
                )
                .await,
            );
            let cn = must(
                edit(
                    g,
                    &c.id,
                    &added[0].id,
                    Edit {
                        flow: opt.into(),
                        properties: title(t),
                        ..Default::default()
                    },
                )
                .await,
            );
            must(accept_on(g, &c.id, opt, "", &cn.id, "u", "ok").await);
            cn.post.expect("post")
        };
        let a = must(g.open_option(&ctx, &c.id, option("stripe")).await);
        let b = must(g.open_option(&ctx, &c.id, option("adyen")).await);
        let va = write(&a.id, "Use Stripe").await;
        write(&b.id, "Use Adyen").await;

        let d = must(
            g.open_decision(
                &ctx,
                &c.id,
                OpenDecisionRequest {
                    question: "Which PSP?".into(),
                    criteria: vec!["fees".into(), "markets".into()],
                    by: "u".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert!(
            d.status == POINT_OPEN
                && d.options.len() == 2
                && decision::threshold(&d) == decision::DEFAULT_THRESHOLD
                && decision::max_rounds(&d) == decision::DEFAULT_ROUNDS,
            "opened: {d:?}"
        );
        assert_kind(
            apply(g, &c.id, "").await,
            ErrorKind::Conflict,
            "apply with a pending decision",
        );
        assert_kind(
            g.rule_decision(&ctx, &c.id, rule(&d.id, OUTCOME_DECIDED, "other", 1.0, "x"))
                .await,
            ErrorKind::Invalid,
            "an option the point does not choose among",
        );
        // undecidable: the fees are unknown
        let d = must(
            g.rule_decision(
                &ctx,
                &c.id,
                RuleRequest {
                    questions: vec!["What are the fees of Adyen?".into()],
                    by: "agent".into(),
                    ..rule(&d.id, OUTCOME_UNDECIDABLE, "", 0.0, "the fees decide")
                },
            )
            .await,
        );
        assert!(
            d.status == POINT_BLOCKED && decision::rounds(&d) == 1 && d.questions.len() == 1,
            "blocked: {d:?}"
        );
        assert_kind(
            g.rule_decision(&ctx, &c.id, rule(&d.id, OUTCOME_DECIDED, &a.id, 1.0, "x"))
                .await,
            ErrorKind::Conflict,
            "a blocked point is not ruled",
        );
        let bb = must(g.blackboard(&ctx, &c.id).await);
        let (points, options) = (decision_points_of(&bb), options_of(&bb));
        assert!(
            points.len() == 1 && options.len() == 2 && points[0].open_questions() == 1,
            "blackboard: {points:?} {options:?}"
        );
        let d = must(
            g.answer_question(
                &ctx,
                &c.id,
                &d.questions[0].id,
                "2.9% per transaction",
                "",
                "analyst",
            )
            .await,
        );
        assert!(
            d.status == POINT_OPEN && !d.questions[0].answer.is_empty(),
            "answered: {d:?}"
        );
        // an agent below the threshold: a person ratifies
        let d = must(
            g.rule_decision(
                &ctx,
                &c.id,
                RuleRequest {
                    by: "agent".into(),
                    ..rule(&d.id, OUTCOME_DECIDED, &a.id, 0.5, "cheaper in our markets")
                },
            )
            .await,
        );
        assert_eq!(d.status, POINT_RATIFYING, "ratifying: {d:?}");
        assert_kind(
            g.rule_decision(&ctx, &c.id, rule(&d.id, OUTCOME_DECIDED, &b.id, 0.9, "x"))
                .await,
            ErrorKind::Conflict,
            "an agent does not rule over a pending ratification",
        );
        let d = must(
            g.ratify_decision(&ctx, &c.id, &d.id, true, "ann", "agreed")
                .await,
        );
        assert!(
            d.status == POINT_DECIDED && d.option == a.id && d.decided_by == "ann",
            "decided: {d:?}"
        );
        let opts = must(g.options(&ctx, &c.id).await);
        assert!(
            opts[0].option_status() == OPTION_SELECTED
                && opts[1].option_status() == OPTION_REJECTED,
            "options after the decision: {opts:?}"
        );
        let res = must(apply(g, &c.id, "").await);
        let head = must(
            g.node(
                &ctx,
                &NodeRef {
                    id: va.id.clone(),
                    version: 0,
                },
            )
            .await,
        );
        assert!(
            res.nodes.get(&va.id) == Some(&2)
                && head.properties.get("title") == Some(&Value::from("Use Stripe")),
            "the chosen option lands: {:?}",
            res.nodes
        );
    })
    .await;
}

// Safeguards: a point that keeps failing to settle, or past its deadline, is escalated: only a person rules it.
#[tokio::test]
async fn decision_escalation() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut f = new_fixture(r).await;
        f.g.decision_policy = Some(Arc::new(decision::Policy));
        let clock = Arc::new(Mutex::new(
            Utc.with_ymd_and_hms(2026, 9, 1, 0, 0, 0).unwrap(),
        ));
        let at = clock.clone();
        f.g.now = Arc::new(move || *at.lock());
        let c = change(&f, "x", false).await;
        let g: &Graph = &f.g;
        let d = must(
            g.open_decision(
                &ctx,
                &c.id,
                question(
                    "Go?",
                    Some(vec![]),
                    json!({decision::KEY_MAX_ROUNDS: 1, decision::KEY_MAX_DURATION: "1h"}),
                ),
            )
            .await,
        );
        let d = must(
            g.rule_decision(
                &ctx,
                &c.id,
                RuleRequest {
                    questions: vec!["cost?".into()],
                    ..rule(&d.id, OUTCOME_UNDECIDABLE, "", 0.0, "unknown")
                },
            )
            .await,
        );
        must(
            g.answer_question(&ctx, &c.id, &d.questions[0].id, "cheap", "", "u")
                .await,
        );
        let d = must(g.decision_points(&ctx, &c.id).await)[0].clone();
        assert!(
            d.status == POINT_ESCALATED && d.needs_human(),
            "one round: escalated: {d:?}"
        );
        assert_kind(
            g.rule_decision(&ctx, &c.id, rule(&d.id, OUTCOME_DECIDED, "", 1.0, "go"))
                .await,
            ErrorKind::Conflict,
            "an agent rules an escalated point",
        );
        let d = must(
            g.rule_decision(
                &ctx,
                &c.id,
                RuleRequest {
                    human: true,
                    by: "ann".into(),
                    ..rule(&d.id, OUTCOME_DECIDED, "", 1.0, "go")
                },
            )
            .await,
        );
        assert_eq!(d.status, POINT_DECIDED, "a person decides: {d:?}");
        // the deadline
        must(
            g.open_decision(
                &ctx,
                &c.id,
                question(
                    "When?",
                    Some(vec![]),
                    json!({decision::KEY_MAX_DURATION: "1h"}),
                ),
            )
            .await,
        );
        {
            let mut now = clock.lock();
            *now += chrono::Duration::hours(2);
        }
        let e = must(g.decision_points(&ctx, &c.id).await)[1].clone();
        assert_eq!(e.status, POINT_ESCALATED, "past the deadline: {e:?}");
    })
    .await;
}

// A graph with no decision policy keeps the mechanism only: whoever rules a point decided settles it, nothing is
// reserved to a person, and policy values are refused (ADR 0067).
#[tokio::test]
async fn decision_without_policy() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let c = change(&f, "x", false).await;
        let g = &f.g;
        assert_kind(
            g.open_decision(
                &ctx,
                &c.id,
                question("Go?", Some(vec![]), json!({"threshold": 0.9})),
            )
            .await,
            ErrorKind::Invalid,
            "policy values without a policy",
        );
        let d = must(
            g.open_decision(&ctx, &c.id, question("Go?", Some(vec![]), json!({})))
                .await,
        );
        assert!(
            d.status == POINT_OPEN && d.policy == Map::new() && !d.needs_human(),
            "opened: {d:?}"
        );
        let d = must(
            g.rule_decision(
                &ctx,
                &c.id,
                RuleRequest {
                    questions: vec!["cost?".into()],
                    ..rule(&d.id, OUTCOME_UNDECIDABLE, "", 0.0, "cost?")
                },
            )
            .await,
        );
        assert_eq!(d.status, POINT_BLOCKED, "blocked: {d:?}");
        let d = must(
            g.answer_question(&ctx, &c.id, &d.questions[0].id, "cheap", "", "u")
                .await,
        );
        let d = must(
            g.rule_decision(
                &ctx,
                &c.id,
                RuleRequest {
                    by: "agent".into(),
                    ..rule(&d.id, OUTCOME_DECIDED, "", 0.01, "go")
                },
            )
            .await,
        );
        assert!(
            d.status == POINT_DECIDED && d.decided_by == "agent",
            "an agent ruling settles the point: {d:?}"
        );
        assert_kind(
            g.ratify_decision(&ctx, &c.id, &d.id, true, "ann", "").await,
            ErrorKind::Conflict,
            "nothing to ratify",
        );
    })
    .await;
}

// Selecting an option by hand settles the decision points that chose among the options.
#[tokio::test]
async fn select_option_settles_its_decision() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let c = change(&f, "x", true).await;
        let g = &f.g;
        let a = must(g.open_option(&ctx, &c.id, option("a")).await);
        must(g.open_option(&ctx, &c.id, option("b")).await);
        must(
            g.open_decision(&ctx, &c.id, question("a or b?", None, json!({})))
                .await,
        );
        must(g.select_option(&ctx, &c.id, &a.id, "ann").await);
        let d = must(g.decision_points(&ctx, &c.id).await)[0].clone();
        assert!(
            d.status == POINT_DECIDED && d.option == a.id && d.decided_by == "ann",
            "settled: {d:?}"
        );
    })
    .await;
}
