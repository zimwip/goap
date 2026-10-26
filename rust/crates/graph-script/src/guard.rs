//! The CEL guard of a lifecycle transition (Go's `lib/guard`): a boolean expression over the node being moved, the
//! nodes a document contains, the change, the impact of the node and its draft.
use cel::{Context, Program, Value};
use serde_json::{Map, Value as Json};

/// A compiled guard (empty: always true).
pub struct Guard {
    prog: Option<Program>,
}

/// The variables a node transition guard reads (Go's `Guard.Check`); the ones of a change transition are empty.
#[derive(Debug, Clone, Default)]
pub struct Env {
    pub node: Map<String, Json>,
    pub children: Vec<Json>,
    pub change: Map<String, Json>,
    pub impact: Map<String, Json>,
    pub draft: Map<String, Json>,
}

impl Guard {
    /// Compiles a guard.
    pub fn compile(expr: &str) -> Result<Guard, String> {
        if expr.is_empty() {
            return Ok(Guard { prog: None });
        }
        Program::compile(expr)
            .map(|p| Guard { prog: Some(p) })
            .map_err(|e| e.to_string())
    }

    /// Evaluates the guard of a node transition: an evaluation error is an error, not a false.
    pub fn check(&self, env: &Env) -> Result<bool, String> {
        let Some(prog) = &self.prog else { return Ok(true) };
        let mut ctx = Context::default();
        let obj = |m: &Map<String, Json>| Json::Object(m.clone());
        let lists = [
            "decisionPoints",
            "actions",
            "risks",
            "verifications",
            "derogations",
            "dependencies",
            "assessments",
        ];
        let vars: Vec<(&str, Json)> = vec![
            ("node", obj(&env.node)),
            ("children", Json::Array(env.children.clone())),
            ("change", obj(&env.change)),
            ("impact", obj(&env.impact)),
            ("draft", obj(&env.draft)),
            ("criticalityPolicy", Json::Object(Map::new())),
            ("world", Json::Object(Map::new())),
            (
                "integrator",
                serde_json::json!({"attached": false, "id": "", "methodology": "", "state": "", "status": ""}),
            ),
            ("history", serde_json::json!({"runs": [], "steps": [], "last": {}})),
        ];
        for (k, v) in vars {
            ctx.add_variable_from_value(k, value(&v));
        }
        for k in lists {
            ctx.add_variable_from_value(k, value(&Json::Array(Vec::new())));
        }
        match prog.execute(&ctx) {
            Ok(Value::Bool(b)) => Ok(b),
            Ok(_) => Err("guard did not return a bool".into()),
            Err(e) => Err(go_message(&e.to_string())),
        }
    }
}

/// An evaluation error as cel-go words it: its messages start in lower case (`no such key: x`).
pub(crate) fn go_message(m: &str) -> String {
    let mut c = m.chars();
    match c.next() {
        Some(f) => f.to_lowercase().chain(c).collect(),
        None => String::new(),
    }
}

/// A JSON value as a CEL one; a number is a double, as cel-go reads one from a decoded JSON value, an integer kept
/// an int where it is one.
pub(crate) fn value(v: &Json) -> Value {
    match v {
        Json::Null => Value::Null,
        Json::Bool(b) => Value::Bool(*b),
        Json::Number(n) => match n.as_i64() {
            Some(i) if !n.is_f64() => Value::Int(i),
            _ => Value::Float(n.as_f64().unwrap_or(0.0)),
        },
        Json::String(s) => Value::String(s.clone().into()),
        Json::Array(a) => Value::List(a.iter().map(value).collect::<Vec<_>>().into()),
        Json::Object(m) => {
            let map: std::collections::HashMap<String, Value> = m.iter().map(|(k, x)| (k.clone(), value(x))).collect();
            Value::Map(map.into())
        }
    }
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    #[test]
    fn checks_a_node_transition() {
        let g = Guard::compile("node.props.owner != '' && impact.review == 'accepted'").unwrap();
        let mut env = Env {
            node: json!({"key": "D", "props": {"owner": "x"}})
                .as_object()
                .cloned()
                .unwrap(),
            impact: json!({"review": "accepted"}).as_object().cloned().unwrap(),
            ..Env::default()
        };
        assert_eq!(g.check(&env), Ok(true));
        env.impact = json!({"review": "proposed"}).as_object().cloned().unwrap();
        assert_eq!(g.check(&env), Ok(false));
        assert!(Guard::compile("").unwrap().check(&env).unwrap());
        assert!(Guard::compile("node.nope == 1").unwrap().check(&env).is_err());
    }
}
