# ADR 0098 — Native tool calling through the model gateway

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0028 (MCPs), 0059 (prompts in the log), 0089
(the ledger of LLM calls), 0093 (global behaviours).

## Context

An `llm` action calls the tools of its MCPs in a loop. The tools were described in the system text and the model
answered `{"tool_calls": [...]}` as JSON text, on top of any model: no provider's tool API was used. Every provider
the gateway speaks (Anthropic Messages, OpenAI-compatible `/chat/completions`, Gemini `generateContent`) has a tool API:
the model is trained on it, the calls come back as structured data (no JSON to parse out of prose), the arguments are
produced against the tool's schema, and the results travel as tool results (data, not instructions). Writing the
protocol in the prompt costs tokens on every call and fails in ways the native path does not (prose around the JSON, a
tool call mixed with the answer, a truncated object).

## Decision

### The contract (`contracts/llm`)

- `Request.Tools []ToolSpec` (`name`, `description`, `inputSchema`): the tools the model may call.
- `Response.ToolCalls []ToolCall` (`id`, `name`, `arguments`); `Response.Text` is then what the model said with them;
  `StopReason` is `tool_use` (`llm.StopToolUse`).
- A `Message` carries the calls of an assistant turn (`ToolCalls`) and the results of the user turn after it
  (`ToolResults`: `callId`, `name`, `content`, `isError`). `Message.Transcript()` / `Response.Transcript()` render them
  as text: the exchange of a call keeps that form (the `model.call` entry of the change log, the ledger's
  `llm_call_exchange`), so neither schema changed.
- The RPC carries them (`ToolSpec`, `ToolCall`, `ToolResult`; arguments and schemas as JSON text).

### The gateway does the provider work (`platform/modelgw/tools.go`)

- **Native**: a provider implementing `NativeTools` gets the tools through its API: Anthropic `tools` + `tool_use` /
  `tool_result` blocks (results first in their user turn, the cache breakpoint on the last block of any kind); OpenAI
  `tools` + `tool_calls` + `role: tool` messages; Gemini `functionDeclarations` (`parametersJsonSchema`) +
  `functionCall` / `functionResponse` parts (no call ids: the position in the answer is one; no JSON mime type with
  function calling, the instruction asks for JSON instead). `jsonInstructionOf` words the JSON instruction for a call
  that may also call tools.
- **Names**: MCP tools are named `<mcp>/<tool>`; the APIs accept `[A-Za-z0-9_-]{1,64}`. The router gives the provider
  a safe name (`/` → `__`, others → `_`, cut, made unique) and maps the calls back; the caller's request is never
  changed.
- **Text protocol**: a provider without `NativeTools` (the fake one, a future one) gets the former protocol, run by the
  gateway: the tools in the system text, the calls and results of the history as JSON text (results marked as data, not
  instructions), the answer read back into `ToolCalls`. An OpenAI-compatible server whose model has no tools answers a
  client error naming tools or functions: the router retries the call through the text protocol and remembers the
  provider model (`textToolsOnly`, for the life of the process). The caller sees the same `Response` either way.
- The retries of a transient failure, the instrumentation and the ledger wrap all of it (one span, one ledger row).

### The engine (`engine/engine/executors.go`)

`LLMExecutor` passes the tools of the action (`toolSpecs`) and a short note on how to use them (`toolsNote`) instead
of the protocol and the list. A native call is run like before (`runTools`: concurrent, journaled, guidance on failure,
`MaxToolFailures`, unknown tools refused with the list) and its results go back as `ToolResults` paired by call id, each
capped at its share of `maxToolResult`. The final answer is still `{"items": [...]}` in the text (`MaxRepairs`). A model
that writes `{"tool_calls": [...]}` in its text is still honoured, its results then go back as text: some models do it
even with native tools, and the scripted clients of the tests rely on it.

### The assistant (`platform/assistantsvc/toolspecs.go`, `turn.go`)

The assistant declares its server tools (`serverTools`: seven then, eight since `search_changes`, ADR 0106, each turn
declaring those of its frame; what each does and the JSON schema of its arguments, moved out of the system text, which
keeps the rules of their use) and the screen tools of the turn (`uiToolSpec`:
`ui.<name>`, the schema built from the descriptor's arguments, the level and guidance in the description, plus a
`rationale` argument: the reason a proposal shows, which `readAnswer` takes out of the arguments before they are
checked against the descriptor). Its answer is plain text; the calls come back natively and their results go back as
`ToolResults` by call id (`toolResults`, each capped at its share of `maxToolResult`); the settling shortcut, the caps
per round and the proposals are unchanged. An answer written as the former JSON `{message, tool_calls}` is still read,
its results going back as text. The system prompt lists the screen tools by name, level, guidance and target only.

## Consequences

- The tool protocol no longer costs prompt tokens on native providers; calls and results are structured end to end.
- Not done: provider-side constrained output (a response schema) for the final answer: the item union is broad and the
  OpenAI-compatible servers differ in what they accept; the answer is still read and repaired by the engine.
- `textToolsOnly` is per process: a restart probes a refusing model once more.
