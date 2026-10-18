//! Methods, roles and subscriptions (`method.go`).

use std::collections::{BTreeMap, HashSet};

use goap_condition::Definition;
use goap_def::NAME_RE;
use goap_domain::Blackboard;
use serde::{Deserialize, Serialize};

use crate::methodology::{
    Action, Agent, Compiled, Goal, Methodology, PLANNER_GOAP, PLANNER_HYBRID, PLANNER_LLM,
    PLANNER_LLM_SCORING, PLANNER_UTILITY,
};
use crate::persist::{Persistence, check_persistence};
use crate::process::{
    Add, CompiledProcesses, Reference, Step, StepWalker, check_references, methods_for,
};
use crate::specialize::rank_by_priority;
use crate::util::{Conds, is_false, is_zero_i64, nd, q, st};

/// Method is how a step capability is carried out in a context (ADR 0035 §1, reshaped by ADR 0050).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Method {
    #[serde(deserialize_with = "st")]
    pub name: String,
    /// For is the capability the method provides; several methods provide one, in different contexts.
    #[serde(rename = "for", deserialize_with = "st")]
    pub r#for: String,
    /// When is a CEL condition on the blackboard: the context where the method applies (empty: always).
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub when: String,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub priority: i64,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub description: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub guidance: String,
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub checklist: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub deliverables: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub references: Vec<Reference>,
    /// Roles involved when the method is used (replacing those of the step, the method being more precise).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub roles: Option<Responsibilities>,
    /// Steps composes the method from its own steps and sub-steps.
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub steps: Vec<Step>,
    /// Actions are the actions that realize the method (ADR 0050): the pool its agent plans over.
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub actions: Vec<String>,
    /// Done are the exit criteria of a method composing no Steps.
    #[serde(skip_serializing_if = "Conds::is_empty", deserialize_with = "nd")]
    pub done: Conds,
    /// Planner (goap by default), Model and MCPs configure the agent instance applying the method.
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub planner: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub model: String,
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub mcps: Vec<String>,
    /// On makes the method reactive (ADR 0110).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub on: Vec<Subscription>,
    /// Persistent makes the agent applying the method persistent (ADR 0112).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub persistent: Option<Persistence>,
}

impl Method {
    /// ActorAgent is the agent generated for the method (ADR 0050): the agent instance that applies it, of its name.
    pub fn actor_agent(&self) -> &str {
        &self.name
    }

    /// Specificity ranks methods of equal priority: how many conditions a method's context states (none: the
    /// generic method, the fallback). The conjuncts of its `when` are counted.
    pub fn specificity(&self) -> i64 {
        if self.when.trim().is_empty() {
            return 0;
        }
        1 + self.when.matches("&&").count() as i64
    }

    /// GuardCondition is the name of the condition of the method's context.
    pub fn guard_condition(&self) -> String {
        format!("method:{}", self.name)
    }
}

/// MethodCapability is the method of a step that names a capability.
pub const METHOD_CAPABILITY: &str = "method";

/// compiledMethods holds the validated methods: their goals and guards.
#[derive(Debug, Clone, Default)]
pub(crate) struct CompiledMethods {
    /// by method name
    pub(crate) goals: BTreeMap<String, Goal>,
    pub(crate) guards: Vec<Definition>,
}

/// compileMethods validates the methods: their names, capabilities, contexts and actors.
pub(crate) fn compile_methods(
    m: &Methodology,
    add: &mut Add<'_>,
    actions: &BTreeMap<String, Action>,
    known: &mut HashSet<String>,
    agents: &BTreeMap<String, Agent>,
    roles: &HashSet<String>,
) -> (CompiledMethods, CompiledProcesses) {
    let mut out = CompiledMethods::default();
    let mut gen_ = CompiledProcesses::default();
    let no_methods = CompiledMethods::default();
    let mut seen: HashSet<String> = HashSet::new();
    // the criteria of the nested processes, shared by the walks of every method (one walker in Go)
    let mut done_map = Default::default();
    for (i, me) in m.methods.iter().enumerate() {
        let path = format!("methods[{i}]");
        let (has_steps, has_actions) = (!me.steps.is_empty(), !me.actions.is_empty());
        if !NAME_RE.is_match(&me.name) {
            add(
                format!("{path}.name"),
                "name required: lowercase letters, digits, '-' or '_', starting with a letter"
                    .into(),
            );
            continue;
        } else if seen.contains(&me.name) {
            add(
                format!("{path}.name"),
                format!("duplicate method {}", me.name),
            );
            continue;
        } else if agents.get(&me.name).is_some_and(|a| !a.name.is_empty()) {
            add(
                format!("{path}.name"),
                format!(
                    "{} is already an agent: the agent applying a method is generated, of the method's name",
                    me.name
                ),
            );
            continue;
        } else if m.processes.iter().any(|p| p.name == me.name) {
            add(
                format!("{path}.name"),
                format!(
                    "{} is already a process: the agent applying a method is generated, of the method's name",
                    me.name
                ),
            );
            continue;
        } else if m.goals.iter().any(|g| g.name == me.name) {
            add(
                format!("{path}.name"),
                format!(
                    "{} is already a goal: a method reaches a goal of its name",
                    me.name
                ),
            );
            continue;
        }
        seen.insert(me.name.clone());
        if !NAME_RE.is_match(&me.r#for) {
            add(
                format!("{path}.for"),
                "for names the capability the method provides: lowercase letters, digits, '-' or '_'"
                    .into(),
            );
        }
        check_references(add, &format!("{path}.references"), &me.references);
        check_responsibilities(add, &format!("{path}.roles"), me.roles.as_ref(), roles);
        if !me.when.is_empty() {
            let d = Definition {
                name: me.guard_condition(),
                expr: me.when.clone(),
                description: format!("the method {} applies here", me.name),
                ..Definition::default()
            };
            match goap_condition::compile(std::slice::from_ref(&d)) {
                Err(e) => {
                    let msg = e.to_string();
                    let prefix = format!("condition {}: ", q(&d.name));
                    add(
                        format!("{path}.when"),
                        msg.strip_prefix(&prefix).unwrap_or(&msg).to_string(),
                    );
                }
                Ok(_) => out.guards.push(d),
            }
        }
        for (j, a) in me.actions.iter().enumerate() {
            if !actions.contains_key(a) {
                add(
                    format!("{path}.actions[{j}]"),
                    format!("unknown action {}", q(a)),
                );
            }
        }
        match me.planner.as_str() {
            "" | PLANNER_GOAP | PLANNER_UTILITY | PLANNER_HYBRID => {}
            PLANNER_LLM | PLANNER_LLM_SCORING => {
                if me.model.is_empty() {
                    add(
                        format!("{path}.model"),
                        format!("model is required for the {} planner", me.planner),
                    );
                }
            }
            _ => add(
                format!("{path}.planner"),
                "planner must be goap, utility, hybrid, llm or llm-scoring".into(),
            ),
        }
        check_persistence(add, &path, me.persistent.as_ref(), known);
        let mut agent = Agent {
            name: me.name.clone(),
            description: me.description.clone(),
            planner: me.planner.clone(),
            model: me.model.clone(),
            mcps: me.mcps.clone(),
            actions: me.actions.clone(),
            goals: vec![me.name.clone()],
            persistent: me.persistent.clone(),
            process: me.name.clone(),
            ..Agent::default()
        };
        if agent.planner.is_empty() {
            agent.planner = PLANNER_GOAP.into();
        }
        if let Some(r) = &me.roles {
            agent.role = r.responsible.clone();
        }
        let goal;
        if !has_steps && !has_actions {
            add(
                format!("{path}.steps"),
                "a method says how: declare the actions that realize it, or its own steps".into(),
            );
            continue;
        } else if has_steps {
            if !me.done.is_empty() {
                add(
                    format!("{path}.done"),
                    "done applies to a method without steps: the exit criteria of a method composing steps are theirs"
                        .into(),
                );
            }
            let mut w = StepWalker {
                m,
                add: &mut *add,
                actions,
                known: &mut *known,
                agents,
                methods: &no_methods,
                roles,
                out: CompiledProcesses::default(),
                root: std::mem::take(&mut gen_),
                done: std::mem::take(&mut done_map),
                refs: Vec::new(),
            };
            let done = w.walk(&me.steps, &format!("{path}.steps"), &me.name, &Conds::new());
            let sub = std::mem::take(&mut w.out);
            gen_ = std::mem::take(&mut w.root);
            done_map = std::mem::take(&mut w.done);
            drop(w);
            gen_.conditions.extend(sub.conditions.iter().cloned());
            gen_.actions.extend(sub.actions.iter().cloned());
            if done.is_empty() {
                continue;
            }
            for a in &sub.actions {
                agent.actions.push(a.name.clone());
            }
            goal = Goal {
                name: me.name.clone(),
                description: me.description.clone(),
                pre: done,
                ..Goal::default()
            };
        } else {
            if me.done.is_empty() {
                add(
                    format!("{path}.done"),
                    "a method without steps states what it reaches: its exit criteria".into(),
                );
                continue;
            }
            for c in me.done.keys() {
                if !known.contains(c) {
                    add(
                        format!("{path}.done"),
                        format!("unknown condition {}", q(c)),
                    );
                }
            }
            goal = Goal {
                name: me.name.clone(),
                description: me.description.clone(),
                pre: me.done.clone(),
                ..Goal::default()
            };
        }
        gen_.goals.push(goal.clone());
        gen_.agents.push(agent);
        out.goals.insert(me.name.clone(), goal);
    }
    (out, gen_)
}

impl Compiled {
    /// MethodsFor returns the methods providing a capability that apply on the blackboard, the highest priority first
    /// then the most specific context (declaration order among equals), each with the goal its agent reaches.
    pub fn methods_for(&self, capability: &str, bb: &Blackboard) -> Vec<MethodChoice> {
        let state = self.method_guards.evaluate(bb).state;
        let candidates: Vec<Method> =
            methods_for(&self.definition, capability, &self.methods.goals)
                .into_iter()
                .cloned()
                .collect();
        let ranked = rank_by_priority(
            candidates,
            |me: &Method| {
                me.when.is_empty() || state.get(&me.guard_condition()).copied().unwrap_or(false)
            },
            |me: &Method| me.priority * 100 + me.specificity().min(99),
        );
        ranked
            .into_iter()
            .map(|me| {
                let agent_goal = self
                    .methods
                    .goals
                    .get(&me.name)
                    .map(|g| g.name.clone())
                    .unwrap_or_default();
                MethodChoice {
                    method: me,
                    agent_goal,
                }
            })
            .collect()
    }

    /// CapabilityGuards are the conditions of the contexts of the methods providing a capability (GuardCondition,
    /// the methods applying everywhere left out).
    pub fn capability_guards(&self, capability: &str) -> Vec<String> {
        methods_for(&self.definition, capability, &self.methods.goals)
            .into_iter()
            .filter(|me| !me.when.is_empty())
            .map(|me| me.guard_condition())
            .collect()
    }

    /// MethodByName returns a method by name.
    pub fn method_by_name(&self, name: &str) -> Option<&Method> {
        self.definition.methods.iter().find(|me| me.name == name)
    }

    /// MethodGoal is the goal the agent of a valid method reaches ("" for an unknown method).
    pub fn method_goal(&self, name: &str) -> String {
        self.methods
            .goals
            .get(name)
            .map(|g| g.name.clone())
            .unwrap_or_default()
    }
}

/// MethodChoice is an applicable method and the goal its agent reaches.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct MethodChoice {
    pub method: Method,
    pub agent_goal: String,
}

impl std::ops::Deref for MethodChoice {
    type Target = Method;
    fn deref(&self) -> &Method {
        &self.method
    }
}

/// Role is a role a methodology needs (ADR 0035 §2).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Role {
    #[serde(deserialize_with = "st")]
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub description: String,
    /// Operations are the operations on a change the role allows (ADR 0103): codes of the catalogue or kinds.
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub operations: Vec<String>,
}

/// Responsibilities assign roles to a step or a method, RACI style.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Responsibilities {
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub responsible: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub accountable: String,
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub consulted: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub informed: Vec<String>,
}

impl Methodology {
    /// compileRoles validates the declared roles and returns their names.
    pub(crate) fn compile_roles(&self, add: &mut Add<'_>) -> HashSet<String> {
        let mut out = HashSet::new();
        for (i, r) in self.roles.iter().enumerate() {
            let path = format!("roles[{i}].name");
            if !NAME_RE.is_match(&r.name) {
                add(
                    path,
                    "role name required: lowercase letters, digits, '-' or '_', starting with a letter"
                        .into(),
                );
            } else if out.contains(&r.name) {
                add(path, format!("duplicate role {}", r.name));
            } else {
                out.insert(r.name.clone());
            }
            for (j, op) in r.operations.iter().enumerate() {
                if !goap_operations::known(op) {
                    add(
                        format!("roles[{i}].operations[{j}]"),
                        format!(
                            "unknown operation {} (a code of the catalogue, or doer / reviewer)",
                            q(op)
                        ),
                    );
                }
            }
        }
        out
    }

    /// Restricts reports whether a role of the methodology lists the operations it allows (ADR 0103).
    pub fn restricts(&self) -> bool {
        self.roles.iter().any(|r| !r.operations.is_empty())
    }

    /// OperationRoles returns the roles of the methodology that allow the operation of code (ADR 0103): the roles
    /// listing it, or its kind, and the roles listing no operation. restricted is false when no role lists any.
    pub fn operation_roles(&self, code: &str) -> (Vec<String>, bool) {
        if !self.restricts() {
            return (Vec::new(), false);
        }
        let roles = self
            .roles
            .iter()
            .filter(|r| r.operations.is_empty() || goap_operations::allows(&r.operations, code))
            .map(|r| r.name.clone())
            .collect();
        (roles, true)
    }

    /// checkElementRoles reports the roles agents and actions name that the methodology does not declare (ADR 0043).
    pub(crate) fn check_element_roles(&self, add: &mut Add<'_>, roles: &HashSet<String>) {
        let mut check = |path: String, list: &[String]| {
            for (j, r) in list.iter().enumerate() {
                if !roles.contains(r) {
                    add(
                        format!("{path}[{j}]"),
                        format!("unknown role {} (declare it in roles)", q(r)),
                    );
                }
            }
        };
        for (i, ag) in self.agents.iter().enumerate() {
            check(format!("agents[{i}].roles"), &ag.roles);
        }
        for (i, a) in self.actions.iter().enumerate() {
            check(format!("actions[{i}].roles"), &a.roles);
        }
    }

    /// Subscriptions returns the events the methodology reacts to (its own, or the default ones).
    pub fn subscriptions(&self) -> Vec<Subscription> {
        if !self.on.is_empty() {
            return self.on.clone();
        }
        default_subscriptions()
    }
}

/// checkResponsibilities reports the roles a step or a method names that the methodology does not declare.
pub(crate) fn check_responsibilities(
    add: &mut Add<'_>,
    path: &str,
    r: Option<&Responsibilities>,
    roles: &HashSet<String>,
) {
    let Some(r) = r else { return };
    let mut check = |field: &str, role: &str| {
        if !role.is_empty() && !roles.contains(role) {
            add(
                format!("{path}.{field}"),
                format!("unknown role {} (declare it in roles)", q(role)),
            );
        }
    };
    check("responsible", &r.responsible);
    check("accountable", &r.accountable);
    for x in &r.consulted {
        check("consulted", x);
    }
    for x in &r.informed {
        check("informed", x);
    }
}

/// Subscription is an event a methodology reacts to: its type and a CEL filter over `event`. From says whose events
/// (ADR 0106, ADR 0108).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Subscription {
    #[serde(deserialize_with = "st")]
    pub event: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub filter: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub from: String,
    /// Supersede makes an event of the subscription arriving while the reactive run is at work interrupt it and run
    /// it again on the change as it is now (ADR 0110).
    #[serde(skip_serializing_if = "is_false")]
    pub supersede: bool,
}

impl Subscription {
    /// Origin is where the events of the subscription come from (FromSelf when unset).
    pub fn origin(&self) -> &str {
        if self.from.is_empty() {
            crate::integrate::FROM_SELF
        } else {
            &self.from
        }
    }
}

/// DefaultSubscriptions are the events a transverse methodology without `on` reacts to.
pub fn default_subscriptions() -> Vec<Subscription> {
    vec![
        Subscription {
            event: goap_events::PROCESS_ATTACHED.into(),
            ..Subscription::default()
        },
        Subscription {
            event: goap_events::STEP_COMPLETED.into(),
            ..Subscription::default()
        },
    ]
}
