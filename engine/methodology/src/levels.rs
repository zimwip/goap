//! The coherence of the levels of a process or method (`levels.go`).

use std::collections::{BTreeMap, BTreeSet, HashMap};

use serde::{Deserialize, Serialize};

use crate::method::METHOD_CAPABILITY;
use crate::methodology::{Compiled, overlap};
use crate::process::{METHOD_ACTION, METHOD_PROCESS, Step, StepInfo, methods_for};
use crate::processgraph::{GraphEdge, GraphStep, condition_edges};
use crate::review::Review;
use crate::util::Conds;

// Level kinds. Process -> Step -> Method -> MethodStep -> Action: below the last step, an agent operates the actions.
pub const LEVEL_AGENT: &str = "agent";
/// The actions that do a leaf step: its internal steps.
pub const LEVEL_ACTION: &str = "action";
pub const LEVEL_PROCESS: &str = "process";
pub const LEVEL_METHOD: &str = "method";
pub const LEVEL_OF_STEP: &str = "step";

/// GapBlocked: a step of the level can never be entered, whatever the order its siblings run in.
pub const GAP_BLOCKED: &str = "blocked";
/// GapOutput: the level's exit criteria are not all reached by its steps.
pub const GAP_OUTPUT: &str = "output";
/// GapInner: a step of the level is not resolved inside.
pub const GAP_INNER: &str = "inner";
/// GapNoop: a condition is both an input and an output.
pub const GAP_NOOP: &str = "noop";
/// GapExternal: a condition the steps need and nothing in the level establishes.
pub const GAP_EXTERNAL: &str = "external";

/// MethodVariant is the method of a LevelNode that stands for a method specializing a capability.
pub const METHOD_VARIANT: &str = "variant";

/// LevelCheck is the coherence of one level of a process or method.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct LevelCheck {
    /// Path of the parent: the process or method name, or the path of a step with sub-steps.
    pub path: String,
    pub kind: String,
    /// Inputs are the entry conditions of the parent (empty for a process or method), Outputs its exit criteria.
    pub inputs: Conds,
    pub outputs: Conds,
    /// Order lists the direct steps in the order the conditions allow, with the layer each can start in.
    pub order: Vec<LevelStep>,
    pub gaps: Vec<LevelGap>,
    /// Agent and Goal: who operates the level (kind agent) and the goal it plans towards.
    pub agent: String,
    pub goal: String,
    /// Steps are the direct steps of the level, Edges the links their conditions draw between them.
    pub steps: Vec<LevelNode>,
    pub edges: Vec<GraphEdge>,
}

/// LevelNode is a direct step of a level.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct LevelNode {
    pub name: String,
    pub path: String,
    pub method: String,
    pub target: String,
    pub capability: String,
    pub foreach: String,
    pub group_by: String,
    pub review: Option<Review>,
    pub entry: Conds,
    pub exit: Conds,
    /// Composite: the step has sub-steps, a level of its own. Broken: that level, or one below it, is not resolved.
    pub composite: bool,
    pub broken: bool,
    /// SubSteps counts the direct sub-steps.
    pub sub_steps: i64,
}

/// LevelStep is a direct step of a level and the layer it can start in.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct LevelStep {
    pub name: String,
    pub layer: i64,
}

/// LevelGap is a break in the chain of a level.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct LevelGap {
    /// Step is the direct step concerned (empty: the outputs of the level).
    pub step: String,
    pub kind: String,
    pub missing: Vec<String>,
    pub message: String,
}

impl LevelCheck {
    /// OK says whether the level has no gap that breaks the chain.
    pub fn ok(&self) -> bool {
        self.gaps.iter().all(|g| g.kind == GAP_EXTERNAL)
    }
}

const NOOP_MESSAGE: &str =
    "inputs that are also outputs: an activity does not take what it makes true";

impl Compiled {
    /// CheckLevels checks, level by level, that a process (or method composing its own steps) can be planned. None
    /// when the methodology has no such process or method.
    pub fn check_levels(&self, name: &str) -> Option<Vec<LevelCheck>> {
        let mut kind = LEVEL_PROCESS;
        let mut steps = self.process_steps(name);
        if steps.is_none() {
            kind = LEVEL_METHOD;
            steps = self.method_steps(name);
        }
        let Some(steps) = steps else {
            // a method of actions alone: its agent operates them
            let me = self.method_by_name(name)?;
            if me.actions.is_empty() {
                return None;
            }
            let mut lc =
                self.operator_level(name, &[], me.actor_agent(), &me.name, None, None, "")?;
            lc.kind = LEVEL_METHOD.into();
            return Some(vec![lc]);
        };
        let steps: Vec<StepInfo> = steps.into_iter().map(lift).collect();
        let mut root = StepInfo {
            path: name.to_string(),
            ..StepInfo::default()
        };
        for s in &steps {
            root.exit
                .extend(s.exit.iter().map(|(k, v)| (k.clone(), *v)));
        }
        root.steps = steps;
        let mut levels: Vec<LevelCheck> = Vec::new();
        fn walk(c: &Compiled, levels: &mut Vec<LevelCheck>, parent: &StepInfo, kind: &str) {
            levels.push(check_level(parent, kind));
            for s in &parent.steps {
                if !s.steps.is_empty() {
                    walk(c, levels, s, LEVEL_OF_STEP);
                } else if let Some(lc) = c.capability_level(s) {
                    levels.push(lc);
                } else if let Some(lc) = c.action_level(s) {
                    levels.push(lc);
                }
            }
        }
        walk(self, &mut levels, &root, kind);
        if kind == LEVEL_METHOD
            && let Some(me) = self.method_by_name(name)
        {
            // the agent performs the activities that compose it
            levels[0].agent = me.actor_agent().to_string();
            levels[0].goal = me.name.clone();
        }
        // what is not resolved inside a step surfaces in the level above
        let mut by_path: HashMap<String, usize> = HashMap::new();
        for (i, l) in levels.iter().enumerate() {
            by_path.insert(l.path.clone(), i);
        }
        // a step done by an action or an agent opens on the actions: they are its level
        for i in 0..levels.len() {
            for j in 0..levels[i].steps.len() {
                let n = &levels[i].steps[j];
                if let Some(&b) = by_path.get(&n.path)
                    && !n.composite
                {
                    let count = levels[b].steps.len() as i64;
                    levels[i].steps[j].composite = true;
                    levels[i].steps[j].sub_steps = count;
                }
            }
        }
        fn is_broken(
            path: &str,
            levels: &[LevelCheck],
            by_path: &HashMap<String, usize>,
            broken: &mut HashMap<String, bool>,
        ) -> bool {
            if let Some(v) = broken.get(path) {
                return *v;
            }
            broken.insert(path.to_string(), false);
            let Some(&i) = by_path.get(path) else {
                return false;
            };
            let l = &levels[i];
            let mut bad = !l.ok();
            for n in &l.steps {
                if n.composite && is_broken(&n.path, levels, by_path, broken) {
                    bad = true;
                }
            }
            broken.insert(path.to_string(), bad);
            bad
        }
        let mut broken: HashMap<String, bool> = HashMap::new();
        for i in 0..levels.len() {
            for j in 0..levels[i].steps.len() {
                let (composite, path, name) = {
                    let n = &levels[i].steps[j];
                    (n.composite, n.path.clone(), n.name.clone())
                };
                if composite && is_broken(&path, &levels, &by_path, &mut broken) {
                    levels[i].steps[j].broken = true;
                    levels[i].gaps.push(LevelGap {
                        step: name,
                        kind: GAP_INNER.into(),
                        missing: Vec::new(),
                        message:
                            "not resolved inside: rework it, the process cannot run through it"
                                .into(),
                    });
                }
            }
        }
        Some(levels)
    }

    /// capabilityLevel is the level below a step that names a capability: the methods that specialize it.
    fn capability_level(&self, s: &StepInfo) -> Option<LevelCheck> {
        if s.method() != METHOD_CAPABILITY {
            return None;
        }
        let methods = methods_for(&self.definition, &s.capability, &self.methods.goals);
        if methods.is_empty() {
            return None;
        }
        let mut lc = LevelCheck {
            path: s.path.clone(),
            kind: LEVEL_OF_STEP.into(),
            inputs: s.entry.clone(),
            outputs: s.exit.clone(),
            ..LevelCheck::default()
        };
        for me in methods {
            let target = if me.when.is_empty() {
                "always".to_string()
            } else {
                format!("when {}", me.when)
            };
            lc.steps.push(LevelNode {
                name: me.name.clone(),
                path: format!("{}/{}", s.path, me.name),
                method: METHOD_VARIANT.into(),
                target,
                capability: s.capability.clone(),
                entry: s.entry.clone(),
                exit: self
                    .methods
                    .goals
                    .get(&me.name)
                    .map(|g| g.pre.clone())
                    .unwrap_or_default(),
                ..LevelNode::default()
            });
            lc.order.push(LevelStep {
                name: me.name.clone(),
                layer: 0,
            });
        }
        Some(lc)
    }

    /// actionLevel is the level below a leaf step done by an action or by alternative actions.
    fn action_level(&self, s: &StepInfo) -> Option<LevelCheck> {
        if s.method() != METHOD_ACTION {
            return None;
        }
        let mut lc = self.operator_level(
            &s.path,
            &s.actions,
            "",
            "",
            Some(&s.entry),
            Some(&s.exit),
            &s.action,
        )?;
        lc.kind = LEVEL_ACTION.into();
        Some(lc)
    }

    /// operatorLevel is the level below a step done by an action, alternative actions or an agent: the actions
    /// themselves. entry / exit None stand for Go's nil maps.
    #[allow(clippy::too_many_arguments)]
    fn operator_level(
        &self,
        path: &str,
        alternatives: &[String],
        agent: &str,
        goal: &str,
        entry: Option<&Conds>,
        exit: Option<&Conds>,
        action: &str,
    ) -> Option<LevelCheck> {
        let mut acts: Vec<(String, Conds, Conds)> = Vec::new();
        let mut goal = goal.to_string();
        let mut parent_exit = exit.cloned();
        if !agent.is_empty() {
            let ag = self.agent(agent)?;
            for a in self.agent_actions(ag) {
                acts.push((a.name.clone(), a.pre.clone(), a.effects.clone()));
            }
            if goal.is_empty() && ag.goals.len() == 1 {
                goal = ag.goals[0].clone();
            }
            if let Some(g) = self.goal(&goal)
                && parent_exit.is_none()
            {
                parent_exit = Some(g.pre);
            }
        } else {
            let names: Vec<String> = if !action.is_empty() {
                vec![action.to_string()]
            } else {
                alternatives.to_vec()
            };
            for n in &names {
                if let Some(a) = self.action(n) {
                    acts.push((a.name.clone(), a.pre.clone(), a.effects.clone()));
                }
            }
        }
        if acts.is_empty() {
            return None;
        }
        let mut parent = StepInfo {
            path: path.to_string(),
            entry: entry.cloned().unwrap_or_default(),
            exit: parent_exit.unwrap_or_default(),
            ..StepInfo::default()
        };
        for (name, pre, effect) in acts {
            parent.steps.push(StepInfo {
                step: Step {
                    name: name.clone(),
                    action: name.clone(),
                    ..Step::default()
                },
                path: format!("{path}/{name}"),
                entry: pre,
                exit: effect,
                ..StepInfo::default()
            });
        }
        let mut lc = check_level(&parent, LEVEL_AGENT);
        lc.agent = agent.to_string();
        lc.goal = goal;
        Some(lc)
    }
}

/// lift gives every step with sub-steps, as entry, what it demands from its context.
fn lift(mut s: StepInfo) -> StepInfo {
    if s.steps.is_empty() {
        return s;
    }
    let mut made: BTreeSet<String> = BTreeSet::new();
    let kids: Vec<StepInfo> = std::mem::take(&mut s.steps)
        .into_iter()
        .map(|c| {
            made.extend(c.exit.keys().cloned());
            lift(c)
        })
        .collect();
    let mut entry = s.entry.clone();
    for c in &kids {
        for (k, v) in &c.entry {
            if !entry.contains_key(k) && !made.contains(k) {
                entry.insert(k.clone(), *v);
            }
        }
    }
    s.steps = kids;
    s.entry = entry;
    s
}

fn check_level(parent: &StepInfo, kind: &str) -> LevelCheck {
    let mut lc = LevelCheck {
        path: parent.path.clone(),
        kind: kind.to_string(),
        inputs: parent.entry.clone(),
        outputs: parent.exit.clone(),
        ..LevelCheck::default()
    };
    let mut nodes = Vec::new();
    for s in &parent.steps {
        let mut n = LevelNode {
            name: s.name.clone(),
            path: s.path.clone(),
            method: s.method().to_string(),
            capability: s.capability.clone(),
            foreach: s.foreach.clone(),
            group_by: s.group_by.clone(),
            review: s.review.clone(),
            entry: s.entry.clone(),
            exit: s.exit.clone(),
            composite: !s.steps.is_empty(),
            sub_steps: s.steps.len() as i64,
            ..LevelNode::default()
        };
        match n.method.as_str() {
            METHOD_ACTION => {
                n.target = if s.action.is_empty() {
                    s.actions.join(", ")
                } else {
                    s.action.clone()
                };
            }
            METHOD_PROCESS => n.target = s.process.clone(),
            METHOD_CAPABILITY => n.target = s.capability.clone(),
            _ => {}
        }
        lc.steps.push(n);
        nodes.push(GraphStep {
            path: s.path.clone(),
            entry: s.entry.clone(),
            exit: s.exit.clone(),
            leaf: true,
            ..GraphStep::default()
        });
    }
    lc.edges = condition_edges(&nodes);
    // an output that is the input does nothing: the parent's, then each direct step's
    let same = overlap(&parent.entry, &parent.exit);
    if !same.is_empty() && kind != LEVEL_PROCESS && kind != LEVEL_METHOD {
        lc.gaps.push(LevelGap {
            step: String::new(),
            kind: GAP_NOOP.into(),
            missing: same,
            message: NOOP_MESSAGE.into(),
        });
    }
    for s in &parent.steps {
        let same = overlap(&s.entry, &s.exit);
        if !same.is_empty() {
            lc.gaps.push(LevelGap {
                step: s.name.clone(),
                kind: GAP_NOOP.into(),
                missing: same,
                message: NOOP_MESSAGE.into(),
            });
        }
    }
    if kind == LEVEL_PROCESS || kind == LEVEL_METHOD {
        lc.inputs = Conds::new(); // a process or method takes no declared input
    }
    let mut avail: BTreeSet<String> = lc
        .inputs
        .iter()
        .filter(|(_, v)| **v)
        .map(|(k, _)| k.clone())
        .collect();
    let produced: BTreeSet<&String> = parent.steps.iter().flat_map(|s| s.exit.keys()).collect();
    // what no step of the level establishes and the parent does not give: it is demanded of the context
    let mut external: BTreeMap<String, bool> = BTreeMap::new();
    for s in &parent.steps {
        for (k, v) in &s.entry {
            if lc.inputs.contains_key(k) || produced.contains(k) {
                continue;
            }
            if *v {
                avail.insert(k.clone());
            }
            external.insert(k.clone(), *v);
        }
    }
    if !external.is_empty() {
        lc.gaps.push(LevelGap {
            step: String::new(),
            kind: GAP_EXTERNAL.into(),
            missing: external.keys().cloned().collect(),
            message:
                "needed by the steps and established by none at this level: the context must provide it"
                    .into(),
        });
    }
    let met = |avail: &BTreeSet<String>, entry: &Conds| -> Vec<String> {
        entry
            .iter()
            .filter(|(k, v)| avail.contains(*k) != **v)
            .map(|(k, _)| k.clone())
            .collect()
    };
    let mut done: BTreeSet<String> = BTreeSet::new();
    let mut layer = 0;
    while done.len() < parent.steps.len() {
        let ready: Vec<&StepInfo> = parent
            .steps
            .iter()
            .filter(|s| !done.contains(&s.name) && met(&avail, &s.entry).is_empty())
            .collect();
        if ready.is_empty() {
            break;
        }
        // the steps of one layer all start from the same state
        for s in &ready {
            done.insert(s.name.clone());
            lc.order.push(LevelStep {
                name: s.name.clone(),
                layer,
            });
        }
        for s in &ready {
            for (k, v) in &s.exit {
                if *v {
                    avail.insert(k.clone());
                } else {
                    avail.remove(k);
                }
            }
        }
        layer += 1;
    }
    for s in &parent.steps {
        if done.contains(&s.name) {
            continue;
        }
        let missing = met(&avail, &s.entry);
        let mut waits: Vec<String> = Vec::new(); // the missing conditions only blocked siblings could establish
        for k in &missing {
            for o in &parent.steps {
                if o.name != s.name
                    && !done.contains(&o.name)
                    && o.exit.get(k).is_some_and(|v| Some(v) == s.entry.get(k))
                {
                    waits.push(o.name.clone());
                    break;
                }
            }
        }
        let mut msg = format!(
            "cannot be entered: {} never holds at this level",
            missing.join(", ")
        );
        if !waits.is_empty() {
            waits.sort();
            waits.dedup();
            msg = format!(
                "cannot be entered: waits for {}, itself blocked (cycle)",
                waits.join(", ")
            );
        }
        lc.gaps.push(LevelGap {
            step: s.name.clone(),
            kind: GAP_BLOCKED.into(),
            missing,
            message: msg,
        });
    }
    let missing = met(&avail, &lc.outputs);
    if !missing.is_empty() {
        lc.gaps.push(LevelGap {
            step: String::new(),
            kind: GAP_OUTPUT.into(),
            message: format!("outputs not reached by the steps: {}", missing.join(", ")),
            missing,
        });
    }
    lc
}
