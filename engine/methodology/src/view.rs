//! The compiled view of the activities of a methodology (ADR 0126 step 3, `view.go`).

use std::collections::{BTreeMap, HashMap, HashSet};
use std::sync::Arc;

use serde::{Deserialize, Serialize};

use crate::method::{METHOD_CAPABILITY, Responsibilities};
use crate::methodology::Compiled;
use crate::process::{METHOD_ACTION, METHOD_PROCESS, Reference, StepInfo};
use crate::review::Review;
use crate::util::{Conds, is_false};

/// View is the compiled shape of the activities of a methodology as they are shown (ADR 0126 step 3).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct View {
    pub methodology: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub version: String,
    /// Goal is the main goal of the methodology (ADR 0096).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub goal: String,
    pub processes: Vec<ActivityView>,
    /// Methods are the methods (ADR 0050), with their steps when they compose some.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub methods: Vec<ActivityView>,
    /// Conditions describes every condition an entry, an exit or a goal of the view names, and their prerequisites.
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub conditions: BTreeMap<String, ConditionView>,
}

/// Kinds of activity of a view.
pub const ACTIVITY_PROCESS: &str = "process";
pub const ACTIVITY_METHOD: &str = "method";

/// ActivityView is a process or a method of the view.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct ActivityView {
    pub name: String,
    pub kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    /// Agent and Goal run the activity as a whole; Done is what its goal asks.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub agent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub goal: String,
    #[serde(skip_serializing_if = "Conds::is_empty")]
    pub done: Conds,
    #[serde(skip_serializing_if = "is_false")]
    pub nested: bool,
    #[serde(skip_serializing_if = "is_false")]
    pub reactive: bool,
    /// For is the capability a method provides, Roles the roles it assigns, Actions the pool of actions it owns.
    #[serde(rename = "for", skip_serializing_if = "String::is_empty")]
    pub r#for: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub roles: Option<Responsibilities>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub actions: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub references: Vec<Reference>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub steps: Vec<StepView>,
}

/// StepView is a step of an activity of the view, with its sub-steps.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct StepView {
    pub path: String,
    pub name: String,
    pub owner: String,
    pub method: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub target: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub capability: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub process: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub foreach: String,
    #[serde(rename = "groupBy", skip_serializing_if = "String::is_empty")]
    pub group_by: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub review: Option<Review>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub guidance: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub checklist: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub references: Vec<Reference>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub roles: Option<Responsibilities>,
    #[serde(skip_serializing_if = "Conds::is_empty")]
    pub entry: Conds,
    #[serde(skip_serializing_if = "Conds::is_empty")]
    pub exit: Conds,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub planned: Vec<String>,
    /// Unavailable, for a step naming a capability, are the context conditions of the methods providing it.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub unavailable: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub steps: Vec<StepView>,
}

/// ConditionView is what a condition means, the actions making it hold and the conditions it cannot hold without.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ConditionView {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(rename = "producedBy", skip_serializing_if = "Vec::is_empty")]
    pub produced_by: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub requires: Vec<String>,
}

impl View {
    /// Process is the process of that name in the view.
    pub fn process(&self, name: &str) -> Option<&ActivityView> {
        self.processes.iter().find(|p| p.name == name)
    }

    /// Method is the method of that name in the view.
    pub fn method(&self, name: &str) -> Option<&ActivityView> {
        self.methods.iter().find(|m| m.name == name)
    }

    /// Condition is what the view says of a condition (nothing when it names none).
    pub fn condition(&self, name: &str) -> ConditionView {
        self.conditions.get(name).cloned().unwrap_or_default()
    }
}

impl Compiled {
    /// View is the view of the activities of the methodology (computed once).
    pub fn view(&self) -> Arc<View> {
        self.view
            .get_or_init(|| Arc::new(self.build_view()))
            .clone()
    }

    fn build_view(&self) -> View {
        let mut v = View {
            methodology: self.name().to_string(),
            version: self.version().to_string(),
            goal: self.main_goal(),
            ..View::default()
        };
        let reactive: HashSet<String> = self.reactions().iter().map(|r| r.owner.clone()).collect();
        let mut agents: HashMap<String, String> = HashMap::new();
        for ag in self.agent_list() {
            let p = ag.process_of();
            if !p.is_empty() && !agents.contains_key(p) {
                agents.insert(p.to_string(), ag.name.clone());
            }
        }
        for p in self.processes() {
            let mut a = ActivityView {
                name: p.name.clone(),
                kind: ACTIVITY_PROCESS.into(),
                description: p.description.clone(),
                agent: agents.get(&p.name).cloned().unwrap_or_default(),
                nested: p.nested,
                reactive: reactive.contains(&p.name),
                steps: self.step_views(&self.process_steps(&p.name).unwrap_or_default()),
                ..ActivityView::default()
            };
            if let Some(d) = self.definition.process_by_name(&p.name) {
                a.references = d.references.clone();
            }
            if let Some(g) = self.goal(&p.name) {
                a.goal = g.name;
                a.done = g.pre;
            }
            v.processes.push(a);
        }
        for me in &self.definition.methods {
            let mut a = ActivityView {
                name: me.name.clone(),
                kind: ACTIVITY_METHOD.into(),
                description: me.description.clone(),
                agent: me.actor_agent().to_string(),
                reactive: reactive.contains(&me.name),
                r#for: me.r#for.clone(),
                roles: me.roles.clone(),
                actions: me.actions.clone(),
                references: me.references.clone(),
                steps: self.step_views(&self.method_steps(&me.name).unwrap_or_default()),
                ..ActivityView::default()
            };
            if let Some(g) = self.methods.goals.get(&me.name) {
                a.goal = g.name.clone();
                a.done = g.pre.clone();
            }
            v.methods.push(a);
        }
        // the conditions named, and their prerequisites
        let mut todo: Vec<String> = Vec::new();
        fn walk(todo: &mut Vec<String>, steps: &[StepView]) {
            for s in steps {
                todo.extend(s.entry.keys().cloned());
                todo.extend(s.exit.keys().cloned());
                todo.extend(s.unavailable.iter().cloned());
                walk(todo, &s.steps);
            }
        }
        for a in v.processes.iter().chain(v.methods.iter()) {
            todo.extend(a.done.keys().cloned());
            walk(&mut todo, &a.steps);
        }
        while let Some(last) = todo.pop() {
            let name = last.strip_prefix('!').unwrap_or(&last).to_string();
            if v.conditions.contains_key(&name) {
                continue;
            }
            let cv = ConditionView {
                description: self.condition_description(&name),
                produced_by: self.producers(&name),
                requires: self.prerequisites(&name),
            };
            todo.extend(cv.requires.iter().cloned());
            v.conditions.insert(name, cv);
        }
        v
    }

    fn step_views(&self, steps: &[StepInfo]) -> Vec<StepView> {
        let mut out = Vec::with_capacity(steps.len());
        for s in steps {
            let mut sv = StepView {
                path: s.path.clone(),
                name: s.name.clone(),
                owner: s.owner.clone(),
                method: s.method().to_string(),
                capability: s.capability.clone(),
                process: s.process.clone(),
                foreach: s.foreach.clone(),
                group_by: s.group_by.clone(),
                review: s.review.clone(),
                description: s.description.clone(),
                guidance: s.guidance.clone(),
                checklist: s.checklist.clone(),
                references: s.references.clone(),
                roles: s.effective.clone(),
                entry: s.entry.clone(),
                exit: s.exit.clone(),
                planned: s.planned.clone(),
                steps: self.step_views(&s.steps),
                ..StepView::default()
            };
            match sv.method.as_str() {
                METHOD_ACTION => {
                    sv.target = if s.action.is_empty() {
                        s.actions.join(" | ")
                    } else {
                        s.action.clone()
                    };
                }
                METHOD_PROCESS => sv.target = s.process.clone(),
                METHOD_CAPABILITY => {
                    sv.target = s.capability.clone();
                    sv.unavailable = self.capability_guards(&s.capability);
                }
                _ => {}
            }
            out.push(sv);
        }
        out
    }
}
