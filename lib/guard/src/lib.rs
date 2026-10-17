//! Compiles and evaluates the guards of node lifecycle transitions (ADR 0014).
use std::{
    collections::BTreeMap,
    sync::{Arc, LazyLock},
};

use cel::{Context, Env, Program, Value as CelValue};
use serde_json::{Map, Value};

mod sets;

/// A JSON object, Go's `map[string]any`.
pub type Object = Map<String, Value>;

/// The variables a guard reads.
pub const VARIABLES: [&str; 16] = [
    "node",
    "children",
    "change",
    "impact",
    "draft",
    "decisionPoints",
    "actions",
    "risks",
    "verifications",
    "derogations",
    "criticalityPolicy",
    "world",
    "integrator",
    "dependencies",
    "history",
    "assessments",
];

/// The namespaces of the functions of the environment (`sets.contains`), which the references list as variables.
const NAMESPACES: [&str; 5] = ["sets", "lists", "strings", "math", "cel"];

/// The errors of a guard.
#[derive(Debug, Clone, PartialEq, thiserror::Error)]
pub enum GuardError {
    /// The expression does not parse, or names a variable no guard has.
    #[error("{0}")]
    Compile(String),
    /// The evaluation failed (an evaluation error is an error, not a false).
    #[error("{0}")]
    Eval(String),
    #[error("guard did not return a bool")]
    NotBool,
}

/// The environment of the guards: the standard library and cel-go's `ext.Strings()`, `ext.Lists()`, `ext.Sets()`.
static ENV: LazyLock<Arc<Env>> = LazyLock::new(|| {
    let mut env = Env::stdlib();
    env.add_extension(cel::extensions::strings)
        .expect("strings extension");
    env.add_extension(cel::extensions::lists)
        .expect("lists extension");
    env.add_extension(sets::extension).expect("sets extension");
    Arc::new(env)
});

/// The CEL environment of the guards, for the packages that evaluate the same language.
pub fn env() -> Arc<Env> {
    ENV.clone()
}

/// A compiled lifecycle transition guard: a boolean CEL expression over the node being moved (`node`: key, type,
/// state, props; the state it goes to), the nodes a document contains (`children`, same shape), the change
/// (`change`), the impact of the node in the change (`impact`: intent, review, reviews; ADR 0076: a transition that
/// requires a review checks it there) and the draft of the node (`draft`: key, type, state, props, owner, links,
/// base, new; ADR 0079: the node has no version while the change works, the draft is what the move acts on, in the
/// state it goes to).
///
/// The guard of a transition of the lifecycle of a change (ADR 0058) is the same language: it sees `change` and, as
/// the conditions of a methodology do, the decision points of the change (`decisionPoints`) and the world state
/// (`world`: condition name to bool); `node` and `children` are empty then. The guardian of an outer methodology of
/// the guardian chain of a change (ADR 0106) sees `integrator` too: the change of that methodology the change is
/// attached to (`attached`, `id`, `methodology`, `state`, `status`; `attached` false and the rest empty when it is
/// not). It sees the changes the change depends on (`dependencies`, ADR 0108) and its history (`history`, ADR 0109)
/// as the conditions do.
///
/// Unlike cel-go, the CEL of the port has no type checker: a guard is refused at compile time when it does not parse
/// or names a variable that is not one of [`VARIABLES`], and one that does not return a bool fails when it is
/// evaluated ([`GuardError::NotBool`]); there is no cost limit.
#[derive(Clone, Default)]
pub struct Guard {
    prog: Option<Arc<Program>>,
}

impl std::fmt::Debug for Guard {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Guard")
            .field("empty", &self.prog.is_none())
            .finish()
    }
}

/// Compiles a transition guard (empty: always true).
pub fn compile(expr: &str) -> Result<Guard, GuardError> {
    if expr.trim().is_empty() {
        return Ok(Guard::default());
    }
    let prog = ENV
        .compile(expr)
        .map_err(|e| GuardError::Compile(e.to_string()))?;
    let refs = prog.references();
    for v in refs.variables() {
        if !VARIABLES.contains(&v) && !NAMESPACES.contains(&v) && !is_bound_in(expr, v) {
            return Err(GuardError::Compile(format!(
                "undeclared reference to '{v}'"
            )));
        }
    }
    Ok(Guard {
        prog: Some(Arc::new(prog)),
    })
}

/// Whether `v` is the variable of a comprehension of `expr` (`list.exists(v, ...)`), which the references list too.
fn is_bound_in(expr: &str, v: &str) -> bool {
    [
        "all",
        "exists",
        "exists_one",
        "existsOne",
        "map",
        "filter",
        "transformList",
        "transformMap",
        "sortBy",
    ]
    .iter()
    .any(|m| {
        let mut rest = expr;
        while let Some(i) = rest.find(&format!("{m}(")) {
            rest = &rest[i + m.len() + 1..];
            let first = rest.split([',', ')']).next().unwrap_or("").trim();
            if first == v {
                return true;
            }
        }
        false
    }) || expr.contains(&format!("cel.bind({v},"))
}

/// What the guard of a change transition reads of the blackboard besides the change.
#[derive(Debug, Clone, Default)]
pub struct Facts {
    pub decision_points: Vec<Value>,
    pub actions: Vec<Value>,
    pub risks: Vec<Value>,
    /// The verification states of the change (ADR 0075).
    pub verifications: Vec<Value>,
    /// The derogations of the change (ADR 0075).
    pub derogations: Vec<Value>,
    /// What the organisation requires of its criticality (None: `{}`).
    pub criticality_policy: Option<Object>,
    /// The impact of the moved node in the change (node transitions).
    pub impact: Option<Object>,
    /// The draft of the moved node (node transitions).
    pub draft: Option<Object>,
    /// The change of an outer methodology of the guardian chain the change is attached to (ADR 0106); None:
    /// [`no_integrator`].
    pub integrator: Option<Object>,
    /// The changes the change depends on (ADR 0108), as `dependencies`.
    pub dependencies: Vec<Value>,
    /// The ratings of the change (ADR 0110).
    pub assessments: Vec<Value>,
    /// The history of the change (ADR 0109 §4), as `history`: {runs, steps, last}; None: nothing ran.
    pub history: Option<Object>,
}

/// What `history` holds for a change on which nothing ran.
pub fn no_history() -> Object {
    let mut m = Map::new();
    m.insert("runs".into(), Value::Array(vec![]));
    m.insert("steps".into(), Value::Array(vec![]));
    m.insert("last".into(), Value::Object(Map::new()));
    m
}

/// What `integrator` holds when the change is attached to no change of the guardian being asked.
pub fn no_integrator() -> Object {
    let mut m = Map::new();
    m.insert("attached".into(), Value::Bool(false));
    for k in ["id", "methodology", "state", "status"] {
        m.insert(k.into(), Value::String(String::new()));
    }
    m
}

impl Guard {
    /// Whether the guard is empty (always true).
    pub fn is_empty(&self) -> bool {
        self.prog.is_none()
    }

    /// Evaluates the guard of a node transition; `impact` is the impact of the node in the change (None: none) and
    /// `draft` its draft (None: none). An evaluation error is an error, not a false.
    pub fn check(
        &self,
        node: &Object,
        children: &[Value],
        change: &Object,
        impact: Option<&Object>,
        draft: Option<&Object>,
    ) -> Result<bool, GuardError> {
        let f = Facts {
            impact: impact.cloned(),
            draft: draft.cloned(),
            ..Default::default()
        };
        self.eval(node, children, change, &f, None)
    }

    /// Evaluates the guard of a transition of the lifecycle of a change (ADR 0058).
    pub fn check_change(
        &self,
        change: &Object,
        f: &Facts,
        world: &BTreeMap<String, bool>,
    ) -> Result<bool, GuardError> {
        let w: Object = world
            .iter()
            .map(|(k, v)| (k.clone(), Value::Bool(*v)))
            .collect();
        self.eval(&Map::new(), &[], change, f, Some(&w))
    }

    fn eval(
        &self,
        node: &Object,
        children: &[Value],
        change: &Object,
        f: &Facts,
        world: Option<&Object>,
    ) -> Result<bool, GuardError> {
        let Some(prog) = &self.prog else {
            return Ok(true);
        };
        let empty = Map::new();
        let integrator = f.integrator.clone().unwrap_or_else(no_integrator);
        let history = f.history.clone().unwrap_or_else(no_history);
        let mut ctx = Context::with_env(ENV.clone());
        let mut add = |name: &str, v: Value| -> Result<(), GuardError> {
            ctx.add_variable_from_value(name, json_to_cel(&v));
            Ok(())
        };
        add("integrator", Value::Object(integrator))?;
        add("node", Value::Object(node.clone()))?;
        add("children", Value::Array(children.to_vec()))?;
        add("change", Value::Object(change.clone()))?;
        add(
            "impact",
            Value::Object(f.impact.clone().unwrap_or_default()),
        )?;
        add("draft", Value::Object(f.draft.clone().unwrap_or_default()))?;
        add("decisionPoints", Value::Array(f.decision_points.clone()))?;
        add("actions", Value::Array(f.actions.clone()))?;
        add("risks", Value::Array(f.risks.clone()))?;
        add("verifications", Value::Array(f.verifications.clone()))?;
        add("derogations", Value::Array(f.derogations.clone()))?;
        add(
            "criticalityPolicy",
            Value::Object(f.criticality_policy.clone().unwrap_or_default()),
        )?;
        add("world", Value::Object(world.unwrap_or(&empty).clone()))?;
        add("dependencies", Value::Array(f.dependencies.clone()))?;
        add("history", Value::Object(history))?;
        add("assessments", Value::Array(f.assessments.clone()))?;
        match prog.execute(&ctx) {
            Ok(CelValue::Bool(b)) => Ok(b),
            Ok(_) => Err(GuardError::NotBool),
            Err(e) => Err(GuardError::Eval(e.to_string())),
        }
    }
}

/// A JSON value as CEL sees it, as cel-go adapts an activation: a whole number is an int (an uint beyond the int
/// range), any other number a double. (Binding through serde would make every positive integer an uint, and
/// `x - 1` then fails on mixed uint / int operands.)
pub fn json_to_cel(v: &Value) -> CelValue {
    use std::{collections::HashMap, sync::Arc};
    match v {
        Value::Null => CelValue::Null,
        Value::Bool(b) => CelValue::Bool(*b),
        Value::Number(n) => {
            if let Some(i) = n.as_i64() {
                CelValue::Int(i)
            } else if let Some(u) = n.as_u64() {
                CelValue::UInt(u)
            } else {
                CelValue::Float(n.as_f64().unwrap_or(f64::NAN))
            }
        }
        Value::String(s) => CelValue::String(Arc::new(s.clone())),
        Value::Array(a) => CelValue::List(Arc::new(a.iter().map(json_to_cel).collect())),
        Value::Object(m) => {
            let map: HashMap<cel::objects::Key, CelValue> = m
                .iter()
                .map(|(k, v)| {
                    (
                        cel::objects::Key::String(Arc::new(k.clone())),
                        json_to_cel(v),
                    )
                })
                .collect();
            CelValue::Map(cel::objects::Map { map: Arc::new(map) })
        }
    }
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    fn obj(v: Value) -> Object {
        match v {
            Value::Object(m) => m,
            _ => panic!("not an object"),
        }
    }

    #[test]
    fn whole_numbers_are_ints() {
        let g = compile("criticalityPolicy.maxDerogationHours - 1 == 335").unwrap();
        let f = Facts {
            criticality_policy: Some(obj(json!({"maxDerogationHours": 336}))),
            ..Default::default()
        };
        assert!(g.check_change(&Map::new(), &f, &BTreeMap::new()).unwrap());
    }

    #[test]
    fn empty_guard_is_true() {
        let g = compile("").unwrap();
        assert!(g.is_empty());
        assert!(g.check(&Map::new(), &[], &Map::new(), None, None).unwrap());
    }

    #[test]
    fn node_guard_reads_the_impact_and_the_draft() {
        let g = compile(r#"impact.review == "accepted" && draft.props.title != """#).unwrap();
        let node =
            obj(json!({"key": "R1", "type": "alm@Requirement", "state": "approved", "props": {}}));
        let impact = obj(json!({"review": "accepted"}));
        let draft = obj(json!({"props": {"title": "T"}}));
        assert!(
            g.check(&node, &[], &Map::new(), Some(&impact), Some(&draft))
                .unwrap()
        );
        let proposed = obj(json!({"review": "proposed"}));
        assert!(
            !g.check(&node, &[], &Map::new(), Some(&proposed), Some(&draft))
                .unwrap()
        );
        // no impact: the map is empty, reading a missing key is an error, not a false
        assert!(matches!(
            g.check(&node, &[], &Map::new(), None, None),
            Err(GuardError::Eval(_))
        ));
    }

    #[test]
    fn children_and_macros() {
        let g = compile(r#"children.all(c, c.state == "done") && size(children) > 1"#).unwrap();
        let done = vec![json!({"state": "done"}), json!({"state": "done"})];
        assert!(
            g.check(&Map::new(), &done, &Map::new(), None, None)
                .unwrap()
        );
        let mixed = vec![json!({"state": "done"}), json!({"state": "open"})];
        assert!(
            !g.check(&Map::new(), &mixed, &Map::new(), None, None)
                .unwrap()
        );
    }

    #[test]
    fn change_guard_sees_world_facts_and_defaults() {
        let g = compile(
            r#"world.ready && decisionPoints.exists(d, d.status == "decided") && !integrator.attached && size(history.runs) == 0 && size(risks) == 0"#,
        )
        .unwrap();
        let f = Facts {
            decision_points: vec![json!({"status": "decided"})],
            ..Default::default()
        };
        let world = BTreeMap::from([("ready".to_string(), true)]);
        assert!(
            g.check_change(&obj(json!({"id": "C1"})), &f, &world)
                .unwrap()
        );
        let world = BTreeMap::from([("ready".to_string(), false)]);
        assert!(
            !g.check_change(&obj(json!({"id": "C1"})), &f, &world)
                .unwrap()
        );
        let attached = Facts {
            integrator: Some(obj(json!({"attached": true}))),
            ..f
        };
        let world = BTreeMap::from([("ready".to_string(), true)]);
        assert!(!g.check_change(&Map::new(), &attached, &world).unwrap());
    }

    #[test]
    fn extensions_of_the_environment() {
        let g = compile(r#"change.title.lowerAscii().startsWith("fix") && sets.contains(change.tags, ["a"]) && change.tags.distinct().size() == 2"#).unwrap();
        let change = obj(json!({"title": "FIX it", "tags": ["a", "b", "a"]}));
        assert!(g.check(&Map::new(), &[], &change, None, None).unwrap());
        let g = compile(
            r#"sets.intersects(change.tags, ["z"]) || sets.equivalent(change.tags, ["b", "a"])"#,
        )
        .unwrap();
        assert!(g.check(&Map::new(), &[], &change, None, None).unwrap());
    }

    #[test]
    fn refusals() {
        assert!(matches!(
            compile("node.state =="),
            Err(GuardError::Compile(_))
        ));
        assert!(matches!(
            compile("unknown.x == 1"),
            Err(GuardError::Compile(_))
        ));
        assert!(compile("children.exists(x, x.state == 'a')").is_ok());
        let g = compile(r#"change.id"#).unwrap();
        assert_eq!(
            g.check(&Map::new(), &[], &obj(json!({"id": "C"})), None, None),
            Err(GuardError::NotBool)
        );
    }

    #[test]
    fn defaults() {
        assert_eq!(
            Value::Object(no_history()),
            json!({"runs": [], "steps": [], "last": {}})
        );
        assert_eq!(
            Value::Object(no_integrator()),
            json!({"attached": false, "id": "", "methodology": "", "state": "", "status": ""})
        );
    }
}
