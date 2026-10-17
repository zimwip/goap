//! How a condition got its value: the formula, the value it took and the value of each part.
use std::collections::{BTreeMap, BTreeSet};

use cel::{
    Value as CelValue,
    common::ast::{Expr, IdedExpr, operators},
};
use goap_domain::Blackboard;
use serde_json::Value;

use crate::{
    activation::Activation,
    condition::{Set, VARIABLES, compile_expr, condition_decls, eval_bool, new_env, run},
    unparse::unparse,
    value::from_cel,
};

/// maxValueText bounds the text of one observed value.
const MAX_VALUE_TEXT: usize = 400;

/// Term is one sub-expression of a formula with the value it took against the blackboard.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Term {
    /// Text is the sub-expression as written.
    pub text: String,
    /// Value is what it evaluated to; empty when skipped or error says why not.
    pub value: String,
    /// Skipped: short-circuited, or inside a macro (exists, all, ...) whose iterations have no single value.
    pub skipped: bool,
    pub error: String,
    pub terms: Vec<Term>,
}

/// Explanation shows how a condition got its value: the formula, the value it took and the value of each part.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Explanation {
    pub name: String,
    pub expr: String,
    pub value: bool,
    /// Error is set when the condition could not be evaluated (unknown).
    pub error: String,
    pub root: Term,
    /// Inputs are the blackboard variables the formula reads, with their value (as JSON text).
    pub inputs: BTreeMap<String, String>,
}

/// The calls that are macros in the environment: in cel-go's checked expression they are a comprehension or a
/// presence test, a leaf of the explanation.
pub(crate) fn is_macro(e: &IdedExpr) -> bool {
    let Expr::Call(c) = &e.expr else {
        return false;
    };
    match c.func_name.as_str() {
        operators::HAS => c.target.is_none() && c.args.len() == 1,
        operators::ALL
        | operators::EXISTS
        | operators::EXISTS_ONE
        | "existsOne"
        | operators::MAP
        | operators::FILTER => {
            c.target.is_some()
                && (c.args.len() == 2 || c.args.len() == 3)
                && matches!(c.args[0].expr, Expr::Ident(_))
        }
        _ => false,
    }
}

/// Parses an expression without macros (cel-go's `parser.NewParser()`): `exists` stays a call.
pub(crate) fn parse_plain(expr: &str) -> Option<IdedExpr> {
    cel::Env::default().parser().parse(expr).ok()
}

impl Set {
    /// Explain evaluates the condition against the blackboard keeping the value of every sub-expression (None for
    /// an unknown condition).
    pub fn explain(&self, name: &str, bb: &Blackboard) -> Option<Explanation> {
        let Some(p) = self.progs.iter().find(|p| p.name == name) else {
            if let Some(a) = self.aggregate_of(name) {
                return Some(self.explain_aggregate(a, bb));
            }
            if !self.scope_of(name).is_empty() {
                return Some(self.explain_scoped(name, bb));
            }
            return None;
        };
        let expr = p.expr.clone();
        let mut ex = Explanation {
            name: name.to_string(),
            expr: expr.clone(),
            ..Default::default()
        };
        let prog = match compile_expr(&condition_decls(), &expr) {
            Ok(p) => p,
            Err(e) => {
                ex.error = e.to_string();
                return Some(ex);
            }
        };
        let act = Activation::new(bb);
        match eval_bool(&prog, &act) {
            Ok(b) => ex.value = b,
            Err(e) => ex.error = e,
        }
        let Some(tree) = parse_plain(&expr) else {
            return Some(ex);
        };
        ex.root = term(&tree, &act, true).0;
        for v in expr_variables(&tree) {
            if let Some(val) = act.get(v) {
                ex.inputs.insert(v.to_string(), text(val));
            }
        }
        Some(ex)
    }
}

/// The term of a sub-expression, evaluated when its parent evaluates it, with the outcome.
fn term(
    e: &IdedExpr,
    act: &Activation<'_>,
    evaluated: bool,
) -> (Term, Option<Result<CelValue, String>>) {
    let src = unparse(e);
    let mut t = Term {
        text: src.clone(),
        ..Default::default()
    };
    let outcome = if evaluated {
        let r = new_env()
            .compile(&src)
            .map_err(|e| e.to_string())
            .and_then(|p| run(&p, act));
        match &r {
            Ok(v) => t.value = text(&from_cel(v)),
            Err(msg) => t.error = msg.clone(),
        }
        Some(r)
    } else {
        t.skipped = true;
        None
    };
    if let Expr::Call(c) = &e.expr
        && !is_macro(e)
    {
        let mut kids: Vec<&IdedExpr> = Vec::new();
        if let Some(tg) = &c.target {
            kids.push(tg);
        }
        kids.extend(c.args.iter());
        let f = c.func_name.as_str();
        // which kids the evaluation of the call reaches, as cel-go's interpreter does
        let mut prev: Vec<Option<Result<CelValue, String>>> = Vec::new();
        for (i, k) in kids.iter().enumerate() {
            let reached = evaluated
                && match (f, i) {
                    (operators::LOGICAL_AND, 1) => {
                        !matches!(prev[0], Some(Ok(CelValue::Bool(false))))
                    }
                    (operators::LOGICAL_OR, 1) => {
                        !matches!(prev[0], Some(Ok(CelValue::Bool(true))))
                    }
                    (operators::CONDITIONAL, 1) => {
                        matches!(prev[0], Some(Ok(CelValue::Bool(true))))
                    }
                    (operators::CONDITIONAL, 2) => {
                        matches!(prev[0], Some(Ok(CelValue::Bool(false))))
                    }
                    (
                        operators::LOGICAL_AND | operators::LOGICAL_OR | operators::CONDITIONAL,
                        _,
                    ) => true,
                    // a strict call stops at the first argument in error
                    _ => prev.iter().all(|p| matches!(p, Some(Ok(_)))),
                };
            let literal = matches!(k.expr, Expr::Literal(_));
            if literal {
                prev.push(if reached {
                    Some(Ok(CelValue::Null))
                } else {
                    None
                });
                continue;
            }
            let (kt, ko) = term(k, act, reached);
            prev.push(ko);
            t.terms.push(kt);
        }
    }
    (t, outcome)
}

/// expr_variables lists the blackboard variables an expression reads, in the order of VARIABLES.
fn expr_variables(e: &IdedExpr) -> Vec<&'static str> {
    let mut seen = BTreeSet::new();
    fn walk(e: &IdedExpr, seen: &mut BTreeSet<String>) {
        match &e.expr {
            Expr::Ident(n) => {
                seen.insert(n.clone());
            }
            Expr::Select(s) => walk(&s.operand, seen),
            Expr::Call(c) => {
                if let Some(t) = &c.target {
                    walk(t, seen);
                }
                for a in &c.args {
                    walk(a, seen);
                }
            }
            Expr::List(l) => {
                for x in &l.elements {
                    walk(x, seen);
                }
            }
            Expr::Comprehension(c) => {
                walk(&c.iter_range, seen);
                walk(&c.accu_init, seen);
                walk(&c.loop_cond, seen);
                walk(&c.loop_step, seen);
                walk(&c.result, seen);
            }
            _ => {}
        }
    }
    walk(e, &mut seen);
    VARIABLES
        .iter()
        .copied()
        .filter(|v| seen.contains(*v))
        .collect()
}

/// text renders an observed value, bounded: its JSON as Go's `encoding/json` writes it.
pub(crate) fn text(v: &Value) -> String {
    let mut s = go_json(v);
    if s.len() > MAX_VALUE_TEXT {
        let mut cut = MAX_VALUE_TEXT;
        while !s.is_char_boundary(cut) {
            cut -= 1;
        }
        s = format!("{}…", s[..cut].trim());
    }
    s
}

/// The JSON of a value as Go's `json.Marshal` writes it: `<`, `>`, `&`, U+2028 and U+2029 escaped.
pub(crate) fn go_json(v: &Value) -> String {
    let s = serde_json::to_string(v).unwrap_or_default();
    if !s.contains(['<', '>', '&', '\u{2028}', '\u{2029}']) {
        return s;
    }
    s.replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('&', "\\u0026")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029")
}
