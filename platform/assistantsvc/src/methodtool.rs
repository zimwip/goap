//! methodology_query (ADR 0094, `methodtool.go`): what a methodology declares, filtered and small.

use goap_ctx::Ctx;
use serde_json::Value;

use crate::methodquery::{Query, run_query};
use crate::service::MAX_METHODOLOGIES;
use crate::turn::Turn;
use crate::util::{Args, q};

/// ToolMethodologyQuery reads what a methodology declares, filtered and small (ADR 0094).
pub const TOOL_METHODOLOGY_QUERY: &str = "methodology_query";

const METHODOLOGY_QUERY_ARGS: [&str; 9] = [
    "methodology",
    "kind",
    "name",
    "q",
    "parent",
    "fields",
    "detail",
    "limit",
    "offset",
];

impl Turn {
    /// methodologyQuery answers the tool: the methodologies of the active project only (the rule of
    /// list_methodologies and list_agents), read as the caller through the registry. It changes nothing and runs
    /// nothing.
    pub(crate) async fn methodology_query(&self, ctx: &Ctx, a: &Args) -> Result<Value, String> {
        for k in a.0.keys() {
            if !METHODOLOGY_QUERY_ARGS.contains(&k.as_str()) {
                return Err(format!(
                    "unknown argument {}: the arguments are {}",
                    q(k),
                    METHODOLOGY_QUERY_ARGS.join(", ")
                ));
            }
        }
        let mut query = Query {
            kind: a.str("kind").to_lowercase(),
            name: a.str("name"),
            q: a.str("q"),
            parent: a.str("parent"),
            detail: a.str("detail").to_lowercase(),
            ..Default::default()
        };
        query.limit = a.integer("limit")?;
        query.offset = a.integer("offset")?;
        match a.0.get("fields") {
            None | Some(Value::Null) => {}
            Some(Value::Array(list)) => {
                for f in list {
                    let Some(s) = f.as_str() else {
                        return Err(r#""fields" must be a list of field names"#.into());
                    };
                    query.fields.push(s.trim().to_string());
                }
            }
            Some(_) => return Err(r#""fields" must be a list of field names"#.into()),
        }
        // the query is checked before anything is read
        query.clone().validated()?;
        let applicable = self.applicable(ctx).await?;
        let mut names = applicable.clone();
        let name = a.str("methodology");
        if !name.is_empty() {
            if !applicable.contains(&name) {
                return Err(format!(
                    "methodology {} does not apply to the active project (see list_methodologies)",
                    q(&name)
                ));
            }
            names = vec![name];
        } else {
            names.truncate(MAX_METHODOLOGIES);
        }
        let mut ms = Vec::new();
        for n in &names {
            match self.s.methodologies.methodology(ctx, n).await {
                Ok(c) => ms.push(c),
                Err(_) => {
                    if names.len() == 1 {
                        return Err(format!("methodology {} cannot be read", q(n)));
                    }
                }
            }
        }
        let defs: Vec<_> = ms.iter().map(|c| c.definition()).collect();
        let res = run_query(query, &defs)?;
        Ok(serde_json::to_value(res).unwrap_or(Value::Null))
    }
}
