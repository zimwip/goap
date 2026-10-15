# ADR 0118 — The workspace of a change: the tool box of a coding agent

**Status**: accepted; phases 1 to 4 implemented (the `workspace` MCP, the local connector, the call context of a
connector; the loop budget of an llm action; the build method of `sdlc`; the docker connector), Kubernetes and the
filtered egress not yet · **Date**: 2026-10 · Builds on ADR 0019 (MCP, connectors, adapters), ADR 0028
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
fix, publish, review), the actions execute. What the `llm` action lacked is a budget fit for code (6 tool rounds,
20 KB of tool result): its `loop` (§5).

The build itself is checked, not believed: a `tool` action runs the declared build on `workspace/bash` and records a
`build_report`, and the condition that the build holds is derived from it (ADR 0109). The code goes to git, on the
branch of the change; the graph keeps its trace (`code_change`, `build_report` artifacts, a `BuildArtifact` impact),
reviewed and merged like any change.

### 5. The loop budget of an llm action
`Action.Loop` (`loop:` on an llm action, `pkg/methodology/loop.go`; proto `registry.v1.ActionLoop`, attribute `loop`
of `methodology@Action`, the *Tool loop budget* fields of the action editor) bounds its tool loop:

```yaml
- name: implement_component
  kind: llm
  mcps: [workspace, goap-graph]
  loop: {maxRounds: 200, maxTokens: 2000000, timeout: 2h, maxToolResultBytes: 60000, compactBytes: 400000}
```

- `maxRounds` (default `DefaultToolRounds` 6, at most 500), `maxToolResultBytes` (default 20 000, at most 200 000),
  `maxTokens` (input and output of every call of the loop; none by default), `timeout` (a Go duration, at most 4 h),
  `compactBytes` (default 400 000, at most 4 MiB). The compiler refuses a loop on another kind, a value out of its
  bounds and a timeout that is no duration.
- **Spent budget**: an action with a loop is not failed when its rounds, its tokens or nine tenths of its time are
  spent: the tools it asked for are not run, and it is asked once, with no tool, for its final answer (what is done,
  what is left); an `llm.budget` event records it. Past the timeout itself the call is cancelled and the action
  fails. An action without a loop keeps its behaviour (6 rounds, then a failure).
- **Compaction**: once the conversation is larger than `compactBytes`, the older tool results (and long texts) are
  elided, the task and the latest 8 messages kept whole, the calls and their results kept paired
  (`engine.compact`). Elision, not a summary: no model call, and the model calls a tool again when it needs the
  content.
- **Tool results** cut past their bound keep their head and their tail (the end of a build log says why it
  failed), for every llm action.
- **Failures** of a tool count in a row in an action with a loop: a success forgets the earlier ones, so a failed
  `edit` early in a long session does not ban the tool for the rest of it.

### 6. The build of `sdlc` from the sources

`sdlc` 0.8.0 builds a component from its sources when it names its repository (`alm@Component.repository`, any git
address, validator `repository-url`; `buildCommand` overrides the command of its technology; `alm` 1.5.0). The step
`software_delivery/build` names the capability `build`, two methods answer it:

- `workspace_build` (priority 20, when a component in scope has a repository): `prepare_workspace` (script) opens the
  workspace of the change with the sources of those components (`workspace/open`, one directory per component key,
  on the branch of the change) and records a `workspace` artifact; then, per component (scoped actions, `where:
  self.conditions.from_sources`): `implement_component` (llm, `mcps: [workspace, goap-graph]`, `parallel: 2`, loop
  `maxRounds: 200`, `maxTokens: 3000000`, `timeout: 2h`) changes and commits the sources and answers a
  `code_change` artifact; `build_component` (script) runs the build command (`workspace/bash`) and records a
  `build_report` at the revision (the number of code changes of the component); `publish_build` (script) pushes the
  branch (`workspace/push`) and records the `alm@BuildArtifact` (`built_from`, coordinates `<remote>#<commit>`) and
  the `build` artifact; the recorded `build` then does the components without a repository.
- `recorded_build`: the recorded `build` alone, as before.

The report decides, never the agent: `implemented` holds once a code change exists and no failed report stands at
the current revision, `build_checked` once a report stands there whatever its outcome (so the build action meets its
effect even when the build fails: `Engine.MaxFailures` is 1), `built` once it is a success. A failed build makes
`implemented` false again: the plan sends the component back to the agent, whose prompt carries the reports of the
component (command, exit, the tail of the output). After three code changes `implemented` holds as it is: the build
fails again, nothing can publish it, and the run waits for a person to take over (`Engine.Unblock`). The agent is not
persistent: its continuity is the workspace itself (the sources and their history) and the reports.

Planning a chain of scoped actions needed one rule of the compiler (`chainScoped`): when what a scoped action makes
true on its subject is what another of the same scope needs, the first is planned to leave a subject pending for the
second (`pending:<second>` among its effects, lifted: never checked once it ran). Without it no plan reached past
the first action of a flat agent (`delivery`). `publish_build` needs `build_checked` and `built`: the first chains it
after the build, the second holds it to a success. Tested end to end on a local git repository
(`pkg/engine/sdlc_workspace_test.go`).

### 7. The docker connector: the commands in a container per workspace

`workspace-docker` (`workspace.NewDocker(host)`, same package and same tools as the local one; `cmd/connector-workspace`
with `GOAP_WORKSPACE_DOCKER` set to the Docker Engine API, `goap-dev` always, at `GOAP_DOCKER_HOST`) keeps the
workspace of a change in a directory of its host as the local connector does, and runs its commands in a container
of its own (`goap-ws-<change>`, created at the first command): the image the unit configures (`image`, required: the
toolchains, git and a shell), the directory mounted at `/workspace`, no network by default (`network`: a network
the administrator gives, whose egress is filtered for the hosts the unit allows), every capability dropped, no
privilege escalation, a read-only root file system with a `/tmp` tmpfs and `HOME` in the workspace, an init, limits
(`memoryMB` 2048, `cpus` 2, `pids` 1024), the connector's uid and gid unless `user` says otherwise (the files the
commands write stay the connector's). A command is an exec (its outputs demultiplexed, its exit code read after);
a timeout or `bash_kill` kills it and its children through the pid it recorded; `close` removes the container.
The host environment never enters the container (no `passEnv`), the files are read, written and searched by the
connector on the host (`os.Root`), and git with the token runs on the host too: the token never reaches a command.
The provisioners of `internal/sandbox` are not reused: they start a runner per script action, not a long-lived
container driven by execs. Not done: one image per technology (one image per unit adapter for now), Kubernetes,
the egress filter itself. Tests: `internal/connectors/workspace/docker_test.go`, over a fake Docker API running the
execs on the host.

## Plan
1. ✅ The `workspace` MCP, the local connector, `change_id` / `process_id` on `InvokeRequest`, `goap-dev`.
2. ✅ A loop budget on the `llm` action (§5), the compaction of a long conversation, tool results cut keeping their
   tail.
3. ✅ The `build` method of `sdlc` (prepare / implement / build / fix / publish) on the components a change
   impacts (`alm@Component.repository`), its conditions derived (§6).
4. ✅ A container connector (`workspace-docker`, §7): one container per change, limits, no network by default.
   Then: Kubernetes, an image per technology, the egress filtered for the hosts the unit lists.

## Consequences
- A methodology names the MCP `workspace`, never where it runs: the organisation decides between a directory and a
  container, per unit.
- Any connector can keep state per change from now on (the call context reaches it).
- The local connector runs commands with the rights of its host: it is given by an administrator, on purpose.
