//! The conditions of a methodology (Go's `engine/condition` `Set`): named CEL expressions over the variables of a
//! blackboard, at the level of the change, about each subject of a scope (`self`), or aggregates of a scoped condition.
use std::collections::{BTreeMap, HashMap};
use std::sync::LazyLock;

use cel::{Context, Program, Value};
use regex::Regex;
use serde::Serialize;
use serde_json::{Map, Value as Json};

use crate::guard::{go_message, value};

/// The scopes of a subject, in Go's order (`condition.Scopes`).
pub const SCOPES: [&str; 5] = ["impact", "item", "risk", "action", "step"];

/// A named condition (`condition.Definition`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Definition {
    pub name: String,
    pub expr: String,
    pub description: String,
    /// The kind of subject the condition is about; empty or `change`: the change.
    pub scope: String,
    /// Narrows the subjects of a scoped condition or of an aggregate.
    pub r#where: String,
    pub all: String,
    pub any: String,
    pub none: String,
    /// The value of an `all` aggregate over no subject.
    pub empty: bool,
}

impl Definition {
    /// A condition of the level of the change.
    pub fn plain(name: impl Into<String>, expr: impl Into<String>, description: impl Into<String>) -> Definition {
        Definition {
            name: name.into(),
            expr: expr.into(),
            description: description.into(),
            ..Definition::default()
        }
    }

    /// Whether the condition is about one subject of a scope.
    pub fn is_scoped(&self) -> bool {
        !self.scope.is_empty() && self.scope != "change"
    }

    /// Whether the condition aggregates a scoped condition.
    pub fn is_aggregate(&self) -> bool {
        !self.all.is_empty() || !self.any.is_empty() || !self.none.is_empty()
    }

    /// The operator and the scoped condition of an aggregate (empty when it is none).
    pub fn aggregate(&self) -> (&'static str, &str) {
        if !self.all.is_empty() {
            ("all", &self.all)
        } else if !self.any.is_empty() {
            ("any", &self.any)
        } else if !self.none.is_empty() {
            ("none", &self.none)
        } else {
            ("", "")
        }
    }
}

/// The scoped conditions an expression reads on its own subject (`condition.SelfReads`).
pub fn self_reads(expr: &str) -> Vec<String> {
    static READ: LazyLock<Regex> = LazyLock::new(|| {
        Regex::new(r#"self\s*\.\s*conditions\s*(?:\.\s*([A-Za-z_][A-Za-z0-9_]*)|\[\s*"([^"]+)"\s*\])"#).unwrap()
    });
    let mut out: Vec<String> = Vec::new();
    for c in READ.captures_iter(expr) {
        let name = c.get(1).or_else(|| c.get(2)).map_or("", |m| m.as_str());
        if !out.iter().any(|n| n == name) {
            out.push(name.to_string());
        }
    }
    out
}

struct Plain {
    name: String,
    expr: String,
    prog: Program,
}

struct Scoped {
    name: String,
    scope: String,
    expr: String,
    r#where: String,
    prog: Program,
    where_prog: Option<Program>,
}

struct Aggregate {
    name: String,
    op: &'static str,
    of: String,
    scope: String,
    r#where: String,
    empty: bool,
    where_prog: Option<Program>,
}

/// A compiled set of conditions.
pub struct Set {
    plain: Vec<Plain>,
    scoped: Vec<Scoped>,
    aggregates: Vec<Aggregate>,
    descriptions: HashMap<String, String>,
}

/// A subject of a scope with the values of the scoped conditions about it (`condition.Subject`).
#[derive(Debug, Clone, Default, PartialEq, Serialize)]
pub struct Subject {
    pub id: String,
    pub values: BTreeMap<String, bool>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub errors: BTreeMap<String, String>,
}

/// The outcome of an evaluation (`condition.Result`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Outcome {
    /// The value of every condition of the level of the change: the world state.
    pub state: BTreeMap<String, bool>,
    /// The conditions that could not be evaluated.
    pub errors: BTreeMap<String, String>,
    /// The values of the scoped conditions, per scope, subject by subject.
    pub subjects: BTreeMap<String, Vec<Subject>>,
}

fn compile(expr: &str) -> Result<Program, String> {
    Program::compile(expr).map_err(|e| e.to_string())
}

impl Set {
    /// Compiles definitions (`condition.Compile`): a scoped condition after those it reads on its subject.
    pub fn compile(defs: &[Definition]) -> Result<Set, String> {
        let mut s = Set {
            plain: Vec::new(),
            scoped: Vec::new(),
            aggregates: Vec::new(),
            descriptions: HashMap::new(),
        };
        let mut seen = std::collections::HashSet::new();
        let (mut scoped, mut aggs) = (Vec::new(), Vec::new());
        for d in defs {
            if d.name.is_empty() {
                return Err("condition without name".into());
            }
            if !seen.insert(d.name.clone()) {
                return Err(format!("duplicate condition {:?}", d.name));
            }
            if !d.description.is_empty() {
                s.descriptions.insert(d.name.clone(), d.description.clone());
            }
            if d.is_aggregate() {
                aggs.push(d);
            } else if d.is_scoped() {
                scoped.push(d);
            } else {
                let prog = compile(&d.expr).map_err(|e| format!("condition {:?}: {e}", d.name))?;
                s.plain.push(Plain {
                    name: d.name.clone(),
                    expr: d.expr.clone(),
                    prog,
                });
            }
        }
        let by_name: HashMap<&str, &Definition> = scoped.iter().map(|d| (d.name.as_str(), *d)).collect();
        let mut state: HashMap<String, u8> = HashMap::new();
        fn visit(
            name: &str,
            by_name: &HashMap<&str, &Definition>,
            state: &mut HashMap<String, u8>,
            out: &mut Vec<Scoped>,
        ) -> Result<(), String> {
            match state.get(name) {
                Some(1) => {
                    return Err(format!(
                        "condition {name:?}: scoped conditions read each other in a cycle"
                    ));
                }
                Some(_) => return Ok(()),
                None => {}
            }
            state.insert(name.to_string(), 1);
            let d = by_name[name];
            for r in self_reads(&d.expr).into_iter().chain(self_reads(&d.r#where)) {
                match by_name.get(r.as_str()) {
                    None => {
                        return Err(format!(
                            "condition {name:?} reads {r:?} on its subject: not a scoped condition"
                        ));
                    }
                    Some(o) if o.scope != d.scope => {
                        return Err(format!(
                            "condition {name:?} (scope {}) reads {r:?} of scope {}",
                            d.scope, o.scope
                        ));
                    }
                    _ => visit(&r, by_name, state, out)?,
                }
            }
            state.insert(name.to_string(), 2);
            let prog = compile(&d.expr).map_err(|e| format!("condition {name:?}: {e}"))?;
            let where_prog = match d.r#where.as_str() {
                "" => None,
                w => Some(compile(w).map_err(|e| format!("condition {name:?}: where: {e}"))?),
            };
            out.push(Scoped {
                name: d.name.clone(),
                scope: d.scope.clone(),
                expr: d.expr.clone(),
                r#where: d.r#where.clone(),
                prog,
                where_prog,
            });
            Ok(())
        }
        for d in &scoped {
            visit(&d.name, &by_name, &mut state, &mut s.scoped)?;
        }
        for d in aggs {
            let (op, of) = d.aggregate();
            let Some(o) = by_name.get(of) else {
                return Err(format!("condition {:?}: {op} {of:?}: not a scoped condition", d.name));
            };
            let where_prog = match d.r#where.as_str() {
                "" => None,
                w => Some(compile(w).map_err(|e| format!("condition {:?}: where: {e}", d.name))?),
            };
            s.aggregates.push(Aggregate {
                name: d.name.clone(),
                op,
                of: of.to_string(),
                scope: o.scope.clone(),
                r#where: d.r#where.clone(),
                empty: d.empty,
                where_prog,
            });
        }
        Ok(s)
    }

    /// Every condition of the set with its expression (`Set.Exprs`): an aggregate as `all: <condition>`, a scoped
    /// condition prefixed by its scope.
    pub fn exprs(&self) -> BTreeMap<String, String> {
        let mut out = BTreeMap::new();
        for p in &self.plain {
            out.insert(p.name.clone(), p.expr.clone());
        }
        for c in &self.scoped {
            let mut t = format!("[{}] {}", c.scope, c.expr);
            if !c.r#where.is_empty() {
                t += &format!(" (where {})", c.r#where);
            }
            out.insert(c.name.clone(), t);
        }
        for a in &self.aggregates {
            let mut t = format!("{}: {}", a.op, a.of);
            if !a.r#where.is_empty() {
                t += &format!(" (where {})", a.r#where);
            }
            if a.op == "all" && a.empty {
                t += " (empty: true)";
            }
            out.insert(a.name.clone(), t);
        }
        out
    }

    /// What a condition of the set means, as its definition says.
    pub fn description(&self, name: &str) -> &str {
        self.descriptions.get(name).map_or("", String::as_str)
    }

    /// Whether a condition is defined, of any level.
    pub fn has(&self, name: &str) -> bool {
        self.plain.iter().any(|p| p.name == name)
            || self.scoped.iter().any(|c| c.name == name)
            || self.aggregates.iter().any(|a| a.name == name)
    }

    /// Evaluates every condition over the variables of a blackboard (`Set.EvaluateActivation`): the scoped conditions
    /// per subject first (their values then on the entries of their scope, `conditions`), the conditions of the
    /// change, then the aggregates.
    pub fn evaluate(&self, act: &Map<String, Json>) -> Outcome {
        let mut act = act.clone();
        let mut out = Outcome::default();
        let entries = self.evaluate_scoped(&mut act, &mut out);
        let ctx = context(&act);
        for p in &self.plain {
            match eval_bool(&p.prog, &ctx) {
                Ok(b) => {
                    out.state.insert(p.name.clone(), b);
                }
                Err(e) => {
                    out.errors.insert(p.name.clone(), e);
                }
            }
        }
        for a in &self.aggregates {
            match self.aggregate(a, &ctx, &entries, &out) {
                Ok(v) => {
                    out.state.insert(a.name.clone(), v);
                }
                Err(e) => {
                    out.errors.insert(a.name.clone(), e);
                }
            }
        }
        out
    }

    /// The scoped conditions on every subject of their scope; the entries of each scope, with their values, are left
    /// in the activation and returned by scope.
    fn evaluate_scoped(&self, act: &mut Map<String, Json>, out: &mut Outcome) -> BTreeMap<String, Vec<Json>> {
        let mut entries: BTreeMap<String, Vec<Json>> = BTreeMap::new();
        if self.scoped.is_empty() {
            return entries;
        }
        for c in &self.scoped {
            if entries.contains_key(&c.scope) {
                continue;
            }
            let list: Vec<Json> = subjects_of(act, &c.scope)
                .into_iter()
                .map(|mut e| {
                    e.insert("conditions".into(), Json::Object(Map::new()));
                    Json::Object(e)
                })
                .collect();
            entries.insert(c.scope.clone(), list);
        }
        // the activation as the scoped conditions read it: every entry with its (empty) conditions
        for (scope, list) in &entries {
            set_subjects(act, scope, list.clone());
        }
        let ctx = context(act);
        for (scope, list) in entries.iter_mut() {
            let mut subjects = Vec::with_capacity(list.len());
            for e in list.iter_mut() {
                let mut sub = Subject {
                    id: subject_id(scope, e),
                    ..Subject::default()
                };
                for c in self.scoped.iter().filter(|c| &c.scope == scope) {
                    let mut inner = ctx.new_inner_scope();
                    inner.add_variable_from_value("self", value(e));
                    if let Some(w) = &c.where_prog {
                        match eval_bool(w, &inner) {
                            Ok(true) => {}
                            Ok(false) => continue,
                            Err(m) => {
                                sub.errors.insert(c.name.clone(), format!("where: {m}"));
                                continue;
                            }
                        }
                    }
                    match eval_bool(&c.prog, &inner) {
                        Ok(b) => {
                            sub.values.insert(c.name.clone(), b);
                            if let Some(Json::Object(conds)) = e.get_mut("conditions") {
                                conds.insert(c.name.clone(), b.into());
                            }
                        }
                        Err(m) => {
                            sub.errors.insert(c.name.clone(), m);
                        }
                    }
                }
                subjects.push(sub);
            }
            out.subjects.insert(scope.clone(), subjects);
        }
        for (scope, list) in &entries {
            set_subjects(act, scope, list.clone());
        }
        entries
    }

    /// The value of an aggregate (`Set.aggregate`): unknown, an error, when a subject that could not be evaluated
    /// might change it.
    fn aggregate(
        &self,
        a: &Aggregate,
        ctx: &Context,
        entries: &BTreeMap<String, Vec<Json>>,
        out: &Outcome,
    ) -> Result<bool, String> {
        let none = Vec::new();
        let subjects = out.subjects.get(&a.scope).unwrap_or(&none);
        let (mut value, mut counted, mut failing, mut errs) = (false, 0, 0, Vec::new());
        for (i, sub) in subjects.iter().enumerate() {
            if let Some(m) = sub.errors.get(&a.of) {
                errs.push(format!("{}: {m}", sub.id));
                continue;
            }
            let Some(&v) = sub.values.get(&a.of) else { continue };
            if let Some(w) = &a.where_prog {
                let mut inner = ctx.new_inner_scope();
                inner.add_variable_from_value("self", value_of(entries, &a.scope, i));
                match eval_bool(w, &inner) {
                    Ok(true) => {}
                    Ok(false) => continue,
                    Err(m) => {
                        errs.push(format!("{}: where: {m}", sub.id));
                        continue;
                    }
                }
            }
            counted += 1;
            match a.op {
                "all" if !v => failing += 1,
                "any" if v => value = true,
                "any" => failing += 1,
                "none" if v => failing += 1,
                _ => {}
            }
        }
        match a.op {
            "all" => value = failing == 0 && (counted > 0 || a.empty),
            "none" => value = failing == 0,
            _ => {}
        }
        if !errs.is_empty() && (value || a.op == "any") && !(a.op == "any" && value) {
            errs.sort();
            return Err(errs.join("; "));
        }
        Ok(value)
    }
}

fn value_of(entries: &BTreeMap<String, Vec<Json>>, scope: &str, i: usize) -> Value {
    entries.get(scope).and_then(|l| l.get(i)).map_or(Value::Null, value)
}

/// A context holding the variables of an activation.
fn context(act: &Map<String, Json>) -> Context<'static, 'static> {
    let mut ctx = Context::default();
    for (k, v) in act {
        ctx.add_variable_from_value(k.clone(), value(v));
    }
    ctx
}

fn eval_bool(p: &Program, ctx: &Context) -> Result<bool, String> {
    match p.execute(ctx) {
        Ok(Value::Bool(b)) => Ok(b),
        Ok(v) => Err(format!("non-bool result {v:?}")),
        Err(e) => Err(go_message(&e.to_string())),
    }
}

/// The entries of a scope as the conditions see them (`condition.SubjectsOf`).
pub fn subjects_of(act: &Map<String, Json>, scope: &str) -> Vec<Map<String, Json>> {
    let list = match scope {
        "impact" => act.get("changeImpacts"),
        "item" => act.get("items"),
        "risk" => act.get("risks"),
        "action" => act.get("actions"),
        "step" => act.get("history").and_then(|h| h.get("steps")),
        _ => None,
    };
    list.and_then(Json::as_array)
        .map(|l| l.iter().filter_map(|x| x.as_object().cloned()).collect())
        .unwrap_or_default()
}

fn set_subjects(act: &mut Map<String, Json>, scope: &str, list: Vec<Json>) {
    let key = match scope {
        "impact" => "changeImpacts",
        "item" => "items",
        "risk" => "risks",
        "action" => "actions",
        "step" => {
            if let Some(Json::Object(h)) = act.get_mut("history") {
                h.insert("steps".into(), Json::Array(list));
            }
            return;
        }
        _ => return,
    };
    act.insert(key.into(), Json::Array(list));
}

/// The identity of an entry of a scope (`condition.SubjectID`).
pub fn subject_id(scope: &str, entry: &Json) -> String {
    let s = |k: &str| entry.get(k).and_then(Json::as_str).unwrap_or_default().to_string();
    match scope {
        "impact" => match s("key") {
            k if !k.is_empty() => k,
            _ => s("id"),
        },
        "item" => s("id"),
        "step" => format!(
            "{}#{}",
            s("path"),
            entry.get("runs").and_then(Json::as_i64).unwrap_or_default()
        ),
        _ => s("key"),
    }
}
