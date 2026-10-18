//! Scopes of a condition (ADR 0109 §3): the kind of subject it is about. A scoped condition is evaluated once per
//! subject (`self`), its values are visible as `conditions` on the entries of its scope, and an aggregate (all, any,
//! none) brings them to the level of the change, the only level the planner reasons on.
use std::{
    collections::{BTreeMap, HashMap},
    result::Result as StdResult,
    sync::{Arc, LazyLock},
};

use cel::Program;
use goap_domain::Blackboard;
use regex::Regex;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{
    activation::{Activation, Object, Overlay},
    check::Ty,
    condition::{
        Definition, Error, Result, Set, compile, compile_expr, condition_decls, eval_bool,
    },
    explain::{Explanation, Term, text},
    unparse::go_quote,
};

pub const SCOPE_CHANGE: &str = "change";
/// SCOPE_IMPACT: the impacts of the change (an entry of changeImpacts), identified by their node key.
pub const SCOPE_IMPACT: &str = "impact";
/// SCOPE_ITEM: the items of the change (an entry of items), identified by their id.
pub const SCOPE_ITEM: &str = "item";
/// SCOPE_RISK, SCOPE_ACTION: the keyed records of the risk register (ADR 0036), identified by their key.
pub const SCOPE_RISK: &str = "risk";
pub const SCOPE_ACTION: &str = "action";
/// SCOPE_STEP: the step runs of the history (an entry of history.steps), identified by "<path>#<run>".
pub const SCOPE_STEP: &str = "step";

/// Scopes lists the scopes of a subject, in a stable order.
pub const SCOPES: [&str; 5] = [
    SCOPE_IMPACT,
    SCOPE_ITEM,
    SCOPE_RISK,
    SCOPE_ACTION,
    SCOPE_STEP,
];

/// Aggregate operators.
pub const AGGREGATE_ALL: &str = "all";
pub const AGGREGATE_ANY: &str = "any";
pub const AGGREGATE_NONE: &str = "none";

impl Definition {
    /// IsScoped reports whether the definition is about one subject of a scope.
    pub fn is_scoped(&self) -> bool {
        !self.scope.is_empty() && self.scope != SCOPE_CHANGE
    }

    /// IsAggregate reports whether the definition aggregates a scoped condition.
    pub fn is_aggregate(&self) -> bool {
        !self.all.is_empty() || !self.any.is_empty() || !self.none.is_empty()
    }

    /// Aggregate returns the operator and the scoped condition of an aggregate (None when it is none).
    pub fn aggregate(&self) -> Option<(&'static str, &str)> {
        if !self.all.is_empty() {
            Some((AGGREGATE_ALL, &self.all))
        } else if !self.any.is_empty() {
            Some((AGGREGATE_ANY, &self.any))
        } else if !self.none.is_empty() {
            Some((AGGREGATE_NONE, &self.none))
        } else {
            None
        }
    }
}

/// Subject is a subject of a scope with the values of the scoped conditions about it (a condition whose where leaves
/// the subject out has no value) and the conditions that could not be evaluated.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Subject {
    pub id: String,
    pub values: BTreeMap<String, bool>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub errors: BTreeMap<String, String>,
}

impl Subject {
    /// Holds reports whether every condition has the expected value on the subject (a missing value does not hold).
    pub fn holds(&self, want: &BTreeMap<String, bool>) -> bool {
        want.iter()
            .all(|(k, v)| self.values.get(k).is_some_and(|g| g == v))
    }
}

pub(crate) struct ScopedCond {
    pub(crate) name: String,
    pub(crate) scope: String,
    expr: String,
    r#where: String,
    prog: Arc<Program>,
    where_prog: Option<Arc<Program>>,
}

impl ScopedCond {
    pub(crate) fn text(&self) -> String {
        let mut out = format!("[{}] {}", self.scope, self.expr);
        if !self.r#where.is_empty() {
            out.push_str(&format!(" (where {})", self.r#where));
        }
        out
    }
}

pub(crate) struct Aggregate {
    pub(crate) name: String,
    op: &'static str,
    of: String,
    scope: String,
    r#where: String,
    empty: bool,
    where_prog: Option<Arc<Program>>,
}

impl Aggregate {
    pub(crate) fn text(&self) -> String {
        let mut out = format!("{}: {}", self.op, self.of);
        if !self.r#where.is_empty() {
            out.push_str(&format!(" (where {})", self.r#where));
        }
        if self.op == AGGREGATE_ALL && self.empty {
            out.push_str(" (empty: true)");
        }
        out
    }
}

/// The declared variables of a scoped condition: the ones of the conditions plus `self`, the subject.
pub(crate) fn scoped_decls() -> Vec<(&'static str, Ty)> {
    let mut d = condition_decls();
    d.push(("self", Ty::map()));
    d
}

/// readRE finds the scoped conditions an expression reads on its subject: self.conditions.x, self.conditions["x"].
static READ_RE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r#"self\s*\.\s*conditions\s*(?:\.\s*([A-Za-z_][A-Za-z0-9_]*)|\[\s*"([^"]+)"\s*\])"#)
        .expect("a regexp")
});

/// SelfReads lists the scoped conditions an expression reads on its own subject.
pub fn self_reads(expr: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for m in READ_RE.captures_iter(expr) {
        let name = m
            .get(1)
            .or_else(|| m.get(2))
            .map(|x| x.as_str().to_string())
            .unwrap_or_default();
        if !out.contains(&name) {
            out.push(name);
        }
    }
    out
}

/// CheckDefinition checks one definition on its own (its scope, its expressions), without the conditions it names:
/// what a compiler reports per condition before the whole set is compiled.
pub fn check_definition(d: &Definition) -> StdResult<(), Error> {
    if d.is_aggregate() {
        let n = [&d.all, &d.any, &d.none]
            .iter()
            .filter(|x| !x.is_empty())
            .count();
        if n > 1 {
            return Err(Error::compile("an aggregate is one of all, any or none"));
        }
        if !d.expr.is_empty() || d.is_scoped() {
            return Err(Error::compile(
                "an aggregate has no expression nor scope: it takes the scope of the condition it aggregates",
            ));
        }
        if d.empty && d.all.is_empty() {
            return Err(Error::compile(
                "empty applies to an all aggregate (any is false and none true on no subject)",
            ));
        }
        if !d.r#where.is_empty() {
            return check_scoped_expr(&d.r#where);
        }
        return Ok(());
    }
    if d.is_scoped() {
        if !SCOPES.contains(&d.scope.as_str()) {
            return Err(Error::compile(format!(
                "unknown scope {} (one of change, {})",
                go_quote(&d.scope),
                SCOPES.join(", ")
            )));
        }
        if !d.r#where.is_empty() {
            check_scoped_expr(&d.r#where).map_err(|e| e.context("where"))?;
        }
        return check_scoped_expr(&d.expr);
    }
    if !d.r#where.is_empty() {
        return Err(Error::compile(
            "where applies to a scoped condition or an aggregate",
        ));
    }
    if d.empty {
        return Err(Error::compile("empty applies to an all aggregate"));
    }
    match compile(&[Definition::new("x", d.expr.clone())]) {
        Ok(_) => Ok(()),
        Err(e) => {
            let m = e.to_string();
            Err(Error::compile(
                m.strip_prefix(r#"condition "x": "#)
                    .unwrap_or(&m)
                    .to_string(),
            ))
        }
    }
}

fn check_scoped_expr(expr: &str) -> StdResult<(), Error> {
    compile_expr(&scoped_decls(), expr).map(|_| ())
}

impl Set {
    /// compileScoped compiles the scoped conditions (ordered so that a condition comes after those it reads on its
    /// subject, refusing a cycle) and the aggregates.
    pub(crate) fn compile_scoped(
        &mut self,
        scoped: &[Definition],
        aggs: &[Definition],
    ) -> StdResult<(), Error> {
        if scoped.is_empty() && aggs.is_empty() {
            return Ok(());
        }
        let decls = scoped_decls();
        let mut by_name: HashMap<&str, &Definition> = HashMap::new();
        for d in scoped {
            check_definition(d)
                .map_err(|e| e.context(&format!("condition {}", go_quote(&d.name))))?;
            by_name.insert(&d.name, d);
        }
        let mut state: HashMap<String, u8> = HashMap::new(); // 1 visiting, 2 done
        fn visit(
            s: &mut Set,
            decls: &[(&str, Ty)],
            by_name: &HashMap<&str, &Definition>,
            state: &mut HashMap<String, u8>,
            name: &str,
            from: &[String],
        ) -> StdResult<(), Error> {
            let d = by_name[name];
            match state.get(name) {
                Some(1) => {
                    let mut path = from.to_vec();
                    path.push(name.to_string());
                    return Err(Error::compile(format!(
                        "condition {}: scoped conditions read each other in a cycle ({})",
                        go_quote(name),
                        path.join(" -> ")
                    )));
                }
                Some(2) => return Ok(()),
                _ => {}
            }
            state.insert(name.to_string(), 1);
            let mut reads = self_reads(&d.expr);
            reads.extend(self_reads(&d.r#where));
            for r in reads {
                let Some(o) = by_name.get(r.as_str()) else {
                    return Err(Error::compile(format!(
                        "condition {} reads {} on its subject: not a scoped condition",
                        go_quote(name),
                        go_quote(&r)
                    )));
                };
                if o.scope != d.scope {
                    return Err(Error::compile(format!(
                        "condition {} (scope {}) reads {} of scope {}",
                        go_quote(name),
                        d.scope,
                        go_quote(&r),
                        o.scope
                    )));
                }
                let mut next = from.to_vec();
                next.push(name.to_string());
                visit(s, decls, by_name, state, &r, &next)?;
            }
            state.insert(name.to_string(), 2);
            let prog = compile_expr(decls, &d.expr)
                .map_err(|e| e.context(&format!("condition {}", go_quote(name))))?;
            let where_prog = if d.r#where.is_empty() {
                None
            } else {
                Some(Arc::new(compile_expr(decls, &d.r#where).map_err(|e| {
                    e.context(&format!("condition {}: where", go_quote(name)))
                })?))
            };
            s.scoped.push(ScopedCond {
                name: d.name.clone(),
                scope: d.scope.clone(),
                expr: d.expr.clone(),
                r#where: d.r#where.clone(),
                prog: Arc::new(prog),
                where_prog,
            });
            Ok(())
        }
        for d in scoped {
            visit(self, &decls, &by_name, &mut state, &d.name, &[])?;
        }
        for d in aggs {
            check_definition(d)
                .map_err(|e| e.context(&format!("condition {}", go_quote(&d.name))))?;
            let (op, of) = d.aggregate().expect("an aggregate");
            let Some(o) = by_name.get(of) else {
                return Err(Error::compile(format!(
                    "condition {}: {} {}: not a scoped condition",
                    go_quote(&d.name),
                    op,
                    go_quote(of)
                )));
            };
            let mut a = Aggregate {
                name: d.name.clone(),
                op,
                of: of.to_string(),
                scope: o.scope.clone(),
                r#where: d.r#where.clone(),
                empty: d.empty,
                where_prog: None,
            };
            if !d.r#where.is_empty() {
                for r in self_reads(&d.r#where) {
                    if by_name.get(r.as_str()).is_none_or(|x| x.scope != o.scope) {
                        return Err(Error::compile(format!(
                            "condition {}: where reads {}: not a scoped condition of scope {}",
                            go_quote(&d.name),
                            go_quote(&r),
                            o.scope
                        )));
                    }
                }
                a.where_prog = Some(Arc::new(compile_expr(&decls, &d.r#where).map_err(|e| {
                    e.context(&format!("condition {}: where", go_quote(&d.name)))
                })?));
            }
            self.aggregates.push(a);
        }
        Ok(())
    }

    /// ScopeOf returns the scope of a scoped condition of the set ("" for a condition of the change or an unknown
    /// one).
    pub fn scope_of(&self, name: &str) -> &str {
        self.scoped
            .iter()
            .find(|c| c.name == name)
            .map(|c| c.scope.as_str())
            .unwrap_or("")
    }

    pub(crate) fn aggregate_of(&self, name: &str) -> Option<&Aggregate> {
        self.aggregates.iter().find(|a| a.name == name)
    }
}

/// The entries of the scopes the set evaluates, by scope.
pub(crate) type ScopeEntries = BTreeMap<String, Vec<ScopeEntry>>;

pub(crate) struct ScopeEntry {
    id: String,
    /// The index of the entry in the list of its scope.
    index: usize,
    entry: Object,
}

/// The list a scope ranges over, in an activation.
fn scope_list<'x>(act: &'x Activation<'_>, scope: &str) -> Option<&'x Vec<Value>> {
    let v = match scope {
        SCOPE_IMPACT => act.get("changeImpacts")?,
        SCOPE_ITEM => act.get("items")?,
        SCOPE_RISK => act.get("risks")?,
        SCOPE_ACTION => act.get("actions")?,
        SCOPE_STEP => act.get("history")?.get("steps")?,
        _ => return None,
    };
    v.as_array()
}

fn scope_list_mut<'x>(act: &'x mut Activation<'_>, scope: &str) -> Option<&'x mut Vec<Value>> {
    let v = match scope {
        SCOPE_IMPACT => act.get_mut("changeImpacts")?,
        SCOPE_ITEM => act.get_mut("items")?,
        SCOPE_RISK => act.get_mut("risks")?,
        SCOPE_ACTION => act.get_mut("actions")?,
        SCOPE_STEP => act.get_mut("history")?.get_mut("steps")?,
        _ => return None,
    };
    v.as_array_mut()
}

/// SubjectsOf returns the entries of a scope as the conditions see them (CEL maps), from an activation (computing a
/// lazy variable).
pub fn subjects_of(act: &Activation<'_>, scope: &str) -> Vec<Object> {
    scope_list(act, scope)
        .map(|l| l.iter().filter_map(|x| x.as_object().cloned()).collect())
        .unwrap_or_default()
}

/// SubjectID is the identity of an entry of a scope.
pub fn subject_id(scope: &str, entry: &Object) -> String {
    let str_of = |k: &str| {
        entry
            .get(k)
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string()
    };
    match scope {
        SCOPE_IMPACT => {
            let k = str_of("key");
            if !k.is_empty() { k } else { str_of("id") }
        }
        SCOPE_ITEM => str_of("id"),
        SCOPE_STEP => {
            let runs = entry.get("runs").and_then(|v| v.as_i64()).unwrap_or(0);
            format!("{}#{}", str_of("path"), runs)
        }
        _ => str_of("key"),
    }
}

/// SubjectEntry returns the entry of a subject of a scope as the conditions see it (None when the blackboard has
/// none): what an action run for that subject gets as `vars.item`.
pub fn subject_entry(bb: &Blackboard, scope: &str, id: &str) -> Option<Object> {
    let act = Activation::new(bb);
    subjects_of(&act, scope)
        .into_iter()
        .find(|e| subject_id(scope, e) == id)
}

/// aggregated is the outcome of an aggregate: its value, the subjects it counted and those that do not hold (for
/// all: false, for none: true, for any: none of them holds when it is false), and the errors that make it unknown.
#[derive(Default)]
struct Aggregated {
    value: bool,
    counted: usize,
    failing: Vec<String>,
    errs: Vec<String>,
    unknown: bool,
    unknown_message: String,
}

impl Set {
    /// evaluateScoped evaluates the scoped conditions on every subject of their scope, records the values in res and
    /// on the entries (`conditions`), the variables of those scopes plain from then on, so that the conditions of the
    /// change read the same entries.
    pub(crate) fn evaluate_scoped(
        &self,
        act: &mut Activation<'_>,
        res: &mut Result,
    ) -> ScopeEntries {
        let mut out = ScopeEntries::new();
        if self.scoped.is_empty() {
            return out;
        }
        for c in &self.scoped {
            if out.contains_key(&c.scope) {
                continue;
            }
            let mut list = Vec::new();
            if let Some(l) = scope_list_mut(act, &c.scope) {
                for (index, x) in l.iter_mut().enumerate() {
                    if let Some(e) = x.as_object_mut() {
                        e.insert("conditions".into(), Value::Object(Map::new()));
                        list.push(ScopeEntry {
                            id: subject_id(&c.scope, e),
                            index,
                            entry: e.clone(),
                        });
                    }
                }
            }
            out.insert(c.scope.clone(), list);
        }
        for (scope, list) in out.iter_mut() {
            let mut subjects = Vec::with_capacity(list.len());
            for se in list.iter_mut() {
                let mut sub = Subject {
                    id: se.id.clone(),
                    ..Default::default()
                };
                for c in self.scoped.iter().filter(|c| &c.scope == scope) {
                    let self_v = Value::Object(se.entry.clone());
                    let outcome = {
                        let sact = Overlay::new(act).with("self", &self_v);
                        let inside = match &c.where_prog {
                            Some(w) => eval_bool(w, &sact).map_err(|e| format!("where: {e}")),
                            None => Ok(true),
                        };
                        match inside {
                            Err(e) => Err(e),
                            Ok(false) => Ok(None),
                            Ok(true) => eval_bool(&c.prog, &sact).map(Some),
                        }
                    };
                    match outcome {
                        Err(e) => {
                            sub.errors.insert(c.name.clone(), e);
                        }
                        Ok(None) => {}
                        Ok(Some(b)) => {
                            sub.values.insert(c.name.clone(), b);
                            set_condition(&mut se.entry, &c.name, b);
                            if let Some(l) = scope_list_mut(act, scope)
                                && let Some(e) = l.get_mut(se.index).and_then(|x| x.as_object_mut())
                            {
                                set_condition(e, &c.name, b);
                            }
                        }
                    }
                }
                subjects.push(sub);
            }
            res.subjects.insert(scope.clone(), subjects);
        }
        out
    }

    fn aggregate(
        &self,
        a: &Aggregate,
        act: &Activation<'_>,
        entries: &ScopeEntries,
        res: &Result,
    ) -> Aggregated {
        let mut out = Aggregated::default();
        let empty = Vec::new();
        let subjects = res.subjects.get(&a.scope).unwrap_or(&empty);
        for (i, sub) in subjects.iter().enumerate() {
            if let Some(msg) = sub.errors.get(&a.of) {
                out.errs.push(format!("{}: {}", sub.id, msg));
                continue;
            }
            let Some(&v) = sub.values.get(&a.of) else {
                continue; // outside the where of the condition
            };
            if let Some(w) = &a.where_prog {
                let entry = entries
                    .get(&a.scope)
                    .and_then(|l| l.get(i))
                    .map(|e| Value::Object(e.entry.clone()))
                    .unwrap_or(Value::Null);
                let sact = Overlay::new(act).with("self", &entry);
                match eval_bool(w, &sact) {
                    Err(e) => {
                        out.errs.push(format!("{}: where: {}", sub.id, e));
                        continue;
                    }
                    Ok(false) => continue,
                    Ok(true) => {}
                }
            }
            out.counted += 1;
            match a.op {
                AGGREGATE_ALL if !v => out.failing.push(sub.id.clone()),
                AGGREGATE_ANY if v => out.value = true,
                AGGREGATE_ANY => out.failing.push(sub.id.clone()),
                AGGREGATE_NONE if v => out.failing.push(sub.id.clone()),
                _ => {}
            }
        }
        match a.op {
            AGGREGATE_ALL => out.value = out.failing.is_empty() && (out.counted > 0 || a.empty),
            AGGREGATE_NONE => out.value = out.failing.is_empty(),
            _ => {}
        }
        if !out.errs.is_empty() && (out.value || a.op == AGGREGATE_ANY) {
            // a subject that could not be evaluated might change the value: the aggregate is unknown
            out.errs.sort();
            out.unknown = true;
            out.unknown_message = out.errs.join("; ");
        }
        if a.op == AGGREGATE_ANY && out.value {
            out.unknown = false;
        }
        out
    }

    /// evaluateAggregates brings the scoped conditions to the level of the change.
    pub(crate) fn evaluate_aggregates(
        &self,
        act: &Activation<'_>,
        entries: &ScopeEntries,
        res: &mut Result,
    ) {
        for a in &self.aggregates {
            let r = self.aggregate(a, act, entries, res);
            if r.unknown {
                res.errors.insert(a.name.clone(), r.unknown_message);
                continue;
            }
            res.state.insert(a.name.clone(), r.value);
        }
    }

    /// explainAggregate explains an aggregate: its value and the subjects that do not hold.
    pub(crate) fn explain_aggregate(&self, a: &Aggregate, bb: &Blackboard) -> Explanation {
        let mut act = Activation::new(bb);
        let mut res = Result::default();
        let entries = self.evaluate_scoped(&mut act, &mut res);
        let r = self.aggregate(a, &act, &entries, &res);
        let mut ex = Explanation {
            name: a.name.clone(),
            expr: a.text(),
            value: r.value,
            ..Default::default()
        };
        if r.unknown {
            ex.error = r.unknown_message.clone();
        }
        ex.inputs.insert("subjects".into(), r.counted.to_string());
        if !r.failing.is_empty() {
            ex.inputs
                .insert("failing".into(), text(&Value::from(r.failing.clone())));
        }
        ex.root = Term {
            text: a.text(),
            value: r.value.to_string(),
            ..Default::default()
        };
        for id in &r.failing {
            ex.root.terms.push(Term {
                text: format!("{} on {}", a.of, id),
                value: (a.op == AGGREGATE_NONE).to_string(),
                ..Default::default()
            });
        }
        ex
    }

    /// explainScoped explains a scoped condition: its value on each subject of its scope. Value is true when it
    /// holds on every subject it has a value for.
    pub(crate) fn explain_scoped(&self, name: &str, bb: &Blackboard) -> Explanation {
        let mut act = Activation::new(bb);
        let mut res = Result::default();
        self.evaluate_scoped(&mut act, &mut res);
        let c = self.scoped.iter().rev().find(|x| x.name == name);
        let (text, scope) = c.map(|c| (c.text(), c.scope.clone())).unwrap_or_default();
        let mut ex = Explanation {
            name: name.to_string(),
            expr: text.clone(),
            value: true,
            root: Term {
                text,
                ..Default::default()
            },
            ..Default::default()
        };
        for sub in res.subjects.get(&scope).into_iter().flatten() {
            let mut t = Term {
                text: format!("{} on {}", name, sub.id),
                ..Default::default()
            };
            if let Some(msg) = sub.errors.get(name) {
                t.error = msg.clone();
            } else if let Some(&v) = sub.values.get(name) {
                t.value = v.to_string();
                ex.value = ex.value && v;
            } else {
                t.skipped = true;
            }
            ex.root.terms.push(t);
        }
        ex
    }
}

fn set_condition(e: &mut Object, name: &str, b: bool) {
    if let Some(Value::Object(conds)) = e.get_mut("conditions") {
        conds.insert(name.to_string(), Value::Bool(b));
    }
}
