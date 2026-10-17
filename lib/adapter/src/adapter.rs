//! The adapter definition and its instance.

use std::collections::BTreeMap;

use goap_algo as algo;
use goap_mcp as mcp;
use serde::{Deserialize, Deserializer, Serialize};
use serde_json::{Map, Value};

/// ParamSecret is the parameter type of a reference to a secret ("<vault path>#<field>" or "env:<VAR>"). Only an
/// adapter has them: the script never reads one, the hub hands the resolved secret to the connector under the
/// parameter name.
pub const PARAM_SECRET: &str = "secret";

/// The error of an invalid definition or instance: Go's wrapping of `mcp.ErrInvalid` (the text ends with
/// ": invalid").
pub(crate) fn invalid(msg: impl std::fmt::Display) -> mcp::Error {
    mcp::Error::Invalid(format!("{msg}: {}", mcp::ERR_INVALID))
}

/// A JSON null reads as the zero value, as with Go's `encoding/json`.
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

/// DefKey is the key of the node of an adapter definition.
pub fn def_key(name: &str) -> String {
    format!("ADD:{name}")
}

/// Key is the key of the Adapter node of an MCP in a unit.
pub fn key(unit: &str, mcp: &str) -> String {
    format!("ADP:{unit}/{mcp}")
}

/// Def defines an adapter: code that implements the tools of one MCP with the operations of one connector, and the
/// parameters an instance sets. It is an AdapterDef node of the "platform" namespace.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Def {
    #[serde(deserialize_with = "null_default")]
    pub name: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub description: String,
    /// MCP is the name of the MCP whose tools the code implements; Connector the id of the connector it calls.
    #[serde(deserialize_with = "null_default")]
    pub mcp: String,
    #[serde(deserialize_with = "null_default")]
    pub connector: String,
    #[serde(deserialize_with = "null_default")]
    pub language: String,
    #[serde(deserialize_with = "null_default")]
    pub code: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub params: Vec<algo::Param>,
}

/// Instance is the instance of a Def with the parameter values of one organisational unit: the one place where unit,
/// MCP and connector meet. The connector and the code come from the definition; the unit gives the values (root
/// directory, account, secret references). It is an Adapter node of the "organisation" namespace owned by its unit.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Instance {
    /// Unit is the key of the OrgUnit the adapter belongs to (the owner of its version).
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub unit: String,
    /// MCP is the name of the MCP of the platform namespace (the definition implements the same).
    #[serde(deserialize_with = "null_default")]
    pub mcp: String,
    /// Adapter is the name of the Def.
    #[serde(deserialize_with = "null_default")]
    pub adapter: String,
    /// Params are the parameter values (secrets as references).
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub params: Map<String, Value>,
    /// Restrictions of the MCP for the unit and its sub-units (ADR 0028). They add up along the unit chain: a unit
    /// can narrow what its ancestors allow, never widen it. An instance may only restrict (no Adapter): the
    /// implementation is then the one of the nearest ancestor.
    ///
    /// Disabled removes the MCP; Tools, when not empty, lists the only tools allowed; Deny lists tools refused;
    /// ReadOnly keeps only the tools marked read-only.
    #[serde(skip_serializing_if = "is_false", deserialize_with = "null_default")]
    pub disabled: bool,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub tools: Vec<String>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub deny: Vec<String>,
    #[serde(
        rename = "readOnly",
        skip_serializing_if = "is_false",
        deserialize_with = "null_default"
    )]
    pub read_only: bool,
}

impl Instance {
    /// Validate checks the instance against the MCP it says it implements: an instance names an adapter definition
    /// unless it only restricts the MCP, and restricts only tools the MCP has.
    pub fn validate(&self, def: &mcp::Def) -> Result<(), mcp::Error> {
        if self.mcp != def.name {
            return Err(invalid(format!(
                "adapter of {} validated against {}",
                self.mcp, def.name
            )));
        }
        if self.adapter.is_empty() && !self.restricts() {
            return Err(invalid(format!(
                "adapter {}: name an adapter definition or restrict the MCP",
                self.mcp
            )));
        }
        if !self.adapter.is_empty() && !mcp::valid_name(&self.adapter) {
            return Err(invalid(format!(
                "adapter {}: adapter must be the lowercase name of an adapter definition",
                self.mcp
            )));
        }
        for t in self.tools.iter().chain(&self.deny) {
            if def.tool(t).is_none() {
                return Err(invalid(format!(
                    "adapter {}: the MCP has no tool {t:?}",
                    self.mcp
                )));
            }
        }
        Ok(())
    }

    /// Implements reports whether the instance names an adapter definition (else it only restricts).
    pub fn implements(&self) -> bool {
        !self.adapter.is_empty()
    }

    /// Restricts reports whether the instance restricts the MCP.
    pub fn restricts(&self) -> bool {
        self.disabled || !self.tools.is_empty() || !self.deny.is_empty() || self.read_only
    }

    /// Props returns the properties of the Adapter node (the unit is its owner).
    pub fn props(&self) -> Map<String, Value> {
        let mut a = self.clone();
        a.unit = String::new();
        to_map(&a)
    }
}

/// FromProps reads an adapter instance from the properties of its node and the unit that owns it.
pub fn from_props(unit: &str, props: &Map<String, Value>) -> Result<Instance, mcp::Error> {
    let mut a: Instance = serde_json::from_value(Value::Object(props.clone()))
        .map_err(|e| invalid(format!("adapter node: {e}")))?;
    a.unit = unit.to_string();
    Ok(a)
}

/// DefFromProps reads an adapter definition from the properties of its node.
pub fn def_from_props(props: &Map<String, Value>) -> Result<Def, mcp::Error> {
    serde_json::from_value(Value::Object(props.clone()))
        .map_err(|e| invalid(format!("adapter definition node: {e}")))
}

fn to_map<T: Serialize>(v: &T) -> Map<String, Value> {
    match serde_json::to_value(v) {
        Ok(Value::Object(m)) => m,
        _ => Map::new(),
    }
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

impl Def {
    /// Validate checks a definition (not its script: `goap-dsl` compiles it).
    pub fn validate(&self) -> Result<(), mcp::Error> {
        if !mcp::valid_name(&self.name) {
            return Err(invalid(format!(
                "adapter definition name {:?} must be a lowercase name",
                self.name
            )));
        }
        let mut issues = Vec::new();
        if !mcp::valid_name(&self.mcp) {
            issues.push("an adapter names the MCP it implements (mcp)".to_string());
        }
        if !mcp::valid_name(&self.connector) {
            issues.push("an adapter names the connector it calls (connector)".to_string());
        }
        issues.extend(self.script().declaration(&[PARAM_SECRET]));
        if let Some(first) = issues.first() {
            return Err(invalid(format!(
                "adapter definition {}: {first}",
                self.name
            )));
        }
        Ok(())
    }

    /// script is the definition as a generic algorithm: the declaration checks of `goap-algo` apply to it.
    fn script(&self) -> algo::Algorithm {
        algo::Algorithm {
            name: self.name.clone(),
            description: self.description.clone(),
            language: self.language.clone(),
            code: self.code.clone(),
            params: self.params.clone(),
            ..Default::default()
        }
    }

    /// Resolve returns what an instance's values give the adapter: the configuration handed to the connector (every
    /// parameter but the secrets, checked against the declared params, defaults filled in) and the secret references
    /// by parameter name. Values for undeclared params are refused. Returns (config, secrets, issues).
    pub fn resolve(
        &self,
        values: &Map<String, Value>,
    ) -> (Map<String, Value>, BTreeMap<String, String>, Vec<String>) {
        let mut plain = self.script();
        plain.params = Vec::new();
        let mut secret: BTreeMap<String, &algo::Param> = BTreeMap::new();
        for p in &self.params {
            if p.r#type == PARAM_SECRET {
                secret.insert(p.name.clone(), p);
            } else {
                plain.params.push(p.clone());
            }
        }
        let rest: Map<String, Value> = values
            .iter()
            .filter(|(k, _)| !secret.contains_key(k.as_str()))
            .map(|(k, v)| (k.clone(), v.clone()))
            .collect();
        let (config, mut issues) = plain.resolve(&rest);
        let mut secrets = BTreeMap::new();
        for (name, p) in secret {
            match values.get(&name) {
                None | Some(Value::Null) => {
                    if p.required {
                        issues.push(format!("param {name} is required"));
                    }
                }
                Some(Value::String(s)) if !s.is_empty() => {
                    secrets.insert(name, s.clone());
                }
                Some(r) => issues.push(format!(
                    "param {name}: expected a secret reference, got {}",
                    go_type(r)
                )),
            }
        }
        issues.sort();
        (config, secrets, issues)
    }

    /// Props returns the properties of the AdapterDef node.
    pub fn props(&self) -> Map<String, Value> {
        to_map(self)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{Operation, Restriction, params, template};
    use serde_json::json;

    fn obj(v: Value) -> Map<String, Value> {
        v.as_object().cloned().unwrap()
    }

    fn tool(name: &str, schema: Value) -> mcp::Tool {
        mcp::Tool {
            name: name.into(),
            input_schema: obj(schema),
            ..Default::default()
        }
    }

    fn docs() -> mcp::Def {
        mcp::Def {
            name: "document-repository".into(),
            tools: vec![
                tool("list", json!({"properties": {"path": {}}})),
                tool(
                    "read",
                    json!({"properties": {"path": {}}, "required": ["path"]}),
                ),
                tool("search", json!({"properties": {"query": {}}})),
            ],
            ..Default::default()
        }
    }

    fn tool_names(d: &mcp::Def) -> Vec<String> {
        d.tools.iter().map(|t| t.name.clone()).collect()
    }

    fn strings(l: &[&str]) -> Vec<String> {
        l.iter().map(|s| s.to_string()).collect()
    }

    fn inst(unit: &str, mcp: &str, adapter: &str) -> Instance {
        Instance {
            unit: unit.into(),
            mcp: mcp.into(),
            adapter: adapter.into(),
            ..Default::default()
        }
    }

    #[test]
    fn instance_validate() {
        let docs = docs();
        let ok = inst("", &docs.name, "localfs-docs");
        ok.validate(&docs).unwrap();
        let mut other = ok.clone();
        other.mcp = "ticketing".into();
        let err = other.validate(&docs).unwrap_err();
        assert!(err.is_invalid(), "adapter of another MCP = {err}");
    }

    #[test]
    fn node_props() {
        let mut a = inst("ORG-A", "docs", "x");
        a.params = obj(json!({"root": "/r"}));
        let props = a.props();
        assert!(
            !props.contains_key("unit"),
            "the unit is carried by the owner of the node, not by the properties"
        );
        let got = from_props("ORG-A", &props).unwrap();
        assert!(
            got.unit == "ORG-A" && got.adapter == "x" && got.params["root"] == "/r",
            "adapter round trip = {got:?}"
        );
    }

    #[test]
    fn restrictions_add_up_along_the_chain() {
        let def = mcp::Def {
            name: "docs".into(),
            tools: vec![
                mcp::Tool {
                    name: "list".into(),
                    read_only: true,
                    ..Default::default()
                },
                mcp::Tool {
                    name: "read".into(),
                    read_only: true,
                    ..Default::default()
                },
                mcp::Tool {
                    name: "write".into(),
                    ..Default::default()
                },
                mcp::Tool {
                    name: "delete".into(),
                    ..Default::default()
                },
            ],
            ..Default::default()
        };
        let u = |f: fn(&mut Instance)| {
            let mut a = inst("U", "", "");
            f(&mut a);
            a
        };
        let p = |f: fn(&mut Instance)| {
            let mut a = inst("P", "", "");
            f(&mut a);
            a
        };
        let cases: Vec<(&str, Vec<Instance>, Vec<&str>)> = vec![
            (
                "no restriction",
                vec![inst("U", "", "a")],
                vec!["list", "read", "write", "delete"],
            ),
            (
                "deny",
                vec![u(|a| a.deny = strings(&["delete"]))],
                vec!["list", "read", "write"],
            ),
            (
                "read-only",
                vec![u(|a| a.read_only = true)],
                vec!["list", "read"],
            ),
            (
                "allow-lists intersect",
                vec![
                    u(|a| a.tools = strings(&["read", "write"])),
                    p(|a| a.tools = strings(&["list", "read"])),
                ],
                vec!["read"],
            ),
            (
                "a child cannot widen",
                vec![
                    u(|a| a.tools = strings(&["write"])),
                    p(|a| a.read_only = true),
                ],
                vec![],
            ),
            (
                "disabled by an ancestor",
                vec![inst("U", "", "a"), p(|a| a.disabled = true)],
                vec![],
            ),
            (
                "deny and allow",
                vec![
                    u(|a| a.deny = strings(&["read"])),
                    p(|a| a.tools = strings(&["list", "read"])),
                ],
                vec!["list"],
            ),
        ];
        for (name, chain, want) in cases {
            let mut r = Restriction::default();
            for a in &chain {
                r.add(a);
            }
            assert_eq!(tool_names(&r.apply(&def)), strings(&want), "{name}");
        }
        let mut r = Restriction::default();
        r.add(&inst("U", "", "a"));
        r.add(&p(|a| a.deny = strings(&["delete"])));
        assert_eq!(r.by, strings(&["P"]), "want the restricting units only");
    }

    #[test]
    fn adapter_may_only_restrict() {
        let def = mcp::Def {
            name: "docs".into(),
            tools: vec![mcp::Tool {
                name: "read".into(),
                ..Default::default()
            }],
            ..Default::default()
        };
        let with = |adapter: &str, f: fn(&mut Instance)| {
            let mut a = inst("", "docs", adapter);
            f(&mut a);
            a
        };
        for a in [
            with("", |a| a.disabled = true),
            with("", |a| a.tools = strings(&["read"])),
            with("impl", |a| a.read_only = true),
        ] {
            assert!(a.validate(&def).is_ok(), "{a:?}");
        }
        for a in [
            with("", |_| {}),                              // neither implements nor restricts
            with("", |a| a.deny = strings(&["write"])),    // unknown tool
            with("Bad", |_| {}),                           // bad name
            inst("", "other", "impl"),                     // another MCP
            with("", |a| a.tools = strings(&["missing"])), // unknown tool
            with("impl", |a| a.deny = strings(&[""])),
        ] {
            let err = a.validate(&def);
            assert!(
                err.as_ref().is_err_and(|e| e.is_invalid()),
                "{a:?} accepted: {err:?}"
            );
        }
    }

    #[test]
    fn template_follows_the_mcp_and_the_connector() {
        let ops = vec![
            Operation {
                name: "list_dir".into(),
                input_schema: obj(json!({"properties": {"path": {}}})),
                ..Default::default()
            },
            Operation {
                name: "read_file".into(),
                description: "Read a text file".into(),
                input_schema: obj(json!({"properties": {"path": {}}})),
            },
        ];
        let code = template(&docs(), "localfs", &ops);
        for want in [
            r#"case "list":"#,
            r#"ctx.call("list_dir", { path: ctx.args().path })"#,
            r#"case "read":"#,
            r#"ctx.call("read_file", { path: ctx.args().path })"#,
            r#"case "search":"#,
            "not implemented: search",
            "read_file(path) - Read a text file",
        ] {
            assert!(code.contains(want), "template misses {want:?}:\n{code}");
        }
        let d = Def {
            name: "a".into(),
            mcp: "document-repository".into(),
            connector: "localfs".into(),
            language: algo::JAVASCRIPT.into(),
            code,
            ..Default::default()
        };
        d.validate().unwrap();
        if let Err(e) = goap_dsl::check_algorithm_code(algo::JAVASCRIPT, &d.code) {
            panic!("the generated code does not compile: {e}\n{}", d.code);
        }
    }

    #[test]
    fn params_follow_the_connector() {
        let schema = obj(json!({"required": ["root"], "properties": {
            "root": {"type": "string", "description": "directory"},
            "retries": {"type": "integer"},
        }}));
        let ps = params(&schema, &["token".to_string()]);
        assert!(
            ps.len() == 3
                && ps[0].name == "retries"
                && ps[0].r#type == algo::PARAM_NUMBER
                && ps[1].name == "root"
                && ps[1].required
                && ps[2].r#type == PARAM_SECRET,
            "params = {ps:?}"
        );
    }

    fn param(name: &str, typ: &str, required: bool) -> algo::Param {
        algo::Param {
            name: name.into(),
            r#type: typ.into(),
            required,
            ..Default::default()
        }
    }

    fn def(name: &str, mcp: &str, connector: &str, language: &str, code: &str) -> Def {
        Def {
            name: name.into(),
            mcp: mcp.into(),
            connector: connector.into(),
            language: language.into(),
            code: code.into(),
            ..Default::default()
        }
    }

    #[test]
    fn def_round_trip_and_validation() {
        let mut d = def(
            "localfs-docs",
            "docs",
            "localfs",
            algo::JAVASCRIPT,
            "return 1",
        );
        d.params = vec![
            param("root", algo::PARAM_STRING, true),
            param("token", PARAM_SECRET, false),
        ];
        d.validate().unwrap();
        let back = def_from_props(&d.props()).unwrap();
        assert!(
            back.name == d.name
                && back.connector == "localfs"
                && back.params.len() == 2
                && back.params[0].name == "root"
                && back.params[0].required,
            "round trip = {back:?}"
        );
        let js = algo::JAVASCRIPT;
        let mut bad_param = def("a", "docs", "c", js, "1");
        bad_param.params = vec![param("x", "vault", false)];
        for (name, bad) in [
            ("name", def("Bad", "docs", "c", js, "1")),
            ("no mcp", def("a", "", "c", js, "1")),
            ("connector", def("a", "docs", "", js, "1")),
            ("language", def("a", "docs", "c", "lua", "1")),
            ("code", def("a", "docs", "c", js, "")),
            ("param", bad_param),
        ] {
            let err = bad.validate();
            assert!(
                err.as_ref().is_err_and(|e| e.is_invalid()),
                "{name} accepted: {err:?}"
            );
        }
    }

    #[tokio::test]
    async fn secret_params_are_not_readable_by_the_script() {
        let mut d = def(
            "a",
            "docs",
            "fs",
            algo::JAVASCRIPT,
            "return {seen: String(ctx.param(\"token\"))}",
        );
        d.params = vec![
            param("root", algo::PARAM_STRING, true),
            param("token", PARAM_SECRET, true),
        ];
        d.validate().unwrap();
        let (config, secrets, issues) = d.resolve(&obj(json!({"root": "/r", "token": "env:T"})));
        assert!(issues.is_empty(), "{issues:?}");
        assert!(
            config["root"] == "/r"
                && !config.contains_key("token")
                && secrets.get("token").map(String::as_str) == Some("env:T")
                && secrets.len() == 1,
            "resolve = {config:?} {secrets:?}"
        );
        // the hub binds only the configuration
        let b = algo::Bound {
            instance: "test".into(),
            algorithm: d.name.clone(),
            language: d.language.clone(),
            code: d.code.clone(),
            params: config,
            ..Default::default()
        };
        let out = goap_dsl::run_adapter(
            &goap_ctx::Ctx::background(),
            &b,
            goap_dsl::AdapterInput::default(),
        )
        .await;
        let seen = out
            .as_ref()
            .ok()
            .and_then(|o| o.result.as_ref())
            .and_then(|r| r.get("seen").cloned());
        assert!(
            seen == Some(Value::String("null".into())),
            "script sees the secret: {:?}",
            out.as_ref().map(|o| &o.result).map_err(|e| e.to_string())
        );
    }

    #[test]
    fn resolve_checks_the_values() {
        let mut d = def("a", "docs", "fs", algo::JAVASCRIPT, "1");
        d.params = vec![
            param("root", algo::PARAM_STRING, true),
            param("token", PARAM_SECRET, true),
            param("other", PARAM_SECRET, false),
        ];
        for (name, values) in [
            ("secret missing", json!({"root": "/r"})),
            ("secret not a ref", json!({"root": "/r", "token": 3})),
            ("empty secret", json!({"root": "/r", "token": ""})),
            ("param missing", json!({"token": "env:T"})),
            (
                "undeclared",
                json!({"root": "/r", "token": "env:T", "extra": 1}),
            ),
            ("wrong param value", json!({"root": 1, "token": "env:T"})),
        ] {
            let (_, _, issues) = d.resolve(&obj(values));
            assert!(!issues.is_empty(), "{name} accepted");
        }
        let (_, secrets, issues) = d.resolve(&obj(json!({"root": "/r", "token": "env:T"})));
        assert!(
            issues.is_empty() && secrets.len() == 1,
            "optional secret: {secrets:?} {issues:?}"
        );
    }
}
