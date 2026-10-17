//! Compiles and evaluates the conditions of a methodology.
//!
//! A condition is a named CEL expression evaluated against the blackboard: the change items (axis change) hydrated
//! with the domain nodes they reference (axis domain). The result of evaluating every condition is the world state the
//! GOAP planner reasons on.
use std::{
    collections::{BTreeMap, HashSet},
    result::Result as StdResult,
    sync::{Arc, OnceLock},
};

use cel::{Context, Program, Value as CelValue, context::VariableResolver};
use goap_domain::Blackboard;

use crate::{
    activation::Activation,
    check::{Ty, check},
    scope::{Aggregate, ScopedCond, Subject},
    unparse::go_quote,
    value::go_text,
};

/// The errors of the package: the messages are Go's.
#[derive(Debug, Clone, PartialEq, thiserror::Error)]
pub enum Error {
    /// A definition or an expression is refused (it does not parse, names an undeclared variable, returns the wrong
    /// type, ...).
    #[error("{0}")]
    Compile(String),
    /// An expression could not be evaluated.
    #[error("{0}")]
    Eval(String),
    /// A guard was refused or failed (lib/guard).
    #[error(transparent)]
    Guard(#[from] goap_guard::GuardError),
}

impl Error {
    pub(crate) fn compile(msg: impl Into<String>) -> Error {
        Error::Compile(msg.into())
    }

    /// The error with a prefix (Go's `fmt.Errorf("prefix: %w", err)`).
    pub(crate) fn context(self, prefix: &str) -> Error {
        match self {
            Error::Compile(m) => Error::Compile(format!("{prefix}: {m}")),
            Error::Eval(m) => Error::Eval(format!("{prefix}: {m}")),
            Error::Guard(g) => Error::Compile(format!("{prefix}: {g}")),
        }
    }
}

/// Definition is a named condition: an expression at the level of the change, an expression about one subject of a
/// scope (scope, where: ADR 0109 §3), or an aggregate of a scoped condition at the level of the change (all, any or
/// none, with where and empty).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Definition {
    pub name: String,
    pub expr: String,
    /// Description says what the condition means (the libraries' own; a methodology describes its conditions
    /// itself).
    pub description: String,
    /// Scope is the kind of subject the condition is about (SCOPE_IMPACT, ...); empty or SCOPE_CHANGE: the change.
    pub scope: String,
    /// Where (CEL, `self` the subject) narrows the subjects of a scoped condition or of an aggregate; a subject
    /// outside it has no value.
    pub r#where: String,
    /// All, Any, None name the scoped condition an aggregate is over (one of them).
    pub all: String,
    pub any: String,
    pub none: String,
    /// Empty is the value of an `all` aggregate over no subject (default false: no vacuous truth).
    pub empty: bool,
}

impl Definition {
    /// A condition of the change.
    pub fn new(name: impl Into<String>, expr: impl Into<String>) -> Definition {
        Definition {
            name: name.into(),
            expr: expr.into(),
            ..Default::default()
        }
    }
}

pub(crate) struct Compiled {
    pub(crate) name: String,
    pub(crate) expr: String,
    pub(crate) prog: Arc<Program>,
}

/// Set is a compiled set of conditions.
#[derive(Default)]
pub struct Set {
    pub(crate) progs: Vec<Compiled>,
    /// scoped are the scoped conditions in the order they are evaluated (a condition after those it reads);
    /// aggregates the aggregates.
    pub(crate) scoped: Vec<ScopedCond>,
    pub(crate) aggregates: Vec<Aggregate>,
    /// descriptions are the descriptions of the conditions that have one.
    descriptions: BTreeMap<String, String>,
    /// implies maps a condition to the ones it implies (implied), computed once.
    pub(crate) implies: OnceLock<BTreeMap<String, Vec<String>>>,
}

impl std::fmt::Debug for Set {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Set").field("names", &self.names()).finish()
    }
}

/// Variables exposed to expressions.
pub const VARIABLES: [&str; 18] = [
    "change",
    "items",
    "changeImpacts",
    "decisions",
    "artifacts",
    "vars",
    "options",
    "activeOption",
    "decisionPoints",
    "questions",
    "risks",
    "actions",
    "verifications",
    "derogations",
    "criticalityPolicy",
    "dependencies",
    "history",
    "assessments",
];

/// The declared variables of the conditions with their types (cel-go's `NewEnv`).
pub(crate) fn condition_decls() -> Vec<(&'static str, Ty)> {
    vec![
        ("change", Ty::map()),
        ("items", Ty::list()),
        ("changeImpacts", Ty::list()),
        ("decisions", Ty::list()),
        ("artifacts", Ty::list()),
        ("vars", Ty::map()),
        // options of the change and the one it works on (ADR 0032 §6); decision points and their questions (ADR 0009 §4)
        ("options", Ty::list()),
        ("activeOption", Ty::String),
        ("decisionPoints", Ty::list()),
        ("questions", Ty::list()),
        // the risk register and the actions of the change (ADR 0036 §1)
        ("risks", Ty::list()),
        ("assessments", Ty::list()),
        ("actions", Ty::list()),
        // the verification of the effects of the action runs (ADR 0075)
        ("verifications", Ty::list()),
        // the derogations of the change (ADR 0075 §2)
        ("derogations", Ty::list()),
        // what the organisation requires of the criticality of the change (ADR 0075 §3)
        ("criticalityPolicy", Ty::map()),
        // the changes the change depends on (ADR 0108)
        ("dependencies", Ty::list()),
        // the history of the change: its runs and the runs of its steps, with their outcome (ADR 0109 §4)
        ("history", Ty::map()),
    ]
}

/// The CEL environment of the conditions: the standard library and cel-go's `ext.Strings()`, `ext.Lists()`,
/// `ext.Sets()` (the environment of the guards). Unlike cel-go, it declares no variable: the checks of
/// [`check`](crate::check) do, and there is no cost limit.
pub fn new_env() -> Arc<cel::Env> {
    goap_guard::env()
}

/// Parses and checks an expression against declarations: the program and the type of its result.
pub(crate) fn compile_checked(
    env: &cel::Env,
    decls: &[(&str, Ty)],
    expr: &str,
) -> StdResult<(Program, Ty), Error> {
    let prog = env
        .compile(expr)
        .map_err(|e| Error::compile(e.to_string()))?;
    let t = check(prog.expression(), prog.source_info(), decls).map_err(Error::compile)?;
    Ok((prog, t))
}

/// A boolean expression (or dyn).
pub(crate) fn compile_expr(decls: &[(&str, Ty)], expr: &str) -> StdResult<Program, Error> {
    let (prog, t) = compile_checked(&new_env(), decls, expr)?;
    if t != Ty::Bool && t != Ty::Dyn {
        return Err(Error::compile(format!(
            "expression must return bool, got {t}"
        )));
    }
    Ok(prog)
}

/// Runs a program over variables.
pub(crate) fn run(prog: &Program, vars: &dyn VariableResolver) -> StdResult<CelValue, String> {
    let mut ctx = Context::with_env(new_env());
    ctx.set_variable_resolver(vars);
    prog.execute(&ctx).map_err(|e| e.to_string())
}

/// Runs a boolean program.
pub(crate) fn eval_bool(prog: &Program, vars: &dyn VariableResolver) -> StdResult<bool, String> {
    match run(prog, vars)? {
        CelValue::Bool(b) => Ok(b),
        other => Err(format!("non-bool result {}", go_text(&other))),
    }
}

/// Compile compiles definitions. Every expression must return a bool.
pub fn compile(defs: &[Definition]) -> StdResult<Set, Error> {
    let mut s = Set::default();
    let decls = condition_decls();
    let mut seen = HashSet::new();
    let mut scoped = Vec::new();
    let mut aggs = Vec::new();
    for d in defs {
        if d.name.is_empty() {
            return Err(Error::compile("condition without name"));
        }
        if !seen.insert(d.name.clone()) {
            return Err(Error::compile(format!(
                "duplicate condition {}",
                go_quote(&d.name)
            )));
        }
        if !d.description.is_empty() {
            s.descriptions.insert(d.name.clone(), d.description.clone());
        }
        if d.is_aggregate() {
            aggs.push(d.clone());
            continue;
        }
        if d.is_scoped() {
            scoped.push(d.clone());
            continue;
        }
        if !d.r#where.is_empty() {
            return Err(Error::compile(format!(
                "condition {}: where applies to a scoped condition or an aggregate",
                go_quote(&d.name)
            )));
        }
        let p = compile_expr(&decls, &d.expr)
            .map_err(|e| e.context(&format!("condition {}", go_quote(&d.name))))?;
        s.progs.push(Compiled {
            name: d.name.clone(),
            expr: d.expr.clone(),
            prog: Arc::new(p),
        });
    }
    s.compile_scoped(&scoped, &aggs)?;
    Ok(s)
}

/// Result is the outcome of an evaluation.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Result {
    /// State is the value of every condition of the level of the change: the world a planner plans from.
    pub state: BTreeMap<String, bool>,
    /// Errors holds the conditions that could not be evaluated (unknown).
    pub errors: BTreeMap<String, String>,
    /// Subjects are the values of the scoped conditions, per scope, subject by subject in the order of the context
    /// (ADR 0109 §3); empty when the set has none.
    pub subjects: BTreeMap<String, Vec<Subject>>,
}

impl Set {
    /// Names returns the names of the conditions of the level of the change (the world of the planner: expressions
    /// and aggregates), sorted.
    pub fn names(&self) -> Vec<String> {
        let mut out: Vec<String> = self
            .progs
            .iter()
            .map(|p| p.name.clone())
            .chain(self.aggregates.iter().map(|a| a.name.clone()))
            .collect();
        out.sort();
        out
    }

    /// Exprs maps every condition of the set to its expression (an aggregate as "all: <condition>", a scoped
    /// condition prefixed by its scope).
    pub fn exprs(&self) -> BTreeMap<String, String> {
        let mut out = BTreeMap::new();
        for p in &self.progs {
            out.insert(p.name.clone(), p.expr.clone());
        }
        for c in &self.scoped {
            out.insert(c.name.clone(), c.text());
        }
        for a in &self.aggregates {
            out.insert(a.name.clone(), a.text());
        }
        out
    }

    /// Description says what a condition of the set means, as its definition does ("" when it says nothing): the
    /// methodology's own words, a library's, or those of the compiler for a condition it generates.
    pub fn description(&self, name: &str) -> &str {
        self.descriptions
            .get(name)
            .map(|s| s.as_str())
            .unwrap_or("")
    }

    /// Has reports whether a condition is defined (of any level).
    pub fn has(&self, name: &str) -> bool {
        self.progs.iter().any(|p| p.name == name)
            || !self.scope_of(name).is_empty()
            || self.aggregate_of(name).is_some()
    }

    /// The expression of a condition of the change level.
    pub(crate) fn expr_of(&self, name: &str) -> Option<&str> {
        self.progs
            .iter()
            .rev()
            .find(|p| p.name == name)
            .map(|p| p.expr.as_str())
    }

    /// Evaluate evaluates every condition against the blackboard: the scoped conditions per subject first (their
    /// values are then visible as `conditions` on the entries of their scope), the conditions of the change, then
    /// the aggregates.
    pub fn evaluate(&self, bb: &Blackboard) -> Result {
        let mut act = Activation::new(bb);
        let mut res = Result::default();
        let entries = self.evaluate_scoped(&mut act, &mut res);
        for p in &self.progs {
            match eval_bool(&p.prog, &act) {
                Ok(b) => {
                    res.state.insert(p.name.clone(), b);
                }
                Err(e) => {
                    res.errors.insert(p.name.clone(), e);
                }
            }
        }
        self.evaluate_aggregates(&act, &entries, &mut res);
        res
    }
}
