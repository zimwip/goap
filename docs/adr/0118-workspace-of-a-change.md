# ADR 0118 — The workspace of a change: the tool box of a coding agent

**Status**: accepted; phase 1 implemented (the `workspace` MCP, the local connector, the call context of a connector),
the phases 2 to 4 of "Plan" not yet · **Date**: 2026-10 · Builds on ADR 0019 (MCP, connectors, adapters), ADR 0028
(built-in MCPs, call context), ADR 0061 (adapter secrets), ADR 0109 (conditions derived from the context),
ADR 0112 (persistent agents)

## Context

The build step of a change needs what a coding agent has: a checkout of the sources, and tools to read, write, edit
and search files and to run commands (build, test, git), driven by an agent that takes its instructions from the
graph (the brief, the change impacts: requirement → function → component) and works until the code builds. Locally,
a directory stands for the container. GOAP had none of it: `localfs` exposes a directory as a document repository
(list, read, write), the sandbox of ADR 0034 runs DSL scripts, not commands in a checkout.

Question answered (CLAUDE.md, rule 8): **WITH WHAT** (the tools of a workspace, their connector and adapter) and
**WHO** (the unit that gives its changes a workspace, and where).

## Decision

### 1. One MCP, `workspace`, with the tools of a coding agent
`pkg/mcpbuiltin/workspace.go` defines the MCP `workspace` (scope both), with the contracts coding models already
know:

| Tool | readOnly | Contract |
|---|---|---|
| `open` | | `{sources: [{repo, ref?, path?, branch?}]}`: clone each source on the branch of the change (`goap/<change>`); an already cloned source is kept |
| `status` | yes | directory, sources (branch, head, changed, remote), background commands |
| `close` | | stop the background commands, remove the directory (`keep` leaves it) |
| `read` | yes | `{path, offset?, limit?}`: the lines numbered, long lines cut, binary files refused |
| `write` | | `{path, content}`: create or replace, directories created |
| `edit` | | `{path, old, new, all?}`: exact replacement, `old` unique unless `all` |
| `ls` | yes | the entries of a directory |
| `glob` | yes | `{pattern, path?}` (`**` any depth): files, the latest modified first |
| `grep` | yes | `{pattern, path?, glob?, mode: files\|content\|count, ignoreCase?, context?}` (RE2) |
| `bash` | | `{command, cwd?, timeout?, background?}`: exit code, stdout, stderr (head and tail kept) |
| `bash_output` / `bash_kill` | | follow / stop a background command |
| `diff` | yes | per source: `git status` and the diff since the commit the change started from (`goap.base`) |
| `push` | | push the branch of the change of a source (only that branch) |

What a coding harness has besides is elsewhere in GOAP: sub-agents are `ctx.runAgent` / `goap-scheduler`, the task
list is the steps and the memory of a persistent agent (ADR 0112), the web is other MCPs.

The workspace is the one of the change the call runs for (`mcp.CallContext.Change`): no tool names it, and a call
running for no change is refused (`workspace.ErrNoChange`). One workspace per change; the options of a change share
it (a workspace per flow may come with them).

Unlike the built-ins of `mcpbuiltin.Names`, **no unit holds it by default**: running commands is a capability an
organisation gives (rule 5). `graphsvc.SeedBuiltins` keeps the MCP and its adapter definitions in sync with the code
and seeds no instance; an administrator gives one to a unit (an `organisation@Adapter`), and restricts it as any
MCP (`readOnly` for an agent that only reads code, `deny: [bash, push]`).

### 2. The connector carries the change
`connector.v1.InvokeRequest` carries `change_id` and `process_id` next to `org_id` (filled by the hub from the call
context); `connectorkit` puts them back in the context of `Connector.Invoke` (`mcp.WithCall`), so an in-process and
a remote connector read the same `mcp.CallFrom(ctx)`. A connector still knows no MCP and no organisation.

### 3. The local connector: a directory per change
`internal/connectors/workspace` (connector `workspace-local`; served in process by `goap-dev`, standalone by
`cmd/connector-workspace`, `GOAP_WORKSPACE_ROOTS` restricting the roots a unit may configure) keeps one directory
per change, `<root>/<change id>`. Its adapter definition `workspace-local` (pass-through) takes `root`, `passEnv`,
`env` (`NAME=value`), `gitName`, `gitEmail`, `branchPrefix` and the secret `git_token`.

- **Files** go through `os.Root`: no path leaves the workspace, `..` and symbolic links included.
- **Commands** run in the workspace (or `cwd`) with a **cleaned environment** (the variables a shell and a
  toolchain need, the ones `passEnv` names, `env`, `GOAP_WORKSPACE`, `GOAP_CHANGE`, the git identity), a timeout
  (120 s by default, 600 s at most; 2 h for a background command, at most 8 at once per workspace) and their whole
  process group killed at the end. Each call is a new shell: `cwd` sets the directory.
- **They are not isolated**: a command can reach outside the directory. The local connector is for development and
  trusted hosts; it says so in its description. An isolated workspace is a container connector (§ Plan).
- **The git token** is given to git only (as configuration in its environment, never on its command line) for
  `open` and `push`, never to the commands, and `push` pushes only the branch of the change: what reaches the
  remote is the work of the change.
- `goap-dev` gives the default organisation a workspace adapter on `GOAP_DEV_WORKSPACE_ROOT` when it is set
  (`devseed.Workspace`).

### 4. Which action: `llm`, not a new kind
The loop of a coding agent is the tool loop of the `LLMExecutor` (native tools, ADR 0098): an `llm` action declaring
`mcps: [workspace, goap-graph]` reads the brief and the impacts, codes, runs the build. It is one task that does not
split ("implement this component"), so rule 6 holds: the agent of the methodology plans (prepare, implement, build,
fix, publish, review), the actions execute. What the `llm` action lacks is a budget fit for code (`MaxToolRounds` 6,
20 KB of tool result): phase 2.

The build itself is checked, not believed: a `tool` action runs the declared build on `workspace/bash` and records a
`build_report`, and the condition that the build holds is derived from it (ADR 0109). The code goes to git, on the
branch of the change; the graph keeps its trace (`code_change`, `build_report` artifacts, a `BuildArtifact` impact),
reviewed and merged like any change.

## Plan
1. ✅ The `workspace` MCP, the local connector, `change_id` / `process_id` on `InvokeRequest`, `goap-dev`.
2. A loop budget on the `llm` action (`loop: {maxRounds, maxToolResultBytes, maxTokens, timeout}`, bounded by the
   platform), the compaction of a long conversation, tool results cut keeping their tail.
3. The `build` method of `sdlc` (prepare / implement / build / fix / publish / review) on the components a change
   impacts (`alm@Component.repository`), its conditions derived, a persistent coding agent per change.
4. A container connector (`workspace-docker`, then Kubernetes) on the provisioners of `internal/sandbox`: one
   container per change, an image per technology, limits, the network closed but to the hosts the unit lists.

## Consequences
- A methodology names the MCP `workspace`, never where it runs: the organisation decides between a directory and a
  container, per unit.
- Any connector can keep state per change from now on (the call context reaches it).
- The local connector runs commands with the rights of its host: it is given by an administrator, on purpose.
