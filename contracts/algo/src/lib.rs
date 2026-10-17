//! Port of `contracts/algo` (see docs/rust-port.md).
//!
//! The algorithms of a domain (ADR 0018): scripts written in JavaScript or Go that plug into the model at fixed
//! extension points. An algorithm has a Type (one of the fixed usages, which decides the DSL context its script runs
//! in and the places it can be plugged), a script and a list of typed parameters; an Instance sets the parameter
//! values and is what gets plugged. The scripts run through pkg/dsl.

use std::{borrow::Cow, collections::BTreeSet, fmt, sync::LazyLock};

use regex::Regex;
use serde::{Deserialize, Deserializer, Serialize, Serializer};
use serde_json::{Map, Value};

/// The errors of the package: every one is a message (Go's `fmt.Errorf`).
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum Error {
    /// A value or a declaration that does not hold.
    #[error("{0}")]
    Invalid(String),
}

impl Error {
    fn invalid(msg: impl Into<String>) -> Self {
        Error::Invalid(msg.into())
    }
}

/// Usage is the fixed list of algorithm types: the extension points of the platform. It holds any text (a
/// declaration may name an unknown one, which [`Usage::known`] tells).
#[derive(Clone, Default, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct Usage(Cow<'static, str>);

impl Usage {
    /// A usage of a static name (the constants).
    pub const fn from_static(s: &'static str) -> Self {
        Usage(Cow::Borrowed(s))
    }

    /// A usage of any name.
    pub fn new(s: impl Into<String>) -> Self {
        Usage(Cow::Owned(s.into()))
    }

    /// The name of the usage.
    pub fn as_str(&self) -> &str {
        &self.0
    }

    /// Pluggable tells whether algorithms of this type can be declared and plugged.
    pub fn pluggable(&self) -> bool {
        *self != USAGE_ACTION && self.known()
    }

    /// Known tells whether u is one of the usages.
    pub fn known(&self) -> bool {
        usages().contains(self)
    }
}

impl fmt::Display for Usage {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl fmt::Debug for Usage {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        fmt::Debug::fmt(&*self.0, f)
    }
}

impl std::ops::Deref for Usage {
    type Target = str;
    fn deref(&self) -> &str {
        &self.0
    }
}

impl From<&str> for Usage {
    fn from(s: &str) -> Self {
        Usage::new(s)
    }
}

impl From<String> for Usage {
    fn from(s: String) -> Self {
        Usage::new(s)
    }
}

impl PartialEq<str> for Usage {
    fn eq(&self, other: &str) -> bool {
        &*self.0 == other
    }
}

impl PartialEq<&str> for Usage {
    fn eq(&self, other: &&str) -> bool {
        &*self.0 == *other
    }
}

impl Serialize for Usage {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_str(&self.0)
    }
}

impl<'de> Deserialize<'de> for Usage {
    fn deserialize<D: Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
        Option::<String>::deserialize(d).map(|s| Usage::new(s.unwrap_or_default()))
    }
}

/// UsageAction: the script of a `kind: script` action. The code stays bound to the agent declaration: there are no
/// algorithms of this type.
pub const USAGE_ACTION: Usage = Usage::from_static("action");
/// UsagePropertyValidator checks the value of a property of a node.
pub const USAGE_PROPERTY_VALIDATOR: Usage = Usage::from_static("property_validator");
/// UsageNodeValidator checks a node as a whole (rules across several attributes), once the attribute validators
/// accepted.
pub const USAGE_NODE_VALIDATOR: Usage = Usage::from_static("node_validator");
/// UsageLinkValidator checks a link as a whole, with the node it leaves and the node it reaches (rules across both
/// ends and the link's properties), once the attribute validators of the link accepted.
pub const USAGE_LINK_VALIDATOR: Usage = Usage::from_static("link_validator");
/// UsageTransitionGuard decides whether a lifecycle transition is allowed.
pub const USAGE_TRANSITION_GUARD: Usage = Usage::from_static("transition_guard");
/// UsageTransitionAction runs when a lifecycle transition is taken and may change properties of the node.
pub const USAGE_TRANSITION_ACTION: Usage = Usage::from_static("transition_action");

/// Usages lists every usage.
pub fn usages() -> Vec<Usage> {
    vec![
        USAGE_ACTION,
        USAGE_PROPERTY_VALIDATOR,
        USAGE_NODE_VALIDATOR,
        USAGE_LINK_VALIDATOR,
        USAGE_TRANSITION_GUARD,
        USAGE_TRANSITION_ACTION,
    ]
}

/// Languages of the scripts.
pub const JAVASCRIPT: &str = "javascript";
/// The Go language of the scripts.
pub const GO: &str = "go";

/// Param types.
pub const PARAM_STRING: &str = "string";
pub const PARAM_NUMBER: &str = "number";
pub const PARAM_BOOLEAN: &str = "boolean";
/// ParamRegex is a regular expression (Go / RE2 syntax), checked when the instance is saved.
pub const PARAM_REGEX: &str = "regex";
/// ParamEnum is one of Param.Values.
pub const PARAM_ENUM: &str = "enum";
/// ParamStrings is a list of strings.
pub const PARAM_STRINGS: &str = "strings";
/// ParamJSON is any JSON value.
pub const PARAM_JSON: &str = "json";

/// ParamTypes lists the parameter types.
pub fn param_types() -> Vec<&'static str> {
    vec![
        PARAM_STRING,
        PARAM_NUMBER,
        PARAM_BOOLEAN,
        PARAM_REGEX,
        PARAM_ENUM,
        PARAM_STRINGS,
        PARAM_JSON,
    ]
}

fn null_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

fn is_false(b: &bool) -> bool {
    !*b
}

/// Param declares a parameter of an algorithm: a typed value the instance sets and the script reads with
/// ctx.param(name).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Param {
    #[serde(deserialize_with = "null_default")]
    pub name: String,
    #[serde(deserialize_with = "null_default")]
    pub r#type: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub description: String,
    #[serde(skip_serializing_if = "is_false", deserialize_with = "null_default")]
    pub required: bool,
    /// The default value; `None` (Go's nil) when unset.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub default: Option<Value>,
    /// Values are the allowed values of an enum.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub values: Vec<String>,
}

/// Algorithm is a reusable script of a fixed type with declared parameters.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Algorithm {
    #[serde(deserialize_with = "null_default")]
    pub name: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub description: String,
    pub r#type: Usage,
    #[serde(deserialize_with = "null_default")]
    pub language: String,
    #[serde(deserialize_with = "null_default")]
    pub code: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub params: Vec<Param>,
}

/// Instance sets the parameter values of an algorithm. Instances are what the domain plugs where the algorithm's
/// type is allowed.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Instance {
    #[serde(deserialize_with = "null_default")]
    pub name: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub description: String,
    #[serde(deserialize_with = "null_default")]
    pub algorithm: String,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub values: Map<String, Value>,
}

/// Bound is an instance resolved with its algorithm: everything needed to run it, so that the graph holds it inline
/// and needs no other lookup (ADR 0014).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Bound {
    #[serde(deserialize_with = "null_default")]
    pub instance: String,
    #[serde(deserialize_with = "null_default")]
    pub algorithm: String,
    pub r#type: Usage,
    #[serde(deserialize_with = "null_default")]
    pub language: String,
    #[serde(deserialize_with = "null_default")]
    pub code: String,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub params: Map<String, Value>,
    /// Property is the attribute a property validator applies to.
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub property: String,
}

static NAME_RE: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"^[a-z][a-z0-9_-]*$").unwrap());
static PARAM_NAME_RE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^[A-Za-z][A-Za-z0-9_]*$").unwrap());

/// ValidName tells whether s is a valid algorithm or instance name.
pub fn valid_name(s: &str) -> bool {
    NAME_RE.is_match(s)
}

/// The name Go's `%T` gives a decoded JSON value.
fn go_type(v: &Value) -> &'static str {
    match v {
        Value::Null => "<nil>",
        Value::Bool(_) => "bool",
        Value::Number(n) if n.is_f64() => "float64",
        Value::Number(_) => "int",
        Value::String(_) => "string",
        Value::Array(_) => "[]interface {}",
        Value::Object(_) => "map[string]interface {}",
    }
}

/// What Go's `%v` prints of a decoded JSON value.
fn go_value(v: &Value) -> String {
    match v {
        Value::Null => "<nil>".into(),
        Value::Bool(b) => b.to_string(),
        Value::Number(n) => match (n.as_i64(), n.as_u64(), n.as_f64()) {
            (Some(i), _, _) => i.to_string(),
            (_, Some(u), _) => u.to_string(),
            (_, _, Some(f)) => go_float_text(f),
            _ => n.to_string(),
        },
        Value::String(s) => s.clone(),
        Value::Array(a) => format!("[{}]", a.iter().map(go_value).collect::<Vec<_>>().join(" ")),
        Value::Object(m) => format!(
            "map[{}]",
            m.iter()
                .map(|(k, v)| format!("{k}:{}", go_value(v)))
                .collect::<Vec<_>>()
                .join(" ")
        ),
    }
}

fn go_float_text(f: f64) -> String {
    if f.fract() == 0.0 && f.abs() < 1e21 {
        format!("{f:.0}")
    } else {
        f.to_string()
    }
}

/// A float64 as a JSON value, the way Go's encoding/json writes it: an integral value is written without a fraction.
pub fn float_value(f: f64) -> Option<Value> {
    if f.fract() == 0.0 && f.abs() < 9_007_199_254_740_992.0 {
        return Some(Value::from(f as i64));
    }
    serde_json::Number::from_f64(f).map(Value::Number)
}

/// Go's `strconv.ParseBool`.
fn parse_bool(s: &str) -> Option<bool> {
    match s {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Some(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Some(false),
        _ => None,
    }
}

impl Algorithm {
    /// Issues checks the declaration of an algorithm (not its script: pkg/dsl compiles it).
    pub fn issues(&self) -> Vec<String> {
        let mut out = Vec::new();
        if !self.r#type.known() {
            out.push(format!("unknown type {:?}", self.r#type.as_str()));
        } else if !self.r#type.pluggable() {
            out.push(format!(
                "type {} has no algorithms: the code stays in the action declaration",
                self.r#type
            ));
        }
        out.extend(self.declaration(&[]));
        out
    }

    /// Declaration checks what an algorithm declares besides its type: name, language, code and parameters. Whoever
    /// owns a script usage of its own (the adapters, pkg/adapter) uses it for the common part of its definition; its
    /// parameters of types this package does not know are listed in extra_params (they are only checked for their
    /// name: the owner coerces their values).
    pub fn declaration(&self, extra_params: &[&str]) -> Vec<String> {
        let mut out = Vec::new();
        if self.name.is_empty() {
            out.push("name required".to_string());
        } else if !valid_name(&self.name) {
            out.push(
                "name must be lowercase letters, digits, '-' or '_' and start with a letter"
                    .to_string(),
            );
        }
        if self.language != JAVASCRIPT && self.language != GO {
            out.push("language must be javascript or go".to_string());
        }
        if self.code.is_empty() {
            out.push("code required".to_string());
        }
        let mut seen = BTreeSet::new();
        for (i, p) in self.params.iter().enumerate() {
            if p.name.is_empty() {
                out.push(format!("param {i} has no name"));
                continue;
            } else if !PARAM_NAME_RE.is_match(&p.name) {
                out.push(format!(
                    "param {}: name must be letters, digits or '_' and start with a letter",
                    p.name
                ));
            } else if seen.contains(&p.name) {
                out.push(format!("duplicate param {}", p.name));
            }
            seen.insert(p.name.clone());
            if extra_params.contains(&p.r#type.as_str()) {
                continue;
            }
            if !param_types().contains(&p.r#type.as_str()) {
                out.push(format!("param {}: unknown type {:?}", p.name, p.r#type));
                continue;
            }
            if p.r#type == PARAM_ENUM && p.values.is_empty() {
                out.push(format!("param {}: an enum needs values", p.name));
            }
            if let Some(d) = &p.default
                && !d.is_null()
                && let Err(e) = p.coerce(d)
            {
                out.push(format!("param {}: default: {e}", p.name));
            }
        }
        out
    }

    /// Resolve returns the parameter values an instance gives the script: its values checked against the declared
    /// params, defaults filled in. Values for undeclared params are refused.
    pub fn resolve(&self, values: &Map<String, Value>) -> (Map<String, Value>, Vec<String>) {
        let mut issues = Vec::new();
        let mut out = Map::new();
        let mut declared = BTreeSet::new();
        for p in &self.params {
            declared.insert(p.name.as_str());
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
            if !declared.contains(k.as_str()) {
                issues.push(format!("unknown param {k}"));
            }
        }
        issues.sort();
        (out, issues)
    }
}

impl Param {
    /// Coerce checks v against the parameter type and returns it in its canonical form (numbers as float64, lists as
    /// lists of strings…).
    pub fn coerce(&self, v: &Value) -> Result<Value, Error> {
        match self.r#type.as_str() {
            PARAM_STRING => match v {
                Value::String(s) => Ok(Value::String(s.clone())),
                _ => Err(Error::invalid(format!(
                    "expected a string, got {}",
                    go_type(v)
                ))),
            },
            PARAM_REGEX => match v {
                Value::String(s) => match Regex::new(s) {
                    Ok(_) => Ok(Value::String(s.clone())),
                    Err(e) => Err(Error::invalid(format!("invalid regular expression: {e}"))),
                },
                _ => Err(Error::invalid(format!(
                    "expected a regular expression, got {}",
                    go_type(v)
                ))),
            },
            PARAM_NUMBER => {
                let f = match v {
                    Value::Number(n) => n.as_f64(),
                    Value::String(s) => s.trim().parse::<f64>().ok().filter(|_| s.trim() == s),
                    _ => None,
                };
                f.and_then(float_value).ok_or_else(|| {
                    Error::invalid(format!("expected a number, got {}", go_value(v)))
                })
            }
            PARAM_BOOLEAN => match v {
                Value::Bool(b) => Ok(Value::Bool(*b)),
                Value::String(s) if parse_bool(s).is_some() => {
                    Ok(Value::Bool(parse_bool(s).unwrap_or_default()))
                }
                _ => Err(Error::invalid(format!(
                    "expected a boolean, got {}",
                    go_value(v)
                ))),
            },
            PARAM_ENUM => match v {
                Value::String(s) if self.values.contains(s) => Ok(Value::String(s.clone())),
                _ => Err(Error::invalid(format!(
                    "expected one of [{}], got {}",
                    self.values.join(" "),
                    go_value(v)
                ))),
            },
            PARAM_STRINGS => match v {
                Value::Array(l) => {
                    let mut out = Vec::with_capacity(l.len());
                    for x in l {
                        match x {
                            Value::String(s) => out.push(Value::String(s.clone())),
                            _ => {
                                return Err(Error::invalid(format!(
                                    "expected a list of strings, got element {}",
                                    go_value(x)
                                )));
                            }
                        }
                    }
                    Ok(Value::Array(out))
                }
                _ => Err(Error::invalid(format!(
                    "expected a list of strings, got {}",
                    go_type(v)
                ))),
            },
            PARAM_JSON => Ok(v.clone()),
            other => Err(Error::invalid(format!("unknown param type {other:?}"))),
        }
    }
}

/// Set is the algorithms and instances of a domain.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Set {
    pub algorithms: Vec<Algorithm>,
    pub instances: Vec<Instance>,
}

impl Set {
    /// Algorithm returns the named algorithm.
    pub fn algorithm(&self, name: &str) -> Option<&Algorithm> {
        self.algorithms.iter().find(|a| a.name == name)
    }

    /// Instance returns the named instance.
    pub fn instance(&self, name: &str) -> Option<&Instance> {
        self.instances.iter().find(|i| i.name == name)
    }

    /// Bind resolves an instance that must be of type want.
    pub fn bind(&self, name: &str, want: &Usage) -> Result<Bound, Error> {
        let inst = self
            .instance(name)
            .ok_or_else(|| Error::invalid(format!("unknown algorithm instance {name}")))?;
        let alg = self.algorithm(&inst.algorithm).ok_or_else(|| {
            Error::invalid(format!(
                "instance {name}: unknown algorithm {}",
                inst.algorithm
            ))
        })?;
        if alg.r#type != *want {
            return Err(Error::invalid(format!(
                "instance {name} is of type {}, expected {want}",
                alg.r#type
            )));
        }
        let (params, issues) = alg.resolve(&inst.values);
        if let Some(first) = issues.first() {
            return Err(Error::invalid(format!("instance {name}: {first}")));
        }
        Ok(Bound {
            instance: name.to_string(),
            algorithm: alg.name.clone(),
            r#type: alg.r#type.clone(),
            language: alg.language.clone(),
            code: alg.code.clone(),
            params,
            property: String::new(),
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn alg() -> Algorithm {
        Algorithm {
            name: "max-len".into(),
            r#type: USAGE_PROPERTY_VALIDATOR,
            language: JAVASCRIPT.into(),
            code: "x".into(),
            params: vec![
                Param {
                    name: "max".into(),
                    r#type: PARAM_NUMBER.into(),
                    required: true,
                    ..Default::default()
                },
                Param {
                    name: "mode".into(),
                    r#type: PARAM_ENUM.into(),
                    values: vec!["a".into(), "b".into()],
                    default: Some(json!("a")),
                    ..Default::default()
                },
            ],
            ..Default::default()
        }
    }

    #[test]
    fn issues_and_usages() {
        assert!(alg().issues().is_empty());
        let mut a = alg();
        a.r#type = USAGE_ACTION;
        assert_eq!(
            a.issues()[0],
            "type action has no algorithms: the code stays in the action declaration"
        );
        a.r#type = Usage::from("nope");
        a.name = "Bad".into();
        assert_eq!(
            a.issues(),
            vec![
                "unknown type \"nope\"".to_string(),
                "name must be lowercase letters, digits, '-' or '_' and start with a letter"
                    .to_string()
            ]
        );
        assert!(USAGE_NODE_VALIDATOR.pluggable() && !USAGE_ACTION.pluggable());
    }

    #[test]
    fn resolve_and_bind() {
        let (out, issues) = alg().resolve(json!({"max": "3", "other": 1}).as_object().unwrap());
        assert_eq!(issues, vec!["unknown param other".to_string()]);
        assert_eq!(Value::Object(out.clone()), json!({"max": 3, "mode": "a"}));
        assert_eq!(
            serde_json::to_string(&out).unwrap(),
            r#"{"max":3,"mode":"a"}"#
        );
        let set = Set {
            algorithms: vec![alg()],
            instances: vec![Instance {
                name: "i".into(),
                algorithm: "max-len".into(),
                values: json!({"max": 2.5}).as_object().unwrap().clone(),
                ..Default::default()
            }],
        };
        let b = set.bind("i", &USAGE_PROPERTY_VALIDATOR).unwrap();
        assert_eq!(b.params["max"], json!(2.5));
        assert_eq!(
            set.bind("i", &USAGE_NODE_VALIDATOR)
                .unwrap_err()
                .to_string(),
            "instance i is of type property_validator, expected node_validator"
        );
        assert_eq!(
            set.bind("x", &USAGE_NODE_VALIDATOR)
                .unwrap_err()
                .to_string(),
            "unknown algorithm instance x"
        );
    }

    #[test]
    fn coerce_messages() {
        let p = |t: &str| Param {
            name: "p".into(),
            r#type: t.into(),
            values: vec!["a".into(), "b".into()],
            ..Default::default()
        };
        assert_eq!(
            p(PARAM_STRING).coerce(&json!(1.5)).unwrap_err().to_string(),
            "expected a string, got float64"
        );
        assert_eq!(
            p(PARAM_ENUM).coerce(&json!("c")).unwrap_err().to_string(),
            "expected one of [a b], got c"
        );
        assert_eq!(p(PARAM_BOOLEAN).coerce(&json!("T")).unwrap(), json!(true));
        assert_eq!(p(PARAM_STRINGS).coerce(&json!([])).unwrap(), json!([]));
        assert!(p(PARAM_REGEX).coerce(&json!("(")).is_err());
    }

    #[test]
    fn json_shape() {
        let p = Param {
            name: "n".into(),
            r#type: PARAM_STRING.into(),
            ..Default::default()
        };
        assert_eq!(
            serde_json::to_string(&p).unwrap(),
            r#"{"name":"n","type":"string"}"#
        );
        let b: Bound =
            serde_json::from_str(r#"{"instance":"i","type":"node_validator","params":null}"#)
                .unwrap();
        assert_eq!(b.r#type, USAGE_NODE_VALIDATOR);
    }
}
