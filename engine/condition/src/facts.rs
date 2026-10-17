//! The facts of a condition (ADR 0112 §5): a condition is a CEL expression over the variables of the context, so what
//! it really asks is known from it. Facts reads it as the conjunction of its `&&` terms, each evaluated against the
//! blackboard; a term `R.exists(v, P1 && ... && Pn)` is read part by part, part i holding when an element of R meets
//! P1 to Pi: "a rating of the description: yes; current: yes; score >= 3: none". The first part that does not hold is
//! what the condition is missing, and an action whose effect asks for the same facts is what produces it.
use std::collections::BTreeMap;

use goap_domain::Blackboard;

use crate::{
    activation::Activation,
    condition::{Set, compile_checked, condition_decls, new_env, run},
    explain::parse_plain,
    implies::{Atom, atom_of, atoms_of, canon, conjuncts, exists_parts},
    unparse::unparse,
    value::go_text,
};

/// Fact is one term of a condition with whether it holds now.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Fact {
    /// Text is the term as written.
    pub text: String,
    pub holds: bool,
    /// Error says why the term could not be evaluated.
    pub error: String,
    /// Over is the list an exists term ranges over; Parts its predicate, part by part, each holding when an element
    /// meets it and the parts before it.
    pub over: String,
    pub parts: Vec<Fact>,
    /// key is the canonical form of the term (of the parts up to this one, for a part), to match producers.
    pub(crate) key: Atom,
}

impl Set {
    /// Facts reads a condition of the change level into its terms evaluated against the blackboard (None for an
    /// unknown condition, a scoped one or an aggregate, which keep their own explanation).
    pub fn facts(&self, name: &str, bb: &Blackboard) -> Option<Vec<Fact>> {
        let expr = self.expr_of(name).filter(|e| !e.is_empty())?;
        let parsed = parse_plain(expr)?;
        let act = Activation::new(bb);
        let mut out = Vec::new();
        for c in conjuncts(&parsed) {
            let mut f = Fact {
                text: unparse(c),
                key: atom_of(c),
                ..Default::default()
            };
            if let Some((target, v, pred)) = exists_parts(c) {
                let rng = unparse(target);
                f.over = rng.clone();
                let mut sofar: Vec<String> = Vec::new();
                let rename = BTreeMap::from([(v.to_string(), "@it".to_string())]);
                for part in conjuncts(pred) {
                    let txt = unparse(part);
                    sofar.push(format!("({txt})"));
                    let mut pf = Fact {
                        text: txt,
                        key: Atom {
                            exists: true,
                            text: f.key.text.clone(),
                            preds: prefix_keys(&f.parts, canon(part, &rename)),
                        },
                        ..Default::default()
                    };
                    (pf.holds, pf.error) = holds(
                        &format!("{}.exists({}, {})", rng, v, sofar.join(" && ")),
                        &act,
                    );
                    f.parts.push(pf);
                }
            }
            (f.holds, f.error) = holds(&f.text, &act);
            out.push(f);
        }
        Some(out)
    }

    /// Establishes reports whether making the condition effect hold makes the fact hold: one of its terms is the
    /// fact or, for a part of an exists term, an exists over the same list asking for every part up to it.
    pub fn establishes(&self, effect: &str, f: &Fact) -> bool {
        let expr = self.expr_of(effect).unwrap_or("");
        if expr.is_empty() {
            return false;
        }
        let Some(as_) = atoms_of(expr) else {
            return false;
        };
        as_.iter().any(|a| a.implies(&f.key))
    }
}

/// prefixKeys are the canonical predicates of the parts before one, and its own.
fn prefix_keys(before: &[Fact], own: String) -> Vec<String> {
    let mut out = Vec::new();
    if let Some(last) = before.last() {
        out.extend(last.key.preds.iter().cloned());
    }
    out.push(own);
    out
}

/// holds evaluates one term against the activation.
fn holds(expr: &str, act: &Activation<'_>) -> (bool, String) {
    let prog = match compile_checked(&new_env(), &condition_decls(), expr) {
        Ok((p, _)) => p,
        Err(e) => return (false, e.to_string()),
    };
    match run(&prog, act) {
        Ok(cel::Value::Bool(b)) => (b, String::new()),
        Ok(other) => (false, format!("non-bool result {}", go_text(&other))),
        Err(e) => (false, e),
    }
}

/// Missing is the first term of the facts that does not hold: the part of an exists term when one fails, else the
/// term.
pub fn missing(facts: &[Fact]) -> Option<Fact> {
    for f in facts {
        if f.holds {
            continue;
        }
        if let Some(p) = f.parts.iter().find(|p| !p.holds) {
            return Some(p.clone());
        }
        return Some(f.clone());
    }
    None
}
