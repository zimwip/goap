use super::{load, parse};
use crate::Compiled;
use goap_goap::{Action as Op, Goal as GoapGoal, Planner, WorldState};

fn load_example() -> Compiled {
    load("../../methodologies/examples/impact-analysis.yaml")
        .compile()
        .unwrap()
}

#[test]
fn example_compiles_and_plans() {
    let c = load_example();
    assert!(
        c.conditions.has("expect:propose_tests"),
        "expects must generate a condition"
    );
    let start = WorldState(
        c.conditions
            .names()
            .into_iter()
            .map(|n| (n, false))
            .collect(),
    );
    let g = c.goal("prepare_change").unwrap();
    let ops: Vec<Op> = c
        .planned_actions()
        .iter()
        .map(|a| Op {
            name: a.name.clone(),
            pre: a.pre.clone(),
            effects: a.effects.clone(),
            cost: a.cost,
        })
        .collect();
    let p = Planner::default()
        .plan(
            &start,
            &ops,
            &GoapGoal {
                name: g.name.clone(),
                pre: g.pre.clone(),
                value: g.value,
            },
        )
        .unwrap();
    let got = p.to_string();
    for want in [
        "identify_impacts",
        "propagate_impacts",
        "propose_requirement_updates",
        "propose_tests",
        "review_proposals",
    ] {
        assert!(got.contains(want), "plan {got} misses {want}");
    }
    assert!(
        !got.contains("select_impacts"),
        "cheaper identify_impacts should be preferred: {got}"
    );
}

#[test]
fn validation() {
    for (name, src) in [
        (
            "unknown condition",
            "\nname: m\ngoals: [{name: g, pre: {nope: true}}]",
        ),
        (
            "no effect",
            "\nname: m\nconditions: [{name: c, expr: \"true\"}]\nactions: [{name: a, kind: builtin, builtin: x}]\ngoals: [{name: g, pre: {c: true}}]",
        ),
        (
            "bad kind",
            "\nname: m\nconditions: [{name: c, expr: \"true\"}]\nactions: [{name: a, kind: magic, effects: {c: true}}]\ngoals: [{name: g, pre: {c: true}}]",
        ),
        (
            "bad expr",
            "\nname: m\nconditions: [{name: c, expr: \"impacts +\"}]\ngoals: [{name: g, pre: {c: true}}]",
        ),
        (
            "unknown field",
            "\nname: m\nbogus: 1\ngoals: [{name: g, pre: {c: true}}]",
        ),
    ] {
        let failed = match crate::Methodology::parse(src.as_bytes()) {
            Err(_) => true,
            Ok(m) => m.compile().is_err(),
        };
        assert!(failed, "{name}: expected an error");
    }
}

#[test]
fn issues_are_pathed() {
    let m = parse(
        r#"
name: Bad Name
conditions:
  - {name: ok, expr: "size(changeImpacts) > 0"}
  - {name: broken, expr: "impacts +"}
actions:
  - {name: a, kind: llm, effects: {ok: true, ghost: true}}
goals:
  - {name: g, pre: {}}
"#,
    );
    let issues = m.validate();
    let got: Vec<&str> = issues.0.iter().map(|i| i.path.as_str()).collect();
    for want in [
        "name",
        "conditions[1].expr",
        "actions[0].prompt",
        "actions[0].effects.ghost",
        "goals[0].pre",
    ] {
        assert!(got.contains(&want), "missing issue {want} in {got:?}");
    }
}

#[test]
fn compile_does_not_mutate_and_yaml_round_trip() {
    let m = load("../../methodologies/examples/impact-analysis.yaml");
    let c = m.compile().unwrap();
    for a in &m.actions {
        assert!(
            !a.effects.contains_key(&a.expect_condition()),
            "compile leaked generated effect into the definition: {}",
            a.name
        );
    }
    let a = c.action("propose_tests").unwrap();
    assert!(
        a.effects.get("expect:propose_tests") == Some(&true),
        "compiled action must carry the generated effect"
    );
    let out = m.yaml().unwrap();
    let m2 = crate::Methodology::parse(out.as_bytes())
        .unwrap_or_else(|e| panic!("re-import: {e}\n{out}"));
    m2.compile().unwrap();
    assert!(
        m2.actions.len() == m.actions.len()
            && m2.actions[2].params.get("maxDepth") == m.actions[2].params.get("maxDepth"),
        "round trip lost data"
    );
}
