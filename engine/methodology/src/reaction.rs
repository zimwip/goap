//! Reactive activities (ADR 0110, `reaction.go`).

use std::collections::BTreeSet;

use goap_def::{Issue, SEVERITY_WARNING};

use crate::compile::CompileState;
use crate::method::Subscription;
use crate::methodology::{Agent, Compiled, Goal, PLANNER_GOAP};
use crate::process::{Step, step_condition};

/// The kinds of activity that may react to events (ADR 0110).
pub const REACT_PROCESS: &str = "process";
pub const REACT_STEP: &str = "step";
pub const REACT_METHOD: &str = "method";
pub const REACT_ACTION: &str = "action";

/// ReactionPrefix starts the name of the agent and the goal generated for a reactive action: "on.<action>".
pub const REACTION_PREFIX: &str = "on.";

/// ReactionAgent is the name of the agent (and of its goal) generated to run a reactive action alone.
pub fn reaction_agent(action: &str) -> String {
    format!("{REACTION_PREFIX}{action}")
}

/// Reaction is an activity of the methodology that declares `on:` (ADR 0110).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Reaction {
    /// Activity is the path of the activity: a process or method name, a step path, an action name.
    pub activity: String,
    /// Owner is the process or method the activity is or belongs to (empty for an action).
    pub owner: String,
    pub kind: String,
    pub agent: String,
    pub goal: String,
    pub on: Vec<Subscription>,
    /// own are the actions whose items are the reaction's own: they do not wake it again.
    pub(crate) own: BTreeSet<String>,
}

impl Reaction {
    /// Owns reports whether an item produced by the action of that name is the reaction's own.
    pub fn owns(&self, produced_by: &str) -> bool {
        !produced_by.is_empty() && self.own.contains(produced_by)
    }

    /// The actions whose items are the reaction's own, sorted.
    pub fn own_actions(&self) -> Vec<String> {
        self.own.iter().cloned().collect()
    }
}

impl Compiled {
    /// Reactions returns the reactive activities of the methodology, in declaration order.
    pub fn reactions(&self) -> &[Reaction] {
        &self.reactions
    }

    /// reactionGoal is the goal of a reactive action run alone: its effects.
    pub(crate) fn reaction_goal(&self, name: &str) -> Option<Goal> {
        let action = name.strip_prefix(REACTION_PREFIX)?;
        for r in &self.reactions {
            if r.kind == REACT_ACTION && r.activity == action {
                let a = self.actions.get(action).cloned().unwrap_or_default();
                return Some(Goal {
                    name: name.to_string(),
                    description: a.description.clone(),
                    pre: a.effects.clone(),
                    ..Goal::default()
                });
            }
        }
        None
    }
}

/// forEachReactive lists each activity declaring on, with the path of its definition.
pub(crate) fn reactive(m: &crate::methodology::Methodology) -> Vec<(String, Reaction)> {
    let mut out = Vec::new();
    fn steps(
        out: &mut Vec<(String, Reaction)>,
        list: &[Step],
        path: &str,
        owner: &str,
        prefix: &str,
        agent: &str,
    ) {
        for (i, st) in list.iter().enumerate() {
            let sp = format!("{path}.steps[{i}]");
            let full = format!("{prefix}/{}", st.name);
            let on = st.reacts_to();
            if !on.is_empty() {
                out.push((
                    sp.clone(),
                    Reaction {
                        activity: full.clone(),
                        owner: owner.to_string(),
                        kind: REACT_STEP.into(),
                        agent: agent.to_string(),
                        goal: full.clone(),
                        on,
                        own: BTreeSet::new(),
                    },
                ));
            }
            steps(out, &st.steps, &sp, owner, &full, agent);
        }
    }
    for (i, p) in m.processes.iter().enumerate() {
        let path = format!("processes[{i}]");
        if !p.on.is_empty() {
            out.push((
                path.clone(),
                Reaction {
                    activity: p.name.clone(),
                    owner: p.name.clone(),
                    kind: REACT_PROCESS.into(),
                    agent: p.name.clone(),
                    goal: p.name.clone(),
                    on: p.on.clone(),
                    own: BTreeSet::new(),
                },
            ));
        }
        steps(&mut out, &p.steps, &path, &p.name, &p.name, &p.name);
    }
    for (i, me) in m.methods.iter().enumerate() {
        let path = format!("methods[{i}]");
        if !me.on.is_empty() {
            out.push((
                path.clone(),
                Reaction {
                    activity: me.name.clone(),
                    owner: me.name.clone(),
                    kind: REACT_METHOD.into(),
                    agent: me.actor_agent().to_string(),
                    goal: me.name.clone(),
                    on: me.on.clone(),
                    own: BTreeSet::new(),
                },
            ));
        }
        steps(
            &mut out,
            &me.steps,
            &path,
            &me.name,
            &me.name,
            me.actor_agent(),
        );
    }
    for (i, a) in m.actions.iter().enumerate() {
        if !a.on.is_empty() {
            out.push((
                format!("actions[{i}]"),
                Reaction {
                    activity: a.name.clone(),
                    owner: String::new(),
                    kind: REACT_ACTION.into(),
                    agent: reaction_agent(&a.name),
                    goal: reaction_agent(&a.name),
                    on: a.on.clone(),
                    own: BTreeSet::new(),
                },
            ));
        }
    }
    out
}

impl CompileState<'_> {
    /// compileReactions checks the `on:` of the activities and builds the reactions (ADR 0110).
    pub(crate) fn compile_reactions(&mut self) {
        let m = self.m;
        let list = reactive(m);
        if !m.applies_to.is_empty() {
            for (path, _) in list {
                self.add(
                    format!("{path}.on"),
                    "a transverse methodology subscribes at its own level (on of the methodology)",
                );
            }
            return;
        }
        for (path, mut r) in list {
            for (i, sub) in r.on.iter().enumerate() {
                self.check_subscription(&format!("{path}.on[{i}]"), sub);
            }
            match r.kind.as_str() {
                REACT_ACTION => {
                    let a = self.actions.get(&r.activity).cloned().unwrap_or_default();
                    if a.is_specialization() {
                        self.add(
                            format!("{path}.on"),
                            "a specialization is not planned: put on on the action it specializes",
                        );
                        continue;
                    }
                    if a.effects.is_empty() {
                        self.add(
                            format!("{path}.on"),
                            "a reactive action declares the effects it reaches (its goal when an event wakes it)",
                        );
                        continue;
                    }
                    self.agents.insert(
                        r.agent.clone(),
                        Agent {
                            name: r.agent.clone(),
                            description: a.description.clone(),
                            planner: PLANNER_GOAP.into(),
                            actions: vec![a.name.clone()],
                            goals: vec![r.goal.clone()],
                            ..Agent::default()
                        },
                    );
                }
                REACT_STEP => {
                    let done = self
                        .procs
                        .criteria
                        .get(&r.activity)
                        .map(|c| c.done.clone())
                        .unwrap_or_default();
                    if done.len() == 1 && done.get(&step_condition(&r.activity)) == Some(&true) {
                        let mut is = Issue::new(
                            format!("{path}.done"),
                            "a reactive step whose exit is only its own run holds once it ran: the events wake it for nothing",
                        );
                        is.severity = SEVERITY_WARNING.into();
                        is.hint = "state a done derived from what the events change".into();
                        self.issues.push(is);
                    }
                }
                _ => {}
            }
            r.own = self.own_actions(&r);
            self.reactions.push(r);
        }
    }

    /// checkSubscription checks one subscription of an activity: a known event, a filter that compiles, a known
    /// origin.
    fn check_subscription(&mut self, path: &str, sub: &Subscription) {
        self.check_event(format!("{path}.event"), &sub.event);
        if let Err(e) = goap_condition::compile_event_filter(&sub.filter) {
            self.add(format!("{path}.filter"), e.to_string());
        }
        self.check_from(format!("{path}.from"), &sub.from);
    }

    /// ownActions are the actions a reaction runs.
    fn own_actions(&self, r: &Reaction) -> BTreeSet<String> {
        let mut own = BTreeSet::from([r.activity.clone()]);
        if r.kind == REACT_ACTION {
            return own;
        }
        for a in &self.procs.actions {
            if a.step == r.activity || a.step.starts_with(&format!("{}/", r.activity)) {
                own.insert(a.name.clone());
                if !a.implements.is_empty() {
                    own.insert(a.implements.clone());
                }
                if let Some(call) = &a.call
                    && !call.capability.is_empty()
                {
                    for me in &self.m.methods {
                        if me.r#for == call.capability {
                            own.extend(me.actions.iter().cloned());
                        }
                    }
                }
            }
        }
        if r.kind == REACT_METHOD {
            for me in &self.m.methods {
                if me.name == r.activity {
                    own.extend(me.actions.iter().cloned());
                }
            }
        }
        own
    }
}
