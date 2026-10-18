//! convert_test.go.

use goap_methodology::{
    Action, Agent, KIND_HUMAN, Method, Methodology, PLANNER_LLM_SCORING, Process, Reference,
    Responsibilities, Role, Step, Subscription,
};

use super::canon;
use crate::convert::{from_pb, summary_to_pb, to_pb};
use crate::store::Record;

fn rec(m: Methodology) -> Record {
    Record {
        methodology: m,
        ..Record::default()
    }
}

fn base() -> Methodology {
    Methodology {
        name: "m".into(),
        version: "1".into(),
        ..Methodology::default()
    }
}

fn step(name: &str) -> Step {
    Step {
        name: name.into(),
        ..Step::default()
    }
}

#[test]
fn agent_model_round_trips_through_pb() {
    let mut m = base();
    let mut a = Agent::default();
    a.name = "a".into();
    a.planner = PLANNER_LLM_SCORING.into();
    a.model = "fast".into();
    m.agents = vec![a];
    let p = to_pb(&rec(m));
    assert!(
        p.agents.len() == 1 && p.agents[0].model == "fast",
        "to_pb dropped Agent.model"
    );
    let m = from_pb(Some(&p));
    assert!(
        m.agents.len() == 1 && m.agents[0].model == "fast",
        "from_pb dropped Agent.model"
    );
}

#[test]
fn processes_round_trip_through_pb() {
    let mut a = step("a");
    a.action = "do".into();
    a.pre.insert("x".into(), true);
    let mut b = step("b");
    b.action = "act".into();
    b.references = vec![Reference {
        r#ref: "doc:SAD-1".into(),
        ..Reference::default()
    }];
    let mut phase = step("phase");
    phase.steps = vec![a, b];
    let mut nested = step("nested");
    nested.process = "other/p".into();
    nested.done.insert("y".into(), true);
    let mut sign = step("sign");
    sign.instructions = "sign it".into();
    sign.guidance = "how".into();
    sign.checklist = vec!["one".into()];
    sign.deliverables = vec!["Report".into()];
    let mut alt = step("alt");
    alt.actions = vec!["x".into(), "y".into()];
    let mut cap = step("cap");
    cap.capability = "design".into();
    cap.roles = Some(Responsibilities {
        responsible: "dev".into(),
        consulted: vec!["arch".into()],
        ..Responsibilities::default()
    });
    let mut inn = base();
    inn.processes = vec![Process {
        name: "flow".into(),
        description: "d".into(),
        references: vec![Reference {
            title: "Delivery guide".into(),
            r#ref: "document-repository:procedures/delivery.md".into(),
            section: "§2".into(),
        }],
        steps: vec![phase, nested, sign, alt, cap],
        ..Process::default()
    }];
    let mut m1 = Method::default();
    m1.name = "m".into();
    m1.r#for = "design".into();
    m1.when = "true".into();
    m1.priority = 3;
    m1.guidance = "g".into();
    m1.checklist = vec!["c".into()];
    m1.deliverables = vec!["D".into()];
    m1.references = vec![Reference {
        r#ref: "doc:X".into(),
        ..Reference::default()
    }];
    m1.actions = vec!["a".into()];
    m1.done.insert("d".into(), true);
    m1.planner = "hybrid".into();
    m1.model = "m".into();
    m1.mcps = vec!["goap-graph".into()];
    m1.roles = Some(Responsibilities {
        accountable: "lead".into(),
        informed: vec!["po".into()],
        ..Responsibilities::default()
    });
    let mut m2 = Method::default();
    m2.name = "inspect".into();
    m2.r#for = "verification".into();
    let mut note = step("note");
    note.action = "write_note".into();
    let mut verify = step("verify");
    verify.action = "check".into();
    m2.steps = vec![note, verify];
    inn.methods = vec![m1, m2];
    inn.roles = vec![
        Role {
            name: "dev".into(),
            description: "d".into(),
            ..Role::default()
        },
        Role {
            name: "lead".into(),
            ..Role::default()
        },
    ];
    inn.applies_to = vec!["sdlc".into()];
    inn.imports = vec!["risks".into()];
    inn.on = vec![Subscription {
        event: "step.completed".into(),
        filter: r#"event.step.process == "x""#.into(),
        ..Subscription::default()
    }];
    let out = from_pb(Some(&to_pb(&rec(inn.clone()))));
    assert_eq!(out.methods, inn.methods);
    assert_eq!(canon(&out.roles), canon(&inn.roles));
    assert_eq!(out.applies_to, inn.applies_to);
    assert_eq!(out.imports, inn.imports);
    assert_eq!(out.on, inn.on);
    assert_eq!(out.processes, inn.processes);
    let s = summary_to_pb(&rec(inn));
    assert!(
        s.agents.len() == 1 && s.agents[0].name == "flow" && s.agents[0].planner == "process",
        "a process is listed as the agent that runs it: {:?}",
        s.agents
    );
}

// The default criticality of a methodology survives the PB round trip (ADR 0075 §3).
#[test]
fn criticality_round_trips_through_pb() {
    let mut m = base();
    m.criticality = "C3".into();
    assert_eq!(from_pb(Some(&to_pb(&rec(m)))).criticality, "C3");
}

#[test]
fn goal_round_trips_through_pb() {
    let mut m = base();
    m.goal = "deliver".into();
    assert_eq!(from_pb(Some(&to_pb(&rec(m)))).goal, "deliver");
}

// The roles allowed to run an agent or an action (ADR 0043) cross the protocol: the methodology editor saves them.
#[test]
fn run_roles_round_trip_through_pb() {
    let mut m = base();
    let mut a = Agent::default();
    a.name = "a".into();
    a.roles = vec!["release_manager".into()];
    m.agents = vec![a];
    let mut x = Action::default();
    x.name = "x".into();
    x.kind = KIND_HUMAN.into();
    x.roles = vec!["developer".into(), "auditor".into()];
    m.actions = vec![x];
    let back = from_pb(Some(&to_pb(&rec(m))));
    assert_eq!(back.agents[0].roles, vec!["release_manager"]);
    assert_eq!(back.actions[0].roles, vec!["developer", "auditor"]);
}
