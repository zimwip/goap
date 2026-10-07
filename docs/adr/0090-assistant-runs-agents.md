# ADR 0090 — The assistant runs agents, after a confirmation

**Status**: accepted, implemented (server side; the proposal card of the web is a later task) · **Date**: 2026-10 ·
Builds on ADR 0043 (project roles, agent roles), 0063 (engine scope), 0085 (conversations), 0087 (the assistant),
0089 (ledger).

## Context

The assistant could list methodologies and create or open a change (ADR 0087) but not do the work: a person asking "run
the impact analysis on this change" had to find the agent themselves. Running an agent costs money and changes the
graph, so the assistant must choose it from what the person is looking at (the open change, or none), from the
methodologies applicable to their project and from the roles they hold, and the person must be able to change their mind
before anything starts.

## Decision

- **Who, why, how, what**: the assistant still answers none of the four questions; it helps *start a process of a
  methodology on a change* for the person, as the person. Nothing it does bypasses the engine's or the graph's checks.
- **Two new tools (six in all, nothing else)**:
  - `list_agents` (read). The agents the caller may run HERE, after three filters. (a) *Context*: when the tab is a change
    (`tab.kind` `change`, `params.id`; else `subject`), the change is read as the caller (`Graph.Change`; a personal change
    of someone else is "not visible") and only the agents of **its methodology** are listed, with the change header
    (methodology, namespace, project, status, lifecycle, state) and its running processes; a change that is no longer
    `draft` / `active`, or that names no methodology, lists nothing and says why. With no change, the agents of the
    applicable methodologies: `Engine.Start` without a change opens one itself (`selectTarget` -> `bindChange`), so every
    agent can start a change. (b) *Project*: only methodologies in `Snapshot.ApplicableMethodologies` of the project (the
    change's project, else the active one). (c) *Profile*: only agents the caller may run, asked of the engine (below).
    Each entry: `methodology`, `agent`, `description`, `examples`, `goals` (name, description; at most 5 shown, any goal is
    accepted), `requiredRoles`, `needsChange` (true when the list is for the change in context, false when the agent
    would open a new one; the engine has no agent that cannot open its own change, so there is no finer meaning), `running`
    (a process of that agent is active on the context change: `clarifying`, `running`, `waiting`, `stuck`, from
    `ListProcesses`). Capped at 30.
  - `start_agent {methodology, agent, goal?, intent?, changeId?, newChange?, rationale?}`. **Starts nothing**: no engine
    call, no change created. It recomputes the list above for this turn and refuses (the error goes back to the model) an
    agent that is not in it, a goal the agent does not have, and a change that is not the one of the context (no change in
    context: `newChange {title, intent}` is required; a change in context: no `newChange`, `changeId` defaults to it). A
    missing goal and intent is accepted only when the agent has exactly one goal. One proposal per answer. It records an
    action of the assistant message.
- **The proposal** is an action of the message, backward compatible with ADR 0085 (`type` and `args`/`result`, plus
  `status`):
  `{"type": "start_agent", "status": "proposed", "label", "rationale", "args": {"methodology", "agent", "goal"?, "intent"?,
  "project", "changeId"? | "newChange": {"title", "intent"}}}`. Later: `status` `starting` (transient claim), `started`
  with `result {processId, changeId}`, `rejected`, `failed` with `error` (and `result.changeId` when the change had been
  created); a decided action also carries `decided {by, at, decision}`.
- **Confirmation: `assistant.v1.AssistantService.ConfirmAction {conversationId, messageId, actionIndex, decision, project}`**
  returns the updated message. Only the owner of the conversation (not found for anyone else, forbidden for a service).
  - `reject`: `proposed` -> `rejected`; always possible, even for a stale proposal.
  - `accept` re-validates everything now, as the caller, before claiming anything: the active project (the request's
    `project`, the token's when empty, checked with `MayAccessProject`) must be the proposal's for a `newChange` (a change
    proposal takes the project of the change); the change must still be visible and `draft` / `active`; the agent must still
    be in the runnable list (applicable to the project, "start" permission, roles). Any failure is `ErrStale`
    (`FailedPrecondition`, with a clear text) and the action **stays proposed**, so it can be rejected.
  - It then claims the action with a compare-and-set (`convsvc.Service.UpdateAction`, atomic with the read in both stores:
    `proposed` -> `starting`); a second decision, even a concurrent one, is `ErrDecided` (`Aborted`). Then, as the caller:
    `Graph.CreateChange` when `newChange` (methodology and namespace of the methodology, the project, `Data` `createdBy`,
    `via: assistant`, `conversation`, like `create_change`), then `StartProcess` with the methodology, agent, goal, intent,
    change, project and `Vars {conversationId, messageId}`. The outcome is written into the action as the platform service
    `system:assistant`: `started` or `failed`. A failure after the change was created leaves the change (it is the person's
    and visible in the failure's `result.changeId`); nothing is rolled back (a change is never deleted).
  - A process dying between the claim and the outcome leaves `starting`; nothing rewrites it yet.
- **The roles are the engine's.** `checkAgentRoles` ran only inside `Engine.Start`; two additions expose the same rule
  without starting: `Engine.MayRunAgent(ctx, methodology, agent, project)` (the `mayRun` of the scope with the caller as
  initiator on the project, the roles the agent declares) and the RPC `engine.v1.EngineService.CheckAgents
  {methodology, projectId, agents[]}` -> `{mayStart, agents: [{agent, mayRun, roles}]}` (`mayStart` is the "start"
  permission `StartProcess` checks). `StartProcess` itself is unchanged and re-checks everything when the process starts, so
  the assistant's checks are a filter for the model and the person, never the authority. The assistant reaches the engine
  through a port (`assistantsvc.Engine`: `CheckAgents`, `StartProcess`, `Active`), `EngineClient` over the engine service
  (the handler in `goap-dev`, a Connect client in `cmd/conversations`, `GOAP_ENGINE_URL`), the caller's identity headers on
  every request. `pkg/graph` and `pkg/engine` still do not import `internal/assistantsvc`.
- **Attached to the conversation.** The process variables carry `conversationId` and `messageId`; the engine puts the
  conversation id (`engine.VarConversation`) in the ledger `CallMeta` of the model calls of that process (ADR 0089), so the
  gateway's ledger can group a conversation's turns and the runs it started. The run's status is read from the process by
  `processId` through the existing engine RPCs; convsvc mirrors nothing.
- **History.** The next turn's history says what the person decided ("the person accepted the proposal to start agent X of
  Y: it was started (process P on change C)", rejected, failed with the error, or "has not decided yet, nothing was
  started"), so the assistant follows up. The ledger source of the model turn stays `assistant`.
- **Prompt**: pick from `list_agents` using the change of the context (or its absence), the project's methodologies and the
  roles of the person (the list already holds only what they may run); explain the choice briefly; propose with
  `start_agent`; never claim a run started before the confirmation; say when nothing fits and what the person could do; ask
  when two agents fit equally.

## Consequences

- Nothing runs on the model's word alone: the person's click is the authorization, and the engine re-checks as them.
- The proposal is only valid in the project it was made in (for a new change) or on a change still open.
- The ledger meta of engine calls now carries the conversation; the engine reads the `conversationId` variable by name
  (accounting only, never an authorization input).

## Not done

The proposal card, run status and Accept / Reject buttons of the web (a later task; the contract is the action shape and
`ConfirmAction` above); expiry of an unanswered proposal; rewriting a stuck `starting`; several proposals in one answer.
