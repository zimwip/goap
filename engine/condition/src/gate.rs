//! The guards and gates of the lifecycle of a change in the environment of the conditions.
use std::collections::BTreeMap;

use chrono::Utc;
use goap_domain::{Blackboard, Criterion, GateResult, Transition};
use goap_guard::Facts;
use serde_json::Value;

use crate::{
    activation::{Activation, Object},
    condition::Error,
};

/// CheckGuard evaluates the guard of a transition of the lifecycle of a change (ADR 0058) in the environment of the
/// conditions: the guard sees the change (`change`, with the transition asked for and, when a decision point gates
/// it, `change.decision`), its decision points, actions, risks, verifications and derogations (`decisionPoints`,
/// `actions`, `risks`, `verifications`, `derogations`), what its organisation requires of its criticality
/// (`criticalityPolicy`, and `change.criticality`), the changes it depends on (`dependencies`, ADR 0108), its
/// history (`history`, ADR 0109) and the world state (`world`, the value of each condition of its methodology; None:
/// empty).
pub fn check_guard(
    expr: &str,
    bb: &Blackboard,
    world: Option<&BTreeMap<String, bool>>,
    transition: &str,
    decision: &str,
) -> Result<bool, Error> {
    let gd = goap_guard::compile(expr)?;
    let (change, f) = guard_inputs(bb, transition, decision);
    Ok(gd.check_change(&change, &f, world.unwrap_or(&BTreeMap::new()))?)
}

/// CheckGuardian evaluates the contribution of an outer methodology of the guardian chain of a change (ADR 0106) to
/// a transition (transition set) or to its merge (transition ""): the environment of check_guard plus `integrator`,
/// the change of that methodology the change is attached to (None: none, goap_guard::no_integrator).
pub fn check_guardian(
    expr: &str,
    bb: &Blackboard,
    world: Option<&BTreeMap<String, bool>>,
    transition: &str,
    integrator: Option<Object>,
) -> Result<bool, Error> {
    let gd = goap_guard::compile(expr)?;
    let (change, mut f) = guard_inputs(bb, transition, "");
    f.integrator = integrator;
    Ok(gd.check_change(&change, &f, world.unwrap_or(&BTreeMap::new()))?)
}

fn list(act: &Activation<'_>, name: &str) -> Vec<Value> {
    act.get(name)
        .and_then(|v| v.as_array())
        .cloned()
        .unwrap_or_default()
}

fn object(act: &Activation<'_>, name: &str) -> Option<Object> {
    act.get(name).and_then(|v| v.as_object()).cloned()
}

fn guard_inputs(bb: &Blackboard, transition: &str, decision: &str) -> (Object, Facts) {
    let act = Activation::new(bb);
    let mut change = object(&act, "change").unwrap_or_default();
    change.insert("transition".into(), Value::from(transition));
    change.insert("decision".into(), Value::from(decision));
    let f = Facts {
        decision_points: list(&act, "decisionPoints"),
        actions: list(&act, "actions"),
        risks: list(&act, "risks"),
        verifications: list(&act, "verifications"),
        derogations: list(&act, "derogations"),
        criticality_policy: object(&act, "criticalityPolicy"),
        dependencies: list(&act, "dependencies"),
        history: object(&act, "history"),
        assessments: list(&act, "assessments"),
        ..Default::default()
    };
    (change, f)
}

/// CheckGate evaluates the vetos and the objectives of a transition (ADR 0075 §3) in the environment of check_guard.
/// A veto or an objective that does not hold is listed; an objective not met is covered when a derogation in force at
/// the instant of the blackboard names it (its rule is the name of the objective): the first one in the register is
/// kept. A criterion that cannot be evaluated is an error, not an unmet one.
pub fn check_gate(
    t: &Transition,
    bb: &Blackboard,
    world: &BTreeMap<String, bool>,
    decision: &str,
) -> Result<GateResult, Error> {
    let mut res = GateResult::default();
    let (change, f) = guard_inputs(bb, &t.name, decision);
    let eval = |c: &Criterion| -> Result<bool, Error> {
        let gd = goap_guard::compile(&c.expr)?;
        Ok(gd.check_change(&change, &f, world)?)
    };
    for c in &t.vetos {
        if !eval(c)? {
            res.vetoed.push(c.name.clone());
        }
    }
    let mut at = bb.at;
    for c in &t.objectives {
        if eval(c)? {
            continue;
        }
        res.unmet.push(c.name.clone());
        let now = *at.get_or_insert_with(Utc::now);
        if let Some(d) = goap_risk::derogations(&bb.change)
            .into_iter()
            .find(|d| d.rule == c.name && d.in_force(now))
        {
            res.reserve.insert(c.name.clone(), d.key);
        }
    }
    Ok(res)
}
