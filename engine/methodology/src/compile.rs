//! The one compile pipeline (ADR 0072, `compile.go`).

use std::collections::{BTreeMap, HashMap, HashSet};
use std::sync::{Arc, OnceLock};

use goap_condition::{AGGREGATE_ALL, AGGREGATE_NONE, AssessmentViews, Definition, SCOPES};
use goap_def::{Issue, Issues, NAME_RE, SEVERITY_WARNING};
use goap_domain::{TypeRef, parse_type_ref};
use goap_form::Field;
use serde_json::Value;

use crate::method::{CompiledMethods, compile_methods};
use crate::methodology::{
    Action, Agent, Compiled, Condition, DEFAULT_AGENT, Error, KIND_ABSTRACT, KIND_BUILTIN,
    KIND_CALL, KIND_HUMAN, KIND_LLM, KIND_SCRIPT, KIND_TOOL, LANG_GO, LANG_JAVASCRIPT, Methodology,
    ORACLE_HUMAN, ORACLE_MODEL, ORACLE_TOOL, PLANNER_GOAP, PLANNER_HYBRID, PLANNER_LLM,
    PLANNER_LLM_SCORING, PLANNER_UTILITY, TARGET_EVENT_CHANGE, TARGET_NEW_CHANGE, TRIGGER_EVENT,
    TRIGGER_SCHEDULE, Trigger, input_and_output, overlap,
};
use crate::persist::check_persistence;
use crate::process::{CompiledProcesses, compile_processes};
use crate::reaction::Reaction;
use crate::util::{Conds, q};

/// CompileOptions sets the policy of the one compile pipeline (ADR 0072).
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct CompileOptions {
    /// Lenient builds what stands and returns it with the issues, instead of nothing.
    pub lenient: bool,
    /// Stored also requires what a stored methodology (the registry) must have: its target namespace, unless it
    /// applies to other methodologies.
    pub stored: bool,
}

impl Methodology {
    /// CompileWith is the one compile pipeline: Compile, CompileLenient, Validate and ValidateStored are policies of
    /// it. With issues, the Compiled is None unless opt.lenient; it is None too when nothing at all can be built.
    pub fn compile_with(&self, opt: CompileOptions) -> (Option<Compiled>, Issues) {
        let (mut c, mut issues) = self.compile_inner(opt.lenient);
        if let Some(cc) = &c
            && !issues.has_errors()
        {
            // remarks come last and only on a methodology that stands
            for mut h in cc.hints() {
                h.severity = SEVERITY_WARNING.into();
                h.activity = self.activity_of(&h.path);
                issues.0.push(h);
            }
        }
        if opt.stored && self.namespace.is_empty() && self.applies_to.is_empty() {
            issues.0.insert(
                0,
                Issue::new(
                    "namespace",
                    "namespace required: the namespace (domain) the changes of the methodology act on",
                ),
            );
            if !opt.lenient {
                c = None;
            }
        }
        (c, issues)
    }

    /// Validate returns every problem of the definition (empty when valid).
    pub fn validate(&self) -> Issues {
        self.compile_with(CompileOptions::default()).1
    }

    /// ValidateStored is Validate plus what a stored methodology must also have: its target namespace.
    pub fn validate_stored(&self) -> Issues {
        self.compile_with(CompileOptions {
            stored: true,
            ..CompileOptions::default()
        })
        .1
    }

    /// Compile validates the methodology and compiles its conditions, including the conditions generated from action
    /// expectations. The methodology itself is not modified.
    pub fn compile(&self) -> Result<Compiled, Error> {
        let (c, issues) = self.compile_with(CompileOptions::default());
        if issues.has_errors() {
            return Err(Error::Invalid {
                name: self.name.clone(),
                issues: issues.errors(),
            });
        }
        // Go returns a nil Compiled without error when the only issues are warnings raised by the phases (not the
        // hints); the port refuses it instead of handing out nothing.
        c.ok_or(Error::Invalid {
            name: self.name.clone(),
            issues,
        })
    }

    /// CompileLenient compiles what can be compiled and returns the issues with it.
    pub fn compile_lenient(&self) -> (Option<Compiled>, Issues) {
        self.compile_with(CompileOptions {
            lenient: true,
            ..CompileOptions::default()
        })
    }

    fn compile_inner(&self, lenient: bool) -> (Option<Compiled>, Issues) {
        let mut s = CompileState::new(self);
        s.check_header();
        s.check_events();
        s.compile_conditions();
        s.inject_libraries();
        s.compile_actions();
        s.chain_scoped();
        s.check_specializations();
        s.check_references();
        s.compile_goals();
        s.compile_agents();
        s.check_transverse();
        s.check_integration();
        s.merge_roles_methods_processes();
        s.check_step_scopes();
        s.compile_reactions();
        s.compile_automatic_reviews();
        s.check_main_goal();
        s.compile_assessments();
        s.finish(lenient)
    }
}

/// compileState holds what the phases of a compilation share.
pub(crate) struct CompileState<'m> {
    pub(crate) m: &'m Methodology,
    pub(crate) issues: Vec<Issue>,
    pub(crate) defs: Vec<Definition>,
    pub(crate) known: HashSet<String>,
    /// the scope of each scoped condition (declared or generated)
    pub(crate) scopes: HashMap<String, String>,
    /// the aggregates, by name
    pub(crate) aggregates: BTreeMap<String, Condition>,
    pub(crate) actions: BTreeMap<String, Action>,
    pub(crate) utilities: Vec<Definition>,
    pub(crate) whens: Vec<Definition>,
    pub(crate) goals: HashSet<String>,
    pub(crate) agents: BTreeMap<String, Agent>,
    pub(crate) roles: HashSet<String>,
    pub(crate) meths: CompiledMethods,
    pub(crate) procs: CompiledProcesses,
    pub(crate) reactions: Vec<Reaction>,
    /// the automatic review step of each action generated for it (ADR 0130)
    pub(crate) automatic_reviews: BTreeMap<String, String>,
    /// the compiled assessment views (ADR 0116)
    pub(crate) assessments: Option<Arc<AssessmentViews>>,
}

impl<'m> CompileState<'m> {
    fn new(m: &'m Methodology) -> Self {
        CompileState {
            m,
            issues: Vec::new(),
            defs: Vec::new(),
            known: HashSet::new(),
            scopes: HashMap::new(),
            aggregates: BTreeMap::new(),
            actions: BTreeMap::new(),
            utilities: Vec::new(),
            whens: Vec::new(),
            goals: HashSet::new(),
            agents: BTreeMap::new(),
            roles: HashSet::new(),
            meths: CompiledMethods::default(),
            procs: CompiledProcesses::default(),
            reactions: Vec::new(),
            automatic_reviews: BTreeMap::new(),
            assessments: None,
        }
    }

    pub(crate) fn add(&mut self, path: impl Into<String>, msg: impl Into<String>) {
        self.issues.push(Issue::new(path, msg));
    }

    fn scope(&self, k: &str) -> &str {
        self.scopes.get(k).map(String::as_str).unwrap_or("")
    }

    fn check_header(&mut self) {
        let m = self.m;
        if m.name.is_empty() {
            self.add("name", "name required");
        } else if !NAME_RE.is_match(&m.name) {
            self.add(
                "name",
                "name must be lowercase letters, digits, '-' or '_' and start with a letter",
            );
        }
        if m.goals.is_empty() && m.processes.is_empty() {
            self.add("goals", "at least one goal or process required");
        }
        if !m.namespace.is_empty() && !NAME_RE.is_match(&m.namespace) {
            self.add(
                "namespace",
                "namespace must be lowercase letters, digits, '-' or '_' and start with a letter",
            );
        }
        self.check_criticality();
        let issues = &mut self.issues;
        m.lint_type_refs(&mut |p, msg| issues.push(Issue::new(p, msg)));
    }

    /// checkMainGoal checks the main goal of the methodology (ADR 0096): a declared goal or a process.
    fn check_main_goal(&mut self) {
        let g = &self.m.goal;
        if g.is_empty() || self.goals.contains(g) {
            return;
        }
        if self.m.processes.iter().any(|p| p.name == *g) {
            return;
        }
        self.add(
            "goal",
            format!(
                "unknown goal {}: name a goal or a process of the methodology",
                q(g)
            ),
        );
    }

    /// compileConditions compiles each expression on its own to report every error, then the scoped conditions and
    /// the aggregates together.
    fn compile_conditions(&mut self) {
        let m = self.m;
        let mut scoped: Vec<Definition> = Vec::new();
        let mut paths: HashMap<String, String> = HashMap::new();
        for (i, c) in m.conditions.iter().enumerate() {
            let path = format!("conditions[{i}]");
            if c.name.is_empty() {
                self.add(format!("{path}.name"), "name required");
                continue;
            }
            if self.known.contains(&c.name) {
                self.add(
                    format!("{path}.name"),
                    format!("duplicate condition {}", c.name),
                );
                continue;
            }
            self.known.insert(c.name.clone());
            let d = c.definition();
            let mut field = ".expr";
            if d.is_aggregate() || !d.r#where.is_empty() {
                field = ".where";
            }
            if d.is_scoped() && !SCOPES.contains(&d.scope.as_str()) {
                field = ".scope";
            }
            if let Err(e) = goap_condition::check_definition(&d) {
                self.add(format!("{path}{field}"), e.to_string());
                continue;
            }
            if d.is_scoped() || d.is_aggregate() {
                paths.insert(d.name.clone(), path);
                if d.is_scoped() {
                    self.scopes.insert(d.name.clone(), d.scope.clone());
                }
                scoped.push(d);
                continue;
            }
            self.defs.push(d);
        }
        for d in &scoped {
            if let Some((op, of)) = d.aggregate() {
                if self.scope(of).is_empty() {
                    self.add(
                        format!("{}.{op}", paths[&d.name]),
                        format!(
                            "{op} {}: name a scoped condition (scope: impact, item, ...)",
                            q(of)
                        ),
                    );
                    continue;
                }
                if let Some(c) = m.conditions.iter().find(|c| c.name == d.name) {
                    self.aggregates.insert(d.name.clone(), c.clone());
                }
            }
        }
        if scoped.is_empty() {
            return;
        }
        if let Err(e) = goap_condition::compile(&scoped) {
            self.add("conditions", e.to_string());
            return;
        }
        self.defs.extend(scoped);
    }

    /// checkStepScopes refuses a step whose entry or exit names a scoped condition.
    fn check_step_scopes(&mut self) {
        let mut found = Vec::new();
        for (path, cr) in &self.procs.criteria {
            for set in [&cr.entry, &cr.done] {
                for k in set.keys() {
                    let scope = self.scope(k);
                    if !scope.is_empty() {
                        found.push(format!(
                            "step {path} names {k}, a condition of scope {scope}: name an aggregate of it (all, any, none)"
                        ));
                    }
                }
            }
        }
        for msg in found {
            self.add("processes", msg);
        }
    }

    /// compileScope lifts a scoped action for the planner (ADR 0109 §3.5).
    pub(crate) fn compile_scope(&mut self, path: &str, mut a: Action) -> Action {
        if !a.is_scoped() {
            if !a.r#where.is_empty() {
                self.add(
                    format!("{path}.where"),
                    "where applies to a scoped action (scope)",
                );
            }
            return a;
        }
        if !SCOPES.contains(&a.scope.as_str()) {
            self.add(
                format!("{path}.scope"),
                format!(
                    "unknown scope {} (one of {})",
                    q(&a.scope),
                    SCOPES.join(", ")
                ),
            );
            return a;
        }
        if !a.r#where.is_empty() {
            let d = Definition {
                name: "where".into(),
                scope: a.scope.clone(),
                expr: a.r#where.clone(),
                ..Definition::default()
            };
            if let Err(e) = goap_condition::check_definition(&d) {
                self.add(format!("{path}.where"), e.to_string());
                return a;
            }
        }
        let split = |s: &mut Self, field: &str, set: &Conds| -> (Conds, Conds, bool) {
            let (mut scoped, mut change, mut ok) = (Conds::new(), Conds::new(), true);
            for (k, v) in set {
                let sc = s.scope(k).to_string();
                if sc.is_empty() {
                    change.insert(k.clone(), *v);
                } else if sc == a.scope {
                    scoped.insert(k.clone(), *v);
                } else {
                    s.add(
                        format!("{path}.{field}.{k}"),
                        format!(
                            "{k} is a condition of scope {sc}, the action is of scope {}",
                            a.scope
                        ),
                    );
                    ok = false;
                }
            }
            (scoped, change, ok)
        };
        let (pre, mut change_pre, ok1) = split(self, "pre", &a.pre);
        let (eff, mut change_eff, ok2) = split(self, "effects", &a.effects);
        if !ok1 || !ok2 {
            return a;
        }
        if eff.is_empty() {
            self.add(
                format!("{path}.effects"),
                format!(
                    "a scoped action makes conditions of its scope ({}) true on its subject: declare them in its effects",
                    a.scope
                ),
            );
            return a;
        }
        // a subject is pending: its pre holds, its effects do not all hold yet
        let term = |k: &str, v: bool| {
            format!(
                "({} in self.conditions && self.conditions[{}] == {v})",
                q(k),
                q(k)
            )
        };
        let have: Vec<String> = pre.iter().map(|(k, v)| term(k, *v)).collect();
        let want: Vec<String> = eff.iter().map(|(k, v)| term(k, *v)).collect();
        let mut expr = format!("!({})", want.join(" && "));
        if !have.is_empty() {
            expr = format!("{} && {expr}", have.join(" && "));
        }
        let (subject, pending) = (a.subject_condition(), a.pending_condition());
        self.defs.push(Definition {
            name: subject.clone(),
            scope: a.scope.clone(),
            r#where: a.r#where.clone(),
            expr,
            description: format!(
                "the action {} is still to be done for this {}",
                a.scope_name(),
                a.scope
            ),
            ..Definition::default()
        });
        self.defs.push(Definition {
            name: pending.clone(),
            any: subject.clone(),
            description: format!(
                "the action {} is still to be done for some {}",
                a.scope_name(),
                a.scope
            ),
            ..Definition::default()
        });
        self.known.insert(subject.clone());
        self.known.insert(pending.clone());
        self.scopes.insert(subject, a.scope.clone());
        a.scoped_pre = pre;
        a.scoped_effects = eff.clone();
        change_pre.insert(pending, true);
        a.pre = change_pre;
        a.lifted = Conds::new();
        a.is_lifted = true;
        for (k, v) in &eff {
            let aggs = self.lifted_aggregates(k, *v, &a.r#where);
            if aggs.is_empty() {
                let op = if *v { AGGREGATE_ALL } else { AGGREGATE_NONE };
                self.add(
                    format!("{path}.effects.{k}"),
                    format!(
                        "declare the aggregate the plan reaches once every subject has {k} {v}: {{name: <name>, {op}: {k}}}"
                    ),
                );
                continue;
            }
            for agg in aggs {
                change_eff.insert(agg.clone(), true);
                a.lifted.insert(agg, true);
            }
        }
        a.effects = change_eff;
        a
    }

    /// liftedAggregates are the aggregates a scoped effect is lifted to.
    fn lifted_aggregates(&self, cond: &str, value: bool, wh: &str) -> Vec<String> {
        let mut within = Vec::new();
        let mut plain = Vec::new();
        for (name, c) in &self.aggregates {
            let d = c.definition();
            let Some((op, of)) = d.aggregate() else {
                continue;
            };
            if of != cond || (value && op != AGGREGATE_ALL) || (!value && op != AGGREGATE_NONE) {
                continue;
            }
            if c.r#where == wh
                || (!wh.is_empty() && c.r#where.starts_with(&format!("{wh} && ")))
                || (wh.is_empty() && !c.r#where.is_empty())
            {
                within.push(name.clone());
            } else if c.r#where.is_empty() {
                plain.push(name.clone());
            }
        }
        if within.is_empty() {
            within = plain;
        }
        within.sort();
        within
    }

    /// injectLibraries adds the conditions of the libraries the methodology imports (ADR 0064).
    fn inject_libraries(&mut self) {
        let mut seen: HashSet<&str> = HashSet::new();
        for (i, name) in self.m.imports.iter().enumerate() {
            let Some(lib) = goap_condition::library(name) else {
                self.add(
                    format!("imports[{i}]"),
                    format!(
                        "unknown condition library {} (one of {})",
                        q(name),
                        goap_condition::LIBRARY_NAMES.join(", ")
                    ),
                );
                continue;
            };
            if seen.contains(name.as_str()) {
                self.add(
                    format!("imports[{i}]"),
                    format!("library {name} imported twice"),
                );
                continue;
            }
            seen.insert(name);
            for d in lib {
                if !self.known.contains(&d.name) {
                    self.known.insert(d.name.clone());
                    self.defs.push(d);
                }
            }
        }
    }

    /// compileActions checks every action and keeps a copy of it in s.actions.
    fn compile_actions(&mut self) {
        let m = self.m;
        for (i, src) in m.actions.iter().enumerate() {
            let path = format!("actions[{i}]");
            let mut a = src.clone();
            if a.name.is_empty() {
                self.add(format!("{path}.name"), "name required");
                continue;
            }
            if self.actions.contains_key(&a.name) {
                self.add(
                    format!("{path}.name"),
                    format!("duplicate action {}", a.name),
                );
                continue;
            }
            if a.kind == KIND_CALL {
                self.add(
                    format!("{path}.kind"),
                    format!(
                        "kind {} is generated for the steps of a process: declare a process step or a method instead",
                        q(&a.kind)
                    ),
                );
            } else if ![
                KIND_LLM,
                KIND_TOOL,
                KIND_HUMAN,
                KIND_BUILTIN,
                KIND_SCRIPT,
                KIND_ABSTRACT,
            ]
            .contains(&a.kind.as_str())
            {
                self.add(
                    format!("{path}.kind"),
                    format!("unknown kind {}", q(&a.kind)),
                );
            }
            self.check_specialization(&path, &a);
            self.check_implementation(&path, &a);
            if !a.utility.is_empty() {
                if let Err(e) = goap_condition::check_number(&a.utility) {
                    self.add(format!("{path}.utility"), e.to_string());
                } else {
                    self.utilities.push(Definition {
                        name: a.name.clone(),
                        expr: a.utility.clone(),
                        ..Definition::default()
                    });
                }
            }
            self.check_invocation(&path, &a);
            self.check_verify(&path, &a);
            a = self.compile_expects(&path, a);
            if a.effects.is_empty() && !a.is_specialization() {
                self.add(
                    format!("{path}.effects"),
                    "no effect: the action can never be planned",
                );
            }
            a = self.compile_scope(&path, a);
            self.actions.insert(a.name.clone(), a);
        }
    }

    /// chainScoped lets the planner chain two scoped actions of the same scope (ADR 0118).
    fn chain_scoped(&mut self) {
        let names: Vec<String> = self.actions.keys().cloned().collect();
        for name in &names {
            let mut a = self.actions[name].clone();
            if !a.is_scoped() || !a.is_lifted {
                continue;
            }
            for other in &names {
                let b = &self.actions[other];
                if other == name || !b.is_scoped() || b.scope != a.scope || !b.is_lifted {
                    continue;
                }
                if a.scoped_effects
                    .iter()
                    .any(|(k, v)| b.scoped_pre.get(k) == Some(v))
                {
                    a.effects.insert(b.pending_condition(), true);
                    a.lifted.insert(b.pending_condition(), true);
                }
            }
            self.actions.insert(name.clone(), a);
        }
    }

    /// checkSpecialization checks the specialization side of an action.
    fn check_specialization(&mut self, path: &str, a: &Action) {
        if !a.is_specialization() {
            if !a.when.is_empty() || a.priority != 0 {
                self.add(
                    format!("{path}.when"),
                    "when and priority apply to specializations only",
                );
            }
            return;
        }
        if a.kind == KIND_ABSTRACT {
            self.add(
                format!("{path}.kind"),
                "a specialization must be implemented",
            );
        }
        if a.specializes.matches('/').count() > 1
            || a.specializes.ends_with('/')
            || a.specializes == a.name
        {
            self.add(
                format!("{path}.specializes"),
                "specializes must be <action> or <methodology>/<action>",
            );
        }
        if !a.pre.is_empty() || !a.effects.is_empty() || a.expects.is_some() {
            self.add(
                format!("{path}.specializes"),
                "a specialization inherits pre, effects and expects from the action it specializes",
            );
        }
        if !a.when.is_empty() {
            let d = Definition {
                name: a.when_condition(),
                expr: a.when.clone(),
                description: format!("the specialization {} applies here", a.name),
                ..Definition::default()
            };
            match goap_condition::compile(std::slice::from_ref(&d)) {
                Err(e) => {
                    let msg = e.to_string();
                    let prefix = format!("condition {}: ", q(&d.name));
                    let msg = msg.strip_prefix(&prefix).unwrap_or(&msg).to_string();
                    self.add(format!("{path}.when"), msg);
                }
                Ok(_) => self.whens.push(d),
            }
        }
    }

    /// checkImplementation checks what an action of a kind needs to run.
    fn check_implementation(&mut self, path: &str, a: &Action) {
        if a.kind == KIND_SCRIPT {
            if a.language != LANG_JAVASCRIPT && a.language != LANG_GO {
                self.add(
                    format!("{path}.language"),
                    "script language must be javascript or go",
                );
            }
            if a.code.trim().is_empty() {
                self.add(format!("{path}.code"), "script action requires code");
            }
        }
        self.check_form(path, a);
        for msg in crate::loops::Loop::issues(a.r#loop.as_ref(), &a.kind) {
            self.add(format!("{path}.loop"), msg);
        }
        if a.kind == KIND_LLM && a.prompt.is_empty() {
            self.add(format!("{path}.prompt"), "llm action requires a prompt");
        } else if a.kind == KIND_TOOL && a.tool.is_empty() {
            self.add(format!("{path}.tool"), "tool action requires a tool");
        } else if a.kind == KIND_TOOL {
            if goap_mcp::split_tool(&a.tool).is_err() {
                self.add(format!("{path}.tool"), "tool must be <mcp>/<tool>");
            }
        } else if a.kind == KIND_BUILTIN && a.builtin.is_empty() {
            self.add(
                format!("{path}.builtin"),
                "builtin action requires a builtin",
            );
        } else if a.kind == KIND_BUILTIN
            && let Some(b) = &self.m.builtins
            && !b.has_builtin(&a.builtin)
        {
            self.add(
                format!("{path}.builtin"),
                format!("unknown builtin {}", q(&a.builtin)),
            );
        }
    }

    /// checkForm checks the form of a human action and the code run on its values (ADR 0115).
    fn check_form(&mut self, path: &str, a: &Action) {
        if a.kind != KIND_HUMAN {
            if a.form.is_some() {
                self.add(format!("{path}.form"), "a form belongs to a human action");
            }
            return;
        }
        if !a.code.trim().is_empty() {
            if a.form.is_none() {
                self.add(
                    format!("{path}.code"),
                    "the code of a human action runs on the values of its form: declare a form",
                );
            }
            if a.language != LANG_JAVASCRIPT && a.language != LANG_GO {
                self.add(
                    format!("{path}.language"),
                    "script language must be javascript or go",
                );
            }
        }
        let Some(f) = &a.form else {
            return;
        };
        for is in goap_form::check_form(f).0 {
            self.add(format!("{path}.form.{}", is.path), is.message);
        }
        self.check_form_types(&format!("{path}.form.fields"), &f.fields);
    }

    /// checkFormTypes checks the node and link types the constraints of form fields name, sub-fields included.
    fn check_form_types(&mut self, path: &str, fields: &[Field]) {
        for (j, f) in fields.iter().enumerate() {
            for (name, link) in [("types", false), ("toTypes", false), ("linkTypes", true)] {
                if let Some(Value::Array(list)) = f.constraints.get(name) {
                    for t in list {
                        if let Value::String(r) = t {
                            let msg = self.m.check_type_ref(r, link);
                            if !msg.is_empty() {
                                self.add(format!("{path}[{j}].constraints.{name}"), msg);
                            }
                        }
                    }
                }
            }
            self.check_form_types(&format!("{path}[{j}].fields"), &f.fields);
        }
    }

    /// checkCriticality checks the default criticality of the methodology (ADR 0075 §3).
    fn check_criticality(&mut self) {
        let c = &self.m.criticality;
        if !c.is_empty() && !["C1", "C2", "C3"].contains(&c.as_str()) {
            self.add(
                "criticality",
                format!("unknown criticality {}: C1, C2 or C3", q(c)),
            );
        }
    }

    /// checkVerify checks the verification an action declares (ADR 0075): a known kind of oracle.
    fn check_verify(&mut self, path: &str, a: &Action) {
        let Some(v) = &a.verify else {
            return;
        };
        match v.oracle.as_str() {
            ORACLE_TOOL | ORACLE_HUMAN | ORACLE_MODEL => {}
            _ => self.add(
                format!("{path}.verify.oracle"),
                format!("unknown oracle {}: tool, human or model", q(&v.oracle)),
            ),
        }
    }

    /// checkInvocation checks the MCPs and the permission an action names.
    fn check_invocation(&mut self, path: &str, a: &Action) {
        for name in &a.mcps {
            if !goap_mcp::valid_name(name) {
                self.add(
                    format!("{path}.mcps"),
                    format!("invalid MCP name {}", q(name)),
                );
            }
        }
        if !a.mcps.is_empty() && a.kind != KIND_LLM && a.kind != KIND_SCRIPT {
            self.add(
                format!("{path}.mcps"),
                "mcps apply to llm and script actions (a tool action names its tool)",
            );
        }
        if !a.permission.is_empty() && !a.permission.contains(':') {
            self.add(
                format!("{path}.permission"),
                "permission must be <resource>:<action>",
            );
        }
    }

    /// compileExpects checks the expectation of an action and generates its expect:* condition.
    fn compile_expects(&mut self, path: &str, mut a: Action) -> Action {
        let Some(e) = a.expects.clone() else {
            return a;
        };
        let m = self.m;
        if !e.produce.node_type.is_empty() {
            let msg = m.check_type_ref(&e.produce.node_type, false);
            if !msg.is_empty() {
                self.add(format!("{path}.expects.produce.nodeType"), msg);
            } else {
                let r: TypeRef = parse_type_ref(&e.produce.node_type).unwrap_or_default();
                if !m.namespace.is_empty() && r.namespace != m.namespace {
                    self.add(
                        format!("{path}.expects.produce.nodeType"),
                        format!(
                            "{} is not a type of {}: a methodology creates nodes of its namespace",
                            e.produce.node_type, m.namespace
                        ),
                    );
                }
            }
        }
        if let Some(l) = &e.link {
            let msg = m.check_type_ref(&l.r#type, true);
            if !msg.is_empty() {
                self.add(format!("{path}.expects.link.type"), msg);
            }
        }
        let expr = match e.expr() {
            Ok(x) => x,
            Err(err) => {
                self.add(format!("{path}.expects"), err.to_string());
                return a;
            }
        };
        let d = Definition {
            name: a.expect_condition(),
            expr,
            description: format!("what the action {} is expected to produce is there", a.name),
            ..Definition::default()
        };
        if let Err(err) = goap_condition::compile(std::slice::from_ref(&d)) {
            self.add(format!("{path}.expects.where"), err.to_string());
            return a;
        }
        self.known.insert(d.name.clone());
        self.defs.push(d);
        a.effects.insert(a.expect_condition(), true);
        a
    }

    /// checkSpecializations requires a local specialization to target a planned action of this methodology.
    fn check_specializations(&mut self) {
        let m = self.m;
        for (i, a) in m.actions.iter().enumerate() {
            let (target, local) = a.specialized_action(&m.name);
            if !a.is_specialization() || !local {
                continue;
            }
            match self.actions.get(&target) {
                None => self.add(
                    format!("actions[{i}].specializes"),
                    format!("unknown action {}", q(&target)),
                ),
                Some(t) if t.is_specialization() => self.add(
                    format!("actions[{i}].specializes"),
                    format!("cannot specialize the specialization {}", q(&target)),
                ),
                _ => {}
            }
        }
    }

    /// checkReferences checks the conditions the actions name and that none is both an input and an output of one.
    fn check_references(&mut self) {
        let m = self.m;
        for (i, a) in m.actions.iter().enumerate() {
            for (field, set) in [("pre", &a.pre), ("effects", &a.effects)] {
                for k in set.keys() {
                    if !self.known.contains(k) {
                        self.add(
                            format!("actions[{i}].{field}.{k}"),
                            format!("unknown condition {}", q(k)),
                        );
                    } else if !self.scope(k).is_empty() && !a.is_scoped() {
                        let sc = self.scope(k).to_string();
                        self.add(
                            format!("actions[{i}].{field}.{k}"),
                            format!(
                                "{k} is a condition of scope {sc}: name it in an action of that scope (scope: {sc}), or name an aggregate of it"
                            ),
                        );
                    }
                }
            }
            for k in overlap(&a.pre, &a.effects) {
                self.add(
                    format!("actions[{i}].pre.{k}"),
                    input_and_output(&k, &format!("the action {}", a.name)),
                );
            }
        }
    }

    fn compile_goals(&mut self) {
        let m = self.m;
        for (i, g) in m.goals.iter().enumerate() {
            let path = format!("goals[{i}]");
            if g.name.is_empty() {
                self.add(format!("{path}.name"), "name required");
            } else if self.goals.contains(&g.name) {
                self.add(format!("{path}.name"), format!("duplicate goal {}", g.name));
            }
            self.goals.insert(g.name.clone());
            if g.pre.is_empty() {
                self.add(format!("{path}.pre"), "a goal needs at least one condition");
            }
            for k in g.pre.keys() {
                if !self.known.contains(k) {
                    self.add(
                        format!("{path}.pre.{k}"),
                        format!("unknown condition {}", q(k)),
                    );
                } else if !self.scope(k).is_empty() {
                    let sc = self.scope(k).to_string();
                    self.add(
                        format!("{path}.pre.{k}"),
                        format!(
                            "{k} is a condition of scope {sc}: a goal names conditions of the change (an aggregate of it)"
                        ),
                    );
                }
            }
        }
    }

    /// compileAgents checks the agents, and adds the implicit default agent of a methodology with goals and no
    /// agent.
    fn compile_agents(&mut self) {
        let m = self.m;
        for (i, ag) in m.agents.iter().enumerate() {
            let path = format!("agents[{i}]");
            if ag.name.is_empty() {
                self.add(format!("{path}.name"), "name required");
            } else if !NAME_RE.is_match(&ag.name) {
                self.add(
                    format!("{path}.name"),
                    "name must be lowercase letters, digits, '-' or '_'",
                );
            } else if self
                .agents
                .get(&ag.name)
                .is_some_and(|a| !a.name.is_empty())
            {
                self.add(
                    format!("{path}.name"),
                    format!("duplicate agent {}", ag.name),
                );
            }
            match ag.planner.as_str() {
                "" | PLANNER_GOAP | PLANNER_UTILITY | PLANNER_HYBRID => {}
                PLANNER_LLM | PLANNER_LLM_SCORING => {
                    if ag.model.is_empty() {
                        self.add(
                            format!("{path}.model"),
                            format!("model is required for the {} planner", ag.planner),
                        );
                    }
                }
                _ => self.add(
                    format!("{path}.planner"),
                    "planner must be goap, utility, hybrid, llm or llm-scoring",
                ),
            }
            for name in &ag.mcps {
                if !goap_mcp::valid_name(name) {
                    self.add(
                        format!("{path}.mcps"),
                        format!("invalid MCP name {}", q(name)),
                    );
                }
            }
            for (j, a) in ag.actions.iter().enumerate() {
                match self.actions.get(a) {
                    None => self.add(
                        format!("{path}.actions[{j}]"),
                        format!("unknown action {}", q(a)),
                    ),
                    Some(act) if act.is_specialization() => self.add(
                        format!("{path}.actions[{j}]"),
                        format!(
                            "{} is a specialization: list the action it specializes",
                            q(a)
                        ),
                    ),
                    _ => {}
                }
            }
            for (j, g) in ag.goals.iter().enumerate() {
                if !self.goals.contains(g) {
                    self.add(
                        format!("{path}.goals[{j}]"),
                        format!("unknown goal {}", q(g)),
                    );
                }
            }
            {
                let issues = &mut self.issues;
                check_persistence(
                    &mut |p, msg| issues.push(Issue::new(p, msg)),
                    &path,
                    ag.persistent.as_ref(),
                    &self.known,
                );
            }
            let mut names: HashSet<&str> = HashSet::new();
            for (j, tr) in ag.triggers.iter().enumerate() {
                let tp = format!("{path}.triggers[{j}]");
                if tr.name.is_empty() {
                    self.add(format!("{tp}.name"), "name required");
                } else if names.contains(tr.name.as_str()) {
                    self.add(
                        format!("{tp}.name"),
                        format!("duplicate trigger {}", tr.name),
                    );
                }
                names.insert(&tr.name);
                self.check_trigger(&tp, ag, tr);
            }
            let mut ag = ag.clone();
            if ag.planner.is_empty() {
                ag.planner = PLANNER_GOAP.into();
            }
            self.agents.insert(ag.name.clone(), ag);
        }
        if m.agents.is_empty() && !m.goals.is_empty() {
            self.agents.insert(
                DEFAULT_AGENT.into(),
                Agent {
                    name: DEFAULT_AGENT.into(),
                    description: m.description.clone(),
                    planner: PLANNER_GOAP.into(),
                    ..Agent::default()
                },
            );
        }
    }

    /// checkTrigger checks the type, event, filter, schedule, target and goal of one trigger of an agent.
    fn check_trigger(&mut self, tp: &str, ag: &Agent, tr: &Trigger) {
        match tr.r#type.as_str() {
            TRIGGER_EVENT => {
                self.check_event(format!("{tp}.event"), &tr.event);
                if let Err(e) = goap_condition::compile_event_filter(&tr.filter) {
                    self.add(format!("{tp}.filter"), e.to_string());
                }
            }
            TRIGGER_SCHEDULE => {
                if let Err(e) = crate::cron::parse_standard(&tr.schedule) {
                    self.add(
                        format!("{tp}.schedule"),
                        format!("invalid cron expression: {e}"),
                    );
                }
            }
            _ => self.add(format!("{tp}.type"), "type must be event or schedule"),
        }
        match tr.target.as_str() {
            "" | TARGET_NEW_CHANGE => {}
            TARGET_EVENT_CHANGE => {
                if tr.r#type != TRIGGER_EVENT
                    || !(tr.event.starts_with("change.") || tr.event.starts_with("process."))
                {
                    self.add(
                        format!("{tp}.target"),
                        "event_change requires a change.* or process.* event",
                    );
                }
            }
            _ => self.add(
                format!("{tp}.target"),
                "target must be new_change or event_change",
            ),
        }
        if !tr.goal.is_empty() && !self.goals.contains(&tr.goal) {
            self.add(
                format!("{tp}.goal"),
                format!("unknown goal {}", q(&tr.goal)),
            );
        } else if !tr.goal.is_empty() && !ag.goals.is_empty() && !ag.goals.contains(&tr.goal) {
            self.add(
                format!("{tp}.goal"),
                format!("goal {} is not a goal of the agent", q(&tr.goal)),
            );
        }
    }

    /// checkTransverse checks appliesTo and on (ADR 0036 §3).
    fn check_transverse(&mut self) {
        let m = self.m;
        for (i, other) in m.applies_to.iter().enumerate() {
            if !NAME_RE.is_match(other) {
                self.add(format!("appliesTo[{i}]"), "appliesTo names methodologies");
            } else if *other == m.name {
                self.add(
                    format!("appliesTo[{i}]"),
                    "a methodology does not apply to itself",
                );
            }
        }
        for (i, sub) in m.on.iter().enumerate() {
            let path = format!("on[{i}]");
            if m.applies_to.is_empty() && sub.origin() == crate::integrate::FROM_SELF {
                self.add(
                    path.clone(),
                    "on applies to transverse methodologies (appliesTo), or to the events of integrated or integrating changes (from)",
                );
            }
            self.check_event(format!("{path}.event"), &sub.event);
            if let Err(e) = goap_condition::compile_event_filter(&sub.filter) {
                self.add(format!("{path}.filter"), e.to_string());
            }
        }
        if !m.applies_to.is_empty() && m.processes.is_empty() {
            self.add(
                "appliesTo",
                "a transverse methodology runs its processes alongside the changes: declare at least one process",
            );
        }
    }

    /// mergeRolesMethodsProcesses compiles the roles, the methods and the processes, and merges the agents, actions
    /// and conditions generated for them in.
    fn merge_roles_methods_processes(&mut self) {
        let m = self.m;
        let (meths, meth_gen) = {
            let issues = &mut self.issues;
            let mut add = |p: String, msg: String| issues.push(Issue::new(p, msg));
            let roles = m.compile_roles(&mut add);
            m.check_element_roles(&mut add, &roles);
            let out = compile_methods(
                m,
                &mut add,
                &self.actions,
                &mut self.known,
                &self.agents,
                &roles,
            );
            self.roles = roles;
            out
        };
        // a method composing its own steps is run by an agent of its own name
        for ag in &meth_gen.agents {
            self.agents.insert(ag.name.clone(), ag.clone());
        }
        // an agent performing a method has access to the activities that compose it
        for (name, x) in &meth_gen.extend {
            let mut ag = self.agents.get(name).cloned().unwrap_or_default();
            if !ag.actions.is_empty() {
                ag.actions.extend(x.actions.iter().cloned());
            }
            if !ag.goals.is_empty() {
                ag.goals.extend(x.goals.iter().cloned());
            }
            self.agents.insert(name.clone(), ag);
        }
        let mut procs = {
            let issues = &mut self.issues;
            let mut add = |p: String, msg: String| issues.push(Issue::new(p, msg));
            compile_processes(
                m,
                &mut add,
                &self.actions,
                &mut self.known,
                &self.agents,
                &meths,
                &self.roles,
            )
        };
        self.meths = meths;
        procs.criteria.extend(meth_gen.criteria);
        procs.conditions.extend(meth_gen.conditions);
        procs.actions.extend(meth_gen.actions);
        procs.agents.extend(meth_gen.agents);
        procs.goals.extend(meth_gen.goals);
        self.defs.extend(procs.conditions.iter().cloned());
        let mut actions = std::mem::take(&mut procs.actions);
        for a in actions.iter_mut() {
            if a.narrowed {
                // a step narrowing the subjects of a scoped action: lifted again over its own subjects
                let path = format!("processes.{}", a.name);
                let mut b = self.compile_scope(&path, std::mem::take(a));
                let done = b.step_done.clone();
                b.effects.extend(done);
                *a = b;
            }
            self.actions.insert(a.name.clone(), a.clone());
        }
        procs.actions = actions;
        for ag in &procs.agents {
            self.agents.insert(ag.name.clone(), ag.clone());
        }
        self.procs = procs;
    }

    /// finish ties the issues to their activity and sorts them, then compiles the condition sets and builds the
    /// Compiled; with issues and not lenient, nothing is built.
    fn finish(mut self, lenient: bool) -> (Option<Compiled>, Issues) {
        let m = self.m;
        if !self.issues.is_empty() {
            for is in self.issues.iter_mut() {
                is.activity = m.activity_of(&is.path);
            }
            self.issues.sort_by(|a, b| a.path.cmp(&b.path));
            if !lenient {
                return (None, Issues(self.issues));
            }
        }
        let fail = |e: String| (None, Issues(vec![Issue::new("", e)]));
        let set = match goap_condition::compile(&self.defs) {
            Ok(s) => s,
            Err(e) => return fail(e.to_string()),
        };
        let uset = match goap_condition::compile_numbers(&self.utilities) {
            Ok(s) => s,
            Err(e) => return fail(e.to_string()),
        };
        let wset = match goap_condition::compile(&self.whens) {
            Ok(s) => s,
            Err(e) => return fail(e.to_string()),
        };
        let mset = match goap_condition::compile(&self.meths.guards) {
            Ok(s) => s,
            Err(e) => return fail(e.to_string()),
        };
        let c = Compiled {
            definition: Arc::new(m.clone()),
            conditions: Arc::new(set),
            actions: self.actions,
            utilities: Arc::new(uset),
            agents: self.agents,
            whens: Arc::new(wset),
            processes: Arc::new(self.procs),
            methods: Arc::new(self.meths),
            method_guards: Arc::new(mset),
            reactions: self.reactions,
            automatic_reviews: self.automatic_reviews,
            assessments: self.assessments,
            view: Arc::new(OnceLock::new()),
        };
        (Some(c), Issues(self.issues))
    }
}
