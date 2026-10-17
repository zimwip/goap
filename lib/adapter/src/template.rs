//! The skeleton of an adapter's script and its parameters, from the MCP and the connector.

use std::fmt::Write as _;

use goap_algo as algo;
use goap_mcp as mcp;
use serde_json::{Map, Value};

use crate::PARAM_SECRET;

/// Operation is an operation a connector exposes.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Operation {
    pub name: String,
    pub description: String,
    /// InputSchema is the JSON Schema of its arguments.
    pub input_schema: Map<String, Value>,
}

fn property_names(schema: &Map<String, Value>) -> Vec<String> {
    match schema.get("properties") {
        Some(Value::Object(props)) => {
            let mut names: Vec<String> = props.keys().cloned().collect();
            names.sort();
            names
        }
        _ => Vec::new(),
    }
}

/// closestOperation picks the operation a tool most likely maps to: the same name, then a name that contains the
/// tool name or that the tool name contains ("read" ~ "read_file").
fn closest_operation<'a>(tool: &str, ops: &'a [Operation]) -> Option<&'a Operation> {
    ops.iter()
        .find(|o| o.name == tool)
        .or_else(|| {
            ops.iter().find(|o| {
                o.name.starts_with(&format!("{tool}_")) || o.name.ends_with(&format!("_{tool}"))
            })
        })
        .or_else(|| ops.iter().find(|o| o.name.contains(tool)))
}

/// Go's `%q`.
fn js_string(s: &str) -> String {
    format!("{s:?}")
}

/// Template generates the skeleton of the JavaScript of an adapter from the definition of the MCP (one case per tool
/// it expects) and the operations of the connector (the ones the code can call): each tool is mapped onto the
/// operation that looks closest, with the arguments the tool receives, or left as a TODO. The author completes the
/// mapping (renaming, reshaping the arguments and the result, several calls).
pub fn template(def: &mcp::Def, connector: &str, ops: &[Operation]) -> String {
    let mut b = String::new();
    let _ = writeln!(
        b,
        "// Adapter of the MCP {} on the connector {connector}.",
        def.name
    );
    b.push_str("// ctx.tool() is the tool called and ctx.args() its arguments; ctx.param(name) reads a parameter of the\n");
    b.push_str("// organisation's instance; ctx.call(operation, args) calls the connector and returns its result;\n");
    b.push_str("// return the result of the tool, or reject with ctx.fail(message).\n");
    if !ops.is_empty() {
        b.push_str("// Operations of the connector:\n");
        for o in ops {
            let _ = write!(
                b,
                "//   {}({})",
                o.name,
                property_names(&o.input_schema).join(", ")
            );
            if !o.description.is_empty() {
                let _ = write!(b, " - {}", o.description);
            }
            b.push('\n');
        }
    }
    b.push_str("switch (ctx.tool()) {\n");
    for t in &def.tools {
        let _ = writeln!(b, "  case {}:", js_string(&t.name));
        let Some(op) = closest_operation(&t.name, ops) else {
            b.push_str("    // TODO: no operation of the connector matches; call the ones needed with ctx.call(...)\n");
            let _ = write!(
                b,
                "    ctx.fail({});\n    return;\n",
                js_string(&format!("not implemented: {}", t.name))
            );
            continue;
        };
        let args: Vec<String> = property_names(&t.input_schema)
            .iter()
            .map(|n| format!("{n}: ctx.args().{n}"))
            .collect();
        let _ = writeln!(
            b,
            "    return ctx.call({}, {{ {} }});",
            js_string(&op.name),
            args.join(", ")
        );
    }
    b.push_str("}\nctx.fail(\"unknown tool \" + ctx.tool());\n");
    b
}

/// Params derives the parameters of an adapter from what the connector announces: one per property of its
/// configuration schema (the connector receives them as its configuration, under the same name) and one secret per
/// secret name.
pub fn params(config_schema: &Map<String, Value>, secret_names: &[String]) -> Vec<algo::Param> {
    let required: Vec<&str> = match config_schema.get("required") {
        Some(Value::Array(req)) => req
            .iter()
            .filter_map(Value::as_str)
            .filter(|n| !n.is_empty())
            .collect(),
        _ => Vec::new(),
    };
    let empty = Map::new();
    let props = match config_schema.get("properties") {
        Some(Value::Object(p)) => p,
        _ => &empty,
    };
    let mut names: Vec<&String> = props.keys().collect();
    names.sort();
    let mut out = Vec::new();
    for n in names {
        let spec = &props[n.as_str()];
        let ty = match spec.get("type").and_then(Value::as_str) {
            Some("number" | "integer") => algo::PARAM_NUMBER,
            Some("boolean") => algo::PARAM_BOOLEAN,
            Some("array") => algo::PARAM_STRINGS,
            Some("object") => algo::PARAM_JSON,
            _ => algo::PARAM_STRING,
        };
        out.push(algo::Param {
            name: n.clone(),
            r#type: ty.into(),
            required: required.contains(&n.as_str()),
            description: spec
                .get("description")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_string(),
            ..Default::default()
        });
    }
    for s in secret_names {
        out.push(algo::Param {
            name: s.clone(),
            r#type: PARAM_SECRET.into(),
            required: true,
            description: format!("reference of the secret {s} (<vault path>#<field> or env:<VAR>)"),
            ..Default::default()
        });
    }
    out
}
