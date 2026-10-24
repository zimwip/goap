//! The algorithms of a domain (Go's `dsl.RunAlgorithm`, `lib/dsl/algo.go`): a JavaScript body run as `run(ctx)` with
//! the context object of its usage. A script rejects with `ctx.fail(message)`, by throwing, or by returning false or a
//! message; a transition action sets and removes properties of the node it moves.
use std::{
    cell::RefCell,
    rc::Rc,
    time::{Duration, Instant},
};

use rquickjs::{Array, CatchResultExt, Context, Ctx, Function, Object, Runtime, Value, function::Rest, prelude::Opt};
use serde_json::{Map, Value as Json};

/// The usages an algorithm runs in (`algo.Usage`).
pub const PROPERTY_VALIDATOR: &str = "property_validator";
pub const NODE_VALIDATOR: &str = "node_validator";
pub const LINK_VALIDATOR: &str = "link_validator";
pub const TRANSITION_GUARD: &str = "transition_guard";
pub const TRANSITION_ACTION: &str = "transition_action";

/// The language of the scripts (`algo.JavaScript`), and its short spelling.
pub const JAVASCRIPT: &str = "javascript";

/// What bounds one run of an algorithm (`dsl.AlgorithmTimeout`).
pub const TIMEOUT: Duration = Duration::from_secs(5);
/// The memory one run may take.
const MEMORY: usize = 64 << 20;
const STACK: usize = 1 << 20;

/// An instance resolved with its algorithm (`algo.Bound`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Bound {
    pub instance: String,
    pub algorithm: String,
    /// its usage
    pub typ: String,
    pub language: String,
    pub code: String,
    pub params: Map<String, Json>,
    /// the attribute a property validator applies to
    pub property: String,
}

/// A node as a script sees it (`dsl.Node`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Node {
    pub id: String,
    pub version: i64,
    pub key: String,
    pub typ: String,
    pub state: String,
    pub props: Map<String, Json>,
}

impl Node {
    fn json(&self) -> Json {
        let mut m = Map::new();
        m.insert("id".into(), Json::String(self.id.clone()));
        m.insert("version".into(), Json::from(self.version));
        m.insert("key".into(), Json::String(self.key.clone()));
        m.insert("type".into(), Json::String(self.typ.clone()));
        m.insert("state".into(), Json::String(self.state.clone()));
        m.insert("props".into(), Json::Object(self.props.clone()));
        Json::Object(m)
    }
}

/// The change a transition is taken in (`dsl.ChangeInfo`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ChangeInfo {
    pub id: String,
    pub title: String,
    pub intent: String,
    pub methodology: String,
    pub goal: String,
}

/// What an algorithm runs on (`dsl.AlgorithmInput`): only the fields of its usage are read.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Input {
    pub node: Node,
    pub property: String,
    pub value: Option<Json>,
    /// the link a link validator checks: its type and properties, and the nodes it leaves and reaches
    pub link_type: String,
    pub link_props: Map<String, Json>,
    pub from: Node,
    pub to: Node,
    pub children: Vec<Node>,
    pub change: ChangeInfo,
    /// the transition: its name, from and to
    pub transition: [String; 3],
    /// the impact of the moved node: its intent and its review
    pub impact: [String; 2],
}

/// A line a script logged.
#[derive(Debug, Clone, PartialEq)]
pub struct LogLine {
    pub level: &'static str,
    pub message: String,
}

/// The result of an algorithm (`dsl.Outcome`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Outcome {
    /// the reasons it rejected; empty: accepted
    pub failures: Vec<String>,
    /// the property changes of a transition action
    pub set: Map<String, Json>,
    pub unset: Vec<String>,
    pub logs: Vec<LogLine>,
}

impl Outcome {
    pub fn ok(&self) -> bool {
        self.failures.is_empty()
    }
}

#[derive(Default)]
struct State {
    failures: Vec<String>,
    set: Map<String, Json>,
    unset: Vec<String>,
    logs: Vec<LogLine>,
}

/// Whether a language is JavaScript (`dsl.isJS`).
pub fn is_js(language: &str) -> bool {
    language == JAVASCRIPT || language == "js"
}

/// The code of an algorithm as the body of `run(ctx)` (`dsl.jsBody`).
fn body(code: &str) -> String {
    format!("function run(ctx) {{\n{code}\n}}")
}

/// Checks that the code of an algorithm compiles, without running it (`dsl.CheckAlgorithmCode`).
pub fn check_code(language: &str, code: &str) -> Result<(), String> {
    if !is_js(language) {
        return Err(format!("unsupported language {}", go_quote(language)));
    }
    let (_rt, context) = runtime(Instant::now() + TIMEOUT)?;
    context.with(|ctx| ctx.eval::<(), _>(body(code)).catch(&ctx).map_err(|e| e.to_string()))
}

fn runtime(deadline: Instant) -> Result<(Runtime, Context), String> {
    let rt = Runtime::new().map_err(|e| e.to_string())?;
    rt.set_memory_limit(MEMORY);
    rt.set_max_stack_size(STACK);
    rt.set_interrupt_handler(Some(Box::new(move || Instant::now() >= deadline)));
    let context = Context::full(&rt).map_err(|e| e.to_string())?;
    Ok((rt, context))
}

/// Runs a bound algorithm on an input. The error is a failure of the script itself (it does not compile, throws, runs
/// out of time), prefixed `algorithm <instance>: ` as Go's: callers treat it as a rejection too. A rejection by the
/// algorithm is in [`Outcome::failures`].
pub fn run_algorithm(b: &Bound, inp: &Input) -> Result<Outcome, String> {
    match try_algorithm(b, inp) {
        (out, None) => Ok(out),
        (_, Some(e)) => Err(e),
    }
}

/// Runs a bound algorithm on an input as a trial does (Go's `RunAlgorithm` answering both): what the script did up
/// to its end, and the failure of the script itself if any.
pub fn try_algorithm(b: &Bound, inp: &Input) -> (Outcome, Option<String>) {
    match b.typ.as_str() {
        PROPERTY_VALIDATOR | NODE_VALIDATOR | LINK_VALIDATOR | TRANSITION_GUARD | TRANSITION_ACTION => {}
        t => {
            let e = format!("algorithm {}: type {} cannot be run", b.instance, go_quote(t));
            return (Outcome::default(), Some(e));
        }
    }
    let state = Rc::new(RefCell::new(State::default()));
    let res = run(b, inp, &state);
    let st = state.take();
    let out = Outcome {
        failures: st.failures,
        set: if b.typ == TRANSITION_ACTION { st.set } else { Map::new() },
        unset: if b.typ == TRANSITION_ACTION {
            st.unset
        } else {
            Vec::new()
        },
        logs: st.logs,
    };
    (out, res.err().map(|e| format!("algorithm {}: {e}", b.instance)))
}

fn run(b: &Bound, inp: &Input, state: &Rc<RefCell<State>>) -> Result<(), String> {
    if !is_js(&b.language) {
        return Err(format!("unsupported language {}", go_quote(&b.language)));
    }
    let deadline = Instant::now() + TIMEOUT;
    let (_rt, context) = runtime(deadline)?;
    context.with(|ctx| -> Result<(), String> {
        let fail = |e: rquickjs::CaughtError| {
            if Instant::now() >= deadline {
                "script interrupted: context deadline exceeded".to_string()
            } else {
                caught(e)
            }
        };
        let obj = context_object(&ctx, b, inp, state)
            .catch(&ctx)
            .map_err(|e| e.to_string())?;
        let g = ctx.globals();
        g.set("ctx", obj.clone()).catch(&ctx).map_err(|e| e.to_string())?;
        g.set("console", console(&ctx, state).catch(&ctx).map_err(|e| e.to_string())?)
            .catch(&ctx)
            .map_err(|e| e.to_string())?;
        ctx.eval::<(), _>(body(&b.code)).catch(&ctx).map_err(fail)?;
        let run: Value = g.get("run").catch(&ctx).map_err(|e| e.to_string())?;
        let Some(f) = run.as_function() else { return Ok(()) };
        let v: Value = f.call((obj,)).catch(&ctx).map_err(fail)?;
        // a run(ctx) may reject by returning false or a message
        if let Some(false) = v.as_bool() {
            state.borrow_mut().failures.push("rejected".into());
        } else if let Some(s) = v.as_string() {
            let s = s.to_string().unwrap_or_default();
            if !s.is_empty() {
                state.borrow_mut().failures.push(s);
            }
        }
        Ok(())
    })
}

fn console<'js>(ctx: &Ctx<'js>, state: &Rc<RefCell<State>>) -> rquickjs::Result<Object<'js>> {
    let console = Object::new(ctx.clone())?;
    for (name, level) in [("log", "info"), ("warn", "warn")] {
        let st = state.clone();
        console.set(
            name,
            Function::new(ctx.clone(), move |args: Rest<Value>| {
                let text: Vec<String> = args.0.iter().map(console_text).collect();
                st.borrow_mut().logs.push(LogLine {
                    level,
                    message: text.join(" "),
                });
            })?,
        )?;
    }
    Ok(console)
}

/// A script's failure as goja words it, as near as QuickJS allows: a thrown value as its text, an error with its
/// stack (`\tat run (script.js:…)`); the positions are QuickJS's.
fn caught(e: rquickjs::CaughtError) -> String {
    match e {
        rquickjs::CaughtError::Value(v) => format!("{}\n", console_text(&v)),
        e => e
            .to_string()
            .replace("    at ", "\tat ")
            .replace("eval_script", "script.js"),
    }
}

/// A value of `console.log` as text (Go's `fmt.Sprintln` of the exported values, approximately).
fn console_text(v: &Value) -> String {
    match to_json(v) {
        Json::String(s) => s,
        Json::Null => "<nil>".into(),
        j => j.to_string(),
    }
}

/// The context object of a usage (Go's `ValidatorCtx`, `NodeValidatorCtx`, `LinkValidatorCtx`, `GuardCtx`,
/// `TransitionCtx`): what it reads of its input, `param`, `fail`, `log` and `warn`.
fn context_object<'js>(
    ctx: &Ctx<'js>,
    b: &Bound,
    inp: &Input,
    state: &Rc<RefCell<State>>,
) -> rquickjs::Result<Object<'js>> {
    let obj = Object::new(ctx.clone())?;
    let params = b.params.clone();
    obj.set(
        "param",
        Function::new(ctx.clone(), move |c: Ctx<'js>, name: String| {
            to_js(&c, params.get(&name).unwrap_or(&Json::Null))
        })?,
    )?;
    let getter = |name: &str, v: Json| -> rquickjs::Result<()> {
        obj.set(name, Function::new(ctx.clone(), move |c: Ctx<'js>| to_js(&c, &v))?)
    };
    match b.typ.as_str() {
        PROPERTY_VALIDATOR => {
            getter("property", Json::String(inp.property.clone()))?;
            getter("value", inp.value.clone().unwrap_or(Json::Null))?;
            getter("node", inp.node.json())?;
        }
        NODE_VALIDATOR => getter("node", inp.node.json())?,
        LINK_VALIDATOR => {
            let mut l = Map::new();
            l.insert("type".into(), Json::String(inp.link_type.clone()));
            l.insert("props".into(), Json::Object(inp.link_props.clone()));
            getter("link", Json::Object(l))?;
            getter("from", inp.from.json())?;
            getter("to", inp.to.json())?;
        }
        _ => {
            getter("node", inp.node.json())?;
            getter("children", Json::Array(inp.children.iter().map(Node::json).collect()))?;
            let c = &inp.change;
            let mut m = Map::new();
            for (k, v) in [
                ("id", &c.id),
                ("title", &c.title),
                ("intent", &c.intent),
                ("methodology", &c.methodology),
                ("goal", &c.goal),
            ] {
                m.insert(k.into(), Json::String(v.clone()));
            }
            getter("change", Json::Object(m))?;
            let mut t = Map::new();
            for (k, v) in ["name", "from", "to"].iter().zip(&inp.transition) {
                t.insert((*k).into(), Json::String(v.clone()));
            }
            getter("transition", Json::Object(t))?;
            if b.typ == TRANSITION_GUARD {
                let mut i = Map::new();
                i.insert("intent".into(), Json::String(inp.impact[0].clone()));
                i.insert("review".into(), Json::String(inp.impact[1].clone()));
                getter("impact", Json::Object(i))?;
            } else {
                let st = state.clone();
                obj.set(
                    "setProp",
                    Function::new(ctx.clone(), move |name: String, v: Opt<Value>| {
                        let mut s = st.borrow_mut();
                        s.set
                            .insert(name.clone(), v.0.as_ref().map(to_json).unwrap_or(Json::Null));
                        s.unset.retain(|x| *x != name);
                    })?,
                )?;
                let st = state.clone();
                obj.set(
                    "removeProp",
                    Function::new(ctx.clone(), move |name: String| {
                        let mut s = st.borrow_mut();
                        s.set.remove(&name);
                        s.unset.retain(|x| *x != name);
                        s.unset.push(name);
                    })?,
                )?;
            }
        }
    }
    let st = state.clone();
    obj.set(
        "fail",
        Function::new(ctx.clone(), move |msg: Opt<Value>| {
            st.borrow_mut()
                .failures
                .push(msg.0.as_ref().map(console_text).unwrap_or_default());
        })?,
    )?;
    for (name, level) in [("log", "info"), ("warn", "warn")] {
        let st = state.clone();
        obj.set(
            name,
            Function::new(ctx.clone(), move |msg: Opt<Value>| {
                st.borrow_mut().logs.push(LogLine {
                    level,
                    message: msg.0.as_ref().map(console_text).unwrap_or_default(),
                });
            })?,
        )?;
    }
    Ok(obj)
}

/// A JSON value as a JavaScript one.
pub fn to_js<'js>(ctx: &Ctx<'js>, v: &Json) -> rquickjs::Result<Value<'js>> {
    Ok(match v {
        Json::Null => Value::new_null(ctx.clone()),
        Json::Bool(b) => Value::new_bool(ctx.clone(), *b),
        Json::Number(n) => match n.as_i64().and_then(|i| i32::try_from(i).ok()) {
            Some(i) => Value::new_int(ctx.clone(), i),
            None => Value::new_float(ctx.clone(), n.as_f64().unwrap_or(0.0)),
        },
        Json::String(s) => rquickjs::String::from_str(ctx.clone(), s)?.into_value(),
        Json::Array(a) => {
            let arr = Array::new(ctx.clone())?;
            for (i, x) in a.iter().enumerate() {
                arr.set(i, to_js(ctx, x)?)?;
            }
            arr.into_value()
        }
        Json::Object(m) => {
            let o = Object::new(ctx.clone())?;
            for (k, x) in m {
                o.set(k.as_str(), to_js(ctx, x)?)?;
            }
            o.into_value()
        }
    })
}

/// A JavaScript value as JSON, as Go exports it (`goja.Value.Export`): undefined and functions as null, an integral
/// number as an integer.
pub fn to_json(v: &Value) -> Json {
    if let Some(b) = v.as_bool() {
        return Json::Bool(b);
    }
    if let Some(i) = v.as_int() {
        return Json::from(i);
    }
    if let Some(f) = v.as_float() {
        if f.fract() == 0.0 && f.abs() < 9.007_199_254_740_992e15 {
            return Json::from(f as i64);
        }
        return serde_json::Number::from_f64(f).map_or(Json::Null, Json::Number);
    }
    if let Some(s) = v.as_string() {
        return Json::String(s.to_string().unwrap_or_default());
    }
    if let Some(a) = v.as_array() {
        return Json::Array(a.iter::<Value>().filter_map(Result::ok).map(|x| to_json(&x)).collect());
    }
    if v.is_function() {
        return Json::Null;
    }
    if let Some(o) = v.as_object() {
        let mut m = Map::new();
        for (k, x) in o.props::<String, Value>().filter_map(Result::ok) {
            m.insert(k, to_json(&x));
        }
        return Json::Object(m);
    }
    Json::Null
}

/// A string quoted as Go's `%q` of a plain string.
fn go_quote(s: &str) -> String {
    serde_json::to_string(s).unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    fn bound(typ: &str, code: &str) -> Bound {
        Bound {
            instance: "i".into(),
            algorithm: "a".into(),
            typ: typ.into(),
            language: JAVASCRIPT.into(),
            code: code.into(),
            params: json!({"max": 3}).as_object().cloned().unwrap(),
            property: "size".into(),
        }
    }

    #[test]
    fn validates_a_property() {
        let b = bound(
            PROPERTY_VALIDATOR,
            "if (ctx.value() > ctx.param('max')) ctx.fail('too big: ' + ctx.value());",
        );
        let mut inp = Input {
            value: Some(json!(5)),
            ..Input::default()
        };
        assert_eq!(run_algorithm(&b, &inp).unwrap().failures, vec!["too big: 5"]);
        inp.value = Some(json!(2));
        assert!(run_algorithm(&b, &inp).unwrap().ok());
    }

    #[test]
    fn rejects_by_returning() {
        let inp = Input::default();
        let out = run_algorithm(
            &bound(NODE_VALIDATOR, "return ctx.node().key === '' ? 'no key' : true;"),
            &inp,
        );
        assert_eq!(out.unwrap().failures, vec!["no key"]);
        let out = run_algorithm(&bound(TRANSITION_GUARD, "return false;"), &inp);
        assert_eq!(out.unwrap().failures, vec!["rejected"]);
    }

    #[test]
    fn sets_properties_in_an_action() {
        let b = bound(
            TRANSITION_ACTION,
            "ctx.setProp('done', ctx.transition().to); ctx.setProp('n', 1.5); ctx.removeProp('old'); ctx.log('x', 1);",
        );
        let inp = Input {
            transition: ["approve".into(), "draft".into(), "done".into()],
            ..Input::default()
        };
        let out = run_algorithm(&b, &inp).unwrap();
        assert_eq!(Json::Object(out.set), json!({"done": "done", "n": 1.5}));
        assert_eq!(out.unset, vec!["old"]);
    }

    #[test]
    fn reports_a_script_failing() {
        let err = run_algorithm(&bound(NODE_VALIDATOR, "throw new Error('boom');"), &Input::default()).unwrap_err();
        assert!(err.starts_with("algorithm i: Error: boom"), "{err}");
        let err = run_algorithm(&bound(NODE_VALIDATOR, "while (true) {}"), &Input::default()).unwrap_err();
        assert_eq!(err, "algorithm i: script interrupted: context deadline exceeded");
        assert!(check_code(JAVASCRIPT, "if (").is_err());
        assert!(check_code(JAVASCRIPT, "return 1;").is_ok());
    }
}
