//! The MCP of the Claude coding agent (ADR 0123).

use goap_adapter as adapter;
use goap_algo as algo;
use goap_mcp as mcp;
use serde_json::{Map, json};

use crate::builtin::{PASS_THROUGH, num, number, schema_obj, string, tool};

/// ClaudeAgent is the MCP of the Claude coding agent (ADR 0123): a Claude Code session, run by the Claude Agent SDK
/// in the workspace of the change, that a methodology hands a coding task to and resumes. It is named for Claude
/// Code on purpose: its tools carry Claude Code's notions (a session, its permission requests, its questions). Like
/// the workspace MCP, it is defined on every platform and held by no unit by default. It is also the id of its
/// adapter definition and of its connector.
pub const CLAUDE_AGENT: &str = "claude-coding-agent";

/// ClaudeAgentDef is the definition of the Claude coding agent MCP. The session works in the workspace of the change
/// the call runs for: no tool names it.
pub fn claude_agent_def() -> mcp::Def {
    let outcome = " Answers {status, sessionId, summary, questions, permission, turns, costUsd, usage, tools, errors}: status done, needs_input (it asked questions), needs_approval (it asked to use a tool the unit does not allow), budget (turns or budget spent, it can be resumed) or failed.";
    mcp::Def {
        name: CLAUDE_AGENT.into(),
        description: "A Claude Code session (Claude Agent SDK) coding in the workspace of the change: run a task, answer its questions, follow it.".into(),
        tools: vec![
        tool("run", false, format!("{}{}", "Run a coding task with Claude Code in the workspace of the change (open it first with workspace/open). Give sessionId to go on with an earlier session of the change.", outcome), schema_obj(json!({"task": string("what to do: the instructions of this turn (the whole brief for a new session, what is new for a resumed one)"), "sessionId": string("session to resume (default: a new session)"), "path": string("directory of the workspace to work in (default: the workspace)"), "model": string("model of the session (default: the adapter's)"), "maxTurns": num("turns of this run (default: the adapter's, 200)"), "maxBudgetUsd": number("budget of this run in USD (default: the adapter's)")}), &["task"])),
        tool("answer", false, format!("{}{}", "Resume a session waiting for a person: their answer, their answers to its questions (by question), the tools they allow (Claude Code permission rules, Bash(npm test)).", outcome), schema_obj(json!({"sessionId": string("session to resume"), "answer": string("free answer or instruction"), "answers": json!({"type": "object", "description": "answers to the questions of the session, by question text", "additionalProperties": json!({"type": "string"})}), "allow": json!({"type": "array", "items": json!({"type": "string"}), "description": "tools allowed for this run (permission rules)"}), "maxTurns": num("turns of this run (default: the adapter's)")}), &["sessionId"])),
        tool("status", true, "The session of the change running now, if any, and the outcome of the latest one.", schema_obj(json!({}), &[])),
        tool("cancel", false, "Stop the session of the change running now (it can be resumed later).", schema_obj(json!({}), &[])),
    ],
        ..Default::default()
    }
}

fn param(name: &str, typ: &str, required: bool, description: &str) -> algo::Param {
    algo::Param {
        name: name.into(),
        r#type: typ.into(),
        required,
        description: description.into(),
        ..Default::default()
    }
}

/// ClaudeAgentAdapterDef is the adapter definition of the Claude coding agent MCP: each tool is the operation of the
/// same name of the connector.
pub fn claude_agent_adapter_def() -> adapter::Def {
    let mut permission_mode = param(
        "permissionMode",
        algo::PARAM_ENUM,
        false,
        "Permission mode of the sessions (default acceptEdits); a call it does not settle ends the turn as needs_approval",
    );
    permission_mode.values = ["default", "acceptEdits", "plan", "dontAsk", "auto"]
        .map(String::from)
        .to_vec();
    adapter::Def {
        name: CLAUDE_AGENT.into(),
        mcp: CLAUDE_AGENT.into(),
        connector: CLAUDE_AGENT.into(),
        language: algo::JAVASCRIPT.into(),
        code: PASS_THROUGH.into(),
        description: "Claude Code sessions (Claude Agent SDK) in the workspaces of the changes, on the connector's host: the sessions run with its rights and call the model through the model gateway (a row of the ledger per call), else with the unit's key".into(),
        params: vec![
            param("root", algo::PARAM_STRING, true, "Directory holding one workspace per change (the root of the unit's workspace adapter)"),
            param("model", algo::PARAM_STRING, false, "Model of the sessions (default: Claude Code's; through the gateway, a model of the platform catalog, alias or name)"),
            permission_mode,
            param("allowedTools", algo::PARAM_STRINGS, false, "Tools allowed without asking (Claude Code permission rules: Read, Edit, Bash(go test:*))"),
            param("disallowedTools", algo::PARAM_STRINGS, false, "Tools removed from the sessions (WebFetch, WebSearch)"),
            param("maxTurns", algo::PARAM_NUMBER, false, "Turns of a run (default 200)"),
            param("maxBudgetUsd", algo::PARAM_NUMBER, false, "Budget of a run in USD (default none)"),
            param("timeoutMinutes", algo::PARAM_NUMBER, false, "Time of a run in minutes (default 60, at most 240)"),
            param("settingSources", algo::PARAM_STRINGS, false, "Claude Code settings loaded: project (the repository's CLAUDE.md and settings, the default), user, local"),
            param("baseURL", algo::PARAM_STRING, false, "Anthropic API endpoint the sessions call (ANTHROPIC_BASE_URL; default Anthropic's); ignored when the hub knows the model gateway, whose endpoint the sessions then call (ADR 0123 §6)"),
            param("env", algo::PARAM_STRINGS, false, "Variables set for the sessions (NAME=value)"),
            param("passEnv", algo::PARAM_STRINGS, false, "Variables of the host environment given to the sessions"),
            param("api_key", adapter::PARAM_SECRET, false, "Anthropic API key of the sessions (ANTHROPIC_API_KEY); not used when the sessions call the model gateway"),
        ],
    }
}

/// ClaudeAgentAdapter is the instance of the Claude coding agent adapter a unit holds, its sessions in the
/// workspaces under root.
pub fn claude_agent_adapter(unit: &str, root: &str) -> adapter::Instance {
    let mut params = Map::new();
    params.insert("root".into(), json!(root));
    adapter::Instance {
        unit: unit.into(),
        mcp: CLAUDE_AGENT.into(),
        adapter: CLAUDE_AGENT.into(),
        params,
        ..Default::default()
    }
}
