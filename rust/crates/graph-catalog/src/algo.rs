//! The algorithms of a domain and their instances (`contracts/algo`, ADR 0018): an instance resolved with its
//! algorithm, its parameter values checked and defaulted, is what the graph runs ([`Bound`]).
use serde::Deserialize;
use serde_json::{Map, Value};

pub use graph_script::js::Bound;

pub const PROPERTY_VALIDATOR: &str = graph_script::js::PROPERTY_VALIDATOR;
pub const NODE_VALIDATOR: &str = graph_script::js::NODE_VALIDATOR;
pub const LINK_VALIDATOR: &str = graph_script::js::LINK_VALIDATOR;
pub const TRANSITION_GUARD: &str = graph_script::js::TRANSITION_GUARD;
pub const TRANSITION_ACTION: &str = graph_script::js::TRANSITION_ACTION;

/// A declared parameter of an algorithm.
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct Param {
    pub name: String,
    #[serde(rename = "type")]
    pub typ: String,
    pub required: bool,
    pub default: Option<Value>,
    pub values: Option<Vec<String>>,
}

/// A reusable script of a fixed usage with declared parameters.
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct Algorithm {
    pub name: String,
    #[serde(rename = "type")]
    pub typ: String,
    pub language: String,
    pub code: String,
    pub params: Option<Vec<Param>>,
}

/// The parameter values of an algorithm, what a domain plugs.
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct Instance {
    pub name: String,
    pub algorithm: String,
    pub values: Option<Map<String, Value>>,
}

/// The algorithms and instances of a domain (`algo.Set`).
#[derive(Debug, Clone, Default)]
pub struct Set {
    pub algorithms: Vec<Algorithm>,
    pub instances: Vec<Instance>,
}

impl Set {
    /// The set of a domain, from the JSON values its definition keeps (an entry that does not read is left out, as
    /// one Go's decoder would have refused the whole domain for: the registry never stores one).
    pub fn of(algorithms: &[Value], instances: &[Value]) -> Set {
        Set {
            algorithms: algorithms
                .iter()
                .filter_map(|v| serde_json::from_value(v.clone()).ok())
                .collect(),
            instances: instances
                .iter()
                .filter_map(|v| serde_json::from_value(v.clone()).ok())
                .collect(),
        }
    }

    /// Resolves an instance that must be of the usage `want` (`Set.Bind`).
    pub fn bind(&self, name: &str, want: &str) -> Result<Bound, String> {
        let inst = self
            .instances
            .iter()
            .find(|i| i.name == name)
            .ok_or_else(|| format!("unknown algorithm instance {name}"))?;
        let alg = self
            .algorithms
            .iter()
            .find(|a| a.name == inst.algorithm)
            .ok_or_else(|| format!("instance {name}: unknown algorithm {}", inst.algorithm))?;
        if alg.typ != want {
            return Err(format!("instance {name} is of type {}, expected {want}", alg.typ));
        }
        let (params, issues) = alg.resolve(inst.values.as_ref());
        if let Some(i) = issues.first() {
            return Err(format!("instance {name}: {i}"));
        }
        Ok(Bound {
            instance: name.to_string(),
            algorithm: alg.name.clone(),
            typ: alg.typ.clone(),
            language: alg.language.clone(),
            code: alg.code.clone(),
            params,
            property: String::new(),
        })
    }

    /// The instances named that resolve as the usage `want`, in order; the others are left out (validation reports
    /// them).
    pub fn bind_all(&self, names: &[String], want: &str, property: &str) -> Vec<Bound> {
        names
            .iter()
            .filter_map(|n| self.bind(n, want).ok())
            .map(|mut b| {
                b.property = property.to_string();
                b
            })
            .collect()
    }
}

/// The usages an algorithm may have (`algo.Usages`); `action` is known but has no algorithms.
const USAGES: [&str; 6] = [
    "action",
    PROPERTY_VALIDATOR,
    NODE_VALIDATOR,
    LINK_VALIDATOR,
    TRANSITION_GUARD,
    TRANSITION_ACTION,
];

/// The types of a parameter (`algo.ParamTypes`).
const PARAM_TYPES: [&str; 7] = ["string", "number", "boolean", "regex", "enum", "strings", "json"];

fn valid_name(s: &str) -> bool {
    let mut c = s.chars();
    c.next().is_some_and(|f| f.is_ascii_lowercase())
        && c.all(|x| x.is_ascii_lowercase() || x.is_ascii_digit() || x == '-' || x == '_')
}

fn valid_param_name(s: &str) -> bool {
    let mut c = s.chars();
    c.next().is_some_and(|f| f.is_ascii_alphabetic()) && c.all(|x| x.is_ascii_alphanumeric() || x == '_')
}

impl Algorithm {
    /// What is wrong with the declaration of an algorithm, not its script (`Algorithm.Issues`).
    pub fn issues(&self) -> Vec<String> {
        let mut out = Vec::new();
        if !USAGES.contains(&self.typ.as_str()) {
            out.push(format!("unknown type {}", quote(&self.typ)));
        } else if self.typ == "action" {
            out.push(format!(
                "type {} has no algorithms: the code stays in the action declaration",
                self.typ
            ));
        }
        if self.name.is_empty() {
            out.push("name required".into());
        } else if !valid_name(&self.name) {
            out.push("name must be lowercase letters, digits, '-' or '_' and start with a letter".into());
        }
        if self.language != graph_script::js::JAVASCRIPT {
            out.push("language must be javascript".into());
        }
        if self.code.is_empty() {
            out.push("code required".into());
        }
        let mut seen = std::collections::HashSet::new();
        for (i, p) in self.params.as_deref().unwrap_or_default().iter().enumerate() {
            if p.name.is_empty() {
                out.push(format!("param {i} has no name"));
                continue;
            } else if !valid_param_name(&p.name) {
                out.push(format!(
                    "param {}: name must be letters, digits or '_' and start with a letter",
                    p.name
                ));
            } else if seen.contains(&p.name) {
                out.push(format!("duplicate param {}", p.name));
            }
            seen.insert(p.name.clone());
            if !PARAM_TYPES.contains(&p.typ.as_str()) {
                out.push(format!("param {}: unknown type {}", p.name, quote(&p.typ)));
                continue;
            }
            if p.typ == "enum" && p.values.as_ref().is_none_or(Vec::is_empty) {
                out.push(format!("param {}: an enum needs values", p.name));
            }
            if let Some(d) = p.default.as_ref().filter(|d| !d.is_null())
                && let Err(e) = p.coerce(d)
            {
                out.push(format!("param {}: default: {e}", p.name));
            }
        }
        out
    }

    /// The parameter values an instance gives the script (`Algorithm.Resolve`): checked against the declared
    /// params, defaults filled in; the issues sorted.
    pub fn resolve(&self, values: Option<&Map<String, Value>>) -> (Map<String, Value>, Vec<String>) {
        let empty = Map::new();
        let values = values.unwrap_or(&empty);
        let mut issues = Vec::new();
        let mut out = Map::new();
        let params = self.params.as_deref().unwrap_or_default();
        for p in params {
            let mut v = values.get(&p.name).filter(|v| !v.is_null());
            if v.is_none() {
                v = p.default.as_ref().filter(|v| !v.is_null());
            }
            let Some(v) = v else {
                if p.required {
                    issues.push(format!("param {} is required", p.name));
                }
                continue;
            };
            match p.coerce(v) {
                Ok(c) => {
                    out.insert(p.name.clone(), c);
                }
                Err(e) => issues.push(format!("param {}: {e}", p.name)),
            }
        }
        for k in values.keys() {
            if !params.iter().any(|p| &p.name == k) {
                issues.push(format!("unknown param {k}"));
            }
        }
        issues.sort();
        (out, issues)
    }
}

fn quote(s: &str) -> String {
    serde_json::to_string(s).unwrap_or_default()
}

/// A value as Go's `%T` names its type once decoded from JSON.
fn go_type(v: &Value) -> &'static str {
    match v {
        Value::Null => "<nil>",
        Value::Bool(_) => "bool",
        Value::Number(_) => "float64",
        Value::String(_) => "string",
        Value::Array(_) => "[]interface {}",
        Value::Object(_) => "map[string]interface {}",
    }
}

/// A value as Go's `%v` prints one decoded from JSON.
fn go_value(v: &Value) -> String {
    match v {
        Value::Null => "<nil>".into(),
        Value::String(s) => s.clone(),
        Value::Number(n) => n.as_f64().map(go_float).unwrap_or_else(|| n.to_string()),
        Value::Bool(b) => b.to_string(),
        Value::Array(a) => format!("[{}]", a.iter().map(go_value).collect::<Vec<_>>().join(" ")),
        Value::Object(m) => {
            let mut ks: Vec<&String> = m.keys().collect();
            ks.sort();
            let parts: Vec<String> = ks.iter().map(|k| format!("{k}:{}", go_value(&m[*k]))).collect();
            format!("map[{}]", parts.join(" "))
        }
    }
}

/// A float64 as Go's `%v` prints it (`strconv.FormatFloat(f, 'g', -1, 64)`, exponent past 21 digits).
fn go_float(f: f64) -> String {
    if f.fract() == 0.0 && f.abs() < 1e21 {
        return format!("{f:.0}");
    }
    let s = format!("{f}");
    if s.len() > 21 { format!("{f:e}") } else { s }
}

impl Param {
    /// A value of the parameter in the type its script gets (`Param.Coerce`).
    pub fn coerce(&self, v: &Value) -> Result<Value, String> {
        match self.typ.as_str() {
            "string" => match v {
                Value::String(_) => Ok(v.clone()),
                _ => Err(format!("expected a string, got {}", go_type(v))),
            },
            "regex" => match v {
                Value::String(s) => regex::Regex::new(s)
                    .map(|_| v.clone())
                    .map_err(|e| format!("invalid regular expression: {e}")),
                _ => Err(format!("expected a regular expression, got {}", go_type(v))),
            },
            "number" => match v {
                Value::Number(n) => Ok(Value::from(n.as_f64().unwrap_or(0.0))),
                Value::String(s) => s
                    .trim()
                    .parse::<f64>()
                    .ok()
                    .filter(|_| s.trim() == s)
                    .map(Value::from)
                    .ok_or_else(|| format!("expected a number, got {}", go_value(v))),
                _ => Err(format!("expected a number, got {}", go_value(v))),
            },
            "boolean" => match v {
                Value::Bool(_) => Ok(v.clone()),
                Value::String(s) => match s.as_str() {
                    "1" | "t" | "T" | "TRUE" | "true" | "True" => Ok(Value::Bool(true)),
                    "0" | "f" | "F" | "FALSE" | "false" | "False" => Ok(Value::Bool(false)),
                    _ => Err(format!("expected a boolean, got {}", go_value(v))),
                },
                _ => Err(format!("expected a boolean, got {}", go_value(v))),
            },
            "enum" => {
                let values = self.values.as_deref().unwrap_or_default();
                match v {
                    Value::String(s) if values.contains(s) => Ok(v.clone()),
                    _ => Err(format!("expected one of [{}], got {}", values.join(" "), go_value(v))),
                }
            }
            "strings" => match v {
                Value::Array(a) => {
                    for x in a {
                        if !x.is_string() {
                            return Err(format!("expected a list of strings, got element {}", go_value(x)));
                        }
                    }
                    Ok(v.clone())
                }
                _ => Err(format!("expected a list of strings, got {}", go_type(v))),
            },
            "json" => Ok(v.clone()),
            t => Err(format!("unknown param type {}", quote(t))),
        }
    }
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    #[test]
    fn binds_an_instance() {
        let set = Set::of(
            &[
                json!({"name": "max-len", "type": "property_validator", "language": "javascript", "code": "return true",
                "params": [{"name": "max", "type": "number", "required": true}, {"name": "re", "type": "regex", "default": "^a"}]}),
            ],
            &[
                json!({"name": "short", "algorithm": "max-len", "values": {"max": "8"}}),
                json!({"name": "broken", "algorithm": "max-len", "values": {"other": 1}}),
            ],
        );
        let b = set.bind("short", PROPERTY_VALIDATOR).unwrap();
        assert_eq!(b.params, json!({"max": 8.0, "re": "^a"}).as_object().cloned().unwrap());
        assert_eq!(
            set.bind("short", NODE_VALIDATOR).unwrap_err(),
            "instance short is of type property_validator, expected node_validator"
        );
        assert_eq!(
            set.bind("broken", PROPERTY_VALIDATOR).unwrap_err(),
            "instance broken: param max is required"
        );
        assert_eq!(
            set.bind("nope", PROPERTY_VALIDATOR).unwrap_err(),
            "unknown algorithm instance nope"
        );
        let names = ["short".to_string(), "broken".to_string()];
        assert_eq!(set.bind_all(&names, PROPERTY_VALIDATOR, "title")[0].property, "title");
        assert_eq!(set.bind_all(&names, PROPERTY_VALIDATOR, "title").len(), 1);
    }
}
