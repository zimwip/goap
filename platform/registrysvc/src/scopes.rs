//! Port of `scopes.go`: where a methodology declares its MCPs (ADR 0028).

use goap_ctx::Ctx;
use goap_def::{Issue, Issues};
use goap_methodology::{KIND_TOOL, Methodology};

use crate::service::Service;

impl Service {
    /// scopeIssues reports the MCPs a methodology declares where their scope forbids it (ADR 0028): an MCP of scope
    /// agent (orchestration: goap-scheduler) belongs to agents[].mcps, one of scope action to the actions. Without
    /// MCPScopes, or when they cannot be read, nothing is reported (unknown MCPs are allowed: the organisation of a
    /// change decides what is bound).
    pub(crate) async fn scope_issues(&self, ctx: &Ctx, m: &Methodology) -> Issues {
        let Some(f) = &self.mcp_scopes else {
            return Issues::default();
        };
        let Ok(scopes) = f(ctx.clone()).await else {
            return Issues::default();
        };
        let mut out = Issues::default();
        for (i, a) in m.actions.iter().enumerate() {
            for name in a.required_mcps() {
                if let Some(sc) = scopes.get(&name)
                    && !goap_mcp::for_actions(sc)
                {
                    let field = if a.kind == KIND_TOOL {
                        ".tool"
                    } else {
                        ".mcps"
                    };
                    out.0.push(Issue::new(
                        format!("actions[{i}]{field}"),
                        format!(
                            "MCP {name} has scope agent: declare it on the agent (agents[].mcps), its llm actions reach it"
                        ),
                    ));
                }
            }
        }
        for (i, ag) in m.agents.iter().enumerate() {
            for name in &ag.mcps {
                if let Some(sc) = scopes.get(name)
                    && !goap_mcp::for_agents(sc)
                {
                    out.0.push(Issue::new(
                        format!("agents[{i}].mcps"),
                        format!(
                            "MCP {name} has scope action: declare it on the actions that use it"
                        ),
                    ));
                }
            }
        }
        out
    }
}
