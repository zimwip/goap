//! The definition of an MCP and of its tools, and the check of the arguments of a call.

use std::sync::LazyLock;

use regex::Regex;
use serde::{Deserialize, Deserializer, Serialize};
use serde_json::{Map, Value};

/// NodeTypeMCP is the type of the node of an MCP (namespace platform).
pub const NODE_TYPE_MCP: &str = "platform@MCP";

/// MCPKey is the key of the node of an MCP.
pub fn mcp_key(name: &str) -> String {
    format!("MCP:{name}")
}

/// The errors of the package. Go's `ErrInvalid` marks a malformed definition: [`Error::is_invalid`].
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum Error {
    /// A malformed definition or call; the message ends with ": invalid", as Go's wrapping of ErrInvalid does.
    #[error("{0}")]
    Invalid(String),
}

impl Error {
    fn invalid(msg: impl std::fmt::Display) -> Error {
        Error::Invalid(format!("{msg}: {ERR_INVALID}"))
    }

    /// `errors.Is(err, ErrInvalid)`.
    pub fn is_invalid(&self) -> bool {
        matches!(self, Error::Invalid(_))
    }
}

/// The text of Go's `ErrInvalid`.
pub const ERR_INVALID: &str = "invalid";

const NAME_PATTERN: &str = "^[a-z][a-z0-9_-]{0,62}$";

static NAME_RE: LazyLock<Regex> = LazyLock::new(|| Regex::new(NAME_PATTERN).expect("name pattern"));

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

/// `%q` of Go: the string quoted, with its escapes.
fn quote(s: &str) -> String {
    format!("{s:?}")
}

/// Tool is one tool of an MCP.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Tool {
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(
        rename = "inputSchema",
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub input_schema: Map<String, Value>,
    /// ReadOnly marks a tool that changes nothing (the MCP readOnlyHint): a unit can keep only these.
    #[serde(rename = "readOnly", skip_serializing_if = "is_false")]
    pub read_only: bool,
}

/// ScopeAction: declared by actions (`actions[].mcps`, a tool action's `<mcp>/<tool>`).
pub const SCOPE_ACTION: &str = "action";
/// ScopeAgent: declared by agents only (`agents[].mcps`), for the llm actions that work at the level
/// of the agent: orchestration tools (start other agents) are not a step's business.
pub const SCOPE_AGENT: &str = "agent";
/// ScopeBoth: either (the default, empty).
pub const SCOPE_BOTH: &str = "both";

/// Def is a generic MCP definition.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Def {
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    /// Scope says where the MCP may be used: action, agent or both (empty: both).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub scope: String,
    #[serde(deserialize_with = "null_default")]
    pub tools: Vec<Tool>,
}

/// ScopeOf normalizes a scope (empty: both).
pub fn scope_of(scope: &str) -> &str {
    if scope.is_empty() { SCOPE_BOTH } else { scope }
}

/// ForActions reports whether an MCP of this scope may be declared by actions.
pub fn for_actions(scope: &str) -> bool {
    scope_of(scope) != SCOPE_AGENT
}

/// ForAgents reports whether an MCP of this scope may be declared by agents.
pub fn for_agents(scope: &str) -> bool {
    scope_of(scope) != SCOPE_ACTION
}

/// ToolInfo is a tool available to an organization, under its qualified name.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct ToolInfo {
    /// "<mcp>/<tool>"
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(
        rename = "inputSchema",
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub input_schema: Map<String, Value>,
    #[serde(rename = "readOnly", skip_serializing_if = "is_false")]
    pub read_only: bool,
    /// Scope is the scope of the MCP of the tool (empty: both).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub scope: String,
}

/// ToolName is the qualified name of a tool: "<mcp>/<tool>".
pub fn tool_name(mcp: &str, tool: &str) -> String {
    format!("{mcp}/{tool}")
}

/// SplitTool splits "<mcp>/<tool>".
pub fn split_tool(name: &str) -> Result<(String, String), Error> {
    match name.split_once('/') {
        Some((mcp, tool)) if !mcp.is_empty() && !tool.is_empty() => {
            Ok((mcp.to_string(), tool.to_string()))
        }
        _ => Err(Error::invalid(format!(
            "tool {}: expected <mcp>/<tool>",
            quote(name)
        ))),
    }
}

/// ValidName reports whether s is a valid MCP, tool or connector name.
pub fn valid_name(s: &str) -> bool {
    NAME_RE.is_match(s)
}

impl Def {
    /// Validate checks a definition.
    pub fn validate(&self) -> Result<(), Error> {
        if !valid_name(&self.name) {
            return Err(Error::invalid(format!(
                "mcp name {} must match {NAME_PATTERN}",
                quote(&self.name)
            )));
        }
        match self.scope.as_str() {
            "" | SCOPE_ACTION | SCOPE_AGENT | SCOPE_BOTH => {}
            _ => {
                return Err(Error::invalid(format!(
                    "mcp {}: scope {} must be action, agent or both",
                    self.name,
                    quote(&self.scope)
                )));
            }
        }
        let mut seen = std::collections::HashSet::new();
        for t in &self.tools {
            if !valid_name(&t.name) {
                return Err(Error::invalid(format!(
                    "mcp {}: tool name {} must match {NAME_PATTERN}",
                    self.name,
                    quote(&t.name)
                )));
            }
            if !seen.insert(t.name.as_str()) {
                return Err(Error::invalid(format!(
                    "mcp {}: duplicate tool {}",
                    self.name, t.name
                )));
            }
        }
        Ok(())
    }

    /// Tool returns the named tool.
    pub fn tool(&self, name: &str) -> Option<&Tool> {
        self.tools.iter().find(|t| t.name == name)
    }

    /// Props returns the properties of the MCP node.
    pub fn props(&self) -> Map<String, Value> {
        match serde_json::to_value(self) {
            Ok(Value::Object(m)) => m,
            _ => Map::new(),
        }
    }
}

/// DefFromProps reads an MCP definition from the properties of its node.
pub fn def_from_props(props: &Map<String, Value>) -> Result<Def, Error> {
    serde_json::from_value(Value::Object(props.clone()))
        .map_err(|e| Error::invalid(format!("mcp node: {e}")))
}

impl Tool {
    /// CheckArgs checks the arguments of a call against the tool's input schema: the required
    /// properties must be present, a property the schema types must have that type (and be one of its
    /// enum when it has one), and a property the schema does not declare is refused when the schema says
    /// additionalProperties: false. Only the top level is checked; the error names what is expected, so
    /// a model calling the tool can correct its call.
    pub fn check_args(&self, args: &Map<String, Value>) -> Result<(), Error> {
        if let Some(Value::Array(req)) = self.input_schema.get("required") {
            for r in req {
                if let Value::String(name) = r
                    && !name.is_empty()
                    && !args.contains_key(name)
                {
                    return Err(Error::invalid(format!(
                        "tool {}: missing argument {}",
                        self.name,
                        quote(name)
                    )));
                }
            }
        }
        let empty = Map::new();
        let props = match self.input_schema.get("properties") {
            Some(Value::Object(p)) => p,
            _ => &empty,
        };
        let closed = self.input_schema.get("additionalProperties") == Some(&Value::Bool(false));
        let mut names: Vec<&String> = args.keys().collect();
        names.sort();
        for name in names {
            let Some(Value::Object(p)) = props.get(name) else {
                if closed {
                    let mut expected: Vec<&str> = props.keys().map(String::as_str).collect();
                    expected.sort();
                    return Err(Error::invalid(format!(
                        "tool {}: unknown argument {} (expected: {})",
                        self.name,
                        quote(name),
                        expected.join(", ")
                    )));
                }
                continue;
            };
            let v = &args[name];
            if v.is_null() {
                continue;
            }
            if let Some(Value::String(typ)) = p.get("type")
                && !typ.is_empty()
                && !has_type(v, typ)
            {
                return Err(Error::invalid(format!(
                    "tool {}: argument {} must be of type {typ}, got {}",
                    self.name,
                    quote(name),
                    go_type(v)
                )));
            }
            if let Some(Value::Array(enum_)) = p.get("enum")
                && !enum_.is_empty()
            {
                let got = go_sprint(v);
                if !enum_.iter().any(|e| go_sprint(e) == got) {
                    return Err(Error::invalid(format!(
                        "tool {}: argument {} must be one of {}",
                        self.name,
                        quote(name),
                        go_sprint(&Value::Array(enum_.clone()))
                    )));
                }
            }
        }
        Ok(())
    }
}

/// hasType says whether a decoded JSON value is of the JSON schema type typ.
fn has_type(v: &Value, typ: &str) -> bool {
    match typ {
        "string" => v.is_string(),
        "boolean" => v.is_boolean(),
        "number" | "integer" => match v {
            Value::Number(n) => {
                if n.is_i64() || n.is_u64() {
                    true
                } else {
                    let f = n.as_f64().unwrap_or(f64::NAN);
                    typ == "number" || f == f.trunc()
                }
            }
            _ => false,
        },
        "object" => v.is_object(),
        "array" => v.is_array(),
        _ => true, // a type this check does not know
    }
}

/// `%T` of Go for a decoded JSON value.
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

/// `fmt.Sprint` of Go for a decoded JSON value.
fn go_sprint(v: &Value) -> String {
    match v {
        Value::Null => "<nil>".into(),
        Value::Bool(b) => b.to_string(),
        Value::Number(n) => match (n.as_i64(), n.as_u64()) {
            (Some(i), _) => i.to_string(),
            (_, Some(u)) => u.to_string(),
            _ => go_float(n.as_f64().unwrap_or(f64::NAN)),
        },
        Value::String(s) => s.clone(),
        Value::Array(a) => format!(
            "[{}]",
            a.iter().map(go_sprint).collect::<Vec<_>>().join(" ")
        ),
        Value::Object(m) => {
            let mut keys: Vec<&String> = m.keys().collect();
            keys.sort();
            format!(
                "map[{}]",
                keys.iter()
                    .map(|k| format!("{k}:{}", go_sprint(&m[*k])))
                    .collect::<Vec<_>>()
                    .join(" ")
            )
        }
    }
}

/// `%v` of a float64 in Go: the shortest representation, with an exponent below 1e-4 and from 1e21.
fn go_float(f: f64) -> String {
    let a = f.abs();
    if f.is_finite() && a != 0.0 && !(1e-4..1e21).contains(&a) {
        let s = format!("{f:e}");
        let (mant, exp) = s.split_once('e').unwrap_or((&s, "0"));
        let (sign, digits) = match exp.strip_prefix('-') {
            Some(d) => ('-', d),
            None => ('+', exp),
        };
        return format!("{mant}e{sign}{digits:0>2}");
    }
    if f.is_nan() {
        return "NaN".into();
    }
    if f.is_infinite() {
        return if f > 0.0 {
            "+Inf".into()
        } else {
            "-Inf".into()
        };
    }
    format!("{f}")
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn obj(v: Value) -> Map<String, Value> {
        match v {
            Value::Object(m) => m,
            _ => panic!("not an object"),
        }
    }

    fn docs() -> Def {
        Def {
            name: "document-repository".into(),
            tools: vec![
                Tool {
                    name: "list".into(),
                    input_schema: obj(json!({"properties": {"path": {}}})),
                    ..Default::default()
                },
                Tool {
                    name: "read".into(),
                    input_schema: obj(json!({"properties": {"path": {}}, "required": ["path"]})),
                    ..Default::default()
                },
                Tool {
                    name: "search".into(),
                    input_schema: obj(json!({"properties": {"query": {}}})),
                    ..Default::default()
                },
            ],
            ..Default::default()
        }
    }

    fn named(name: &str, tools: &[&str]) -> Def {
        Def {
            name: name.into(),
            tools: tools
                .iter()
                .map(|t| Tool {
                    name: (*t).into(),
                    ..Default::default()
                })
                .collect(),
            ..Default::default()
        }
    }

    #[test]
    fn validate() {
        docs().validate().unwrap();
        for bad in [
            named("Bad", &[]),
            named("x", &["a", "a"]),
            named("x", &["A b"]),
        ] {
            assert!(
                bad.validate().is_err_and(|e| e.is_invalid()),
                "{bad:?} accepted"
            );
        }
        assert_eq!(
            split_tool("document-repository/read").unwrap(),
            ("document-repository".into(), "read".into())
        );
        assert!(split_tool("read").is_err());
        assert_eq!(
            split_tool("read").unwrap_err().to_string(),
            "tool \"read\": expected <mcp>/<tool>: invalid"
        );
    }

    #[test]
    fn check_args() {
        let d = docs();
        let read = d.tool("read").unwrap();
        read.check_args(&obj(json!({"path": "a"}))).unwrap();
        assert!(read.check_args(&Map::new()).is_err_and(|e| e.is_invalid()));
    }

    #[test]
    fn node_props() {
        let d = docs();
        let back = def_from_props(&d.props()).unwrap();
        assert_eq!(back.name, d.name);
        assert_eq!(back.tools.len(), 3);
    }

    #[test]
    fn scopes() {
        for s in ["", SCOPE_ACTION, SCOPE_AGENT, SCOPE_BOTH] {
            Def {
                name: "x".into(),
                scope: s.into(),
                ..Default::default()
            }
            .validate()
            .unwrap();
        }
        assert!(
            Def {
                name: "x".into(),
                scope: "step".into(),
                ..Default::default()
            }
            .validate()
            .is_err_and(|e| e.is_invalid())
        );
        assert!(
            for_actions("")
                && for_agents("")
                && !for_actions(SCOPE_AGENT)
                && !for_agents(SCOPE_ACTION)
        );
    }

    #[test]
    fn check_args_against_the_input_schema() {
        let mut schema = obj(
            json!({"type":"object","required":["path"],"additionalProperties":false,"properties":{
            "path":{"type":"string"},"limit":{"type":"integer"},"ratio":{"type":"number"},"deep":{"type":"boolean"},
            "mode":{"type":"string","enum":["a","b"]},"tags":{"type":"array"},"opts":{"type":"object"},"any":{"type":["string","null"]}}}),
        );
        let tool = Tool {
            name: "fs/read".into(),
            input_schema: schema.clone(),
            ..Default::default()
        };
        let cases: Vec<(Value, &str)> = vec![
            (json!({"path": "x"}), ""),
            (
                json!({"path": "x", "limit": 3.0, "ratio": 0.5, "deep": true, "mode": "b", "tags": [], "opts": {}, "any": 2.0}),
                "",
            ),
            (json!({"path": "x", "limit": 3}), ""), // an integer from an in-process caller
            (json!({"path": "x", "limit": null}), ""), // null: not checked
            (json!({}), r#"missing argument "path""#),
            (
                json!({"path": 1.0}),
                r#"argument "path" must be of type string"#,
            ),
            (
                json!({"path": "x", "limit": 2.5}),
                r#""limit" must be of type integer"#,
            ),
            (
                json!({"path": "x", "mode": "c"}),
                r#""mode" must be one of [a b]"#,
            ),
            (
                json!({"path": "x", "tags": "t"}),
                r#""tags" must be of type array"#,
            ),
            (
                json!({"path": "x", "colour": "red"}),
                r#"unknown argument "colour" (expected: any, deep, limit, mode, opts, path, ratio, tags)"#,
            ),
        ];
        for (args, want) in cases {
            let res = tool.check_args(&obj(args.clone()));
            let ok = if want.is_empty() {
                res.is_ok()
            } else {
                res.as_ref()
                    .is_err_and(|e| e.to_string().contains(want) && e.is_invalid())
            };
            assert!(ok, "{args}: err {res:?}, want {want:?}");
        }
        // an open schema accepts what it does not declare
        schema.remove("additionalProperties");
        let tool = Tool {
            name: "fs/read".into(),
            input_schema: schema,
            ..Default::default()
        };
        tool.check_args(&obj(json!({"path": "x", "colour": "red"})))
            .unwrap();
    }

    #[test]
    fn go_formatting() {
        assert_eq!(
            go_sprint(&json!([1, 0.5, "a", null, {"b": true}])),
            "[1 0.5 a <nil> map[b:true]]"
        );
        assert_eq!(go_float(1e21), "1e+21");
        assert_eq!(go_float(1.5e-7), "1.5e-07");
        assert_eq!(go_float(3.0), "3");
    }
}
