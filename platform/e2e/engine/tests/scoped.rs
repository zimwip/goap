//! Go: scoped_test.go.

use std::collections::BTreeSet;
use std::sync::Arc;
use std::time::{Duration, Instant};

use goap_ctx::Ctx;
use goap_domain::{self as domain, ChangeID, ChangeImpact};
use goap_engine::{ItemInput, LLMExecutor, STATUS_COMPLETED, STATUS_WAITING, StartRequest};
use goap_graph::{Graph, NewChange};
use goap_llm as llm;
use goap_methodology as methodology;
use parking_lot::Mutex;
use serde_json::json;

use crate::common::*;

/// SCOPED_YAML qualifies every impact of the change, one run per impact (ADR 0109 §3.5).
const SCOPED_YAML: &str = r#"
name: scoped
version: 1.0.0
namespace: alm
conditions:
  - {name: impacted, expr: 'changeImpacts.exists(i, i.intent == "modified")'}
  - {name: qualified, scope: impact, expr: 'artifacts.exists(a, a.target == "qualified" && a.data.impact == self.key)'}
  - {name: all_qualified, all: qualified}
actions:
  - {name: qualify, kind: human, scope: impact, description: Qualify the impact, pre: {impacted: true}, effects: {qualified: true}}
processes:
  - name: analysis
    steps:
      - {name: qualification, action: qualify}
"#;

/// impacted is a change of methodology on alm with the nodes of keys proposed as modified.
async fn impacted(
    g: &Graph,
    methodology: &str,
    base: &domain::BaselineID,
    keys: &[&str],
) -> ChangeID {
    let ctx = ctx();
    let ch = g
        .create_change(
            &ctx,
            NewChange {
                title: "Q".into(),
                intent: "qualify".into(),
                methodology: methodology.into(),
                namespace: "alm".into(),
                project_id: TEST_PROJECT.into(),
                baseline_id: base.clone(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let mut impacts = Vec::new();
    for key in keys {
        let n = g.node_by_key(&ctx, "alm", key).await.unwrap();
        impacts.push(ChangeImpact {
            intent: domain::INTENT_MODIFIED,
            pre: Some(n.r#ref()),
            rationale: "in scope".into(),
            ..Default::default()
        });
    }
    g.propose_impact(&ctx, &ch.id, impacts).await.unwrap();
    ch.id
}

fn qualified(subject: &str) -> Vec<ItemInput> {
    vec![ItemInput {
        r#type: "artifact".into(),
        target: "qualified".into(),
        data: obj(json!({"impact": subject})),
        ..Default::default()
    }]
}

fn start(methodology: &str, id: &ChangeID) -> StartRequest {
    StartRequest {
        methodology: methodology.into(),
        goal: "analysis".into(),
        change_id: id.clone(),
        project_id: TEST_PROJECT.into(),
        ..Default::default()
    }
}

#[tokio::test]
async fn scoped_action_runs_once_per_subject() {
    let ctx = ctx();
    let mut s = setup().await;
    let c = add_yaml(&mut s, SCOPED_YAML);
    let a = c.action("analysis/qualification").unwrap();
    assert!(
        a.is_scoped()
            && a.pre.get("pending:qualify") == Some(&true)
            && a.effects.get("all_qualified") == Some(&true)
            && a.scoped_effects.get("qualified") == Some(&true),
        "lifted action {a:?}"
    );
    let (e, g, base, _errs) = s.share();
    let id = impacted(&g, "scoped", &base, &["REQ-1", "CMP-1"]).await;
    let mut p = e.start(&ctx, start("scoped", &id)).await.unwrap();
    let mut seen = BTreeSet::new();
    for _ in 0..2 {
        p = e.run(&ctx, &p.id).await.unwrap();
        assert!(
            p.status == STATUS_WAITING && p.pending.is_some(),
            "waits for a person to qualify a subject: {} {:?}",
            p.status,
            p.pending
        );
        let subject = p.steps[p.pending.as_ref().unwrap().step as usize]
            .subject
            .clone();
        assert!(
            !subject.is_empty() && !seen.contains(&subject),
            "one run per subject, got {subject:?} after {seen:?}"
        );
        seen.insert(subject.clone());
        p = e.submit(&ctx, &p.id, qualified(&subject)).await.unwrap();
    }
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert!(
        p.status == STATUS_COMPLETED
            && p.world.get("all_qualified") == Some(&true)
            && seen.contains("REQ-1")
            && seen.contains("CMP-1"),
        "completed once every impact is qualified: {} {:?} {seen:?} ({})",
        p.status,
        p.world,
        p.error
    );
}

/// NARROWED_YAML qualifies the impacts in two passes, the requirements first, the components next: two steps run one
/// scoped action, each over its part of the subjects (Step.Where).
const NARROWED_YAML: &str = r#"
name: narrowed
version: 1.0.0
namespace: alm
conditions:
  - {name: impacted, expr: 'changeImpacts.exists(i, i.intent == "modified")'}
  - {name: qualified, scope: impact, expr: 'artifacts.exists(a, a.target == "qualified" && a.data.impact == self.key)'}
  - {name: requirement, scope: impact, expr: '"alm@Requirement" in self.types'}
  - {name: component, scope: impact, expr: '"alm@Component" in self.types'}
  - {name: requirements_qualified, all: qualified, where: self.conditions.requirement, empty: true}
  - {name: components_qualified, all: qualified, where: self.conditions.component, empty: true}
actions:
  - {name: qualify, kind: human, scope: impact, description: Qualify the impact, pre: {impacted: true}, effects: {qualified: true}}
processes:
  - name: analysis
    steps:
      - {name: requirements, action: qualify, where: self.conditions.requirement, done: {requirements_qualified: true}}
      - {name: components, action: qualify, where: self.conditions.component, pre: {requirements_qualified: true}, done: {components_qualified: true}}
"#;

#[tokio::test]
async fn step_narrows_the_subjects_of_a_scoped_action() {
    let c = compile_yaml(NARROWED_YAML);
    let req = c.action("analysis/requirements").unwrap();
    let cmp = c.action("analysis/components").unwrap();
    let t = |m: &methodology::Conds, k: &str| m.get(k) == Some(&true);
    assert!(
        req.narrowed
            && t(&req.pre, "pending:analysis/requirements")
            && t(&req.effects, "requirements_qualified")
            && !t(&req.effects, "components_qualified"),
        "requirements pass {req:?}"
    );
    assert!(
        t(&cmp.pre, "pending:analysis/components")
            && t(&cmp.effects, "components_qualified")
            && !t(&cmp.effects, "requirements_qualified"),
        "components pass {cmp:?}"
    );
    let ctx = ctx();
    let mut s = setup().await;
    s.add(c.clone());
    let (e, g, base, _errs) = s.share();
    let id = impacted(&g, "narrowed", &base, &["CMP-1", "REQ-1"]).await;
    let mut p = e.start(&ctx, start("narrowed", &id)).await.unwrap();
    let mut passes = Vec::new();
    for _ in 0..2 {
        p = e.run(&ctx, &p.id).await.unwrap();
        assert!(
            p.status == STATUS_WAITING && p.pending.is_some(),
            "waits for a person to qualify a subject: {} {:?}",
            p.status,
            p.pending
        );
        let st = p.steps[p.pending.as_ref().unwrap().step as usize].clone();
        passes.push(format!("{} {}", st.action, st.subject));
        p = e.submit(&ctx, &p.id, qualified(&st.subject)).await.unwrap();
    }
    // the requirement first, whatever the order of the impacts
    assert_eq!(
        passes,
        ["analysis/requirements REQ-1", "analysis/components CMP-1"],
        "passes"
    );
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert_eq!(p.status, STATUS_COMPLETED, "analysis: {}", p.error);
}

/// PARALLEL_YAML qualifies every impact with a model, several impacts at once.
const PARALLEL_YAML: &str = r#"
name: parallel
version: 1.0.0
namespace: alm
conditions:
  - {name: impacted, expr: 'changeImpacts.exists(i, i.intent == "modified")'}
  - {name: qualified, scope: impact, expr: 'artifacts.exists(a, a.target == "qualified" && a.data.impact == self.key)'}
  - {name: all_qualified, all: qualified}
actions:
  - name: qualify
    kind: llm
    scope: impact
    parallel: %d
    description: Qualify the impact
    pre: {impacted: true}
    effects: {qualified: true}
    prompt: 'Qualify impact {{ .Subject.key }}.'
processes:
  - name: analysis
    steps:
      - {name: qualification, action: qualify}
"#;

/// subject_of reads the key of "Qualify impact <key>." in a prompt.
fn subject_of(text: &str) -> Option<String> {
    let rest = &text[text.find("Qualify impact ")? + "Qualify impact ".len()..];
    let end = rest.find(char::is_whitespace).unwrap_or(rest.len());
    rest[..end].strip_suffix('.').map(str::to_string)
}

/// The model calls of a scoped action for its subjects are in flight at once (the model answers only once every
/// subject called it), and one after the other with parallel: 1.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn scoped_action_runs_subjects_in_parallel() {
    const KEYS: [&str; 3] = ["REQ-1", "CMP-1", "TST-1"];
    for parallel in [0usize, 1] {
        let ctx = ctx();
        let mut s = setup().await;
        add_yaml(
            &mut s,
            &PARALLEL_YAML.replacen("%d", &parallel.to_string(), 1),
        );
        // (in flight, most at once)
        let flight: Arc<Mutex<(usize, usize)>> = Arc::default();
        {
            let flight = flight.clone();
            s.e.executors.insert(
                methodology::KIND_LLM.into(),
                Arc::new(LLMExecutor {
                    client: Some(llm::ClientFunc::shared(move |_ctx: Ctx, req: llm::Request| {
                        let flight = flight.clone();
                        async move {
                            {
                                let mut f = flight.lock();
                                f.0 += 1;
                                f.1 = f.1.max(f.0);
                            }
                            // hold the answer until every subject called, or give up (the runs are then one after the
                            // other)
                            let deadline = Instant::now() + Duration::from_secs(2);
                            while parallel != 1 && flight.lock().1 != KEYS.len() && Instant::now() < deadline {
                                tokio::time::sleep(Duration::from_millis(5)).await;
                            }
                            flight.lock().0 -= 1;
                            let key = req
                                .messages
                                .iter()
                                .filter_map(|m| subject_of(&m.content))
                                .next_back()
                                .unwrap_or_default();
                            let out = json!({"items": [{"type": "artifact", "target": "qualified", "data": {"impact": key}}]});
                            Ok(llm::Response {
                                text: out.to_string(),
                                provider: "test".into(),
                                model: req.model.clone(),
                                ..Default::default()
                            })
                        }
                    })),
                }),
            );
        }
        let (e, g, base, _errs) = s.share();
        let id = impacted(&g, "parallel", &base, &KEYS).await;
        let p = e.start(&ctx, start("parallel", &id)).await.unwrap();
        let p = e.run(&ctx, &p.id).await.unwrap();
        assert_eq!(
            p.status, STATUS_COMPLETED,
            "parallel={parallel}: analysis: {}",
            p.error
        );
        let want = if parallel == 1 { 1 } else { KEYS.len() };
        let most = flight.lock().1;
        assert_eq!(
            most, want,
            "parallel={parallel}: model calls in flight at most"
        );
        let mut seen = BTreeSet::new();
        for st in &p.steps {
            assert!(
                !st.subject.is_empty() && !seen.contains(&st.subject) && st.effects_met,
                "parallel={parallel}: one successful run per subject: {:?}",
                p.steps
            );
            seen.insert(st.subject.clone());
        }
        assert_eq!(
            seen.len(),
            KEYS.len(),
            "parallel={parallel}: runs for {seen:?}"
        );
    }
}
