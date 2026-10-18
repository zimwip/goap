//! The declarative format of an enterprise methodology (conditions, actions, goals, agents) and its compiled form
//! (`methodology.go`).

use std::collections::{BTreeMap, BTreeSet};
use std::sync::{Arc, OnceLock};

use goap_condition::{AssessmentViews, Expectation, NumberSet, Set};
use goap_def::{Issues, TypeSet};
use goap_domain::Blackboard;
use goap_form::Form;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::assessment::AssessmentView;
use crate::domain::BuiltinSet;
use crate::event::Event;
use crate::integrate::{Guardian, Integration};
use crate::loops::Loop;
use crate::method::{CompiledMethods, Method, Role, Subscription};
use crate::persist::Persistence;
use crate::process::{CompiledProcesses, Process, Step};
use crate::reaction::Reaction;
use crate::util::{Conds, is_false, is_zero_f64, is_zero_i64, nd, st};
use crate::view::View;

/// Methodology is the declarative definition deployed in the registry.
#[derive(Clone, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct Methodology {
    #[serde(deserialize_with = "st")]
    pub name: String,
    #[serde(deserialize_with = "st")]
    pub version: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub description: String,
    /// Namespace is the target namespace (ADR 0013 §3): the namespace the changes of the methodology act on, hence the
    /// domain of the nodes it creates and modifies (alm; methodology for the self-observation). Required.
    #[serde(deserialize_with = "st")]
    pub namespace: String,
    /// Domains pins the version of the domains the methodology is written against (ADR 0113), by domain name.
    #[serde(skip_serializing_if = "BTreeMap::is_empty", deserialize_with = "nd")]
    pub domains: BTreeMap<String, String>,
    /// Administrative exempts the changes of the methodology from naming a project (ADR 0039).
    #[serde(skip_serializing_if = "is_false")]
    pub administrative: bool,
    /// Lifecycle names the lifecycle of the domain the changes of the methodology follow (ADR 0058).
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub lifecycle: String,
    /// Criticality is the default criticality of the changes of the methodology (ADR 0075 §3): C1, C2 or C3.
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub criticality: String,
    /// Goal names the main goal of the methodology (ADR 0096): a declared goal or a process.
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub goal: String,
    #[serde(deserialize_with = "nd")]
    pub conditions: Vec<Condition>,
    #[serde(deserialize_with = "nd")]
    pub actions: Vec<Action>,
    #[serde(deserialize_with = "nd")]
    pub goals: Vec<Goal>,
    /// Agents run the methodology; without agents an implicit "default" agent has every action and goal and the goap
    /// planner.
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub agents: Vec<Agent>,
    /// Processes describe how the objective of a change is reached (ADR 0034).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub processes: Vec<Process>,
    /// Methods are the documentary references of how a step capability is carried out in a context (ADR 0035 §1).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub methods: Vec<Method>,
    /// Roles are the roles the processes and methods assign (ADR 0035 §2).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub roles: Vec<Role>,
    /// AppliesTo makes the methodology transverse (ADR 0036 §3).
    #[serde(
        rename = "appliesTo",
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "nd"
    )]
    pub applies_to: Vec<String>,
    /// Imports names the built-in condition libraries (ADR 0064) whose conditions the methodology uses.
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub imports: Vec<String>,
    /// On are the events of those changes a transverse methodology reacts to (ADR 0036 §3).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub on: Vec<Subscription>,
    /// Events are the events of the methodology (ADR 0110), in addition to the standard ones.
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub events: Vec<Event>,
    /// Integrates names the methodologies whose changes the changes of this one integrate as sub-changes (ADR 0106).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub integrates: Vec<Integration>,
    /// Guardian is what the methodology requires of the changes it integrates (ADR 0106).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub guardian: Option<Guardian>,
    /// Assessments say how the assessments of the subjects of its changes read (ADR 0116).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub assessments: Vec<AssessmentView>,
    /// Types resolves the qualified type references of the methodology (set by Resolve). None: the references are
    /// only checked for their form.
    #[serde(skip)]
    pub types: Option<Arc<dyn TypeSet>>,
    /// Builtins resolves the builtin names of the actions (set by WithBuiltins, ADR 0062). None: not checked.
    #[serde(skip)]
    pub builtins: Option<Arc<dyn BuiltinSet>>,
}

impl std::fmt::Debug for Methodology {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Methodology")
            .field("name", &self.name)
            .field("version", &self.version)
            .field("namespace", &self.namespace)
            .finish_non_exhaustive()
    }
}

// Planners.
/// A* over the world state.
pub const PLANNER_GOAP: &str = "goap";
/// Greedy: the applicable action with the highest utility.
pub const PLANNER_UTILITY: &str = "utility";
/// A* with costs weighted by utilities (CEL).
pub const PLANNER_HYBRID: &str = "hybrid";
/// The LLM picks the next action directly.
pub const PLANNER_LLM: &str = "llm";
/// The LLM scores utility; A* costs weighted by it, like hybrid.
pub const PLANNER_LLM_SCORING: &str = "llm-scoring";

/// DefaultAgent is the name of the implicit agent.
pub const DEFAULT_AGENT: &str = "default";

/// Agent is a planner with a set of admissible actions and goals (Embabel terminology).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Agent {
    #[serde(deserialize_with = "st")]
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub description: String,
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub examples: Vec<String>,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub planner: String,
    /// Model is the LLM alias the llm and llm-scoring planners call each planning cycle.
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub model: String,
    /// Actions admissible for the agent (empty: all).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub actions: Vec<String>,
    /// Goals of the agent (empty: all).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub goals: Vec<String>,
    /// Triggers run the agent automatically (outside the intent loop).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub triggers: Vec<Trigger>,
    /// MCPs whose tools the llm and script actions of the agent may use.
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub mcps: Vec<String>,
    /// Roles allowed to run the agent (ADR 0043).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub roles: Vec<String>,
    /// Role is the role the agent acts as (ADR 0050).
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub role: String,
    /// Persistent makes the agent persistent (ADR 0112).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub persistent: Option<Persistence>,
    /// process is set on the agent generated to run a process (ADR 0034).
    #[serde(skip)]
    pub(crate) process: String,
}

// Trigger types, events and targets.
pub const TRIGGER_EVENT: &str = "event";
pub const TRIGGER_SCHEDULE: &str = "schedule";
pub const TARGET_NEW_CHANGE: &str = "new_change";
pub const TARGET_EVENT_CHANGE: &str = "event_change";

/// TriggerEvents lists the events a trigger can react to.
pub const TRIGGER_EVENTS: &[&str] = goap_events::ALL;

/// Trigger starts an agent automatically on an event or a schedule.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Trigger {
    #[serde(deserialize_with = "st")]
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub description: String,
    #[serde(rename = "type", deserialize_with = "st")]
    pub r#type: String,
    /// Event and Filter (CEL over `event`) for event triggers.
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub event: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub filter: String,
    /// Schedule is a 5-field cron expression (UTC) for schedule triggers.
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub schedule: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub goal: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub intent: String,
    /// Target: new_change (default) or event_change (the change of the event).
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub target: String,
    /// Supersede (event triggers on the event's change): an event firing the trigger cancels its runs still at work
    /// on that change before it starts the new one (ADR 0110).
    #[serde(skip_serializing_if = "is_false")]
    pub supersede: bool,
    /// Roles of the service identity running the process.
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub roles: Vec<String>,
    pub enabled: bool,
}

/// Condition is a named CEL predicate on the blackboard: about the change (the default), about one subject of a
/// scope (Scope, Where: `self` is the subject, ADR 0109 §3), or an aggregate bringing a scoped condition to the level
/// of the change (All, Any or None, Where, Empty).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Condition {
    #[serde(deserialize_with = "st")]
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub description: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub expr: String,
    /// Scope is the kind of subject: change (default), impact, item, risk, action or step.
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub scope: String,
    /// Where narrows the subjects of a scoped condition or an aggregate (CEL, `self` the subject).
    #[serde(
        rename = "where",
        skip_serializing_if = "String::is_empty",
        deserialize_with = "st"
    )]
    pub r#where: String,
    /// All, Any, None: the scoped condition an aggregate is over.
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub all: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub any: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub none: String,
    /// Empty is the value of an all aggregate over no subject (default false).
    #[serde(skip_serializing_if = "is_false")]
    pub empty: bool,
}

impl Condition {
    /// Definition is the condition as the condition package compiles it.
    pub fn definition(&self) -> goap_condition::Definition {
        goap_condition::Definition {
            name: self.name.clone(),
            description: self.description.clone(),
            expr: self.expr.clone(),
            scope: self.scope.clone(),
            r#where: self.r#where.clone(),
            all: self.all.clone(),
            any: self.any.clone(),
            none: self.none.clone(),
            empty: self.empty,
        }
    }
}

// Action kinds.
pub const KIND_LLM: &str = "llm";
pub const KIND_TOOL: &str = "tool";
pub const KIND_HUMAN: &str = "human";
pub const KIND_BUILTIN: &str = "builtin";
pub const KIND_SCRIPT: &str = "script";
/// KindAbstract declares a role without implementation: a specialization is chosen at execution (ADR 0009 §5).
pub const KIND_ABSTRACT: &str = "abstract";
/// KindCall is the kind of the actions the compiler generates for the steps done by a sub-agent (a nested process, a
/// method): the action runs the sub-agent its Call describes and waits for it. A methodology does not declare it.
pub const KIND_CALL: &str = "call";

/// Call is what an action of kind KindCall runs (ADR 0034, ADR 0035 §1, ADR 0050): a sub-agent on the same change,
/// waited for.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Call {
    /// Methodology is the methodology of the agent (empty: the one of the action).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub methodology: String,
    /// Agent and Goal are the sub-agent and its goal, when the step names them (a nested process).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub agent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub goal: String,
    /// Capability makes the engine choose, when the step runs, the method providing it that applies there.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub capability: String,
    /// Foreach (CEL list) runs the call once per element; GroupBy (CEL value) once per group of elements instead.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub foreach: String,
    #[serde(rename = "groupBy", skip_serializing_if = "String::is_empty")]
    pub group_by: String,
}

// Script languages.
pub const LANG_JAVASCRIPT: &str = "javascript";
pub const LANG_GO: &str = "go";

/// Action is a unit of work.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Action {
    #[serde(deserialize_with = "st")]
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub description: String,
    #[serde(deserialize_with = "st")]
    pub kind: String,
    #[serde(skip_serializing_if = "Conds::is_empty", deserialize_with = "nd")]
    pub pre: Conds,
    #[serde(skip_serializing_if = "Conds::is_empty", deserialize_with = "nd")]
    pub effects: Conds,
    #[serde(skip_serializing_if = "is_zero_f64")]
    pub cost: f64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub expects: Option<Expectation>,
    /// Verify says how the effect of the action is verified (ADR 0075).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub verify: Option<Verify>,
    /// Permission required from the process initiator to run the action automatically.
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub permission: String,
    /// Roles allowed to run the action (ADR 0043).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub roles: Vec<String>,
    // llm
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub model: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub prompt: String,
    /// Loop is the budget of the tool loop of an llm action (ADR 0118).
    #[serde(rename = "loop", skip_serializing_if = "Option::is_none")]
    pub r#loop: Option<Loop>,
    /// tool: "<mcp>/<tool>", a tool of an MCP the organization of the change binds
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub tool: String,
    /// MCPs the action uses (llm and script actions).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub mcps: Vec<String>,
    // builtin
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub builtin: String,
    // human
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub instructions: String,
    #[serde(skip_serializing_if = "Map::is_empty", deserialize_with = "nd")]
    pub params: Map<String, Value>,
    /// NodeTypes restricts which qualified node types a human task may create or pick to edit.
    #[serde(
        rename = "nodeTypes",
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "nd"
    )]
    pub node_types: Vec<String>,
    /// Form is what a person fills in to carry out a human action (ADR 0115).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub form: Option<Form>,
    // script
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub language: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub code: String,
    /// Utility is a CEL expression returning a number, used by the utility and hybrid planners (default: 1).
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub utility: String,
    /// Specializes makes the action a specialization of another one: "<action>" or "<methodology>/<action>".
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub specializes: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub when: String,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub priority: i64,
    /// Incremental actions reach their effects over several executions.
    #[serde(skip_serializing_if = "is_false")]
    pub incremental: bool,
    /// On makes the action reactive (ADR 0110).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub on: Vec<Subscription>,
    /// Emits are the events of the methodology the action raises when it reaches its effects (ADR 0110).
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub emits: Vec<String>,
    /// Step is set on the actions generated for the steps of a process (ADR 0034): the path of the step;
    /// Implements names the declared action a step runs.
    #[serde(skip)]
    pub step: String,
    #[serde(skip)]
    pub implements: String,
    /// Call is the sub-agent a generated action of kind KindCall runs (compiled only).
    #[serde(skip)]
    pub call: Option<Call>,
    /// Scope makes the action scoped (ADR 0109 §3.5): it runs once per pending subject of that scope.
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub scope: String,
    #[serde(
        rename = "where",
        skip_serializing_if = "String::is_empty",
        deserialize_with = "st"
    )]
    pub r#where: String,
    /// Parallel bounds the subjects a scoped llm or tool action runs for at once.
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub parallel: i64,
    /// ScopedPre / ScopedEffects are the scoped conditions of a compiled scoped action; Lifted the aggregates its
    /// effects were lifted to (compiled only).
    #[serde(skip)]
    pub scoped_pre: Conds,
    #[serde(skip)]
    pub scoped_effects: Conds,
    #[serde(skip)]
    pub lifted: Conds,
    /// Narrowed is set on the action of a step whose where narrows the subjects of the scoped action it runs.
    #[serde(skip)]
    pub narrowed: bool,
    /// The exit criteria of the step of a narrowed action, merged again once it is lifted.
    #[serde(skip)]
    pub(crate) step_done: Conds,
    /// Whether Lifted is set (Go's non-nil map): the action was lifted by the compiler.
    #[serde(skip)]
    pub(crate) is_lifted: bool,
}

impl Action {
    /// scopeName names the generated conditions of a scoped action: the declared action, or the step narrowing it.
    pub(crate) fn scope_name(&self) -> &str {
        if self.narrowed {
            &self.name
        } else {
            self.declared()
        }
    }

    /// SubjectCondition is the generated scoped condition of a scoped action.
    pub fn subject_condition(&self) -> String {
        format!("subject:{}", self.scope_name())
    }

    /// PendingCondition is the generated condition of the change of a scoped action: some subject is pending.
    pub fn pending_condition(&self) -> String {
        format!("pending:{}", self.scope_name())
    }

    /// IsScoped reports whether the action runs per subject of a scope.
    pub fn is_scoped(&self) -> bool {
        !self.scope.is_empty() && self.scope != goap_condition::SCOPE_CHANGE
    }

    /// Declared returns the name of the declared action behind a planned one.
    pub fn declared(&self) -> &str {
        if !self.implements.is_empty() {
            &self.implements
        } else {
            &self.name
        }
    }

    /// RequiredMCPs returns the MCPs the action needs: its declared ones and the MCP of its tool.
    pub fn required_mcps(&self) -> Vec<String> {
        let mut out = self.mcps.clone();
        if self.kind == KIND_TOOL
            && let Some((m, _)) = self.tool.split_once('/')
            && !out.iter().any(|x| x == m)
        {
            out.push(m.to_string());
        }
        out
    }

    /// IsSpecialization reports whether the action specializes another one.
    pub fn is_specialization(&self) -> bool {
        !self.specializes.is_empty()
    }

    /// WhenCondition is the name of the guard condition of a specialization.
    pub fn when_condition(&self) -> String {
        format!("when:{}", self.name)
    }

    /// ExpectCondition is the name of the condition generated from an action's expects.
    pub fn expect_condition(&self) -> String {
        format!("expect:{}", self.name)
    }

    /// SpecializedAction returns the action a specialization targets and whether it belongs to the methodology named
    /// self.
    pub fn specialized_action(&self, this: &str) -> (String, bool) {
        if let Some(i) = self.specializes.find('/') {
            return (
                self.specializes[i + 1..].to_string(),
                &self.specializes[..i] == this,
            );
        }
        (self.specializes.clone(), true)
    }
}

// Oracles: the kinds of verifier of an action's effect (ADR 0075).
pub const ORACLE_TOOL: &str = "tool";
pub const ORACLE_HUMAN: &str = "human";
pub const ORACLE_MODEL: &str = "model";

/// Verify is the verification an action declares (ADR 0075).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Verify {
    /// Oracle is the kind of verifier the effect needs: tool, human or model.
    #[serde(deserialize_with = "st")]
    pub oracle: String,
    /// Independent says the verifier is not the producer; None (unset) is true.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub independent: Option<bool>,
}

impl Verify {
    /// IsIndependent reports whether the verifier must differ from the producer (the default when verify is set).
    pub fn is_independent(v: Option<&Verify>) -> bool {
        v.is_some_and(|v| v.independent.unwrap_or(true))
    }
}

/// Goal is a target state.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Goal {
    #[serde(deserialize_with = "st")]
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub description: String,
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "nd")]
    pub examples: Vec<String>,
    #[serde(deserialize_with = "nd")]
    pub pre: Conds,
    #[serde(skip_serializing_if = "is_zero_f64")]
    pub value: f64,
}

/// The errors of the package.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// The YAML (or JSON) does not decode into a methodology, or names a field no definition has.
    #[error("parse methodology: {0}")]
    Parse(String),
    /// The methodology has issues (Compile): `methodology <name>: <issues>`.
    #[error("methodology {name}: {issues}")]
    Invalid { name: String, issues: Issues },
    /// A file that cannot be read.
    #[error("{0}")]
    Io(#[from] std::io::Error),
    /// The domains of a methodology file that do not load.
    #[error(transparent)]
    Domains(#[from] goap_def::Error),
    /// A methodology that does not render (YAML).
    #[error("{0}")]
    Render(String),
}

/// Parse decodes a YAML (or JSON) methodology. Unknown fields are rejected.
pub fn parse(data: &[u8]) -> Result<Methodology, Error> {
    let text = std::str::from_utf8(data).map_err(|e| Error::Parse(e.to_string()))?;
    if text.trim().is_empty() {
        return Err(Error::Parse("EOF".into()));
    }
    let mut unknown: Vec<String> = Vec::new();
    let m: Methodology =
        serde_ignored::deserialize(serde_yaml_ng::Deserializer::from_str(text), |p| {
            unknown.push(segments(&p).join("."));
        })
        .map_err(|e| Error::Parse(e.to_string()))?;
    if !unknown.is_empty() {
        let fields: Vec<String> = unknown
            .iter()
            .map(|p| {
                format!(
                    "field {} not found (at {p})",
                    p.rsplit('.').next().unwrap_or(p)
                )
            })
            .collect();
        return Err(Error::Parse(fields.join("; ")));
    }
    Ok(m)
}

/// The segments of the path of an ignored field: map keys and sequence indexes, the option wrappers left out.
fn segments(p: &serde_ignored::Path<'_>) -> Vec<String> {
    use serde_ignored::Path;
    let mut out = Vec::new();
    let mut cur = p;
    loop {
        cur = match cur {
            Path::Root => break,
            Path::Seq { parent, index } => {
                out.push(index.to_string());
                parent
            }
            Path::Map { parent, key } => {
                out.push(key.clone());
                parent
            }
            Path::Some { parent }
            | Path::NewtypeStruct { parent }
            | Path::NewtypeVariant { parent } => parent,
        };
    }
    out.reverse();
    out
}

impl Methodology {
    /// Parse decodes a YAML (or JSON) methodology (see [`parse`]).
    pub fn parse(data: &[u8]) -> Result<Methodology, Error> {
        parse(data)
    }

    /// MainGoal is the goal the changes of the methodology start with (ADR 0096): the declared Goal, else the first
    /// declared goal, else the first process, else "".
    pub fn main_goal(&self) -> String {
        if !self.goal.is_empty() {
            self.goal.clone()
        } else if let Some(g) = self.goals.first() {
            g.name.clone()
        } else if let Some(p) = self.processes.first() {
            p.name.clone()
        } else {
            String::new()
        }
    }

    /// Supertypes maps each node type to its ancestors, nearest first (ADR 0009 §6), from the resolved types (empty
    /// when they are not resolved).
    pub fn supertypes(&self) -> BTreeMap<String, Vec<String>> {
        match &self.types {
            Some(t) => t.supertypes(),
            None => BTreeMap::new(),
        }
    }

    /// YAML is the export of the definition (import/export format).
    pub fn yaml(&self) -> Result<String, Error> {
        serde_yaml_ng::to_string(self).map_err(|e| Error::Render(e.to_string()))
    }
}

/// Compiled is a validated methodology ready for planning.
#[derive(Clone, Default)]
pub struct Compiled {
    /// definition is the methodology compiled: Definition gives it to those who edit, store or show it; what runs it
    /// (the engine) reads the compiled elements through the methods of Compiled (ADR 0126).
    pub(crate) definition: Arc<Methodology>,
    /// Conditions are the compiled conditions of the world of the change.
    pub conditions: Arc<Set>,
    pub(crate) actions: BTreeMap<String, Action>,
    pub(crate) utilities: Arc<NumberSet>,
    pub(crate) agents: BTreeMap<String, Agent>,
    /// whens are the guards of the specializations (not part of the world state)
    pub(crate) whens: Arc<Set>,
    /// processes are the actions, goals and agents generated for the processes (ADR 0034)
    pub(crate) processes: Arc<CompiledProcesses>,
    /// methods are the valid methods and the guards of their contexts (ADR 0035 §1)
    pub(crate) methods: Arc<CompiledMethods>,
    pub(crate) method_guards: Arc<Set>,
    /// reactions are the activities declaring on (ADR 0110)
    pub(crate) reactions: Vec<Reaction>,
    /// automaticReviews gives the automatic review step of each action generated for it (ADR 0130)
    pub(crate) automatic_reviews: BTreeMap<String, String>,
    /// assessments are the compiled assessment views (ADR 0116); None: the defaults
    pub(crate) assessments: Option<Arc<AssessmentViews>>,
    /// view is the shape of the activities as they are shown (ADR 0126 step 3), computed once
    pub(crate) view: Arc<OnceLock<Arc<View>>>,
}

impl std::fmt::Debug for Compiled {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Compiled")
            .field("name", &self.definition.name)
            .field("version", &self.definition.version)
            .finish_non_exhaustive()
    }
}

impl Compiled {
    /// Definition is the methodology as written, compiled into c.
    pub fn definition(&self) -> &Methodology {
        &self.definition
    }

    /// The definition, shared.
    pub fn definition_arc(&self) -> Arc<Methodology> {
        self.definition.clone()
    }

    /// WithDefinition is a copy of c holding m as its definition (another version of the same rules, a fake of a
    /// test); c may be a zero Compiled.
    pub fn with_definition(&self, m: Methodology) -> Compiled {
        let mut out = self.clone();
        out.definition = Arc::new(m);
        out.view = Arc::new(OnceLock::new()); // computed again from m on demand
        out
    }

    /// Action returns an action by name.
    pub fn action(&self, name: &str) -> Option<&Action> {
        self.actions.get(name)
    }

    /// Goal returns a goal by name (a declared goal, or the goal of a process).
    pub fn goal(&self, name: &str) -> Option<Goal> {
        if let Some(g) = self.definition.goals.iter().find(|g| g.name == name) {
            return Some(g.clone());
        }
        if let Some(g) = self.processes.goals.iter().find(|g| g.name == name) {
            return Some(g.clone());
        }
        // a step path ("<process-or-method>/<step>") names the goal of that step: its exit criteria (ADR 0097)
        if name.contains('/')
            && let Some(s) = self.step_by_path(name)
            && !s.exit.is_empty()
        {
            return Some(Goal {
                name: name.to_string(),
                description: s.step.description.clone(),
                pre: s.exit.clone(),
                ..Goal::default()
            });
        }
        self.reaction_goal(name)
    }

    /// NeededBy returns the conditions a state needs to reach the goal conditions, by backward closure over the
    /// actions (declared and generated for steps) (ADR 0097).
    pub fn needed_by(&self, goal: &Conds) -> BTreeSet<String> {
        let mut need: BTreeSet<String> = BTreeSet::new();
        let add = |need: &mut BTreeSet<String>, k: &str| -> bool {
            if need.contains(k) {
                return false;
            }
            need.insert(k.to_string());
            for p in self.prerequisites(k) {
                need.insert(p);
            }
            true
        };
        for k in goal.keys() {
            add(&mut need, k);
        }
        let all = self.all_actions();
        let mut changed = true;
        while changed {
            changed = false;
            for a in &all {
                if a.is_specialization() {
                    continue;
                }
                let hit = a
                    .effects
                    .iter()
                    .any(|(k, v)| *v && self.establishes(k, &need));
                if !hit {
                    continue;
                }
                for k in a.pre.keys() {
                    if add(&mut need, k) {
                        changed = true;
                    }
                }
            }
        }
        need
    }

    /// allActions are the declared actions and the ones generated for the steps.
    pub(crate) fn all_actions(&self) -> Vec<&Action> {
        self.actions
            .values()
            .chain(self.processes.actions.iter())
            .collect()
    }

    /// establishes reports whether making an effect hold makes one of the needed conditions hold.
    fn establishes(&self, effect: &str, need: &BTreeSet<String>) -> bool {
        need.contains(effect) || self.prerequisites(effect).iter().any(|p| need.contains(p))
    }

    /// Prerequisites are the conditions a condition cannot hold without (ADR 0112 §5).
    pub fn prerequisites(&self, name: &str) -> Vec<String> {
        self.conditions.implied(name)
    }

    /// Producers are the actions (sorted names) that make a condition hold: one of their effects is the condition, or
    /// implies it.
    pub fn producers(&self, name: &str) -> Vec<String> {
        self.producers_where(|effect| {
            effect == name || self.prerequisites(effect).iter().any(|p| p == name)
        })
    }

    /// ProducersWhere are the actions (sorted names) one of whose effects, made true, satisfies establishes.
    pub fn producers_where(&self, establishes: impl Fn(&str) -> bool) -> Vec<String> {
        let mut out: Vec<String> = Vec::new();
        for a in self.all_actions() {
            if a.is_specialization() || out.contains(&a.name) {
                continue;
            }
            if a.effects.iter().any(|(k, v)| *v && establishes(k)) {
                out.push(a.name.clone());
            }
        }
        out.sort();
        out
    }

    /// StepActions returns the actions generated for the steps of a process, in step order.
    pub fn step_actions(&self, process: &str) -> Vec<Action> {
        self.processes
            .actions
            .iter()
            .filter(|a| a.step.split('/').next() == Some(process))
            .cloned()
            .collect()
    }

    /// PlannedActions returns the actions a planner plans over (every declared action but the specializations), in
    /// declaration order.
    pub fn planned_actions(&self) -> Vec<Action> {
        let mut out = Vec::new();
        for src in &self.definition.actions {
            let a = self.actions.get(&src.name).cloned().unwrap_or_default();
            if !a.is_specialization() {
                out.push(a);
            }
        }
        out
    }

    /// SpecializationsOf returns the specializations this methodology declares for the action
    /// "<methodology>/<action>", in declaration order.
    pub fn specializations_of(&self, methodology_name: &str, action: &str) -> Vec<Action> {
        let mut out = Vec::new();
        let this = &self.definition.name;
        for src in &self.definition.actions {
            let a = self.actions.get(&src.name).cloned().unwrap_or_default();
            if !a.is_specialization() {
                continue;
            }
            let (target, local) = a.specialized_action(this);
            if target != action {
                continue;
            }
            if (local && methodology_name == this)
                || (!local && a.specializes.starts_with(&format!("{methodology_name}/")))
            {
                out.push(a);
            }
        }
        out
    }

    /// Applicable reports whether the guard of a specialization holds on the blackboard (no guard: always; evaluation
    /// error: no).
    pub fn applicable(&self, a: &Action, bb: &Blackboard) -> bool {
        if a.when.is_empty() {
            return true;
        }
        let res = self.whens.evaluate(bb);
        res.state.get(&a.when_condition()).copied().unwrap_or(false)
    }

    /// Agent returns an agent (the implicit default agent when the methodology declares none).
    pub fn agent(&self, name: &str) -> Option<&Agent> {
        self.agents.get(name)
    }

    /// AgentList returns the agents in declaration order, then the agents of the processes.
    pub fn agent_list(&self) -> Vec<Agent> {
        let mut out = Vec::new();
        if self.definition.agents.is_empty()
            && let Some(ag) = self.agents.get(DEFAULT_AGENT)
        {
            out.push(ag.clone());
        }
        for a in &self.definition.agents {
            out.push(self.agents.get(&a.name).cloned().unwrap_or_default());
        }
        out.extend(self.processes.agents.iter().cloned());
        out
    }

    /// UsesMCPs reports whether an action of the methodology needs an MCP.
    pub fn uses_mcps(&self) -> bool {
        self.actions.values().any(|a| !a.required_mcps().is_empty())
    }

    /// AgentActions returns the actions admissible for an agent, which its planner plans over: for the agent of a
    /// process, the actions of its steps.
    pub fn agent_actions(&self, ag: &Agent) -> Vec<Action> {
        if !ag.process.is_empty() {
            let mut out = self.step_actions(&ag.process);
            // the agent generated for a method plans over the actions of its pool too (ADR 0050)
            for a in self.planned_actions() {
                if ag.actions.contains(&a.name) {
                    out.push(a);
                }
            }
            return out;
        }
        let all = self.planned_actions();
        if ag.actions.is_empty() {
            return all;
        }
        all.into_iter()
            .filter(|a| ag.actions.contains(&a.name))
            .collect()
    }

    /// AgentGoals returns the goals of an agent.
    pub fn agent_goals(&self, ag: &Agent) -> Vec<Goal> {
        if ag.goals.is_empty() {
            return self.definition.goals.clone();
        }
        self.definition
            .goals
            .iter()
            .chain(self.processes.goals.iter())
            .filter(|g| ag.goals.contains(&g.name))
            .cloned()
            .collect()
    }

    /// Utilities evaluates the utility of every action against a blackboard (actions without utility expression: 1;
    /// evaluation errors: 0).
    pub fn utilities(&self, bb: &goap_condition::Input) -> BTreeMap<String, f64> {
        let vals = self.utilities.evaluate(bb);
        let mut out = BTreeMap::new();
        for (name, a) in &self.actions {
            let v = match vals.get(name) {
                Some(v) => *v,
                None if !a.utility.is_empty() => 0.0,
                None => 1.0,
            };
            out.insert(name.clone(), v);
        }
        out
    }

    // What the engine reads of a compiled methodology to run it (ADR 0126).

    /// Name is the name of the methodology.
    pub fn name(&self) -> &str {
        &self.definition.name
    }

    /// Version is the version compiled.
    pub fn version(&self) -> &str {
        &self.definition.version
    }

    /// Description says what the methodology is for.
    pub fn description(&self) -> &str {
        &self.definition.description
    }

    /// Namespace is the namespace the changes of the methodology act on.
    pub fn namespace(&self) -> &str {
        &self.definition.namespace
    }

    /// Administrative reports whether the methodology manages the organisation, its projects, policies or adapters.
    pub fn administrative(&self) -> bool {
        self.definition.administrative
    }

    /// Criticality is the criticality the changes of the methodology start with (empty: none).
    pub fn criticality(&self) -> &str {
        &self.definition.criticality
    }

    /// MainGoal is the goal a change of the methodology is driven towards (Methodology::main_goal).
    pub fn main_goal(&self) -> String {
        self.definition.main_goal()
    }

    /// AppliesTo are the methodologies a transverse methodology runs alongside (ADR 0036 §3).
    pub fn applies_to(&self) -> &[String] {
        &self.definition.applies_to
    }

    /// Subscriptions are the events the methodology follows at its own level (ADR 0036 §3, ADR 0106).
    pub fn subscriptions(&self) -> Vec<Subscription> {
        self.definition.subscriptions()
    }

    /// Supertypes maps the node types the methodology's domains declare to their ancestors.
    pub fn supertypes(&self) -> BTreeMap<String, Vec<String>> {
        self.definition.supertypes()
    }

    /// Processes are the processes of the methodology, in declaration order.
    pub fn processes(&self) -> Vec<ProcessInfo> {
        let this = &self.definition.name;
        let mut nested: BTreeSet<String> = BTreeSet::new();
        fn walk(steps: &[Step], this: &str, nested: &mut BTreeSet<String>) {
            for s in steps {
                if !s.process.is_empty() {
                    let (other, proc) = s.nested_process();
                    if other.is_empty() || other == this {
                        nested.insert(proc.to_string());
                    }
                }
                walk(&s.steps, this, nested);
            }
        }
        for p in &self.definition.processes {
            walk(&p.steps, this, &mut nested);
        }
        self.definition
            .processes
            .iter()
            .map(|p| ProcessInfo {
                name: p.name.clone(),
                description: p.description.clone(),
                nested: nested.contains(&p.name),
            })
            .collect()
    }

    /// Process is the process of that name.
    pub fn process(&self, name: &str) -> Option<ProcessInfo> {
        self.processes().into_iter().find(|p| p.name == name)
    }
}

/// ProcessInfo is a process of a compiled methodology: its steps are ProcessSteps, its agent the one generated for it
/// (Agent::process_of).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ProcessInfo {
    pub name: String,
    pub description: String,
    /// Nested is set on a process a step of another process of the methodology carries out.
    pub nested: bool,
}

/// overlap lists the conditions that are both an input (entry, pre) and an output (exit, effect) of an activity, with
/// whatever values (ADR 0051), sorted.
pub(crate) fn overlap(entry: &Conds, exit: &Conds) -> Vec<String> {
    exit.keys()
        .filter(|k| entry.contains_key(*k))
        .cloned()
        .collect()
}

pub(crate) fn input_and_output(k: &str, of: &str) -> String {
    format!(
        "condition {k} is both an input and an output of {of}: an activity does not take what it makes true (drop it from the entry; what is already true is not planned again)"
    )
}
