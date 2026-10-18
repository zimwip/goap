use std::collections::{BTreeMap, HashMap};

use goap_domain::{ChangeImpact, ChangeImpactID, INTENT_MODIFIED};
use goap_journal::{
    KIND_ACTION, KIND_PROCESS_ENDED, KIND_PROCESS_STARTED, KIND_TICK, ModelCall, Record,
};
use goap_methodology::{Action, Agent, Condition, Goal, Methodology};
use goap_observe::*;
use serde_json::json;

fn rec(kind: &str, action: &str, action_kind: &str, mutate: impl FnOnce(&mut Record)) -> Record {
    let mut r = Record {
        id: format!("{action}{kind}"),
        process_id: "p".into(),
        methodology: "m".into(),
        methodology_version: "1.0.0".into(),
        agent: "ag".into(),
        kind: kind.into(),
        action: action.into(),
        action_kind: action_kind.into(),
        ..Default::default()
    };
    mutate(&mut r);
    r
}

#[test]
fn analyze_findings() {
    let mut nodes = HashMap::new();
    for id in ["n1", "n2"] {
        nodes.insert(
            ChangeImpactID::from(id),
            ChangeImpact {
                intent: INTENT_MODIFIED,
                ..Default::default()
            },
        );
    }
    let mut recs = vec![
        rec(KIND_PROCESS_STARTED, "", "", |_| {}),
        rec(KIND_TICK, "", "", |r| {
            r.data = json!({"replanned": false}).as_object().unwrap().clone();
        }),
        rec(KIND_ACTION, "classify", "llm", |r| {
            r.model_calls = vec![ModelCall {
                input_tokens: 900,
                output_tokens: 100,
                ..Default::default()
            }];
            r.input_tokens = 900;
            r.output_tokens = 100;
            r.nodes = vec!["n1".into(), "n2".into()];
            r.effects_met = Some(true);
        }),
    ];
    for _ in 0..3 {
        recs.push(rec(KIND_ACTION, "check", "human", |r| {
            r.effects_met = Some(false);
            r.duration_ms = 40_000;
        }));
    }
    recs.push(rec(KIND_PROCESS_ENDED, "", "", |r| {
        r.status = "stuck".into()
    }));
    let spans = vec![Span {
        name: "execute_tool crm/search".into(),
        duration_ms: 12_000,
        attributes: BTreeMap::from([("gen_ai.tool.name".to_string(), "crm/search".to_string())]),
        ..Default::default()
    }];
    let r = analyze(
        &recs,
        &HashMap::new(),
        &nodes,
        &spans,
        Thresholds::default(),
    );
    let mut kinds = HashMap::new();
    for f in &r.findings {
        kinds.insert(f.kind.clone(), format!("{}{}", f.action, f.span));
    }
    for (k, want) in [
        (FIND_SYSTEMATIZABLE, "classify"),
        (FIND_LOOP, "check"),
        (FIND_FAILURE, "check"),
        (FIND_SLOW_ACTION, "check"),
        (FIND_SLOW_SPAN, "execute_tool crm/search"),
    ] {
        assert_eq!(
            kinds.get(k).map(String::as_str),
            Some(want),
            "finding {k} ({:?})",
            r.findings
        );
    }
    assert!(
        r.status == "stuck"
            && r.tokens == 1000
            && r.steps == 4
            && r.actions[0].outputs.get("changeImpact/modified") == Some(&2),
        "report: {r:?}"
    );
}

fn action(name: &str, kind: &str, prompt: &str, effect: &str, cost: f64) -> Action {
    let mut a = Action::default();
    a.name = name.into();
    a.kind = kind.into();
    a.prompt = prompt.into();
    a.effects = BTreeMap::from([(effect.to_string(), true)]);
    a.cost = cost;
    a
}

#[test]
fn propose_and_apply() {
    let effects = |c: &str| BTreeMap::from([(c.to_string(), true)]);
    let mut m = Methodology {
        name: "m".into(),
        version: "1.0.0".into(),
        conditions: vec![
            Condition {
                name: "c".into(),
                expr: "true".into(),
                ..Default::default()
            },
            Condition {
                name: "d".into(),
                expr: "true".into(),
                ..Default::default()
            },
        ],
        goals: vec![Goal {
            name: "g".into(),
            pre: effects("c"),
            ..Default::default()
        }],
        ..Default::default()
    };
    m.actions = vec![
        action("classify", "llm", "Classify", "c", 0.0),
        action("check", "human", "", "d", 1.5),
        action("summarize", "llm", "Summarize", "d", 0.0),
    ];
    let mut ag = Agent::default();
    ag.name = "ag".into();
    m.agents = vec![ag];
    let finding = |kind: &str, action: &str, agent: &str| Finding {
        kind: kind.into(),
        action: action.into(),
        agent: agent.into(),
        ..Default::default()
    };
    let r = Report {
        methodology: "m".into(),
        agent: "ag".into(),
        findings: vec![
            finding(FIND_SYSTEMATIZABLE, "classify", "ag"),
            finding(FIND_LLM_HEAVY, "summarize", "ag"),
            finding(FIND_LOOP, "check", "ag"),
            finding(FIND_DISABLED, "check", "ag"),
            Finding {
                kind: FIND_SLOW_SPAN.into(),
                span: "execute_tool crm/search".into(),
                ..Default::default()
            },
            Finding {
                kind: FIND_REPLANNING.into(),
                agent: "ag".into(),
                evidence: "the plan changed".into(),
                ..Default::default()
            },
        ],
        actions: vec![ActionStats {
            action: "classify".into(),
            outputs: BTreeMap::from([("changeImpact/modified".to_string(), 2)]),
            ..Default::default()
        }],
        ..Default::default()
    };
    let (props, notes) = propose(&r, &m);
    assert!(
        props.len() == 5 && notes.len() == 1,
        "proposals {props:?} notes {notes:?}"
    );
    let mut edits: Vec<Edit> = props
        .iter()
        .map(|p| Edit {
            op: p.op.clone(),
            key: p.key.clone(),
            r#type: p.r#type.clone(),
            props: p.props.clone(),
        })
        .collect();
    edits.push(Edit {
        op: "update_node".into(),
        key: "MV:other@1.0.0/action/x".into(),
        props: json!({"cost": 1}).as_object().unwrap().clone(),
        ..Default::default()
    });
    let d = apply(&m, &edits).unwrap();
    assert!(
        d.applied.len() == 5 && d.skipped.len() == 1 && d.tool_requests.len() == 1,
        "draft: {d:?}"
    );
    let out = &d.methodology;
    let is = out.validate();
    assert!(is.0.is_empty(), "draft invalid: {is:?}");
    assert!(
        out.actions[1].cost == 3.0
            && out.actions[2].model == "fast"
            && out.agents[0].actions == ["classify", "summarize"],
        "edits not applied: {:?} {:?}",
        out.actions,
        out.agents
    );
    let spec = &out.actions[3];
    assert!(
        spec.specializes == "classify"
            && spec.priority == 10
            && spec.code.contains("ctx.impactNode"),
        "specialization: {spec:?}"
    );
    assert!(
        m.actions[1].cost == 1.5 && m.actions.len() == 3,
        "Apply must not modify its input"
    );
}

#[test]
fn next_version_bumps() {
    let v = next_version("1.2.3", |v| v == "1.2.4");
    assert_eq!(v, "1.2.5");
    let v = next_version("2024-q1", |_| false);
    assert_eq!(v, "2024-q1.1");
}
