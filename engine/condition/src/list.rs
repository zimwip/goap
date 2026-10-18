//! Expressions returning a list (the `foreach` of a step) or any value (its `groupBy`).
use std::sync::Arc;

use cel::{Program, Value as CelValue};
use goap_domain::Blackboard;

use crate::{
    activation::Activation,
    check::Ty,
    condition::{Error, compile_checked, condition_decls, new_env, run},
    value::from_cel,
};

/// List is a compiled CEL expression returning a list (the `foreach` of a step, ADR 0050): evaluated against the
/// blackboard, it yields the items a step is carried out for, one stream each.
#[derive(Debug, Clone)]
pub struct List {
    prog: Arc<Program>,
}

fn compile_list_prog(expr: &str) -> Result<Program, Error> {
    let (prog, t) = compile_checked(&new_env(), &condition_decls(), expr)?;
    match t {
        Ty::List(_) | Ty::Dyn => Ok(prog),
        t => Err(Error::Compile(format!(
            "expression must return a list, got {t}"
        ))),
    }
}

/// CheckList validates a list expression.
pub fn check_list(expr: &str) -> Result<(), Error> {
    compile_list_prog(expr).map(|_| ())
}

/// CompileList compiles a list expression.
pub fn compile_list(expr: &str) -> Result<List, Error> {
    Ok(List {
        prog: Arc::new(compile_list_prog(expr)?),
    })
}

impl List {
    /// Eval returns the elements of the list as plain values (maps, lists, strings, numbers, bools).
    pub fn eval(&self, bb: &Blackboard) -> Result<Vec<serde_json::Value>, Error> {
        let act = Activation::new(bb);
        match run(&self.prog, &act).map_err(Error::Eval)? {
            CelValue::List(l) => Ok(l.iter().map(from_cel).collect()),
            other => Err(Error::Eval(format!(
                "not a list: unsupported type conversion from '{}' to *structpb.ListValue",
                other.type_of()
            ))),
        }
    }
}

/// Value is a compiled CEL expression of any type: the `groupBy` of a step (ADR 0050), evaluated per element, whose
/// result is the key of the group the element belongs to.
#[derive(Debug, Clone)]
pub struct Value {
    prog: Arc<Program>,
}

/// CompileValue compiles an expression of any type.
pub fn compile_value(expr: &str) -> Result<Value, Error> {
    let (prog, _) = compile_checked(&new_env(), &condition_decls(), expr)?;
    Ok(Value {
        prog: Arc::new(prog),
    })
}

impl Value {
    /// Eval returns the value as a plain value (map, list, string, number, bool or null).
    pub fn eval(&self, bb: &Blackboard) -> Result<serde_json::Value, Error> {
        let act = Activation::new(bb);
        Ok(from_cel(&run(&self.prog, &act).map_err(Error::Eval)?))
    }
}
