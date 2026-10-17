//! Processes: trees of steps sequenced by their conditions (ADR 0034) (`process.go`).

use std::collections::{BTreeMap, HashMap, HashSet};

use goap_condition::Definition;
use goap_def::{Issue, NAME_RE};
use serde::{Deserialize, Serialize};

use crate::method::{CompiledMethods, METHOD_CAPABILITY, Method, Responsibilities, Subscription};
use crate::methodology::{
    Action, Agent, Call, Compiled, Goal, KIND_CALL, KIND_HUMAN, Methodology, PLANNER_GOAP,
    input_and_output, overlap,
};
use crate::review::Review;
use crate::util::{Conds, nd, q, st};

/// Process describes how an objective (the realization of a change) is reached, as a tree of steps (ADR 0034).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Process {
    #[serde(deserialize_with = "st")]
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub description: String,
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub examples: Vec<String>,
    /// References are the reference documents that describe the process.
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub references: Vec<Reference>,
    #[serde(deserialize_with = "nd")]
    pub steps: Vec<Step>,
    /// On makes the process reactive (ADR 0110).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub on: Vec<Subscription>,
}

/// Reference points at a document of the documentary repository that describes a process or a step.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Reference {
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub title: String,
    #[serde(rename = "ref", deserialize_with = "st")]
    pub r#ref: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub section: String,
}

impl std::fmt::Display for Reference {
    /// The reference as a person reads it.
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let mut out = self.r#ref.clone();
        if !self.title.is_empty() {
            out = format!("{} ({})", self.title, self.r#ref);
        }
        if !self.section.is_empty() {
            out.push_str(", ");
            out.push_str(&self.section);
        }
        f.write_str(&out)
    }
}

/// Step is a step of a process. Its method is at most one of Steps, Action (or Actions), Agent and Process; none: a
/// manual step, a human task that shows Instructions (or Description).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Step {
    #[serde(deserialize_with = "st")]
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub description: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub instructions: String,
    /// References are the reference documents that describe the step.
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub references: Vec<Reference>,
    /// Guidance (markdown), Checklist, Deliverables (ADR 0035 §5).
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub guidance: String,
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub checklist: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub deliverables: Vec<String>,
    /// Roles assign the step, RACI style (ADR 0035 §2); sub-steps without their own inherit them.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub roles: Option<Responsibilities>,
    /// Pre are the entry conditions of the step (and of its sub-steps).
    #[serde(skip_serializing_if = "Conds::is_empty", deserialize_with = "nd")]
    pub pre: Conds,
    /// Done are the exit criteria (default: what the step runs reaches, else "step:<path>").
    #[serde(skip_serializing_if = "Conds::is_empty", deserialize_with = "nd")]
    pub done: Conds,
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub steps: Vec<Step>,
    /// Action of the methodology that does the step, or Actions: alternatives the planner chooses among.
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub action: String,
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub actions: Vec<String>,
    /// Process nested in the step: "<process>" of this methodology or "<methodology>/<process>".
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub process: String,
    /// Capability names what the step needs done (ADR 0035 §1). YAML: `method: <capability>`.
    #[serde(
        rename = "method",
        skip_serializing_if = "String::is_empty",
        deserialize_with = "st"
    )]
    pub capability: String,
    /// Foreach (ADR 0050): a CEL list on a step naming a capability, one stream per element.
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub foreach: String,
    /// GroupBy: one stream per group of the elements of Foreach.
    #[serde(
        rename = "groupBy",
        skip_serializing_if = "String::is_empty",
        deserialize_with = "st"
    )]
    pub group_by: String,
    /// Where narrows the subjects of the scoped action the step runs (ADR 0109 §3.5).
    #[serde(
        rename = "where",
        skip_serializing_if = "String::is_empty",
        deserialize_with = "st"
    )]
    pub r#where: String,
    /// On makes the step reactive (ADR 0110).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub on: Vec<Subscription>,
    /// Review declares the step a review of a subject of the change (ADR 0130).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub review: Option<Review>,
}

// Step methods.
pub const METHOD_STEPS: &str = "steps";
pub const METHOD_ACTION: &str = "action";
pub const METHOD_PROCESS: &str = "process";
pub const METHOD_MANUAL: &str = "manual";

impl Step {
    /// Method returns how the step is done.
    pub fn method(&self) -> &'static str {
        if !self.steps.is_empty() {
            METHOD_STEPS
        } else if !self.action.is_empty() || !self.actions.is_empty() {
            METHOD_ACTION
        } else if !self.process.is_empty() {
            METHOD_PROCESS
        } else if !self.capability.is_empty() {
            METHOD_CAPABILITY
        } else {
            METHOD_MANUAL
        }
    }

    /// NestedProcess returns the methodology (empty: the one declaring it) and the process a step nests.
    pub fn nested_process(&self) -> (&str, &str) {
        match self.process.find('/') {
            Some(i) => (&self.process[..i], &self.process[i + 1..]),
            None => ("", &self.process),
        }
    }
}

/// StepCondition is the name of the generated condition of a step done once it has run.
pub fn step_condition(path: &str) -> String {
    format!("step:{path}")
}

/// StatePrefix starts the generated condition of a state of the lifecycle of the change (ADR 0058).
pub const STATE_PREFIX: &str = "state:";

impl Agent {
    /// ProcessOf returns the process an agent runs ("" for a declared agent).
    pub fn process_of(&self) -> &str {
        &self.process
    }
}

impl Methodology {
    /// ProcessByName returns a process by name.
    pub fn process_by_name(&self, name: &str) -> Option<&Process> {
        self.processes.iter().find(|p| p.name == name)
    }

    pub(crate) fn process_index(&self, name: &str) -> i64 {
        self.processes
            .iter()
            .position(|p| p.name == name)
            .map(|i| i as i64)
            .unwrap_or(-1)
    }
}

/// compiledProcesses is what the processes add to a compiled methodology.
#[derive(Debug, Clone, Default)]
pub(crate) struct CompiledProcesses {
    pub(crate) actions: Vec<Action>,
    pub(crate) goals: Vec<Goal>,
    pub(crate) agents: Vec<Agent>,
    pub(crate) conditions: Vec<Definition>,
    /// criteria are the entry and exit criteria of every step, by path
    pub(crate) criteria: BTreeMap<String, StepCriteria>,
    /// extend gives an agent that performs a method the activities that compose it and their goal
    pub(crate) extend: BTreeMap<String, AgentExt>,
}

#[derive(Debug, Clone, Default)]
pub(crate) struct AgentExt {
    pub(crate) actions: Vec<String>,
    pub(crate) goals: Vec<String>,
}

#[derive(Debug, Clone, Default)]
pub(crate) struct StepCriteria {
    pub(crate) entry: Conds,
    pub(crate) done: Conds,
}

/// The sink of the issues of a compilation.
pub(crate) type Add<'a> = dyn FnMut(String, String) + 'a;

/// compileProcesses validates the processes and generates what runs them.
pub(crate) fn compile_processes(
    m: &Methodology,
    add: &mut Add<'_>,
    actions: &BTreeMap<String, Action>,
    known: &mut HashSet<String>,
    agents: &BTreeMap<String, Agent>,
    methods: &CompiledMethods,
    roles: &HashSet<String>,
) -> CompiledProcesses {
    let mut names: HashSet<String> = HashSet::new();
    for (i, p) in m.processes.iter().enumerate() {
        let path = format!("processes[{i}]");
        if p.name.is_empty() {
            add(format!("{path}.name"), "name required".into());
            continue;
        } else if !NAME_RE.is_match(&p.name) {
            add(
                format!("{path}.name"),
                "name must be lowercase letters, digits, '-' or '_' and start with a letter".into(),
            );
            continue;
        } else if names.contains(&p.name) {
            add(
                format!("{path}.name"),
                format!("duplicate process {}", p.name),
            );
            continue;
        } else if agents.get(&p.name).is_some_and(|a| !a.name.is_empty()) {
            add(
                format!("{path}.name"),
                format!(
                    "{} is already an agent: a process is run by an agent of its name",
                    p.name
                ),
            );
            continue;
        } else if m.goals.iter().any(|g| g.name == p.name) {
            add(
                format!("{path}.name"),
                format!(
                    "{} is already a goal: a process reaches a goal of its name",
                    p.name
                ),
            );
            continue;
        }
        names.insert(p.name.clone());
        check_references(add, &format!("{path}.references"), &p.references);
    }
    if check_process_cycles(m, add) {
        return CompiledProcesses::default();
    }
    let mut w = StepWalker {
        m,
        add,
        actions,
        known,
        agents,
        methods,
        roles,
        out: CompiledProcesses::default(),
        root: CompiledProcesses::default(),
        done: HashMap::new(),
        refs: Vec::new(),
    };
    for (i, p) in m.processes.iter().enumerate() {
        if names.contains(&p.name) {
            w.process(i);
        }
    }
    let refs = std::mem::take(&mut w.refs);
    let mut out = std::mem::take(&mut w.root);
    let StepWalker { add, known, .. } = w;
    // the conditions a step names are checked once every step has its own: a step may name "step:<path>" of any other
    for r in refs {
        if let Some(state) = r.cond.strip_prefix(STATE_PREFIX)
            && !state.is_empty()
            && !known.contains(&r.cond)
        {
            // "state:<name>" is generated (ADR 0058): true while the change is in that state of its lifecycle
            known.insert(r.cond.clone());
            out.conditions.push(Definition {
                name: r.cond.clone(),
                expr: format!("change.state == {}", q(state)),
                description: format!("the change is in the state {state} of its lifecycle"),
                ..Definition::default()
            });
        }
        if !known.contains(&r.cond) {
            add(r.path, format!("unknown condition {}", q(&r.cond)));
        }
    }
    // in declaration order
    out.goals.sort_by_key(|g| m.process_index(&g.name));
    out.agents.sort_by_key(|a| m.process_index(&a.name));
    out
}

pub(crate) fn check_references(add: &mut Add<'_>, path: &str, refs: &[Reference]) {
    for (i, r) in refs.iter().enumerate() {
        if r.r#ref.trim().is_empty() {
            add(
                format!("{path}[{i}].ref"),
                "a reference names its document (doc:<key>, <mcp>:<path> or a URL)".into(),
            );
        }
    }
}

/// processCriteria is what a process makes true once done, and what it needs from outside to get there.
#[derive(Debug, Clone)]
pub(crate) struct ProcessCriteria {
    done: Conds,
    needs: Conds,
}

/// prerequisites are the preconditions of a process's steps that none of its steps establishes. A condition
/// required both true and false by two steps is left out.
fn prerequisites(actions: &[Action]) -> Conds {
    let produced: HashSet<&String> = actions.iter().flat_map(|a| a.effects.keys()).collect();
    let mut out = Conds::new();
    let mut conflict: HashSet<String> = HashSet::new();
    for a in actions {
        for (k, v) in &a.pre {
            if produced.contains(k) || (a.is_scoped() && *k == a.pending_condition()) {
                // a scoped action's pending subjects come with the work of the process, not before it
                continue;
            }
            if let Some(have) = out.get(k)
                && have != v
            {
                conflict.insert(k.clone());
            }
            out.insert(k.clone(), *v);
        }
    }
    for k in conflict {
        out.remove(&k);
    }
    out
}

pub(crate) struct StepWalker<'a, 'b> {
    pub(crate) m: &'a Methodology,
    pub(crate) add: &'a mut Add<'b>,
    pub(crate) actions: &'a BTreeMap<String, Action>,
    pub(crate) known: &'a mut HashSet<String>,
    #[allow(dead_code)]
    pub(crate) agents: &'a BTreeMap<String, Agent>,
    pub(crate) methods: &'a CompiledMethods,
    pub(crate) roles: &'a HashSet<String>,
    /// what the process being generated adds
    pub(crate) out: CompiledProcesses,
    /// what every process adds
    pub(crate) root: CompiledProcesses,
    /// the criteria of the processes generated so far (None while one is being generated)
    pub(crate) done: HashMap<String, Option<ProcessCriteria>>,
    /// the conditions the steps name, checked once every step condition exists
    pub(crate) refs: Vec<ConditionRef>,
}

pub(crate) struct ConditionRef {
    path: String,
    cond: String,
}

impl StepWalker<'_, '_> {
    fn add(&mut self, path: String, msg: String) {
        (self.add)(path, msg)
    }

    /// process generates the process at index i once and returns its criteria (None when the process is invalid).
    fn process(&mut self, i: usize) -> Option<ProcessCriteria> {
        let p = &self.m.processes[i];
        if let Some(c) = self.done.get(&p.name) {
            return c.clone();
        }
        self.done.insert(p.name.clone(), None);
        let path = format!("processes[{i}]");
        if p.steps.is_empty() {
            self.add(
                format!("{path}.steps"),
                "a process needs at least one step".into(),
            );
            return None;
        }
        let saved = std::mem::take(&mut self.out);
        let done = self.walk(&p.steps, &format!("{path}.steps"), &p.name, &Conds::new());
        let sub = std::mem::replace(&mut self.out, saved);
        self.root.conditions.extend(sub.conditions.iter().cloned());
        self.root.actions.extend(sub.actions.iter().cloned());
        if done.is_empty() {
            return None;
        }
        let c = ProcessCriteria {
            done: done.clone(),
            needs: prerequisites(&sub.actions),
        };
        self.done.insert(p.name.clone(), Some(c.clone()));
        let own: Vec<String> = sub.actions.iter().map(|a| a.name.clone()).collect();
        self.root.goals.push(Goal {
            name: p.name.clone(),
            description: p.description.clone(),
            examples: p.examples.clone(),
            pre: done,
            ..Goal::default()
        });
        self.root.agents.push(Agent {
            name: p.name.clone(),
            description: p.description.clone(),
            examples: p.examples.clone(),
            planner: PLANNER_GOAP.into(),
            actions: own,
            goals: vec![p.name.clone()],
            process: p.name.clone(),
            ..Agent::default()
        });
        Some(c)
    }

    /// walk generates the steps of one level and returns what they make true once all done. inherited are the entry
    /// conditions of the steps containing the level.
    pub(crate) fn walk(
        &mut self,
        steps: &[Step],
        path: &str,
        prefix: &str,
        inherited: &Conds,
    ) -> Conds {
        let mut all = Conds::new();
        let mut seen: HashSet<&str> = HashSet::new();
        for (i, s) in steps.iter().enumerate() {
            let sp = format!("{path}[{i}]");
            if s.name.is_empty() || !NAME_RE.is_match(&s.name) {
                self.add(
                    format!("{sp}.name"),
                    "step name required: lowercase letters, digits, '-' or '_', starting with a letter"
                        .into(),
                );
                continue;
            }
            if !seen.insert(&s.name) {
                self.add(format!("{sp}.name"), format!("duplicate step {}", s.name));
                continue;
            }
            check_references(&mut *self.add, &format!("{sp}.references"), &s.references);
            crate::method::check_responsibilities(
                &mut *self.add,
                &format!("{sp}.roles"),
                s.roles.as_ref(),
                self.roles,
            );
            for (field, c) in [("pre", &s.pre), ("done", &s.done)] {
                for k in c.keys() {
                    self.refs.push(ConditionRef {
                        path: format!("{sp}.{field}.{k}"),
                        cond: k.clone(),
                    });
                }
            }
            let mut need = inherited.clone();
            self.merge(&format!("{sp}.pre"), &mut need, &s.pre);
            let first = self.out.actions.len();
            let full = format!("{prefix}/{}", s.name);
            let d = self.step(s, &sp, &full, &need);
            self.root.criteria.insert(
                full.clone(),
                StepCriteria {
                    entry: need.clone(),
                    done: d.clone(),
                },
            );
            let mut reported: HashSet<String> = HashSet::new();
            for k in overlap(&need, &d) {
                reported.insert(k.clone());
                let msg = input_and_output(&k, &format!("the step {full}"));
                self.add(format!("{sp}.done.{k}"), msg);
            }
            let mut extra = Vec::new();
            for a in &self.out.actions[first..] {
                if a.step != full || a.implements.is_empty() {
                    continue;
                }
                for k in overlap(&a.pre, &a.effects) {
                    if reported.insert(k.clone()) {
                        extra.push((
                            format!("{sp}.pre.{k}"),
                            input_and_output(
                                &k,
                                &format!("the step {full} (through its action {})", a.implements),
                            ),
                        ));
                    }
                }
            }
            for (p, msg) in extra {
                self.add(p, msg);
            }
            self.merge(&format!("{sp}.done"), &mut all, &d);
        }
        all
    }

    /// step generates one step and returns its exit criteria.
    fn step(&mut self, s: &Step, sp: &str, path: &str, need: &Conds) -> Conds {
        if !s.group_by.is_empty() && s.foreach.is_empty() {
            self.add(
                format!("{sp}.groupBy"),
                "groupBy groups the elements of foreach: declare foreach".into(),
            );
            return Conds::new();
        }
        if !s.foreach.is_empty() && s.capability.is_empty() {
            self.add(
                format!("{sp}.foreach"),
                "foreach applies to a step naming a capability (method): each element is given the most specific method"
                    .into(),
            );
            return Conds::new();
        }
        let methods = [
            !s.steps.is_empty(),
            !s.action.is_empty(),
            !s.actions.is_empty(),
            !s.process.is_empty(),
            !s.capability.is_empty(),
        ]
        .iter()
        .filter(|b| **b)
        .count();
        if methods > 1 {
            self.add(
                sp.to_string(),
                "a step is made of one thing: sub-steps, an action, a process or a method".into(),
            );
            return Conds::new();
        }
        // Go's nil map: an unset done
        let mut done: Option<Conds> = if s.done.is_empty() {
            None
        } else {
            Some(s.done.clone())
        };
        let mut gen_ = Action {
            name: path.to_string(),
            description: s.description.clone(),
            pre: need.clone(),
            cost: 1.0,
            step: path.to_string(),
            ..Action::default()
        };
        match s.method() {
            METHOD_STEPS => {
                let sub = self.walk(&s.steps, &format!("{sp}.steps"), path, need);
                let mut done = done.unwrap_or_default();
                self.merge(&format!("{sp}.done"), &mut done, &sub);
                return done;
            }
            METHOD_ACTION => {
                let (names, field) = if !s.action.is_empty() {
                    (vec![s.action.clone()], format!("{sp}.action"))
                } else {
                    (s.actions.clone(), format!("{sp}.actions"))
                };
                for name in &names {
                    let Some(a) = self.actions.get(name) else {
                        self.add(field.clone(), format!("unknown action {}", q(name)));
                        return Conds::new();
                    };
                    if a.is_specialization() {
                        self.add(
                            field.clone(),
                            format!(
                                "{} is a specialization: name the action it specializes",
                                q(name)
                            ),
                        );
                        return Conds::new();
                    }
                    if done.as_ref().is_none_or(|d| d.is_empty()) {
                        done = Some(a.effects.clone());
                    }
                    let mut alt = a.clone();
                    alt.name = path.to_string();
                    alt.step = path.to_string();
                    alt.implements = a.name.clone();
                    alt.utility = String::new();
                    if names.len() > 1 {
                        alt.name = format!("{path}:{}", a.name); // one planned action per alternative
                    }
                    if !s.description.is_empty() {
                        alt.description = s.description.clone();
                    }
                    alt.pre = need.clone();
                    let apre = a.pre.clone();
                    self.merge(&field, &mut alt.pre, &apre);
                    alt.effects = a.effects.clone();
                    if !s.r#where.is_empty() {
                        if !a.is_scoped() {
                            self.add(
                                format!("{sp}.where"),
                                "where applies to a step running a scoped action (scope)".into(),
                            );
                            return Conds::new();
                        }
                        if s.done.is_empty() {
                            self.add(
                                format!("{sp}.done"),
                                format!(
                                    "a step narrowing the subjects of {} states its exit criteria: the aggregates over its subjects",
                                    a.name
                                ),
                            );
                            return Conds::new();
                        }
                        // the subjects are narrowed: the action is lifted again over them once the steps are compiled
                        alt = narrow(alt, &s.r#where, need);
                    }
                    let d = done.clone().unwrap_or_default();
                    self.merge(&format!("{sp}.done"), &mut alt.effects, &d);
                    if alt.narrowed {
                        alt.step_done = d;
                    }
                    self.out.actions.push(alt);
                }
                return done.unwrap_or_default();
            }
            METHOD_PROCESS => {
                let (other, proc) = s.nested_process();
                let (mut other, proc) = (other.to_string(), proc.to_string());
                if !NAME_RE.is_match(&proc) || (!other.is_empty() && !NAME_RE.is_match(&other)) {
                    self.add(
                        format!("{sp}.process"),
                        "process must be <process> or <methodology>/<process>".into(),
                    );
                    return Conds::new();
                }
                if (other.is_empty() || other == self.m.name)
                    && !self.m.processes.iter().any(|p| p.name == proc)
                {
                    self.add(
                        format!("{sp}.process"),
                        format!("unknown process {}", q(&proc)),
                    );
                    return Conds::new();
                }
                if other == self.m.name {
                    other = String::new();
                }
                if other.is_empty() {
                    // a process of this methodology: done by its exit criteria, and entered once what it needs from
                    // outside holds
                    let idx = self.m.process_index(&proc) as usize;
                    if let Some(c) = self.process(idx) {
                        if done.is_none() {
                            done = Some(c.done.clone());
                        }
                        gen_.pre = need.clone();
                        self.merge(&format!("{sp}.pre"), &mut gen_.pre, &c.needs);
                    }
                }
                gen_.kind = KIND_CALL.into();
                gen_.call = Some(Call {
                    methodology: other,
                    agent: proc.clone(),
                    goal: proc,
                    ..Call::default()
                });
            }
            METHOD_CAPABILITY => {
                let candidates = methods_for(self.m, &s.capability, &self.methods.goals);
                if candidates.is_empty() {
                    self.add(
                        format!("{sp}.method"),
                        format!(
                            "no valid method provides {} (methods[].for)",
                            q(&s.capability)
                        ),
                    );
                    return Conds::new();
                }
                if !s.foreach.is_empty() {
                    if let Err(e) = goap_condition::check_list(&s.foreach) {
                        self.add(format!("{sp}.foreach"), e.to_string());
                        return Conds::new();
                    }
                    if !s.group_by.is_empty()
                        && let Err(e) = goap_condition::compile_value(&s.group_by)
                    {
                        self.add(format!("{sp}.groupBy"), e.to_string());
                        return Conds::new();
                    }
                } else if done.is_none() {
                    let shared = shared_criteria(&candidates, &self.methods.goals);
                    if shared.is_empty() {
                        self.add(
                            format!("{sp}.done"),
                            format!(
                                "the methods providing {} reach nothing in common: state the exit criteria of the step",
                                q(&s.capability)
                            ),
                        );
                        return Conds::new();
                    }
                    done = Some(shared);
                }
                gen_.kind = KIND_CALL.into();
                gen_.call = Some(Call {
                    capability: s.capability.clone(),
                    foreach: s.foreach.clone(),
                    group_by: s.group_by.clone(),
                    ..Call::default()
                });
            }
            _ => {
                gen_.kind = KIND_HUMAN.into();
                gen_.instructions = s.instructions.clone();
                if gen_.instructions.is_empty() {
                    gen_.instructions = s.description.clone();
                }
                if gen_.description.is_empty() {
                    gen_.description = format!("Step {path}");
                }
            }
        }
        let done = match done {
            Some(d) => d,
            None => {
                // done once it has run, and its run still stands: the history of the change says so (ADR 0109 §5)
                let name = step_condition(path);
                if !self.known.contains(&name) {
                    self.known.insert(name.clone());
                    self.out.conditions.push(Definition {
                        name: name.clone(),
                        expr: format!(
                            "{} in history.last && history.last[{}].current",
                            q(path),
                            q(path)
                        ),
                        description: format!(
                            "the step {path} has run, and what it did still stands"
                        ),
                        ..Definition::default()
                    });
                }
                Conds::from([(name, true)])
            }
        };
        if gen_.effects.is_empty() {
            gen_.effects = done.clone();
        }
        self.out.actions.push(gen_);
        done
    }

    /// merge adds src to dst, reporting a condition both require with opposite values.
    fn merge(&mut self, path: &str, dst: &mut Conds, src: &Conds) {
        for (k, v) in src {
            if let Some(have) = dst.get(k)
                && have != v
            {
                self.add(
                    path.to_string(),
                    format!("condition {k} is required both true and false"),
                );
                continue;
            }
            dst.insert(k.clone(), *v);
        }
    }
}

/// methodsFor returns the valid methods providing a capability, in declaration order.
pub(crate) fn methods_for<'m>(
    m: &'m Methodology,
    capability: &str,
    valid: &BTreeMap<String, Goal>,
) -> Vec<&'m Method> {
    m.methods
        .iter()
        .filter(|me| valid.contains_key(&me.name) && me.r#for == capability)
        .collect()
}

/// sharedCriteria are the conditions every method's goal makes true with the same value.
pub(crate) fn shared_criteria(methods: &[&Method], goals: &BTreeMap<String, Goal>) -> Conds {
    let mut out: Option<Conds> = None;
    let empty = Conds::new();
    for me in methods {
        let pre = goals.get(&me.name).map(|g| &g.pre).unwrap_or(&empty);
        match &mut out {
            None => out = Some(pre.clone()),
            Some(o) => o.retain(|k, v| pre.get(k) == Some(v)),
        }
    }
    out.unwrap_or_default()
}

/// checkProcessCycles reports processes of the methodology that nest themselves.
fn check_process_cycles(m: &Methodology, add: &mut Add<'_>) -> bool {
    let mut nested: HashMap<String, Vec<String>> = HashMap::new();
    fn collect(
        m: &Methodology,
        p: &str,
        steps: &[Step],
        nested: &mut HashMap<String, Vec<String>>,
    ) {
        for s in steps {
            let (other, proc) = s.nested_process();
            if !s.process.is_empty() && (other.is_empty() || other == m.name) {
                nested
                    .entry(p.to_string())
                    .or_default()
                    .push(proc.to_string());
            }
            collect(m, p, &s.steps, nested);
        }
    }
    for p in &m.processes {
        collect(m, &p.name, &p.steps, &mut nested);
    }
    let mut found = false;
    for (i, p) in m.processes.iter().enumerate() {
        let mut seen: HashSet<String> = HashSet::new();
        let mut stack = nested.get(&p.name).cloned().unwrap_or_default();
        while let Some(n) = stack.pop() {
            if n == p.name {
                add(
                    format!("processes[{i}].steps"),
                    format!("process {} nests itself", p.name),
                );
                found = true;
                break;
            }
            if seen.insert(n.clone()) {
                stack.extend(nested.get(&n).cloned().unwrap_or_default());
            }
        }
    }
    found
}

/// StepInfo is a step of a compiled process: its definition, its path, what it needs and makes true, and the planned
/// actions it compiled to (none for a step of sub-steps).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct StepInfo {
    /// The definition of the step (its sub-steps left out: they are `steps`).
    pub step: Step,
    /// Effective are the roles in force for the step: its own, else those of the steps containing it.
    pub effective: Option<Responsibilities>,
    /// Owner is the process or the method whose step tree holds the step; Path its path in that tree (ADR 0034).
    pub owner: String,
    pub path: String,
    pub entry: Conds,
    pub exit: Conds,
    pub planned: Vec<String>,
    pub steps: Vec<StepInfo>,
}

impl std::ops::Deref for StepInfo {
    type Target = Step;
    fn deref(&self) -> &Step {
        &self.step
    }
}

impl StepInfo {
    /// Leaves returns the steps that run something (no sub-steps), depth first.
    pub fn leaves(&self) -> Vec<StepInfo> {
        if self.steps.is_empty() {
            return vec![self.clone()];
        }
        self.steps.iter().flat_map(|c| c.leaves()).collect()
    }
}

impl Compiled {
    /// ProcessSteps returns the compiled step tree of a process (None when the methodology has no such process).
    pub fn process_steps(&self, name: &str) -> Option<Vec<StepInfo>> {
        let p = self.definition.process_by_name(name)?;
        Some(self.step_tree(&p.steps, name))
    }

    /// MethodSteps returns the compiled step tree of a method composing its own steps (None when it has none).
    pub fn method_steps(&self, name: &str) -> Option<Vec<StepInfo>> {
        self.definition
            .methods
            .iter()
            .find(|me| me.name == name && !me.steps.is_empty())
            .map(|me| self.step_tree(&me.steps, name))
    }

    /// stepTree builds the compiled step tree of owner (the name of the process or method).
    fn step_tree(&self, top: &[Step], owner: &str) -> Vec<StepInfo> {
        let mut planned: HashMap<&str, Vec<&Action>> = HashMap::new();
        for a in &self.processes.actions {
            planned.entry(a.step.as_str()).or_default().push(a);
        }
        fn build(
            c: &Compiled,
            planned: &HashMap<&str, Vec<&Action>>,
            steps: &[Step],
            prefix: &str,
            roles: Option<&Responsibilities>,
            owner: &str,
        ) -> Vec<StepInfo> {
            let mut out = Vec::with_capacity(steps.len());
            for s in steps {
                let path = format!("{prefix}/{}", s.name);
                let cr = c.processes.criteria.get(&path).cloned().unwrap_or_default();
                let eff = s.roles.as_ref().or(roles);
                let mut info = StepInfo {
                    step: s.clone(),
                    effective: eff.cloned(),
                    owner: owner.to_string(),
                    path: path.clone(),
                    entry: cr.entry,
                    exit: cr.done,
                    planned: Vec::new(),
                    steps: build(c, planned, &s.steps, &path, eff, owner),
                };
                info.step.steps = Vec::new();
                if let Some(list) = planned.get(path.as_str()) {
                    for (i, a) in list.iter().enumerate() {
                        info.planned.push(a.name.clone());
                        if i == 0 {
                            info.entry = a.pre.clone(); // with the preconditions of what it runs
                        }
                    }
                }
                out.push(info);
            }
            out
        }
        build(self, &planned, top, owner, None, owner)
    }

    /// ConditionDescription says what a condition of the methodology means (Description of its definition).
    pub fn condition_description(&self, name: &str) -> String {
        for set in [&self.conditions, &self.method_guards, &self.whens] {
            let d = set.description(name);
            if !d.is_empty() {
                return d.to_string();
            }
        }
        String::new()
    }

    /// StepByPath returns the step of a path ("<process>/<step>/<sub-step>").
    pub fn step_by_path(&self, path: &str) -> Option<StepInfo> {
        let process = path.split('/').next().unwrap_or("");
        fn find(steps: &[StepInfo], path: &str) -> Option<StepInfo> {
            for s in steps {
                if s.path == path {
                    return Some(s.clone());
                }
                if path.starts_with(&format!("{}/", s.path)) {
                    return find(&s.steps, path);
                }
            }
            None
        }
        if let Some(s) = find(&self.process_steps(process).unwrap_or_default(), path) {
            return Some(s);
        }
        find(&self.method_steps(process).unwrap_or_default(), path)
    }

    /// StepCriteria returns the entry and exit criteria of a compiled step or method-step by its full path.
    pub fn step_criteria(&self, path: &str) -> Option<(Conds, Conds)> {
        self.processes
            .criteria
            .get(path)
            .map(|cr| (cr.entry.clone(), cr.done.clone()))
    }

    /// StepDependencies maps the path of each step of the processes to the paths of the steps it relies on, directly
    /// or through other steps (ADR 0109 §5).
    pub fn step_dependencies(&self) -> BTreeMap<String, BTreeMap<String, bool>> {
        #[derive(Default)]
        struct S {
            pre: Conds,
            exit: Conds,
        }
        let mut steps: BTreeMap<&str, S> = BTreeMap::new();
        for a in self.actions.values() {
            if a.step.is_empty() {
                continue;
            }
            let s = steps.entry(a.step.as_str()).or_default();
            s.pre.extend(a.pre.iter().map(|(k, v)| (k.clone(), *v)));
            s.exit
                .extend(a.effects.iter().map(|(k, v)| (k.clone(), *v)));
        }
        let mut direct: BTreeMap<&str, Vec<&str>> = BTreeMap::new();
        for (later, l) in &steps {
            for (earlier, e) in &steps {
                if later == earlier {
                    continue;
                }
                if l.pre.iter().any(|(k, v)| e.exit.get(k) == Some(v)) {
                    direct.entry(later).or_default().push(earlier);
                }
            }
        }
        let mut out = BTreeMap::new();
        for later in direct.keys() {
            let mut seen: BTreeMap<String, bool> = BTreeMap::new();
            let mut stack: Vec<&str> = vec![later];
            while let Some(p) = stack.pop() {
                for e in direct.get(p).map(|v| v.as_slice()).unwrap_or(&[]) {
                    if !seen.contains_key(*e) && e != later {
                        seen.insert(e.to_string(), true);
                        stack.push(e);
                    }
                }
            }
            out.insert(later.to_string(), seen);
        }
        out
    }
}

/// StepWithin reports whether the step of path is the step of ancestor or one of its sub-steps.
pub fn step_within(path: &str, ancestor: &str) -> bool {
    path == ancestor || path.starts_with(&format!("{ancestor}/"))
}

/// narrow is the action of a step running the scoped action a over a part of its subjects (Step.Where).
fn narrow(mut a: Action, wh: &str, need: &Conds) -> Action {
    let mut pre = need.clone();
    let pending = a.pending_condition();
    for (k, v) in &a.pre {
        if *k != pending {
            pre.insert(k.clone(), *v);
        }
    }
    pre.extend(a.scoped_pre.iter().map(|(k, v)| (k.clone(), *v)));
    let mut eff = Conds::new();
    for (k, v) in &a.effects {
        if !a.lifted.get(k).copied().unwrap_or(false) {
            eff.insert(k.clone(), *v);
        }
    }
    eff.extend(a.scoped_effects.iter().map(|(k, v)| (k.clone(), *v)));
    a.pre = pre;
    a.effects = eff;
    a.r#where = and_where(&a.r#where, wh);
    a.narrowed = true;
    a.scoped_pre = Conds::new();
    a.scoped_effects = Conds::new();
    a.lifted = Conds::new();
    a.is_lifted = false;
    a
}

/// andWhere is the conjunction of two where expressions, "a && b" (each side parenthesized when it holds an
/// alternative).
pub(crate) fn and_where(a: &str, b: &str) -> String {
    let wrap = |x: &str| {
        if x.contains("||") || x.contains('?') {
            format!("({x})")
        } else {
            x.to_string()
        }
    };
    if a.is_empty() {
        return b.to_string();
    }
    if b.is_empty() {
        return a.to_string();
    }
    format!("{} && {}", wrap(a), wrap(b))
}

/// The issue of a path and message.
#[allow(dead_code)]
pub(crate) fn issue(path: impl Into<String>, msg: impl Into<String>) -> Issue {
    Issue::new(path, msg)
}
