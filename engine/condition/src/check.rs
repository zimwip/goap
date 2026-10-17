//! The compile-time checks cel-go's type checker made and the `cel` crate does not: every identifier is a declared
//! variable (or a variable of an enclosing comprehension, or the namespace of a function), and the type of the result,
//! inferred structurally, is the one asked for. What a declared `dyn` (a field of a map of `dyn`, an element of a list
//! of `dyn`) leads to stays `dyn` and is accepted, as cel-go accepts it.
use std::fmt;

use cel::common::ast::{Expr, IdedExpr, LiteralValue, SourceInfo, operators};

/// The type of an expression as far as it can be decided structurally.
#[derive(Debug, Clone, PartialEq)]
pub(crate) enum Ty {
    Dyn,
    Bool,
    Int,
    Uint,
    Double,
    String,
    Bytes,
    Null,
    Type,
    Timestamp,
    Duration,
    List(Box<Ty>),
    Map(Box<Ty>, Box<Ty>),
}

impl Ty {
    pub(crate) fn list() -> Ty {
        Ty::List(Box::new(Ty::Dyn))
    }

    pub(crate) fn map() -> Ty {
        Ty::Map(Box::new(Ty::String), Box::new(Ty::Dyn))
    }

    pub(crate) fn map_of(v: Ty) -> Ty {
        Ty::Map(Box::new(Ty::String), Box::new(v))
    }
}

impl fmt::Display for Ty {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Ty::Dyn => write!(f, "dyn"),
            Ty::Bool => write!(f, "bool"),
            Ty::Int => write!(f, "int"),
            Ty::Uint => write!(f, "uint"),
            Ty::Double => write!(f, "double"),
            Ty::String => write!(f, "string"),
            Ty::Bytes => write!(f, "bytes"),
            Ty::Null => write!(f, "null_type"),
            Ty::Type => write!(f, "type"),
            Ty::Timestamp => write!(f, "google.protobuf.Timestamp"),
            Ty::Duration => write!(f, "google.protobuf.Duration"),
            Ty::List(e) => write!(f, "list({e})"),
            Ty::Map(k, v) => write!(f, "map({k}, {v})"),
        }
    }
}

/// The namespaces of the functions of the environment (`sets.contains(...)` names no variable `sets`).
const NAMESPACES: [&str; 7] = [
    "sets", "lists", "strings", "math", "cel", "base64", "optional",
];

/// The names of the types, identifiers of every environment (`type(x) == int`).
const TYPE_NAMES: [&str; 11] = [
    "int",
    "uint",
    "double",
    "bool",
    "string",
    "bytes",
    "list",
    "map",
    "null_type",
    "type",
    "dyn",
];

/// Checks an expression against the declared variables, returning the type of its result or the message of cel-go's
/// checker for the first undeclared reference.
pub(crate) fn check(
    expr: &IdedExpr,
    info: &SourceInfo,
    decls: &[(&str, Ty)],
) -> Result<Ty, String> {
    let mut c = Checker {
        decls,
        info,
        scopes: Vec::new(),
    };
    c.ty(expr)
}

struct Checker<'a> {
    decls: &'a [(&'a str, Ty)],
    info: &'a SourceInfo,
    scopes: Vec<(String, Ty)>,
}

impl Checker<'_> {
    fn ident(&self, e: &IdedExpr, name: &str) -> Result<Ty, String> {
        if let Some((_, t)) = self.scopes.iter().rev().find(|(n, _)| n == name) {
            return Ok(t.clone());
        }
        if let Some((_, t)) = self.decls.iter().find(|(n, _)| *n == name) {
            return Ok(t.clone());
        }
        if name.starts_with('@') {
            return Ok(Ty::Dyn);
        }
        if TYPE_NAMES.contains(&name) {
            return Ok(Ty::Type);
        }
        Err(self.undeclared(e.id, name))
    }

    fn undeclared(&self, id: u64, name: &str) -> String {
        let msg = format!("undeclared reference to '{name}' (in container '')");
        error_at(self.info, id, &msg)
    }

    fn ty(&mut self, e: &IdedExpr) -> Result<Ty, String> {
        match &e.expr {
            Expr::Unspecified => Ok(Ty::Dyn),
            Expr::Ident(name) => self.ident(e, name),
            Expr::Literal(l) => Ok(match l {
                LiteralValue::Boolean(_) => Ty::Bool,
                LiteralValue::Bytes(_) => Ty::Bytes,
                LiteralValue::Double(_) => Ty::Double,
                LiteralValue::Int(_) => Ty::Int,
                LiteralValue::Null => Ty::Null,
                LiteralValue::String(_) => Ty::String,
                LiteralValue::UInt(_) => Ty::Uint,
            }),
            Expr::Select(s) => {
                let t = self.ty(&s.operand)?;
                if s.test {
                    return Ok(Ty::Bool);
                }
                Ok(match t {
                    Ty::Map(_, v) => *v,
                    _ => Ty::Dyn,
                })
            }
            Expr::List(l) => {
                let mut elem: Option<Ty> = None;
                for x in &l.elements {
                    let t = self.ty(x)?;
                    elem = Some(match elem {
                        None => t,
                        Some(p) if p == t => p,
                        Some(_) => Ty::Dyn,
                    });
                }
                Ok(Ty::List(Box::new(elem.unwrap_or(Ty::Dyn))))
            }
            Expr::Map(m) => {
                let (mut kt, mut vt): (Option<Ty>, Option<Ty>) = (None, None);
                for entry in &m.entries {
                    if let cel::common::ast::EntryExpr::MapEntry(me) = &entry.expr {
                        let k = self.ty(&me.key)?;
                        let v = self.ty(&me.value)?;
                        kt = Some(unify(kt, k));
                        vt = Some(unify(vt, v));
                    }
                }
                Ok(Ty::Map(
                    Box::new(kt.unwrap_or(Ty::Dyn)),
                    Box::new(vt.unwrap_or(Ty::Dyn)),
                ))
            }
            Expr::Struct(s) => {
                for entry in &s.entries {
                    if let cel::common::ast::EntryExpr::StructField(f) = &entry.expr {
                        self.ty(&f.value)?;
                    }
                }
                Ok(Ty::Dyn)
            }
            Expr::Comprehension(c) => {
                let range = self.ty(&c.iter_range)?;
                let (it, it2) = match &range {
                    Ty::List(e) => (Ty::Int, (**e).clone()),
                    Ty::Map(k, v) => ((**k).clone(), (**v).clone()),
                    _ => (Ty::Dyn, Ty::Dyn),
                };
                let accu = self.ty(&c.accu_init)?;
                let depth = self.scopes.len();
                match &c.iter_var2 {
                    Some(v2) => {
                        self.scopes.push((c.iter_var.clone(), it));
                        self.scopes.push((v2.clone(), it2));
                    }
                    None => {
                        let one = match &range {
                            Ty::List(e) => (**e).clone(),
                            Ty::Map(k, _) => (**k).clone(),
                            _ => Ty::Dyn,
                        };
                        self.scopes.push((c.iter_var.clone(), one));
                    }
                }
                self.scopes.push((c.accu_var.clone(), accu));
                let out = (|| {
                    self.ty(&c.loop_cond)?;
                    self.ty(&c.loop_step)?;
                    self.ty(&c.result)
                })();
                self.scopes.truncate(depth);
                out
            }
            Expr::Call(call) => self.call(call),
        }
    }

    fn call(&mut self, call: &cel::common::ast::CallExpr) -> Result<Ty, String> {
        let name = call.func_name.as_str();
        let mut target = None;
        if let Some(t) = &call.target {
            // `sets.contains(...)`: a function of a namespace, not a member of a variable
            if let Expr::Ident(ns) = &t.expr
                && NAMESPACES.contains(&ns.as_str())
                && self.ident(t, ns).is_err()
            {
                target = None;
            } else {
                target = Some(self.ty(t)?);
            }
        }
        let mut args = Vec::with_capacity(call.args.len());
        for a in &call.args {
            args.push(self.ty(a)?);
        }
        Ok(result_of(name, target.as_ref(), &args))
    }
}

fn unify(prev: Option<Ty>, t: Ty) -> Ty {
    match prev {
        None => t,
        Some(p) if p == t => p,
        Some(_) => Ty::Dyn,
    }
}

fn numeric(t: &Ty) -> bool {
    matches!(t, Ty::Int | Ty::Uint | Ty::Double)
}

/// The type a function returns given the types of its target and arguments.
fn result_of(name: &str, target: Option<&Ty>, args: &[Ty]) -> Ty {
    match name {
        operators::LOGICAL_AND
        | operators::LOGICAL_OR
        | operators::LOGICAL_NOT
        | operators::EQUALS
        | operators::NOT_EQUALS
        | operators::LESS
        | operators::LESS_EQUALS
        | operators::GREATER
        | operators::GREATER_EQUALS
        | operators::IN
        | operators::NOT_STRICTLY_FALSE
        | "startsWith"
        | "endsWith"
        | "contains"
        | "containsAll"
        | "matches"
        | "equivalent"
        | "intersects"
        | "has"
        | "exists"
        | "all"
        | "exists_one"
        | "existsOne"
        | "bool"
        | "hasValue" => Ty::Bool,
        "size" | "indexOf" | "lastIndexOf" | "int" | "getFullYear" | "getMonth"
        | "getDayOfYear" | "getDayOfMonth" | "getDate" | "getDayOfWeek" | "getHours"
        | "getMinutes" | "getSeconds" | "getMilliseconds" => Ty::Int,
        "uint" => Ty::Uint,
        "double" => Ty::Double,
        "string" | "lowerAscii" | "upperAscii" | "trim" | "replace" | "substring" | "charAt"
        | "format" | "join" | "quote" => Ty::String,
        "bytes" => Ty::Bytes,
        "type" => Ty::Type,
        "timestamp" => Ty::Timestamp,
        "duration" => Ty::Duration,
        "split" | "distinct" | "flatten" | "slice" | "sort" | "sortBy" | "range" | "map"
        | "filter" => Ty::list(),
        "reverse" => match target {
            Some(t @ (Ty::String | Ty::List(_))) => t.clone(),
            _ => Ty::Dyn,
        },
        operators::NEGATE => args.first().cloned().unwrap_or(Ty::Dyn),
        operators::ADD
        | operators::SUBSTRACT
        | operators::MULTIPLY
        | operators::DIVIDE
        | operators::MODULO => match (args.first(), args.get(1)) {
            (Some(a), Some(b)) if a == b && (numeric(a) || name == operators::ADD) => a.clone(),
            _ => Ty::Dyn,
        },
        operators::INDEX => match args.first() {
            Some(Ty::List(e)) => (**e).clone(),
            Some(Ty::Map(_, v)) => (**v).clone(),
            _ => Ty::Dyn,
        },
        operators::CONDITIONAL => match (args.get(1), args.get(2)) {
            (Some(a), Some(b)) if a == b => a.clone(),
            _ => Ty::Dyn,
        },
        _ => Ty::Dyn,
    }
}

/// A message located like cel-go's: `ERROR: <input>:line:col: msg`, the line and a caret under the column.
pub(crate) fn error_at(info: &SourceInfo, id: u64, msg: &str) -> String {
    let Some((start, _)) = info.offset_for(id) else {
        return format!("ERROR: <input>:-1:0: {msg}");
    };
    // the offsets count characters
    let chars: Vec<char> = info.source.chars().collect();
    let start = (start as usize).min(chars.len());
    let mut line_start = 0;
    let mut line_no = 1;
    for (i, ch) in chars.iter().enumerate().take(start) {
        if *ch == '\n' {
            line_no += 1;
            line_start = i + 1;
        }
    }
    let col = start - line_start + 1;
    let line: String = chars[line_start..]
        .iter()
        .take_while(|c| **c != '\n')
        .collect();
    format!(
        "ERROR: <input>:{line_no}:{col}: {msg}\n | {line}\n | {}^",
        ".".repeat(col - 1)
    )
}
