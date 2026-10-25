//! The values of an expression and their evaluation (Go's `lib/cypher/eval.go`): null, booleans, numbers (every number
//! a float), strings, lists, maps and the entities a caller binds (nodes, relationships, paths).
use std::any::Any;
use std::collections::BTreeMap;
use std::fmt;
use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;

use crate::ast::{AGGREGATES, Call, Case, Exists, Expr, Op, Unary};

/// A node or a relationship bound by a match.
pub trait Entity: Send + Sync + fmt::Debug {
    /// "node" or "relationship"
    fn kind(&self) -> &'static str;
    fn id(&self) -> &str;
    /// a node's types, a relationship's type alone
    fn labels(&self) -> Vec<String>;
    /// a property: the entity's own fields first (key, type, state…), None when it has none of that name
    fn property(&self, key: &str) -> Option<Value>;
    /// its properties, as keys() and properties() return them
    fn properties(&self) -> BTreeMap<String, Value>;
    fn as_any(&self) -> &dyn Any;
}

/// A path a pattern bound: `nodes[i]` and `nodes[i+1]` joined by `rels[i]`.
#[derive(Debug, Clone, Default)]
pub struct Path {
    pub nodes: Vec<Arc<dyn Entity>>,
    pub rels: Vec<Arc<dyn Entity>>,
}

/// A value of an expression.
#[derive(Debug, Clone, Default)]
pub enum Value {
    #[default]
    Null,
    Bool(bool),
    Num(f64),
    Str(String),
    List(Vec<Value>),
    Map(BTreeMap<String, Value>),
    Entity(Arc<dyn Entity>),
    Path(Path),
}

impl Value {
    pub fn is_null(&self) -> bool {
        matches!(self, Value::Null)
    }

    /// The value read as a predicate: only true holds (Go's `Truthy`).
    pub fn truthy(&self) -> bool {
        matches!(self, Value::Bool(true))
    }

    /// A stored JSON value as a value of an expression (Go's `Normalize`): every number a float.
    pub fn from_json(v: &serde_json::Value) -> Value {
        match v {
            serde_json::Value::Null => Value::Null,
            serde_json::Value::Bool(b) => Value::Bool(*b),
            serde_json::Value::Number(n) => Value::Num(n.as_f64().unwrap_or_default()),
            serde_json::Value::String(s) => Value::Str(s.clone()),
            serde_json::Value::Array(xs) => Value::List(xs.iter().map(Value::from_json).collect()),
            serde_json::Value::Object(m) => {
                Value::Map(m.iter().map(|(k, v)| (k.clone(), Value::from_json(v))).collect())
            }
        }
    }
}

/// An expression that cannot be computed: an unknown variable, an operator on values of the wrong type.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct EvalError(pub String);

impl fmt::Display for EvalError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for EvalError {}

fn err<T, E: From<EvalError>>(msg: String) -> Result<T, E> {
    Err(E::from(EvalError(msg)))
}

/// A future of an evaluation.
pub type EvalFuture<'a, T, E> = Pin<Box<dyn Future<Output = Result<T, E>> + Send + 'a>>;

/// What an expression reads its variables from, and matches its EXISTS in.
pub trait Scope: Send {
    type Error: From<EvalError> + Send;

    fn lookup(&self, name: &str) -> Option<Value>;

    /// Matches the patterns of an EXISTS; a scope with no graph refuses it.
    fn exists<'a>(&'a mut self, x: &'a Exists) -> EvalFuture<'a, bool, Self::Error> {
        let _ = x;
        Box::pin(async { err("EXISTS and patterns are read in MATCH … WHERE and RETURN only".to_string()) })
    }
}

/// A scope over a map of variables: no EXISTS.
pub type MapScope = BTreeMap<String, Value>;

impl Scope for MapScope {
    type Error = EvalError;

    fn lookup(&self, name: &str) -> Option<Value> {
        self.get(name).cloned()
    }
}

/// Evaluates an expression in a scope that matches nothing: its future never waits.
pub fn eval_now(e: &Expr, s: &mut MapScope) -> Result<Value, EvalError> {
    let mut fut = eval(e, s);
    let waker = std::task::Waker::noop();
    let mut cx = std::task::Context::from_waker(waker);
    match fut.as_mut().poll(&mut cx) {
        std::task::Poll::Ready(v) => v,
        std::task::Poll::Pending => unreachable!("a map scope never waits"),
    }
}

/// Computes an expression in a scope (Go's `Eval`). An aggregate is an error here: RETURN computes them.
pub fn eval<'a, S: Scope>(e: &'a Expr, s: &'a mut S) -> EvalFuture<'a, Value, S::Error> {
    Box::pin(async move {
        match e {
            Expr::Lit(v) => Ok(v.clone()),
            Expr::Exists(x) => Ok(Value::Bool(s.exists(x).await?)),
            Expr::Var(name) => match s.lookup(name) {
                Some(v) => Ok(v),
                None => err(format!("variable {name} is not defined")),
            },
            Expr::Prop(x, key) => {
                let v = eval(x, s).await?;
                Ok(property(&v, key)?)
            }
            Expr::Index(x, i) => {
                let v = eval(x, s).await?;
                let i = eval(i, s).await?;
                Ok(index(&v, &i)?)
            }
            Expr::Slice(x, from, to) => {
                let v = eval(x, s).await?;
                if v.is_null() {
                    return Ok(Value::Null);
                }
                let Value::List(list) = v else {
                    return err(format!("slice of {}", type_name(&v)));
                };
                let from = bound(from.as_deref(), 0, list.len(), s).await?;
                let to = bound(to.as_deref(), list.len(), list.len(), s).await?;
                if from >= to {
                    return Ok(Value::List(Vec::new()));
                }
                Ok(Value::List(list[from..to].to_vec()))
            }
            Expr::Unary(op, x) => {
                let v = eval(x, s).await?;
                match (op, v) {
                    (_, Value::Null) => Ok(Value::Null),
                    (Unary::Not, Value::Bool(b)) => Ok(Value::Bool(!b)),
                    (Unary::Not, v) => err(format!("NOT of {}", type_name(&v))),
                    (Unary::Neg, Value::Num(f)) => Ok(Value::Num(-f)),
                    (Unary::Neg, v) => err(format!("minus of {}", type_name(&v))),
                }
            }
            Expr::Binary(op, l, r) => {
                let l = eval(l, s).await?;
                // three-valued logic: a known side may decide alone
                match (op, &l) {
                    (Op::And, Value::Bool(false)) => return Ok(Value::Bool(false)),
                    (Op::Or, Value::Bool(true)) => return Ok(Value::Bool(true)),
                    _ => {}
                }
                let r = eval(r, s).await?;
                Ok(binary(*op, l, r)?)
            }
            Expr::IsNull(x, not) => {
                let v = eval(x, s).await?;
                Ok(Value::Bool(v.is_null() != *not))
            }
            Expr::HasLabels(x, labels) => {
                let v = eval(x, s).await?;
                match v {
                    Value::Null => Ok(Value::Null),
                    Value::Entity(en) => Ok(Value::Bool(labels.iter().all(|l| has_label(en.as_ref(), l)))),
                    v => err(format!("labels of {}", type_name(&v))),
                }
            }
            Expr::List(items) => {
                let mut out = Vec::with_capacity(items.len());
                for it in items {
                    out.push(eval(it, s).await?);
                }
                Ok(Value::List(out))
            }
            Expr::Map(kv) => {
                let mut out = BTreeMap::new();
                for (k, v) in kv {
                    let v = eval(v, s).await?;
                    out.insert(k.clone(), v);
                }
                Ok(Value::Map(out))
            }
            Expr::Case(c) => eval_case(c, s).await,
            Expr::Call(c) => eval_call(c, s).await,
        }
    })
}

/// A bound of a slice: within the list, from its end when negative.
async fn bound<S: Scope>(e: Option<&Expr>, def: usize, len: usize, s: &mut S) -> Result<usize, S::Error> {
    let Some(e) = e else {
        return Ok(def);
    };
    let b = eval(e, s).await?;
    let Value::Num(f) = b else {
        return err(format!("slice bound {}", type_name(&b)));
    };
    let mut k = f as i64;
    if k < 0 {
        k += len as i64;
    }
    Ok(k.clamp(0, len as i64) as usize)
}

async fn eval_case<S: Scope>(c: &Case, s: &mut S) -> Result<Value, S::Error> {
    let subject = match &c.subject {
        Some(x) => Some(eval(x, s).await?),
        None => None,
    };
    for (w, t) in c.whens.iter().zip(&c.thens) {
        let v = eval(w, s).await?;
        let hit = match &subject {
            Some(sub) => equal(sub, &v).truthy(),
            None => v.truthy(),
        };
        if hit {
            return eval(t, s).await;
        }
    }
    match &c.otherwise {
        Some(e) => eval(e, s).await,
        None => Ok(Value::Null),
    }
}

async fn eval_call<S: Scope>(c: &Call, s: &mut S) -> Result<Value, S::Error> {
    if AGGREGATES.contains(&c.name.as_str()) {
        return err(format!("{}() is an aggregate: it belongs in RETURN", c.name));
    }
    let mut args = Vec::with_capacity(c.args.len());
    for a in &c.args {
        args.push(eval(a, s).await?);
    }
    let Some(f) = function(&c.name) else {
        return err(format!("unknown function {}", c.name));
    };
    if args.len() < f.min || f.max.is_some_and(|m| args.len() > m) {
        return err(format!("{}() takes {}", c.name, f.arity()));
    }
    Ok((f.call)(&args)?)
}

/// Whether an entity holds a label: as written, or, for a label naming no namespace, the label in any namespace
/// (`Requirement` matches `alm@Requirement`).
pub fn has_label(en: &dyn Entity, label: &str) -> bool {
    en.labels().iter().any(|l| label_matches(l, label))
}

/// Whether a qualified type matches a label of a query: the same, or the same name when the label names no
/// namespace.
pub fn label_matches(typ: &str, label: &str) -> bool {
    if typ == label {
        return true;
    }
    if label.contains('@') {
        return false;
    }
    typ.split_once('@').is_some_and(|(_, name)| name == label)
}

fn property(v: &Value, key: &str) -> Result<Value, EvalError> {
    match v {
        Value::Null => Ok(Value::Null),
        Value::Entity(en) => Ok(en.property(key).unwrap_or_default()),
        Value::Map(m) => Ok(m.get(key).cloned().unwrap_or_default()),
        v => Err(EvalError(format!("property {key} of {}", type_name(v)))),
    }
}

fn index(v: &Value, i: &Value) -> Result<Value, EvalError> {
    if v.is_null() || i.is_null() {
        return Ok(Value::Null);
    }
    match v {
        Value::List(xs) => {
            let Value::Num(n) = i else {
                return Err(EvalError(format!("index of a list by {}", type_name(i))));
            };
            let mut k = *n as i64;
            if k < 0 {
                k += xs.len() as i64;
            }
            if k < 0 || k >= xs.len() as i64 {
                return Ok(Value::Null);
            }
            Ok(xs[k as usize].clone())
        }
        Value::Map(m) => {
            let Value::Str(k) = i else {
                return Err(EvalError(format!("key of a map by {}", type_name(i))));
            };
            Ok(m.get(k).cloned().unwrap_or_default())
        }
        Value::Entity(_) => {
            let Value::Str(k) = i else {
                return Err(EvalError(format!("property by {}", type_name(i))));
            };
            property(v, k)
        }
        v => Err(EvalError(format!("index of {}", type_name(v)))),
    }
}

fn binary(op: Op, l: Value, r: Value) -> Result<Value, EvalError> {
    match op {
        Op::And | Op::Or | Op::Xor => logic(op, &l, &r),
        Op::Eq => Ok(equal(&l, &r)),
        Op::Ne => Ok(match equal(&l, &r) {
            Value::Bool(b) => Value::Bool(!b),
            v => v,
        }),
        Op::Lt | Op::Gt | Op::Le | Op::Ge => {
            if l.is_null() || r.is_null() {
                return Ok(Value::Null);
            }
            let Some(c) = compare(&l, &r) else {
                return Ok(Value::Null);
            };
            Ok(Value::Bool(match op {
                Op::Lt => c.is_lt(),
                Op::Gt => c.is_gt(),
                Op::Le => c.is_le(),
                _ => c.is_ge(),
            }))
        }
        Op::StartsWith | Op::EndsWith | Op::Contains => {
            let (Value::Str(ls), Value::Str(rs)) = (&l, &r) else {
                return Ok(Value::Null);
            };
            Ok(Value::Bool(match op {
                Op::StartsWith => ls.starts_with(rs.as_str()),
                Op::EndsWith => ls.ends_with(rs.as_str()),
                _ => ls.contains(rs.as_str()),
            }))
        }
        Op::In => {
            if r.is_null() {
                return Ok(Value::Null);
            }
            let Value::List(list) = &r else {
                return Err(EvalError(format!("IN {}: a list is expected", type_name(&r))));
            };
            let mut out = Value::Bool(false);
            for it in list {
                match equal(&l, it) {
                    Value::Bool(true) => return Ok(Value::Bool(true)),
                    Value::Null => out = Value::Null,
                    _ => {}
                }
            }
            Ok(out)
        }
        Op::Add => {
            if l.is_null() || r.is_null() {
                return Ok(Value::Null);
            }
            match (l, r) {
                (Value::Num(a), Value::Num(b)) => Ok(Value::Num(a + b)),
                (Value::Str(a), r) => Ok(Value::Str(a + &text(&r))),
                (Value::List(mut a), Value::List(b)) => {
                    a.extend(b);
                    Ok(Value::List(a))
                }
                (Value::List(mut a), r) => {
                    a.push(r);
                    Ok(Value::List(a))
                }
                (l, Value::Str(b)) => Ok(Value::Str(text(&l) + &b)),
                (l, r) => Err(EvalError(format!("{} + {}", type_name(&l), type_name(&r)))),
            }
        }
        Op::Sub | Op::Mul | Op::Div | Op::Mod | Op::Pow => {
            if l.is_null() || r.is_null() {
                return Ok(Value::Null);
            }
            let (Value::Num(a), Value::Num(b)) = (&l, &r) else {
                return Err(EvalError(format!(
                    "{} {} {}",
                    type_name(&l),
                    op.as_str(),
                    type_name(&r)
                )));
            };
            let (a, b) = (*a, *b);
            Ok(Value::Num(match op {
                Op::Sub => a - b,
                Op::Mul => a * b,
                Op::Div => {
                    if b == 0.0 {
                        return Err(EvalError("division by zero".into()));
                    }
                    a / b
                }
                Op::Mod => {
                    if b == 0.0 {
                        return Err(EvalError("modulo by zero".into()));
                    }
                    a % b
                }
                _ => a.powf(b),
            }))
        }
    }
}

fn logic(op: Op, l: &Value, r: &Value) -> Result<Value, EvalError> {
    let lb = match l {
        Value::Bool(b) => Some(*b),
        Value::Null => None,
        _ => {
            return Err(EvalError(format!(
                "{} of {} and {}",
                op.as_str(),
                type_name(l),
                type_name(r)
            )));
        }
    };
    let rb = match r {
        Value::Bool(b) => Some(*b),
        Value::Null => None,
        _ => {
            return Err(EvalError(format!(
                "{} of {} and {}",
                op.as_str(),
                type_name(l),
                type_name(r)
            )));
        }
    };
    Ok(match op {
        Op::And => match (lb, rb) {
            (Some(false), _) | (_, Some(false)) => Value::Bool(false),
            (Some(true), Some(true)) => Value::Bool(true),
            _ => Value::Null,
        },
        Op::Or => match (lb, rb) {
            (Some(true), _) | (_, Some(true)) => Value::Bool(true),
            (Some(false), Some(false)) => Value::Bool(false),
            _ => Value::Null,
        },
        _ => match (lb, rb) {
            (Some(a), Some(b)) => Value::Bool(a != b),
            _ => Value::Null,
        },
    })
}

/// `=`: null when a side is null.
fn equal(l: &Value, r: &Value) -> Value {
    if l.is_null() || r.is_null() {
        return Value::Null;
    }
    Value::Bool(same(l, r))
}

/// Whether two values are the same (an entity by identity; Go's `Equal`).
pub fn same(l: &Value, r: &Value) -> bool {
    match (l, r) {
        (Value::Null, Value::Null) => true,
        (Value::Bool(a), Value::Bool(b)) => a == b,
        (Value::Num(a), Value::Num(b)) => a == b,
        (Value::Str(a), Value::Str(b)) => a == b,
        (Value::Entity(a), Value::Entity(b)) => a.kind() == b.kind() && a.id() == b.id(),
        (Value::List(a), Value::List(b)) => a.len() == b.len() && a.iter().zip(b).all(|(x, y)| same(x, y)),
        (Value::Map(a), Value::Map(b)) => {
            a.len() == b.len() && a.iter().all(|(k, v)| same(v, b.get(k).unwrap_or(&Value::Null)))
        }
        (Value::Path(_), Value::Path(_)) => key(l) == key(r),
        _ => false,
    }
}

/// Orders two values of one kind (numbers, strings, booleans); None for values that do not compare.
pub fn compare(l: &Value, r: &Value) -> Option<std::cmp::Ordering> {
    use std::cmp::Ordering;
    match (l, r) {
        (Value::Num(a), Value::Num(b)) => Some(if a < b {
            Ordering::Less
        } else if a > b {
            Ordering::Greater
        } else {
            Ordering::Equal
        }),
        (Value::Str(a), Value::Str(b)) => Some(a.as_bytes().cmp(b.as_bytes())),
        (Value::Bool(a), Value::Bool(b)) => Some(a.cmp(b)),
        _ => None,
    }
}

/// The total order of ORDER BY (Go's `Order`): null last, then by kind (maps, entities, paths, lists, strings,
/// booleans, numbers), then within a kind.
pub fn order(l: &Value, r: &Value) -> std::cmp::Ordering {
    let rank = |v: &Value| match v {
        Value::Map(_) => 0,
        Value::Entity(_) => 1,
        Value::Path(_) => 2,
        Value::List(_) => 3,
        Value::Str(_) => 4,
        Value::Bool(_) => 5,
        Value::Num(_) => 6,
        Value::Null => 8,
    };
    let (a, b) = (rank(l), rank(r));
    if a != b {
        return a.cmp(&b);
    }
    if let Some(c) = compare(l, r) {
        return c;
    }
    key(l).as_bytes().cmp(key(r).as_bytes())
}

/// A text identifying a value, for DISTINCT and grouping (Go's `Key`).
pub fn key(v: &Value) -> String {
    match v {
        Value::Null => "null".into(),
        Value::Entity(e) => format!("{}:{}", e.kind(), e.id()),
        Value::Path(p) => {
            let mut b = String::from("path:");
            for (i, n) in p.nodes.iter().enumerate() {
                b.push_str(n.id());
                if let Some(r) = p.rels.get(i) {
                    b.push('|');
                    b.push_str(r.id());
                    b.push('|');
                }
            }
            b
        }
        Value::List(xs) => format!("[{}]", xs.iter().map(key).collect::<Vec<_>>().join(",")),
        Value::Map(m) => format!(
            "{{{}}}",
            m.iter()
                .map(|(k, v)| format!("{}:{}", go_quote(k), key(v)))
                .collect::<Vec<_>>()
                .join(",")
        ),
        Value::Str(s) => go_quote(s),
        Value::Bool(b) => b.to_string(),
        Value::Num(f) => go_float_v(*f),
    }
}

/// A string quoted as Go's `strconv.Quote` does.
fn go_quote(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{7}' => out.push_str("\\a"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{b}' => out.push_str("\\v"),
            c if (c as u32) < 0x20 || c as u32 == 0x7f => out.push_str(&format!("\\x{:02x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// A float as Go's `%v` prints it: the shortest digits, an exponent below 1e-4 and from 1e6.
fn go_float_v(f: f64) -> String {
    if f.is_nan() {
        return "NaN".into();
    }
    if f.is_infinite() {
        return if f > 0.0 { "+Inf".into() } else { "-Inf".into() };
    }
    if f == 0.0 {
        return if f.is_sign_negative() { "-0".into() } else { "0".into() };
    }
    let sci = format!("{f:e}");
    let (mant, exp) = sci.split_once('e').unwrap_or((&sci, "0"));
    let exp: i32 = exp.parse().unwrap_or(0);
    if !(-4..6).contains(&exp) {
        let sign = if exp < 0 { '-' } else { '+' };
        return format!("{mant}e{sign}{:02}", exp.abs());
    }
    format!("{f}")
}

/// A value as toString renders it (Go's `Text`).
pub fn text(v: &Value) -> String {
    match v {
        Value::Null => String::new(),
        Value::Str(s) => s.clone(),
        Value::Num(f) => {
            if f.is_infinite() {
                if *f > 0.0 { "+Inf".into() } else { "-Inf".into() }
            } else {
                format!("{f}")
            }
        }
        Value::Bool(b) => b.to_string(),
        v => key(v),
    }
}

/// The name of the kind of a value, as the errors say it.
pub fn type_name(v: &Value) -> String {
    match v {
        Value::Null => "null".into(),
        Value::Bool(_) => "a boolean".into(),
        Value::Num(_) => "a number".into(),
        Value::Str(_) => "a string".into(),
        Value::List(_) => "a list".into(),
        Value::Map(_) => "a map".into(),
        Value::Path(_) => "a path".into(),
        Value::Entity(e) => format!("a {}", e.kind()),
    }
}

/// Computes an aggregating call over the values its argument took in a group (`count(*)`: one per row, the values
/// unused).
pub fn aggregate(c: &Call, values: &[Value]) -> Result<Value, EvalError> {
    if c.star {
        return Ok(Value::Num(values.len() as f64));
    }
    let mut kept: Vec<&Value> = Vec::new();
    let mut seen = std::collections::HashSet::new();
    for v in values {
        if v.is_null() {
            continue;
        }
        if c.distinct && !seen.insert(key(v)) {
            continue;
        }
        kept.push(v);
    }
    match c.name.as_str() {
        "count" => Ok(Value::Num(kept.len() as f64)),
        "collect" => Ok(Value::List(kept.into_iter().cloned().collect())),
        "sum" | "avg" => {
            let mut sum = 0.0;
            for v in &kept {
                let Value::Num(f) = v else {
                    return Err(EvalError(format!("{}() of {}", c.name, type_name(v))));
                };
                sum += f;
            }
            if c.name == "avg" {
                if kept.is_empty() {
                    return Ok(Value::Null);
                }
                return Ok(Value::Num(sum / kept.len() as f64));
            }
            Ok(Value::Num(sum))
        }
        "min" | "max" => {
            let mut out: Option<&Value> = None;
            for v in kept {
                match out {
                    None => out = Some(v),
                    Some(o) => {
                        let cmp = order(v, o);
                        if (c.name == "min" && cmp.is_lt()) || (c.name == "max" && cmp.is_gt()) {
                            out = Some(v);
                        }
                    }
                }
            }
            Ok(out.cloned().unwrap_or_default())
        }
        _ => Err(EvalError(format!("unknown aggregate {}", c.name))),
    }
}

/// A scalar function of the subset.
pub struct Function {
    min: usize,
    /// None: any number of arguments
    max: Option<usize>,
    call: fn(&[Value]) -> Result<Value, EvalError>,
}

impl Function {
    fn arity(&self) -> String {
        match self.max {
            None => format!("at least {} argument(s)", self.min),
            Some(m) if m == self.min => format!("{} argument(s)", self.min),
            Some(m) => format!("{} to {m} arguments", self.min),
        }
    }
}

/// The scalar functions of the subset, by their lowercase name.
pub fn function(name: &str) -> Option<Function> {
    let one = |call: fn(&[Value]) -> Result<Value, EvalError>| {
        Some(Function {
            min: 1,
            max: Some(1),
            call,
        })
    };
    match name {
        "id" => one(|a| entity_fn("id", &a[0], |e| Value::Str(e.id().to_string()))),
        "type" => one(|a| match &a[0] {
            Value::Null => Ok(Value::Null),
            Value::Entity(e) if e.kind() == "relationship" => {
                Ok(Value::Str(e.labels().into_iter().next().unwrap_or_default()))
            }
            v => Err(EvalError(format!("type() of {}", type_name(v)))),
        }),
        "labels" => one(|a| {
            entity_fn("labels", &a[0], |e| {
                Value::List(e.labels().into_iter().map(Value::Str).collect())
            })
        }),
        "keys" => one(|a| {
            entity_fn("keys", &a[0], |e| {
                Value::List(e.properties().into_keys().map(Value::Str).collect())
            })
        }),
        "properties" => one(|a| entity_fn("properties", &a[0], |e| Value::Map(e.properties()))),
        "nodes" => one(|a| match &a[0] {
            Value::Null => Ok(Value::Null),
            Value::Path(p) => Ok(Value::List(p.nodes.iter().cloned().map(Value::Entity).collect())),
            v => Err(EvalError(format!("nodes() of {}", type_name(v)))),
        }),
        "relationships" => one(|a| match &a[0] {
            Value::Null => Ok(Value::Null),
            Value::Path(p) => Ok(Value::List(p.rels.iter().cloned().map(Value::Entity).collect())),
            Value::List(xs) => Ok(Value::List(xs.clone())),
            v => Err(EvalError(format!("relationships() of {}", type_name(v)))),
        }),
        "length" => one(|a| match &a[0] {
            Value::Null => Ok(Value::Null),
            Value::Path(p) => Ok(Value::Num(p.rels.len() as f64)),
            Value::List(xs) => Ok(Value::Num(xs.len() as f64)),
            Value::Str(s) => Ok(Value::Num(s.chars().count() as f64)),
            v => Err(EvalError(format!("length() of {}", type_name(v)))),
        }),
        "size" => one(|a| match &a[0] {
            Value::Null => Ok(Value::Null),
            Value::List(xs) => Ok(Value::Num(xs.len() as f64)),
            Value::Str(s) => Ok(Value::Num(s.chars().count() as f64)),
            Value::Map(m) => Ok(Value::Num(m.len() as f64)),
            v => Err(EvalError(format!("size() of {}", type_name(v)))),
        }),
        "head" => one(|a| match &a[0] {
            Value::Null => Ok(Value::Null),
            Value::List(xs) => Ok(xs.first().cloned().unwrap_or_default()),
            v => Err(EvalError(format!("head() of {}", type_name(v)))),
        }),
        "last" => one(|a| match &a[0] {
            Value::Null => Ok(Value::Null),
            Value::List(xs) => Ok(xs.last().cloned().unwrap_or_default()),
            v => Err(EvalError(format!("last() of {}", type_name(v)))),
        }),
        "tolower" => one(|a| str_fn("toLower", &a[0], |s| s.to_lowercase())),
        "toupper" => one(|a| str_fn("toUpper", &a[0], |s| s.to_uppercase())),
        "trim" => one(|a| str_fn("trim", &a[0], |s| s.trim_matches(go_space).to_string())),
        "tostring" => one(|a| match &a[0] {
            Value::Null => Ok(Value::Null),
            v => Ok(Value::Str(text(v))),
        }),
        "tointeger" => one(|a| {
            Ok(match &a[0] {
                Value::Num(f) => Value::Num(f.trunc()),
                Value::Str(s) => {
                    go_parse_float(s.trim_matches(go_space)).map_or(Value::Null, |f| Value::Num(f.trunc()))
                }
                _ => Value::Null,
            })
        }),
        "tofloat" => one(|a| {
            Ok(match &a[0] {
                Value::Num(f) => Value::Num(*f),
                Value::Str(s) => go_parse_float(s.trim_matches(go_space)).map_or(Value::Null, Value::Num),
                _ => Value::Null,
            })
        }),
        "abs" => one(|a| match &a[0] {
            Value::Null => Ok(Value::Null),
            Value::Num(f) => Ok(Value::Num(f.abs())),
            v => Err(EvalError(format!("abs() of {}", type_name(v)))),
        }),
        "coalesce" => Some(Function {
            min: 1,
            max: None,
            call: |a| Ok(a.iter().find(|v| !v.is_null()).cloned().unwrap_or_default()),
        }),
        _ => None,
    }
}

fn entity_fn(name: &str, v: &Value, f: impl Fn(&dyn Entity) -> Value) -> Result<Value, EvalError> {
    match v {
        Value::Null => Ok(Value::Null),
        Value::Entity(e) => Ok(f(e.as_ref())),
        v => Err(EvalError(format!("{name}() of {}", type_name(v)))),
    }
}

fn str_fn(name: &str, v: &Value, f: impl Fn(&str) -> String) -> Result<Value, EvalError> {
    match v {
        Value::Null => Ok(Value::Null),
        Value::Str(s) => Ok(Value::Str(f(s))),
        v => Err(EvalError(format!("{name}() of {}", type_name(v)))),
    }
}

/// The white space Go's `strings.TrimSpace` trims.
fn go_space(c: char) -> bool {
    c.is_whitespace()
}

/// A number as Go's `strconv.ParseFloat` reads it, for the forms a query meets: decimal, with an exponent,
/// `Inf` / `NaN` spelled as Go spells them.
fn go_parse_float(s: &str) -> Option<f64> {
    let lower = s.to_ascii_lowercase();
    let body = lower.trim_start_matches(['+', '-']);
    match body {
        "inf" | "infinity" => {
            return Some(if lower.starts_with('-') {
                f64::NEG_INFINITY
            } else {
                f64::INFINITY
            });
        }
        "nan" => return Some(f64::NAN),
        _ => {}
    }
    if body.starts_with("0x") || body.contains('_') || body.is_empty() {
        return None; // hexadecimal floats and underscores: not read here
    }
    if !body
        .chars()
        .all(|c| c.is_ascii_digit() || matches!(c, '.' | 'e' | '+' | '-'))
    {
        return None;
    }
    s.parse::<f64>().ok()
}

/// Evaluates a list of arguments as Go's tree holds them: a convenience of the tests.
#[cfg(test)]
pub(crate) fn eval_str(src: &str) -> Result<Value, EvalError> {
    let q = crate::parse(&format!("MATCH (n) RETURN {src}")).ok_or_else(|| EvalError("not read".into()))?;
    let mut scope = MapScope::new();
    eval_now(&q.ret.items[0].expr, &mut scope)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn num(src: &str) -> f64 {
        match eval_str(src).unwrap() {
            Value::Num(f) => f,
            v => panic!("{src}: {v:?}"),
        }
    }

    #[test]
    fn evaluates_as_go_does() {
        assert_eq!(num("1 + 2 * 3 ^ 2"), 19.0);
        assert_eq!(num("size('été')"), 3.0);
        assert_eq!(num("toInteger(' 3.7 ')"), 3.0);
        assert!(matches!(eval_str("null AND false").unwrap(), Value::Bool(false)));
        assert!(eval_str("null OR false").unwrap().is_null());
        assert!(matches!(eval_str("2 IN [1, null, 2]").unwrap(), Value::Bool(true)));
        assert!(eval_str("3 IN [1, null]").unwrap().is_null());
        assert!(matches!(eval_str("'a' + 1.5").unwrap(), Value::Str(s) if s == "a1.5"));
        assert!(matches!(eval_str("[1, 2, 3][-1]").unwrap(), Value::Num(f) if f == 3.0));
        assert!(matches!(eval_str("1 < 2 < 3").unwrap(), Value::Bool(true)));
        assert!(matches!(eval_str("CASE 2 WHEN 1 THEN 'a' WHEN 2 THEN 'b' END").unwrap(), Value::Str(s) if s == "b"));
        assert_eq!(eval_str("1 / 0").unwrap_err().0, "division by zero");
        assert_eq!(
            eval_str("count(1)").unwrap_err().0,
            "count() is an aggregate: it belongs in RETURN"
        );
        assert_eq!(key(&Value::Num(1e21)), "1e+21");
        assert_eq!(key(&Value::Num(1234567.0)), "1.234567e+06");
        assert_eq!(key(&Value::Num(123456.0)), "123456");
        assert_eq!(key(&Value::Num(0.0001)), "0.0001");
        assert_eq!(key(&Value::Num(0.00001)), "1e-05");
    }
}
