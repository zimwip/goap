//! scopes_test.go.

use std::collections::BTreeMap;

use goap_mcp::{SCOPE_ACTION, SCOPE_AGENT, SCOPE_BOTH};
use goap_mcpbuiltin::SCHEDULER;
use goap_methodology::{Action, Agent, KIND_LLM, KIND_TOOL, PLANNER_GOAP};

use super::{bg, example, memory, registry, with_alm};
use crate::service::mcp_scopes_fn;

// An agent-scoped MCP (goap-scheduler) is declared by agents only, an action-scoped one by actions only (ADR 0028).
#[tokio::test]
async fn mcp_scopes_are_validated() {
    let (s, d) = registry(memory(), false, |s| {
        s.mcp_scopes = Some(mcp_scopes_fn(|_| async {
            Ok(BTreeMap::from([
                (SCHEDULER.to_string(), SCOPE_AGENT.to_string()),
                ("files".to_string(), SCOPE_ACTION.to_string()),
                ("notes".to_string(), SCOPE_BOTH.to_string()),
            ]))
        }))
    });
    with_alm(&d).await;
    let mut m = example();
    let issues = s.validate(&bg(), &m).await;
    assert!(issues.0.is_empty(), "example: {issues}");

    // the agent level may orchestrate; a step may not
    let mut lead = Agent::default();
    lead.name = "lead".into();
    lead.planner = PLANNER_GOAP.into();
    lead.mcps = vec![SCHEDULER.into(), "notes".into()];
    m.agents = vec![lead];
    let issues = s.validate(&bg(), &m).await;
    assert!(
        issues.0.is_empty(),
        "agent-level scheduler refused: {issues}"
    );
    let mut delegate = Action::default();
    delegate.name = "delegate".into();
    delegate.kind = KIND_TOOL.into();
    delegate.tool = format!("{SCHEDULER}/start");
    delegate.cost = 1.0;
    let mut think = Action::default();
    think.name = "think".into();
    think.kind = KIND_LLM.into();
    think.prompt = "p".into();
    think.mcps = vec![SCHEDULER.into(), "files".into()];
    think.cost = 1.0;
    m.actions.push(delegate);
    m.actions.push(think);
    m.agents[0].mcps.push("files".into());
    let got: Vec<String> = s
        .scope_issues(&bg(), &m)
        .await
        .0
        .into_iter()
        .map(|i| i.path)
        .collect();
    let n = m.actions.len();
    let want = vec![
        format!("actions[{}].tool", n - 2),
        format!("actions[{}].mcps", n - 1),
        "agents[0].mcps".to_string(),
    ];
    assert_eq!(got, want);
}
