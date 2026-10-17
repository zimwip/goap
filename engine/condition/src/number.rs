//! Numeric expressions (action utilities).
use std::{collections::BTreeMap, sync::Arc};

use cel::{Program, Value as CelValue};
use goap_domain::Blackboard;

use crate::{
    activation::Activation,
    check::Ty,
    condition::{Definition, Error, compile_checked, condition_decls, new_env, run},
};

/// Input is what expressions are evaluated against.
pub type Input = Blackboard;

/// NumberSet is a compiled set of numeric expressions (action utilities).
#[derive(Debug, Clone, Default)]
pub struct NumberSet {
    progs: BTreeMap<String, Arc<Program>>,
}

fn compile_number(expr: &str) -> Result<Program, Error> {
    let (prog, t) = compile_checked(&new_env(), &condition_decls(), expr)?;
    match t {
        Ty::Double | Ty::Int | Ty::Uint | Ty::Dyn => Ok(prog),
        t => Err(Error::Compile(format!(
            "expression must return a number, got {t}"
        ))),
    }
}

/// CheckNumber validates a numeric expression.
pub fn check_number(expr: &str) -> Result<(), Error> {
    compile_number(expr).map(|_| ())
}

/// CompileNumbers compiles numeric expressions by name.
pub fn compile_numbers(defs: &[Definition]) -> Result<NumberSet, Error> {
    let mut s = NumberSet::default();
    for d in defs {
        let p = compile_number(&d.expr).map_err(|e| e.context(&d.name))?;
        s.progs.insert(d.name.clone(), Arc::new(p));
    }
    Ok(s)
}

impl NumberSet {
    /// Evaluate returns the value of every expression that evaluates to a number.
    pub fn evaluate(&self, bb: &Input) -> BTreeMap<String, f64> {
        let mut out = BTreeMap::new();
        if self.progs.is_empty() {
            return out;
        }
        let act = Activation::new(bb);
        for (name, p) in &self.progs {
            match run(p, &act) {
                Ok(CelValue::Float(x)) => {
                    out.insert(name.clone(), x);
                }
                Ok(CelValue::Int(x)) => {
                    out.insert(name.clone(), x as f64);
                }
                Ok(CelValue::UInt(x)) => {
                    out.insert(name.clone(), x as f64);
                }
                _ => {}
            }
        }
        out
    }
}
