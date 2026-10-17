//! Implications between derived conditions (ADR 0112 §5): a condition B implies a condition A when B cannot hold
//! without A holding, read from their expressions. Reaching A is then a prerequisite of B: `description_accepted`
//! (a current rating of the description scoring 3 or more) needs `description_rated` (a current rating), which the
//! rating action produces. The analysis is structural and conservative: an implication it finds holds, one it misses
//! is only not shown.
//!
//! An expression is read as a conjunction of atoms (its `&&` terms). B implies A when every atom of A is implied by an
//! atom of B: the same term, or, for `R.exists(v, P)`, a term `R.exists(w, Q)` over the same range whose predicate Q
//! has every conjunct of P (the iteration variables renamed alike).
use std::collections::BTreeMap;

use cel::common::ast::{Expr, IdedExpr, LiteralValue, operators};

use crate::{condition::Set, explain::parse_plain, unparse::go_quote};

/// atom is one conjunct of an expression: a term, or an exists over a range with the conjuncts of its predicate.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub(crate) struct Atom {
    pub(crate) text: String,
    pub(crate) exists: bool,
    pub(crate) preds: Vec<String>,
}

/// atomsOf reads an expression as its conjuncts (None when it cannot be parsed).
pub(crate) fn atoms_of(expr: &str) -> Option<Vec<Atom>> {
    let parsed = parse_plain(expr)?;
    Some(conjuncts(&parsed).into_iter().map(atom_of).collect())
}

/// The variable and predicate of `R.exists(v, P)`.
pub(crate) fn exists_parts(e: &IdedExpr) -> Option<(&IdedExpr, &str, &IdedExpr)> {
    let Expr::Call(c) = &e.expr else {
        return None;
    };
    let target = c.target.as_deref()?;
    if c.func_name != operators::EXISTS || c.args.len() != 2 {
        return None;
    }
    let Expr::Ident(v) = &c.args[0].expr else {
        return None;
    };
    Some((target, v.as_str(), &c.args[1]))
}

pub(crate) fn atom_of(e: &IdedExpr) -> Atom {
    if let Some((target, v, pred)) = exists_parts(e) {
        let rename = BTreeMap::from([(v.to_string(), "@it".to_string())]);
        let mut a = Atom {
            exists: true,
            text: canon(target, &BTreeMap::new()),
            preds: Vec::new(),
        };
        for p in conjuncts(pred) {
            a.preds.push(canon(p, &rename));
        }
        a.preds.sort();
        return a;
    }
    Atom {
        text: canon(e, &BTreeMap::new()),
        ..Default::default()
    }
}

/// conjuncts flattens the `&&` terms of an expression.
pub(crate) fn conjuncts(e: &IdedExpr) -> Vec<&IdedExpr> {
    if let Expr::Call(c) = &e.expr
        && c.func_name == operators::LOGICAL_AND
    {
        return c.args.iter().flat_map(conjuncts).collect();
    }
    vec![e]
}

/// The kind number of cel-go's `ast.ExprKind`.
fn kind_number(e: &IdedExpr) -> u8 {
    match &e.expr {
        Expr::Unspecified => 0,
        Expr::Call(_) => 1,
        Expr::Comprehension(_) => 2,
        Expr::Ident(_) => 3,
        Expr::List(_) => 4,
        Expr::Literal(_) => 5,
        Expr::Map(_) => 6,
        Expr::Select(_) => 7,
        Expr::Struct(_) => 8,
    }
}

/// Go's `fmt.Sprintf("%#v", literal.Value())`.
fn literal_text(l: &LiteralValue) -> String {
    match l {
        LiteralValue::Boolean(b) => b.inner().to_string(),
        LiteralValue::Bytes(b) => format!("{:?}", b.inner()),
        LiteralValue::Double(d) => crate::unparse::format_double(*d.inner()),
        LiteralValue::Int(i) => i.inner().to_string(),
        LiteralValue::UInt(u) => format!("0x{:x}", u.inner()),
        LiteralValue::Null => "0".to_string(),
        LiteralValue::String(s) => go_quote(s.inner()),
    }
}

/// canon is a canonical text of an expression, the identifiers renamed (for comparison only).
pub(crate) fn canon(e: &IdedExpr, rename: &BTreeMap<String, String>) -> String {
    match &e.expr {
        Expr::Ident(n) => rename.get(n).cloned().unwrap_or_else(|| n.clone()),
        Expr::Select(s) => {
            if s.test {
                format!("has({}.{})", canon(&s.operand, rename), s.field)
            } else {
                format!("{}.{}", canon(&s.operand, rename), s.field)
            }
        }
        Expr::Literal(l) => literal_text(l),
        Expr::List(l) => {
            let els: Vec<String> = l.elements.iter().map(|x| canon(x, rename)).collect();
            format!("[{}]", els.join(","))
        }
        Expr::Call(c) => {
            let mut args: Vec<String> = c.args.iter().map(|a| canon(a, rename)).collect();
            let f = c.func_name.as_str();
            if f == operators::LOGICAL_AND
                || f == operators::LOGICAL_OR
                || f == operators::EQUALS
                || f == operators::NOT_EQUALS
            {
                args.sort(); // commutative
            }
            let mut head = f.to_string();
            if let Some(t) = &c.target {
                // a nested macro: its variable is renamed by position so that `x.exists(b, ...)` matches
                // `x.exists(c, ...)`
                if c.args.len() == 2
                    && let Expr::Ident(v) = &c.args[0].expr
                {
                    let mut inner = rename.clone();
                    let r = format!("@{}", rename.len());
                    inner.insert(v.clone(), r.clone());
                    args = vec![r, canon(&c.args[1], &inner)];
                }
                head = format!("{}.{}", canon(t, rename), head);
            }
            format!("{}({})", head, args.join(","))
        }
        _ => format!("#{}", kind_number(e)),
    }
}

impl Atom {
    /// implies reports whether atom b (self) cannot hold without atom a holding.
    pub(crate) fn implies(&self, a: &Atom) -> bool {
        if a.exists != self.exists || a.text != self.text {
            return false;
        }
        if !a.exists {
            return true;
        }
        a.preds.iter().all(|p| self.preds.contains(p))
    }
}

/// Implies reports whether an expression b cannot hold without an expression a holding (structurally).
pub fn implies(b: &str, a: &str) -> bool {
    let Some(ab) = atoms_of(b) else {
        return false;
    };
    let Some(aa) = atoms_of(a) else {
        return false;
    };
    if aa.is_empty() {
        return false;
    }
    aa.iter().all(|x| ab.iter().any(|y| y.implies(x)))
}

impl Set {
    /// implications computes, for every condition expression of the set, the other ones it implies.
    fn implications(&self) -> BTreeMap<String, Vec<String>> {
        let mut atoms: BTreeMap<&str, Vec<Atom>> = BTreeMap::new();
        for p in &self.progs {
            if let Some(a) = atoms_of(&p.expr)
                && !a.is_empty()
            {
                atoms.insert(&p.name, a);
            }
        }
        let mut out: BTreeMap<String, Vec<String>> = BTreeMap::new();
        for (b, ab) in &atoms {
            for (a, aa) in &atoms {
                if a == b {
                    continue;
                }
                if aa.iter().all(|x| ab.iter().any(|y| y.implies(x))) {
                    out.entry(b.to_string()).or_default().push(a.to_string());
                }
            }
        }
        for v in out.values_mut() {
            v.sort();
        }
        out
    }

    /// Implied lists the conditions of the set that the condition cannot hold without (its prerequisites), sorted.
    /// Two conditions implying each other (the same expression) are not each other's prerequisite.
    pub fn implied(&self, name: &str) -> Vec<String> {
        let implies = self.implies.get_or_init(|| self.implications());
        let empty = Vec::new();
        implies
            .get(name)
            .unwrap_or(&empty)
            .iter()
            .filter(|a| !implies.get(*a).is_some_and(|x| x.iter().any(|n| n == name)))
            .cloned()
            .collect()
    }
}
