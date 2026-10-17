//! The algorithm usages of the DSL (ADR 0018) share the script engine of the actions but each one runs in its own
//! context object, which only offers what its extension point needs:
//!
//! A JavaScript algorithm is the body of a function of ctx, so it may `return`.
//!
//! | usage | ctx |
//! |---|---|
//! | property_validator | ValidatorCtx: param, instance, algorithm, property, value, node, fail, log, warn |
//! | node_validator | NodeValidatorCtx: param, instance, algorithm, node, fail, log, warn |
//! | link_validator | LinkValidatorCtx: param, instance, algorithm, link, from, to, fail, log, warn |
//! | transition_guard | GuardCtx: param, instance, algorithm, node, children, change, transition, impact, fail, log, warn |
//! | transition_action | TransitionCtx: param, instance, algorithm, node, children, change, transition, setProp, removeProp, fail, log, warn |
//!
//! They are pure: no LLM, tool or sub-agent calls, no blackboard. A script rejects with ctx.fail(message), by
//! throwing, or by returning false or a string from its body.

use std::{sync::Arc, time::Duration};

use goap_algo::{
    Bound, USAGE_LINK_VALIDATOR, USAGE_NODE_VALIDATOR, USAGE_PROPERTY_VALIDATOR,
    USAGE_TRANSITION_ACTION, USAGE_TRANSITION_GUARD, Usage,
};
use parking_lot::Mutex;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{
    Error,
    bridge::Bridge,
    dsl::{LogLine, Node, null_default},
    engine::{ScriptHost, ScriptSource, engine},
    run::{check_language, drive},
    values::{arg, arg_str, to_value},
};

/// AlgorithmTimeout bounds one execution of an algorithm.
pub const ALGORITHM_TIMEOUT: Duration = Duration::from_secs(5);

/// ChangeInfo describes the change a transition is taken in.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct ChangeInfo {
    #[serde(deserialize_with = "null_default")]
    pub id: String,
    #[serde(deserialize_with = "null_default")]
    pub title: String,
    #[serde(deserialize_with = "null_default")]
    pub intent: String,
    #[serde(deserialize_with = "null_default")]
    pub methodology: String,
    #[serde(deserialize_with = "null_default")]
    pub goal: String,
}

/// TransitionInfo describes the lifecycle transition being taken.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct TransitionInfo {
    #[serde(deserialize_with = "null_default")]
    pub name: String,
    #[serde(deserialize_with = "null_default")]
    pub from: String,
    #[serde(deserialize_with = "null_default")]
    pub to: String,
}

/// ImpactInfo is the impact of the moved node in the change (ADR 0076): a transition that requires a review checks
/// it.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct ImpactInfo {
    #[serde(deserialize_with = "null_default")]
    pub intent: String,
    /// Review is the review of the impact: proposed, accepted or rejected ("" when the change holds no impact on the
    /// node).
    #[serde(deserialize_with = "null_default")]
    pub review: String,
}

/// LinkInfo is the link a link validator checks: its type and properties.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct LinkInfo {
    #[serde(deserialize_with = "null_default")]
    pub r#type: String,
    #[serde(deserialize_with = "null_default")]
    pub props: Map<String, Value>,
}

/// AlgorithmInput is what an algorithm is run on. Only the fields of its usage are read.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct AlgorithmInput {
    /// Node is the node concerned (after the change for a validator; the moved node, in its new state, for a
    /// transition).
    pub node: Node,
    /// Property and Value: the property a validator checks (`Value::Null` when the node has none).
    pub property: String,
    pub value: Value,
    /// Link, From and To: the link a link validator checks and the nodes it leaves and reaches, as they will be.
    pub link: LinkInfo,
    pub from: Node,
    pub to: Node,
    /// Children: the nodes a document contains (transitions).
    pub children: Vec<Node>,
    pub change: ChangeInfo,
    pub transition: TransitionInfo,
    /// Impact is the impact of the moved node in the change (transitions).
    pub impact: ImpactInfo,
}

/// Outcome is the result of an algorithm.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Outcome {
    /// Failures are the reasons the algorithm rejected; empty: accepted.
    pub failures: Vec<String>,
    /// Set and Unset are the property changes of a transition action.
    pub set: Map<String, Value>,
    pub unset: Vec<String>,
    pub logs: Vec<LogLine>,
}

impl Outcome {
    /// OK tells whether the algorithm accepted.
    pub fn ok(&self) -> bool {
        self.failures.is_empty()
    }
}

/// The part of every algorithm context (adapters included).
#[derive(Default)]
pub(crate) struct Common {
    pub(crate) failures: Vec<String>,
    pub(crate) logs: Vec<LogLine>,
}

impl Common {
    pub(crate) fn logf(&mut self, level: &str, msg: String) {
        self.logs.push(LogLine::now(level, msg));
    }

    pub(crate) fn fail(&mut self, msg: String) {
        self.failures.push(msg);
    }
}

/// The parameter, instance and algorithm calls of every algorithm context, and the logs and failures.
pub(crate) fn common_call(
    b: &Bound,
    c: &Mutex<Common>,
    method: &str,
    a: &[Value],
) -> Option<Option<Value>> {
    Some(match method {
        "param" => Some(b.params.get(&arg_str(a, 0)).cloned().unwrap_or(Value::Null)),
        "instance" => Some(b.instance.clone().into()),
        "algorithm" => Some(b.algorithm.clone().into()),
        "fail" => {
            c.lock().fail(arg_str(a, 0));
            None
        }
        "log" => {
            c.lock().logf("info", arg_str(a, 0));
            None
        }
        "warn" => {
            c.lock().logf("warn", arg_str(a, 0));
            None
        }
        _ => return None,
    })
}

const VALIDATOR_METHODS: &[&str] = &[
    "param",
    "instance",
    "algorithm",
    "property",
    "value",
    "node",
    "fail",
    "log",
    "warn",
];
const NODE_VALIDATOR_METHODS: &[&str] = &[
    "param",
    "instance",
    "algorithm",
    "node",
    "fail",
    "log",
    "warn",
];
const LINK_VALIDATOR_METHODS: &[&str] = &[
    "param",
    "instance",
    "algorithm",
    "link",
    "from",
    "to",
    "fail",
    "log",
    "warn",
];
const GUARD_METHODS: &[&str] = &[
    "param",
    "instance",
    "algorithm",
    "node",
    "children",
    "change",
    "transition",
    "impact",
    "fail",
    "log",
    "warn",
];
const TRANSITION_METHODS: &[&str] = &[
    "param",
    "instance",
    "algorithm",
    "node",
    "children",
    "change",
    "transition",
    "setProp",
    "removeProp",
    "fail",
    "log",
    "warn",
];

/// The context of an algorithm: ValidatorCtx, NodeValidatorCtx, LinkValidatorCtx, GuardCtx or TransitionCtx,
/// by the methods of its usage.
struct AlgoCtx {
    b: Bound,
    methods: &'static [&'static str],
    input: AlgorithmInput,
    common: Mutex<Common>,
    /// The property changes of a transition action.
    changes: Mutex<(Map<String, Value>, Vec<String>)>,
}

impl ScriptHost for AlgoCtx {
    fn methods(&self) -> &'static [&'static str] {
        self.methods
    }

    fn call(&self, method: &str, a: &[Value]) -> Result<Option<Value>, String> {
        if !self.methods.contains(&method) {
            return Err(format!("ctx has no method {method}"));
        }
        if let Some(r) = common_call(&self.b, &self.common, method, a) {
            return Ok(r);
        }
        let i = &self.input;
        Ok(match method {
            "property" => Some(i.property.clone().into()),
            "value" => Some(i.value.clone()),
            "node" => Some(to_value(&i.node)),
            "link" => Some(to_value(&i.link)),
            "from" => Some(to_value(&i.from)),
            "to" => Some(to_value(&i.to)),
            "children" => Some(to_value(&i.children)),
            "change" => Some(to_value(&i.change)),
            "transition" => Some(to_value(&i.transition)),
            "impact" => Some(to_value(&i.impact)),
            "setProp" => {
                let name = arg_str(a, 0);
                let mut ch = self.changes.lock();
                ch.0.insert(name.clone(), arg(a, 1).clone());
                ch.1.retain(|x| *x != name);
                None
            }
            "removeProp" => {
                let name = arg_str(a, 0);
                let mut ch = self.changes.lock();
                ch.0.remove(&name);
                ch.1.retain(|x| *x != name);
                ch.1.push(name);
                None
            }
            _ => return Err(format!("ctx has no method {method}")),
        })
    }

    fn log(&self, level: &str, msg: String) {
        self.common.lock().logf(level, msg);
    }
}

fn methods_of(u: &Usage) -> Option<&'static [&'static str]> {
    Some(if *u == USAGE_PROPERTY_VALIDATOR {
        VALIDATOR_METHODS
    } else if *u == USAGE_NODE_VALIDATOR {
        NODE_VALIDATOR_METHODS
    } else if *u == USAGE_LINK_VALIDATOR {
        LINK_VALIDATOR_METHODS
    } else if *u == USAGE_TRANSITION_GUARD {
        GUARD_METHODS
    } else if *u == USAGE_TRANSITION_ACTION {
        TRANSITION_METHODS
    } else {
        return None;
    })
}

/// A JavaScript `run(ctx)` of an algorithm may reject by returning false or a message.
fn rejection(v: &Value) -> Option<String> {
    match v {
        Value::Bool(false) => Some("rejected".into()),
        Value::String(s) if !s.is_empty() => Some(s.clone()),
        _ => None,
    }
}

/// RunAlgorithm runs a bound algorithm on an input. The error is a failure of the script itself (does not compile,
/// throws, times out): callers treat it as a rejection too. A rejection by the algorithm is in Outcome.failures.
pub async fn run_algorithm(
    ctx: &goap_ctx::Ctx,
    b: &Bound,
    input: AlgorithmInput,
) -> Result<Outcome, Error> {
    let Some(methods) = methods_of(&b.r#type) else {
        return Err(Error::Invalid(format!(
            "algorithm {}: type {:?} cannot be run",
            b.instance,
            b.r#type.as_str()
        )));
    };
    let x = Arc::new(AlgoCtx {
        b: b.clone(),
        methods,
        input,
        common: Mutex::default(),
        changes: Mutex::default(),
    });
    let (_bridge, rx) = Bridge::new();
    let ran = drive(
        ctx,
        ALGORITHM_TIMEOUT,
        &b.language,
        js_body(&b.code),
        x.clone(),
        rx,
        |_, _| async { Err(Error::host("an algorithm calls nothing outside")) },
    )
    .await;
    let mut common = x.common.lock();
    if let Ok(Some(v)) = &ran
        && let Some(msg) = rejection(v)
    {
        common.fail(msg);
    }
    let (set, unset) = std::mem::take(&mut *x.changes.lock());
    let out = Outcome {
        failures: std::mem::take(&mut common.failures),
        set,
        unset,
        logs: std::mem::take(&mut common.logs),
    };
    match ran {
        Ok(_) => Ok(out),
        Err(e) => Err(Error::Algorithm {
            instance: b.instance.clone(),
            source: Box::new(e),
        }),
    }
}

/// jsBody makes the code of a JavaScript algorithm the body of run(ctx): it can use `return` (false or a message
/// rejects).
pub(crate) fn js_body(code: &str) -> String {
    format!("function run(ctx) {{\n{code}\n}}")
}

/// CheckAlgorithmCode checks that the code of an algorithm (or an adapter) compiles. It does not run it.
pub fn check_algorithm_code(language: &str, code: &str) -> Result<(), Error> {
    check_language(language)?;
    engine()
        .check(&ScriptSource::new(js_body(code)))
        .map_err(|e| Error::Script(e.to_string()))
}
