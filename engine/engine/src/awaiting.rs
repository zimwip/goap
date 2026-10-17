//! Waiting or stuck (ADR 0036 §3, Go's `awaiting.go`).

use std::collections::{BTreeMap, BTreeSet};

use goap_goap::{Action as Op, Goal, Planner, WorldState};

use crate::engine::Engine;

impl Engine {
    /// awaited tells a process that waits from one that is stuck, when no plan reaches its goal: a condition is
    /// established outside the process when no action of its agent has it as an effect with the value required. If
    /// assuming those conditions lets the admissible actions reach the goal, the process waits for them: awaited
    /// returns those the plan relies on ("name" when expected true, "!name" when expected false), else nothing.
    pub(crate) fn awaited(
        &self,
        world: &WorldState,
        all: &[Op],
        admissible: &[Op],
        goal: &Goal,
    ) -> Vec<String> {
        awaited_with(&self.planner, world, all, admissible, goal)
    }
}

/// awaited_with is Engine::awaited with the planner given (PreviewPlan plans with the default one).
pub(crate) fn awaited_with(
    planner: &Planner,
    world: &WorldState,
    all: &[Op],
    admissible: &[Op],
    goal: &Goal,
) -> Vec<String> {
    {
        let mut produced: BTreeMap<&str, BTreeSet<bool>> = BTreeMap::new();
        for a in all {
            for (k, v) in &a.effects {
                produced.entry(k.as_str()).or_default().insert(*v);
            }
        }
        let mut wanted: BTreeMap<String, BTreeSet<bool>> = BTreeMap::new();
        let mut want = |req: &BTreeMap<String, bool>| {
            for (k, v) in req {
                if world.0.get(k) == Some(v)
                    || produced.get(k.as_str()).is_some_and(|s| s.contains(v))
                {
                    continue;
                }
                wanted.entry(k.clone()).or_default().insert(*v);
            }
        };
        want(&goal.pre);
        for a in admissible {
            want(&a.pre);
        }
        let mut relaxed = world.clone();
        let mut external = BTreeSet::new();
        for (k, vs) in &wanted {
            if vs.len() != 1 {
                continue; // required both ways: nothing to assume
            }
            let v = *vs.iter().next().unwrap_or(&true);
            relaxed.0.insert(k.clone(), v);
            external.insert(k.clone());
        }
        if external.is_empty() {
            return Vec::new();
        }
        let Ok(plan) = planner.plan(&relaxed, admissible, goal) else {
            return Vec::new();
        };
        let mut out = BTreeSet::new();
        let mut relies = |req: &BTreeMap<String, bool>| {
            for (k, v) in req {
                if !external.contains(k) || relaxed.0.get(k) != Some(v) {
                    continue;
                }
                out.insert(if *v { k.clone() } else { format!("!{k}") });
            }
        };
        relies(&goal.pre);
        for a in &plan.actions {
            relies(&a.pre);
        }
        out.into_iter().collect()
    }
}
