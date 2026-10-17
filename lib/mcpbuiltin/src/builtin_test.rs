use goap_mcp as mcp;

use super::*;

#[test]
fn definitions() {
    let defs = defs();
    let adapters = adapter_defs();
    assert!(
        defs.len() == NAMES.len() && adapters.len() == defs.len(),
        "{} definitions, {} adapters for {} built-ins",
        defs.len(),
        adapters.len(),
        NAMES.len()
    );
    for (i, d) in defs.iter().enumerate() {
        d.validate().unwrap();
        assert!(
            is(&d.name) && adapters[i].mcp == d.name && adapters[i].connector == d.name,
            "adapter {:?} does not implement {}",
            adapters[i],
            d.name
        );
        adapters[i].validate().unwrap();
        adapter("ORG-DEFAULT", &d.name).validate(d).unwrap();
    }
    assert!(
        !is("document-repository"),
        "document-repository is not built in"
    );
}

// Starting other agents is the agent level's business only.
#[test]
fn only_the_scheduler_is_for_agents() {
    for d in defs() {
        let want = d.name == SCHEDULER;
        assert_eq!(
            d.scope == mcp::SCOPE_AGENT,
            want,
            "{} has scope {:?}",
            d.name,
            d.scope
        );
    }
}

// The workspace MCP is defined with its adapter, but is not one of the built-ins every unit holds (ADR 0118).
#[test]
fn workspace() {
    let d = workspace_def();
    d.validate().unwrap();
    assert!(
        !is(WORKSPACE),
        "the workspace MCP is held by no unit by default"
    );
    for a in workspace_adapter_defs() {
        let err = a.validate();
        assert!(
            err.is_ok() && a.mcp == WORKSPACE,
            "adapter {}: {err:?}",
            a.name
        );
    }
    workspace_docker_adapter("ORG-DEFAULT", "/tmp/ws", "goap/builder:1")
        .validate(&d)
        .unwrap();
    workspace_adapter("ORG-DEFAULT", "/tmp/ws")
        .validate(&d)
        .unwrap();
}

// The Claude coding agent MCP is defined with its adapter, held by no unit by default (ADR 0123).
#[test]
fn claude_agent() {
    let d = claude_agent_def();
    d.validate().unwrap();
    assert!(
        !is(CLAUDE_AGENT),
        "the Claude coding agent MCP is held by no unit by default"
    );
    let a = claude_agent_adapter_def();
    let err = a.validate();
    assert!(
        err.is_ok() && a.mcp == CLAUDE_AGENT && a.connector == CLAUDE_AGENT,
        "adapter {}: {err:?}",
        a.name
    );
    claude_agent_adapter("ORG-DEFAULT", "/tmp/ws")
        .validate(&d)
        .unwrap();
}
