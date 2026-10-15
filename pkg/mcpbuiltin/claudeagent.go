package mcpbuiltin

import (
	"github.com/zimwip/goap/pkg/adapter"
	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/mcp"
)

// ClaudeAgent is the MCP of the Claude coding agent (ADR 0123): a Claude Code session, run by the Claude Agent SDK
// in the workspace of the change, that a methodology hands a coding task to and resumes. It is named for Claude
// Code on purpose: its tools carry Claude Code's notions (a session, its permission requests, its questions). Like
// the workspace MCP, it is defined on every platform and held by no unit by default. It is also the id of its
// adapter definition and of its connector.
const ClaudeAgent = "claude-coding-agent"

// ClaudeAgentDef is the definition of the Claude coding agent MCP. The session works in the workspace of the change
// the call runs for: no tool names it.
func ClaudeAgentDef() mcp.Def {
	outcome := " Answers {status, sessionId, summary, questions, permission, turns, costUsd, usage, tools, errors}: status done, needs_input (it asked questions), needs_approval (it asked to use a tool the unit does not allow), budget (turns or budget spent, it can be resumed) or failed."
	return mcp.Def{Name: ClaudeAgent, Description: "A Claude Code session (Claude Agent SDK) coding in the workspace of the change: run a task, answer its questions, follow it.", Tools: []mcp.Tool{
		{Name: "run", Description: "Run a coding task with Claude Code in the workspace of the change (open it first with workspace/open). Give sessionId to go on with an earlier session of the change." + outcome,
			InputSchema: schemaObj(map[string]any{"task": str("what to do: the instructions of this turn (the whole brief for a new session, what is new for a resumed one)"),
				"sessionId": str("session to resume (default: a new session)"), "path": str("directory of the workspace to work in (default: the workspace)"),
				"model": str("model of the session (default: the adapter's)"), "maxTurns": num("turns of this run (default: the adapter's, 200)"),
				"maxBudgetUsd": number("budget of this run in USD (default: the adapter's)")}, "task")},
		{Name: "answer", Description: "Resume a session waiting for a person: their answer, their answers to its questions (by question), the tools they allow (Claude Code permission rules, Bash(npm test))." + outcome,
			InputSchema: schemaObj(map[string]any{"sessionId": str("session to resume"), "answer": str("free answer or instruction"),
				"answers":  map[string]any{"type": "object", "description": "answers to the questions of the session, by question text", "additionalProperties": map[string]any{"type": "string"}},
				"allow":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "tools allowed for this run (permission rules)"},
				"maxTurns": num("turns of this run (default: the adapter's)")}, "sessionId")},
		{Name: "status", ReadOnly: true, Description: "The session of the change running now, if any, and the outcome of the latest one.",
			InputSchema: schemaObj(map[string]any{})},
		{Name: "cancel", Description: "Stop the session of the change running now (it can be resumed later).",
			InputSchema: schemaObj(map[string]any{})},
	}}
}

// ClaudeAgentAdapterDef is the adapter definition of the Claude coding agent MCP: each tool is the operation of the
// same name of the connector.
func ClaudeAgentAdapterDef() adapter.Def {
	return adapter.Def{Name: ClaudeAgent, MCP: ClaudeAgent, Connector: ClaudeAgent, Language: algo.JavaScript,
		Code:        "return ctx.call(ctx.tool(), ctx.args());\n",
		Description: "Claude Code sessions (Claude Agent SDK) in the workspaces of the changes, on the connector's host: the sessions run with its rights and call the model with the unit's key",
		Params: []algo.Param{
			{Name: "root", Type: algo.ParamString, Required: true, Description: "Directory holding one workspace per change (the root of the unit's workspace adapter)"},
			{Name: "model", Type: algo.ParamString, Description: "Model of the sessions (default: Claude Code's)"},
			{Name: "permissionMode", Type: algo.ParamEnum, Values: []string{"default", "acceptEdits", "plan", "dontAsk", "auto"}, Description: "Permission mode of the sessions (default acceptEdits); a call it does not settle ends the turn as needs_approval"},
			{Name: "allowedTools", Type: algo.ParamStrings, Description: "Tools allowed without asking (Claude Code permission rules: Read, Edit, Bash(go test:*))"},
			{Name: "disallowedTools", Type: algo.ParamStrings, Description: "Tools removed from the sessions (WebFetch, WebSearch)"},
			{Name: "maxTurns", Type: algo.ParamNumber, Description: "Turns of a run (default 200)"},
			{Name: "maxBudgetUsd", Type: algo.ParamNumber, Description: "Budget of a run in USD (default none)"},
			{Name: "timeoutMinutes", Type: algo.ParamNumber, Description: "Time of a run in minutes (default 60, at most 240)"},
			{Name: "settingSources", Type: algo.ParamStrings, Description: "Claude Code settings loaded: project (the repository's CLAUDE.md and settings, the default), user, local"},
			{Name: "baseURL", Type: algo.ParamString, Description: "Anthropic API endpoint the sessions call (ANTHROPIC_BASE_URL; default Anthropic's)"},
			{Name: "env", Type: algo.ParamStrings, Description: "Variables set for the sessions (NAME=value)"},
			{Name: "passEnv", Type: algo.ParamStrings, Description: "Variables of the host environment given to the sessions"},
			{Name: "api_key", Type: adapter.ParamSecret, Description: "Anthropic API key of the sessions (ANTHROPIC_API_KEY)"},
		}}
}

// ClaudeAgentAdapter is the instance of the Claude coding agent adapter a unit holds, its sessions in the workspaces
// under root.
func ClaudeAgentAdapter(unit, root string) adapter.Instance {
	return adapter.Instance{Unit: unit, MCP: ClaudeAgent, Adapter: ClaudeAgent, Params: map[string]any{"root": root}}
}
