//! Trigger filters: boolean CEL expressions over the event.
use std::sync::{Arc, LazyLock};

use cel::{Env, Program, Value as CelValue};

use crate::{
    activation::Object,
    check::Ty,
    condition::{Error, compile_checked},
};

/// The environment of the filters: the standard library, cel-go's `ext.Strings()` and `ext.Lists()`.
static EVENT_ENV: LazyLock<Arc<Env>> = LazyLock::new(|| {
    let mut env = Env::stdlib();
    env.add_extension(cel::extensions::strings)
        .expect("strings extension");
    env.add_extension(cel::extensions::lists)
        .expect("lists extension");
    Arc::new(env)
});

/// EventFilter is a compiled trigger filter: a boolean CEL expression over the variable `event` ({type, change
/// {...}, process {...}, methodology {...}}).
#[derive(Debug, Clone, Default)]
pub struct EventFilter {
    prog: Option<Arc<Program>>,
}

/// CompileEventFilter compiles a trigger filter (empty: always true).
pub fn compile_event_filter(expr: &str) -> Result<EventFilter, Error> {
    if expr.is_empty() {
        return Ok(EventFilter::default());
    }
    let (prog, t) = compile_checked(&EVENT_ENV, &[("event", Ty::map())], expr)?;
    if t != Ty::Bool && t != Ty::Dyn {
        return Err(Error::Compile(format!("filter must return bool, got {t}")));
    }
    Ok(EventFilter {
        prog: Some(Arc::new(prog)),
    })
}

impl EventFilter {
    /// Match evaluates the filter; evaluation errors do not match.
    pub fn matches(&self, event: &Object) -> bool {
        let Some(prog) = &self.prog else {
            return true;
        };
        let mut ctx = cel::Context::with_env(EVENT_ENV.clone());
        ctx.add_variable_as_val(
            "event",
            crate::value::to_val(&serde_json::Value::Object(event.clone())),
        );
        matches!(prog.execute(&ctx), Ok(CelValue::Bool(true)))
    }
}
