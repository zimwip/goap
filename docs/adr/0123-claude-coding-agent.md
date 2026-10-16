# ADR 0123 — The Claude coding agent: a Claude Code session driven by a methodology

**Status**: accepted; phases 1 and 2 implemented (the `claude-coding-agent` MCP, its adapter and its connector over the
Claude Agent SDK; the gateway endpoint, §6), the `sdlc` method not yet · **Date**: 2026-10 · Builds on ADR 0019 (MCP,
connectors, adapters), ADR 0061 (adapter secrets), ADR 0089 (one ledger of the model calls), ADR 0112 (persistent
agents, the discussion of a topic), ADR 0115 (forms of human actions), ADR 0118 (the workspace of a change)

## Context

ADR 0118 gave the build step of a change a workspace and the tools of a coding agent (`read`, `edit`, `bash`...),
driven by an `llm` action with a loop budget. It retained that option (A) and set aside (B): handing the task to a
real Claude Code. (A) reproduces the tools, not the harness around them: the sub-agents, the todo list, the plan
mode, the summary of a long context, the skills and the `CLAUDE.md` of the repository, the prompts tuned on many
sessions. That harness is what makes a coding agent adapt to a repository it has never seen.

The Claude Agent SDK (`@anthropic-ai/claude-agent-sdk`) is Claude Code as a library: `query({prompt, options})`
runs a session in a directory (`cwd`), resumable by its id (`resume`), bounded (`maxTurns`, `maxBudgetUsd`), its
tool calls checked by a callback (`canUseTool`), and answers one `result` message per turn (`subtype` `success`,
`error_max_turns`, `error_max_budget_usd`, `error_during_execution`; `session_id`, `num_turns`, `total_cost_usd`,
`usage`). It exists in TypeScript and Python, not in Go.

Question answered (CLAUDE.md, rule 8): **WITH WHAT** (a real service driving a coding agent: a connector, its MCP
and its adapter). The methodology keeps the **HOW**: it decides what to implement, when, who answers its questions,
and checks the result.

## Decision

### 1. One MCP named for what it is: `claude-coding-agent`

The MCP is specific to Claude Code, not a generic "coding agent": its tools carry Claude Code's notions (a session,
its permission requests, its `AskUserQuestion`) and a methodology that names it knows what it gets. Another agent
would be another MCP. Tools (`lib/mcpbuiltin/claudeagent.go`):

| Tool | Does |
|---|---|
| `run {task, sessionId?, path?, model?, maxTurns?, maxBudgetUsd?}` | Runs a turn of a session in the workspace of the change (`path`: a directory of it); resumes `sessionId` when given |
| `answer {sessionId, answer?, answers?, allow?}` | Resumes a session waiting for a person: their answer, their answers to its questions, the tools they allow |
| `status {}` | The session of the change running now and the outcome of the latest one |
| `cancel {}` | Stops the session of the change running now |

`run` and `answer` give back `{status, sessionId, summary, questions?, permission?, turns, costUsd, usage, tools,
errors?}`, `status` being `done`, `needs_input` (it asked a question), `needs_approval` (it asked to use a tool the
adapter does not allow), `budget` (turns or budget spent: it can be resumed) or `failed`.

Like `workspace`, the MCP is defined on every platform and held by no unit by default (`SeedBuiltins` syncs its
definition and its adapter definition, `mcpbuiltin.Is` does not know it): running a coding agent is a capability an
administrator gives.

### 2. The connector: Go around a Node runner of the Agent SDK

`mcp/connectors/claudeagent` (`ID` `claude-coding-agent`; in `goap-dev`, standalone
`platform/cmd/connector-claude-coding-agent`) is a Go connector like the others (connectorkit, registers itself). Each call
starts the runner (`runner/runner.mjs`, embedded, written into the connector's runner directory next to its
`package.json`; `make claude-agent-runner` / `.\make.ps1 claude-agent-runner` installs the pinned SDK there): one
JSON request on its standard input, JSON lines on its output (the tool uses as they happen, then one `result`).
The runner is the only code that knows the SDK; the Go side knows the protocol.

- **Where**: the workspace of the change, the same directory as the `workspace` MCP's (`<root>/<change>`,
  `workspace.DirName`; `root` is a parameter of the adapter, the same as the unit's workspace adapter). A call for
  no change is refused (`ErrNoChange`); a workspace that does not exist yet is refused too (open it first with
  `workspace/open`). One session runs at a time per change (`ErrBusy`).
- **Environment**: cleaned like the workspace commands (the host variables, `passEnv`, `env`), plus
  `ANTHROPIC_API_KEY` from the secret `api_key` and `ANTHROPIC_BASE_URL` from `baseURL`. Nothing else of the host.
- **Settings**: `settingSources: ["project"]` by default, so the repository's `CLAUDE.md` and project settings
  apply, never the host user's.
- **Permissions**: `permissionMode` (default `acceptEdits`), `allowedTools` / `disallowedTools` of the adapter.
  A tool call the mode and the lists do not settle reaches `canUseTool`, which denies it with `interrupt` and
  records it: the turn ends `needs_approval` with `{tool, input}`. `AskUserQuestion` is caught the same way: the
  turn ends `needs_input` with its questions (question, header, options, multiSelect).
- **Bounds**: `maxTurns` (default 200), `maxBudgetUsd` (none by default), `timeoutMinutes` (default 60, at most 240:
  the process is stopped, the session can be resumed). `cancel` aborts the running session.

### 3. Waiting for a person ends the turn

A session never waits for a person: the turn ends and the methodology takes over, as with any human action
(ADR 0115, 0117). The planner chains three actions on conditions:

```yaml
- name: implement_component            # tool: claude-coding-agent/run, sessionId from the artifact coding_session
  effects: {implemented: true}         # or the question / approval recorded as an artifact
- name: answer_coding_question         # human, form: the questions as select fields; roles: the step's
- name: continue_implementation        # tool: claude-coding-agent/answer
```

The question reaches a holder of the role through the change's asks, its answer resumes the same session.

### 4. The discussion is the session

The session id is the continuity (ADR 0112 §4: the discussion of a topic, not a process): the action records it as
an artifact of the change (`coding_session`, target the component) and the next run on that component resumes it.
The brief is given in the first prompt; later prompts carry only what is new (a failed build, a review).

### 5. Checked, not believed

The connector reports what the session says; the methodology checks: the build is run again by a `tool` action
(`build_report`, ADR 0118 §6), the diff is read with `workspace/diff`, the push goes through `workspace/push` and the
branch is reviewed like any change.

### 6. The model calls and the ledger

Claude Code calls the model itself. Its calls come back into the ledger through the gateway's Anthropic Messages
endpoint (phase 2, done):

- **Who mints the token.** The connector declares `ConnectorInfo.calls_models`; for each of its calls the MCP hub,
  which knows the caller, mints a token of the call (`lib/modeltoken`: HMAC-SHA256 with `GOAP_MODEL_PROXY_SECRET`,
  shared by the hub and the gateway, valid five hours) and sends it with the endpoint (`InvokeRequest.model_url` /
  `model_token`, `mcp.CallContext.ModelURL` / `ModelToken` in process). The token names a system subject of the
  connector (`system:connector:claude-coding-agent`) acting for the caller's end user (ADR 0114), the unit, the
  project, the change, the process and the source (`connector:claude-coding-agent`). The connector holds no secret and
  can name no one else.
- **The session.** It takes the endpoint as `ANTHROPIC_BASE_URL` and the token as `ANTHROPIC_API_KEY`, over the
  adapter's `baseURL` and `api_key`.
- **The endpoint.** `modelgw.AnthropicProxy` serves `POST /anthropic/v1/messages` (and `count_tokens`, passed on with
  no row). It checks the token, resolves the model to a model of the platform catalog served by a provider of the
  `anthropic` protocol (an alias, `provider/model`, or the model's name: the catalog must hold the models the sessions
  ask for, the small one included), admits it like any call (enabled, the roles of the end user, the quota), and
  passes the request as it is with the provider's key: streaming, tools and caching untouched, no global behaviour
  added (ADR 0093; the session's instructions are its own). It writes one `llm_call` row per call, the tokens read
  from the answer (input counting the cached tokens) and the exchange (each call carries the whole conversation, cut
  at `journal.MaxExchangeText`).
- **Without the secret** (no `GOAP_MODEL_PROXY_SECRET`, or the hub without `GOAP_MODEL_PROXY_URL`), the sessions call
  the model with the adapter's key, outside the ledger, as in phase 1. `goap-dev` generates the secret
  (`.goap/model_proxy_secret`) and serves the endpoint at its own address.

## Plan
1. ✅ The `claude-coding-agent` MCP and adapter definition, the connector and its runner, `goap-dev`
   (`GOAP_CLAUDE_AGENT_DIR`), the standalone command, `make claude-agent-runner`.
2. ✅ An Anthropic Messages compatible endpoint of the gateway (`/anthropic/v1/messages`, streaming, native tools)
   taking a token of the call minted by the hub, so the sessions' calls are ledger rows (§6).
3. A second method of the `build` capability in `sdlc` (`claude_code_build`, when the unit holds the MCP): run,
   questions and approvals as human actions, the session as an artifact.
4. The tool uses of a session as entries of the change log (the runner's events), shown on the change tab.

## Consequences
- A methodology chooses Claude Code by naming its MCP; the unit decides whether it has it and with which key,
  model, permissions and budget.
- A session runs with the rights of the connector's host, in the workspace of the change; like `workspace-local`,
  it is given by an administrator, on purpose.
- With the gateway's endpoint configured, every model call of a session is a ledger row of the person who ran it,
  under the catalog's quotas and roles; a deployment that leaves the secret unset keeps the sessions outside the
  ledger, paid with the unit's key.
