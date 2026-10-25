//! Whether cel-go compiles a guard (`guard.Compile`): parses and type-checks it in the guard's environment, its
//! result a bool or dyn. The Rust parser only parses; this checker follows cel-go's typing over the subset guards are
//! written in. It refuses only what surely does not compile, in its own words (ADR 0163: Rust decides); a guard it does
//! not follow is accepted, any error left to its evaluation.
use cel::Program;
use cel::common::ast::operators as op;
use cel::common::ast::{EntryExpr, Expr, IdedExpr, LiteralValue};

/// A CEL type as cel-go's checker infers it, over what guards use.
#[derive(Debug, Clone, PartialEq)]
enum T {
    Bool,
    Int,
    Uint,
    Double,
    String,
    Bytes,
    Null,
    List(Box<T>),
    Map(Box<T>, Box<T>),
    Dyn,
}

/// The variables of the guard environment (`guard.guardEnv`).
fn variable(name: &str) -> Option<T> {
    let map = || T::Map(Box::new(T::String), Box::new(T::Dyn));
    let list = || T::List(Box::new(T::Dyn));
    Some(match name {
        "node" | "change" | "impact" | "draft" | "criticalityPolicy" | "world" | "integrator" | "history" => map(),
        "children" | "decisionPoints" | "actions" | "risks" | "verifications" | "derogations" | "dependencies"
        | "assessments" => list(),
        _ => return None,
    })
}

/// What the checker says of a guard: it compiles, it is refused (Rust's words: a syntax error, or the type of what
/// it returns, as cel-go's `guard must return bool`), or the checker does not follow it.
#[derive(Debug, Clone, PartialEq)]
pub enum Verdict {
    Compiles,
    Refused(String),
    Unknown,
}

/// Judges a guard: refused only when it surely is (it does not parse, or returns a type that is not a bool).
pub fn judge(expr: &str) -> Verdict {
    if expr.is_empty() {
        return Verdict::Compiles;
    }
    let p = match Program::compile(expr) {
        Ok(p) => p,
        Err(e) => return Verdict::Refused(e.to_string()),
    };
    let mut scope = Vec::new();
    match type_of(p.expression(), &mut scope) {
        Some(T::Bool | T::Dyn) => Verdict::Compiles,
        Some(t) => Verdict::Refused(format!("guard must return bool, got {}", t.name())),
        None => Verdict::Unknown,
    }
}

impl T {
    /// The name cel-go prints for the type.
    fn name(&self) -> String {
        match self {
            T::Bool => "bool".into(),
            T::Int => "int".into(),
            T::Uint => "uint".into(),
            T::Double => "double".into(),
            T::String => "string".into(),
            T::Bytes => "bytes".into(),
            T::Null => "null_type".into(),
            T::List(e) => format!("list({})", e.name()),
            T::Map(k, v) => format!("map({}, {})", k.name(), v.name()),
            T::Dyn => "dyn".into(),
        }
    }
}

/// The type both of two types are, as cel-go joins the branches of a conditional or the elements of an aggregate.
fn join(a: &T, b: &T) -> Option<T> {
    match (a, b) {
        (T::Dyn, _) | (_, T::Dyn) => Some(T::Dyn),
        (T::List(x), T::List(y)) => Some(T::List(Box::new(join(x, y)?))),
        (T::Map(k, v), T::Map(l, w)) => Some(T::Map(Box::new(join(k, l)?), Box::new(join(v, w)?))),
        _ if a == b => Some(a.clone()),
        _ => None,
    }
}

/// Whether a value of one type may be compared for equality with one of the other (`_==_(A, A)`).
fn comparable(a: &T, b: &T) -> bool {
    join(a, b).is_some()
}

fn numeric(t: &T) -> bool {
    matches!(t, T::Int | T::Uint | T::Double)
}

fn ordered(t: &T) -> bool {
    matches!(t, T::Int | T::Uint | T::Double | T::String | T::Bytes | T::Bool)
}

fn boolish(t: &T) -> bool {
    matches!(t, T::Bool | T::Dyn)
}

fn stringish(t: &T) -> bool {
    matches!(t, T::String | T::Dyn)
}

fn type_of(e: &IdedExpr, scope: &mut Vec<(String, T)>) -> Option<T> {
    match &e.expr {
        Expr::Literal(l) => Some(match l {
            LiteralValue::Boolean(_) => T::Bool,
            LiteralValue::Int(_) => T::Int,
            LiteralValue::UInt(_) => T::Uint,
            LiteralValue::Double(_) => T::Double,
            LiteralValue::String(_) => T::String,
            LiteralValue::Bytes(_) => T::Bytes,
            LiteralValue::Null => T::Null,
        }),
        Expr::Ident(name) => scope
            .iter()
            .rev()
            .find(|(n, _)| n == name)
            .map(|(_, t)| t.clone())
            .or_else(|| variable(name)),
        Expr::Select(s) => {
            let operand = type_of(&s.operand, scope)?;
            let value = match operand {
                T::Map(k, v) if *k == T::String || *k == T::Dyn => *v,
                T::Dyn => T::Dyn,
                _ => return None,
            };
            Some(if s.test { T::Bool } else { value })
        }
        Expr::List(l) => {
            if !l.optional_indices.is_empty() {
                return None;
            }
            let mut elem: Option<T> = None;
            for x in &l.elements {
                let t = type_of(x, scope)?;
                // a list of mixed elements is a list(dyn): cel-go leaves homogeneous literals off by default
                elem = Some(match elem {
                    None => t,
                    Some(prev) => join(&prev, &t).unwrap_or(T::Dyn),
                });
            }
            Some(T::List(Box::new(elem.unwrap_or(T::Dyn))))
        }
        Expr::Map(m) => {
            let (mut key, mut val): (Option<T>, Option<T>) = (None, None);
            for entry in &m.entries {
                let EntryExpr::MapEntry(x) = &entry.expr else {
                    return None;
                };
                if x.optional {
                    return None;
                }
                let k = type_of(&x.key, scope)?;
                if !matches!(k, T::Int | T::Uint | T::Bool | T::String | T::Dyn) {
                    return None;
                }
                let v = type_of(&x.value, scope)?;
                key = Some(key.map_or(k.clone(), |p| join(&p, &k).unwrap_or(T::Dyn)));
                val = Some(val.map_or(v.clone(), |p| join(&p, &v).unwrap_or(T::Dyn)));
            }
            Some(T::Map(Box::new(key.unwrap_or(T::Dyn)), Box::new(val.unwrap_or(T::Dyn))))
        }
        Expr::Comprehension(c) => {
            let range = type_of(&c.iter_range, scope)?;
            let (first, second) = match range {
                T::List(e) => (T::Int, *e),
                T::Map(k, v) => (*k, *v),
                T::Dyn => (T::Dyn, T::Dyn),
                _ => return None,
            };
            let init = type_of(&c.accu_init, scope)?;
            let base = scope.len();
            match &c.iter_var2 {
                // a one-variable comprehension iterates the elements of a list, the keys of a map
                None => {
                    let v = if matches!(range_kind(&c.iter_range, scope), Some(Kind::List)) {
                        second
                    } else {
                        first
                    };
                    scope.push((c.iter_var.clone(), v));
                }
                Some(v2) => {
                    scope.push((c.iter_var.clone(), first));
                    scope.push((v2.clone(), second));
                }
            }
            scope.push((c.accu_var.clone(), init.clone()));
            let out = (|| {
                if !boolish(&type_of(&c.loop_cond, scope)?) {
                    return None;
                }
                let step = type_of(&c.loop_step, scope)?;
                let accu = join(&init, &step)?;
                let last = scope.len() - 1;
                scope[last].1 = refine(&init, &step).unwrap_or(accu);
                type_of(&c.result, scope)
            })();
            scope.truncate(base);
            out
        }
        Expr::Call(call) => self::call(&call.func_name, call.target.as_deref(), &call.args, scope),
        Expr::Struct(_) | Expr::Unspecified => None,
    }
}

/// The accumulator of a comprehension once its step is known: an empty list literal takes the element type the
/// step gives it (`map`, `filter`).
fn refine(init: &T, step: &T) -> Option<T> {
    match (init, step) {
        (T::List(e), T::List(_)) if **e == T::Dyn => Some(step.clone()),
        _ => None,
    }
}

enum Kind {
    List,
    Other,
}

fn range_kind(e: &IdedExpr, scope: &mut Vec<(String, T)>) -> Option<Kind> {
    Some(match type_of(e, scope)? {
        T::List(_) => Kind::List,
        _ => Kind::Other,
    })
}

fn call(name: &str, target: Option<&IdedExpr>, args: &[IdedExpr], scope: &mut Vec<(String, T)>) -> Option<T> {
    let mut ts = Vec::with_capacity(args.len());
    for a in args {
        ts.push(type_of(a, scope)?);
    }
    let recv = match target {
        Some(t) => Some(type_of(t, scope)?),
        None => None,
    };
    match (name, recv.as_ref(), ts.as_slice()) {
        (op::LOGICAL_AND | op::LOGICAL_OR, None, [a, b]) if boolish(a) && boolish(b) => Some(T::Bool),
        (op::LOGICAL_NOT | op::NOT_STRICTLY_FALSE, None, [a]) if boolish(a) => Some(T::Bool),
        (op::CONDITIONAL, None, [c, a, b]) if boolish(c) => join(a, b),
        (op::EQUALS | op::NOT_EQUALS, None, [a, b]) if comparable(a, b) => Some(T::Bool),
        (op::LESS | op::LESS_EQUALS | op::GREATER | op::GREATER_EQUALS, None, [a, b])
            if (ordered(a) && a == b) || (*a == T::Dyn && ordered_or_dyn(b)) || (*b == T::Dyn && ordered_or_dyn(a)) =>
        {
            Some(T::Bool)
        }
        (op::ADD, None, [a, b]) => arithmetic(a, b, true),
        (op::SUBSTRACT | op::MULTIPLY | op::DIVIDE, None, [a, b]) => arithmetic(a, b, false),
        (op::MODULO, None, [a, b]) => match (a, b) {
            (T::Int, T::Int) | (T::Uint, T::Uint) => Some(a.clone()),
            (T::Dyn, T::Int | T::Uint | T::Dyn) => Some(if *b == T::Dyn { T::Dyn } else { b.clone() }),
            (T::Int | T::Uint, T::Dyn) => Some(a.clone()),
            _ => None,
        },
        (op::NEGATE, None, [a]) => match a {
            T::Int | T::Double | T::Dyn => Some(a.clone()),
            _ => None,
        },
        (op::INDEX, None, [c, i]) => match c {
            T::List(e) if matches!(i, T::Int | T::Uint | T::Dyn) => Some(*e.clone()),
            T::Map(k, v) if comparable(k, i) => Some(*v.clone()),
            T::Dyn => Some(T::Dyn),
            _ => None,
        },
        (op::IN, None, [x, c]) => match c {
            T::List(e) if comparable(x, e) => Some(T::Bool),
            T::Map(k, _) if comparable(x, k) => Some(T::Bool),
            T::Dyn => Some(T::Bool),
            _ => None,
        },
        ("size", None, [a]) | ("size", Some(a), []) if sized(a) => Some(T::Int),
        ("contains" | "startsWith" | "endsWith" | "matches", Some(r), [a]) if stringish(r) && stringish(a) => {
            Some(T::Bool)
        }
        ("lowerAscii" | "upperAscii" | "trim", Some(r), []) if stringish(r) => Some(T::String),
        ("string", None, [a]) if !matches!(a, T::List(_) | T::Map(..) | T::Null) => Some(T::String),
        ("int", None, [T::Int | T::Uint | T::Double | T::String | T::Dyn]) => Some(T::Int),
        ("double", None, [T::Int | T::Uint | T::Double | T::String | T::Dyn]) => Some(T::Double),
        ("dyn", None, [_]) => Some(T::Dyn),
        _ => None,
    }
}

fn ordered_or_dyn(t: &T) -> bool {
    ordered(t) || *t == T::Dyn
}

fn sized(t: &T) -> bool {
    matches!(t, T::String | T::Bytes | T::List(_) | T::Map(..) | T::Dyn)
}

/// The type of an arithmetic operation: the operands' when both are one numeric type (or strings, bytes, lists for
/// an addition), the known one's beside a dyn, dyn of two dyn.
fn arithmetic(a: &T, b: &T, add: bool) -> Option<T> {
    let fits = |t: &T| numeric(t) || (add && matches!(t, T::String | T::Bytes | T::List(_)));
    match (a, b) {
        (T::Dyn, T::Dyn) => Some(T::Dyn),
        (T::Dyn, x) | (x, T::Dyn) if fits(x) => Some(x.clone()),
        (T::List(x), T::List(y)) if add => Some(T::List(Box::new(join(x, y)?))),
        _ if a == b && fits(a) => Some(a.clone()),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::{Verdict, judge};

    #[test]
    fn follows_the_guards_of_the_domains() {
        for ok in [
            "",
            "impact.review == \"accepted\"",
            "node.props.owner != '' && impact.review == 'accepted'",
            "children.all(c, c.state == 'done')",
            "size(children) > 0",
            "has(node.props.title)",
            "risks.exists(r, r.data.level >= 3)",
            "world.exists(k, world[k] == true)",
            "change.state in ['a', 'b']",
            "children.map(c, c.state).exists(s, s == 'x')",
            "children.exists_one(c, c.key == 'k')",
            "node.props.title.startsWith('x')",
        ] {
            assert_eq!(judge(ok), Verdict::Compiles, "{ok}");
        }
        for (refused, msg) in [
            ("'text'", "guard must return bool, got string"),
            ("size(children)", "guard must return bool, got int"),
        ] {
            assert_eq!(judge(refused), Verdict::Refused(msg.into()), "{refused}");
        }
        assert!(matches!(judge("node."), Verdict::Refused(_)));
        for unsure in [
            "nope == 1",
            "1 + 'a'",
            "node.state == 1u + 1",
            "1 < 1.0",
            "node.x.foo()",
        ] {
            assert_eq!(judge(unsure), Verdict::Unknown, "{unsure}");
        }
    }
}
