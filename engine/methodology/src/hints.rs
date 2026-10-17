//! Quality remarks on a compiled methodology (ADR 0097, `hints.go`).

use std::collections::{BTreeMap, BTreeSet};

use goap_def::Issue;
use goap_domain::{Blackboard, Change};

use crate::methodology::Compiled;
use crate::process::StepInfo;
use crate::util::{Conds, q};

impl Compiled {
    /// Hints returns the quality remarks on a compiled methodology: things that do not stop it from being published
    /// but that an author wants to know (ADR 0097). They are never compile issues.
    pub fn hints(&self) -> Vec<Issue> {
        let name = self.definition.main_goal();
        let Some(g) = self.goal(&name) else {
            return Vec::new();
        };
        if g.pre.is_empty() {
            return Vec::new();
        }
        // an empty change: one that exists, with nothing on it yet
        let bb = Blackboard {
            change: Change {
                id: "empty".into(),
                ..Change::default()
            },
            ..Blackboard::default()
        };
        let world = self.conditions.evaluate(&bb).state;
        let holds = |want: &Conds| {
            want.iter()
                .all(|(k, v)| world.get(k).copied().unwrap_or(false) == *v)
        };
        if holds(&g.pre) {
            return Vec::new();
        }
        let needed = self.needed_by(&g.pre);
        let towards = |effects: &Conds| effects.keys().any(|k| needed.contains(k));
        let mut waits: BTreeSet<String> = BTreeSet::new();
        let mut possible = false;
        let def = &self.definition;
        if !def.processes.is_empty() {
            let mut nested: BTreeSet<String> = BTreeSet::new();
            fn walk(nested: &mut BTreeSet<String>, this: &str, steps: &[StepInfo]) {
                for s in steps {
                    if !s.process.is_empty() {
                        let (other, proc) = s.nested_process();
                        if other.is_empty() || other == this {
                            nested.insert(proc.to_string());
                        }
                    }
                    walk(nested, this, &s.steps);
                }
            }
            for p in &def.processes {
                walk(
                    &mut nested,
                    &def.name,
                    &self.process_steps(&p.name).unwrap_or_default(),
                );
            }
            for p in &def.processes {
                if nested.contains(&p.name) {
                    continue;
                }
                for s in self.process_steps(&p.name).unwrap_or_default() {
                    if !towards(&s.exit) || (!s.exit.is_empty() && holds(&s.exit)) {
                        continue;
                    }
                    let miss = missing(&world, &s.entry, &s.exit);
                    if !miss.is_empty() {
                        waits.extend(miss);
                        continue;
                    }
                    possible = true;
                }
            }
        } else {
            for a in self.actions.values() {
                if a.is_specialization() || !towards(&a.effects) || holds(&a.effects) {
                    continue;
                }
                let miss = missing(&world, &a.pre, &a.effects);
                if !miss.is_empty() {
                    waits.extend(miss);
                    continue;
                }
                possible = true;
            }
        }
        if possible {
            return Vec::new();
        }
        let first: Vec<String> = waits.into_iter().take(5).collect();
        let mut msg = format!(
            "nothing can start on an empty change towards the goal {}: every step that works towards it waits for a condition",
            q(&name)
        );
        if !first.is_empty() {
            msg += &format!(" (first: {})", first.join(", "));
        }
        vec![Issue::new(
            "goal",
            format!(
                "{msg}; give a first step no precondition, or a condition an empty change satisfies"
            ),
        )]
    }
}

/// missing lists the entry conditions that do not hold in a world, leaving out the activity's own "not done yet"
/// guards, sorted.
pub fn missing(world: &BTreeMap<String, bool>, entry: &Conds, exit: &Conds) -> Vec<String> {
    let mut out = Vec::new();
    for (k, v) in entry {
        if exit.get(k).is_some_and(|w| w != v) {
            continue;
        }
        if world.get(k) != Some(v) {
            out.push(if *v { k.clone() } else { format!("!{k}") });
        }
    }
    out.sort();
    out
}
