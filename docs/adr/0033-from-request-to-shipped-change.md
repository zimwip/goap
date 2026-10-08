# ADR 0033 — From a request to a shipped change: intake, proposals, participation

**Status**: proposed; §1 superseded by ADR 0098 (the request is an object of the change component, not an intake process) · **Date**: 2026-09 ·
Builds on ADR 0001 (the change as blackboard), ADR 0004 (change application), ADR 0016 (sub-changes),
ADR 0020 (access control), ADR 0026 (node index), ADR 0031 (deferred change binding).

## Context

The platform is hard to use. The principle it should follow is simple:

1. A person expresses a **request** through the assistant.
2. The request either **belongs to an existing change** or **needs a new one**.
   - New change: an LLM agent **formulates** it (title, intent, scope, unit, methodology) and **proposes** it.
     The change is created only if the proposal is **accepted**.
   - Existing change: the request **amends the intent** of that change.
3. Starting from the change, several **people and agents take part** in carrying it out.
4. By default, the goal of a change is to be **shipped** (applied).

A walkthrough of the code shows that almost none of this path exists as such:

| Step | Today | Where |
|---|---|---|
| Request | Every assistant message starts a new process; the conversation lives only in the browser's localStorage; no follow-up in context. | `web/src/lib/stores/assistant.svelte.ts` (`sendRequest`) |
| Identification | The intent loop picks (methodology, agent, goal) with the **lexical** ranker by default; the LLM ranker only with `GOAP_INTENT_RANKER=llm`. | `pkg/intent`, `cmd/engine/main.go`, `cmd/goap-dev/main.go` |
| Change creation | Eager: `selectTarget` → `bindChange` creates a change as soon as the goal is known, with no proposal and no acceptance. | `pkg/engine/engine.go` (`selectTarget`, `resolveChange`) |
| Matching to an existing change | Only in one LLM action of `sdlc` (`find_or_create_change`). When it matches, the change opened eagerly for the request stays behind as an orphan `draft`. | `methodologies/sdlc.yaml` (intake) |
| Self-attach | `goap-scheduler/attach` on its own process calls `Engine.AttachChange`, which takes the process lock that `Run` already holds, and `Run` later saves its stale copy over the new binding. | `pkg/engine/engine.go` (`AttachChange`, `Run`) |
| Clarification by intake | The question is written as an artifact; the effect is not met, so after 2 rounds the action is disabled and the process goes `stuck`. The requester has no way to answer. | `sdlc.yaml`, `pkg/engine` (`recordFailure`) |
| Amending an intent | Possible (`goap-change/reformulate`, `UpdateChange`), but never proposed to anyone; the requester is not recorded. | `internal/connectors/builtin/change.go` |
| Default goal | None. "I need X" matches `intake/formulate`, which stops once the change is formulated; nothing chains to `deliver`. | `sdlc.yaml` |
| Participation | A process has at most one pending human task, without assignee or role. No participants on a change; no inbox; notifications are computed in the browser from the live stream and lost when it is closed. Decision points, questions, merges and changes ready to ship are notified nowhere. | `engine.proto` (`HumanTask`), `web/src/lib/stores/notifications.svelte.ts` |
| Chat result | No link to the change; the pending kinds `flow`, `board`, `relaunched` render nothing in the chat. | `web/src/lib/views/assistant/AssistantRun.svelte` |
| First screen | An authoring IDE: Methodologies panel, empty Events console, shortcut table, DSL help. The assistant is one icon among eleven. No login (a Bearer token pasted in a menu). | `web/src/lib/shell/layout.svelte.ts`, `views/Welcome.svelte` |
| Vocabulary | Baseline, namespace, own branch, flow, option, decision point, board, ticks, world state, raw 8-character IDs, JSON dumps. | `ChangeTab.svelte`, `NewChangeForm.svelte`, `HumanTaskForm.svelte` |
| Shipping | *Apply* has no confirmation; the reason it is disabled is only a tooltip. The `ApplyChange` RPC does not check `change:apply` itself. | `ChangeTab.svelte`, `internal/graphsvc/handler.go` |

## Decision (proposed)

Make the principle above the **main path of the platform**, reusing what exists (processes, deferred binding,
human tasks, `reformulate`, the node index, the change log) rather than adding parallel concepts.

### 1. The request is an intake process with deferred binding

- The assistant starts every new conversation as an **intake process** of the platform (a built-in `intake`
  agent, the generalisation of `sdlc`'s `intake`), with **deferred binding** (ADR 0031, `change_bound`). No
  change is created eagerly for an assistant request anymore; the eager default stays for triggers and for
  callers that name a methodology and a goal.
- The conversation is the **process log** (`intent.turn` entries, ADR 0031), not localStorage: the assistant
  lists the user's conversations with `ListProcesses{mine}` and reopens them on any device.
- A follow-up message in a conversation is a turn of **that** process (a new `AddTurn` / `SendMessage` RPC on
  the engine, generalising `AnswerIntent`), never a new process. A new conversation is an explicit action.
- The intake identifies the target with the **LLM ranker** whenever a model is configured (lexical stays the
  fallback without a model).

### 2. Matching: find the change the request belongs to

The intake agent looks for candidates among the open changes (`draft`, `active`, `merge_pending`) that the
requester may see, ranked by:

1. the conversation's context (a conversation opened from a change, or already bound to one, targets it);
2. the requester's unit and its ancestors (rule 3 of the design rules);
3. semantic similarity of the request to the change intents and titles (node index, ADR 0026: index the
   change intent as it indexes nodes).

It produces **one proposal** (§3): amend change X, or create a new change. When it is not sure, it asks one
question as a **human task of kind `input`** addressed to the requester (not an artifact that fails the
action), so clarification is a turn of the conversation.

### 3. Proposals: nothing is created or amended without acceptance

A new pending kind, **`proposal`**, holds what the intake formulated, **before** anything is written:

```text
proposal {
  kind: "create" | "amend"
  // create
  title, intent, scope (what it touches), namespace, ownerOrg, methodology, goal (default: ship)
  // amend
  changeId, currentIntent, proposedIntent, rationale
  alternatives: [changeId, score, why]   // the other candidates, to switch target in one click
}
```

- The requester **accepts**, **edits then accepts**, **switches target** (another candidate, or "new change
  instead"), or **rejects** (the conversation continues).
- **Accept / create**: the engine binds the process to a new change (`AttachChange` with the proposal as the
  `AttachRequest`), in the same locked cycle as the acceptance, which removes the self-attach hazard.
- **Accept / amend**: the engine binds the process to change X and records the amendment with
  `reformulate` (a new `intent` artifact superseding the previous one, with its rationale). If the requester
  is not allowed to change X's intent (not a participant with the `owner` role, §5), the amendment becomes a
  **decision point** of X (ADR 0009 §4) whose decider is X's owner; the request is attached to X as pending
  until it is ruled.
- Every accepted request is recorded on the change as a **`request` fact** (requester, text, date,
  conversation id) in the change log (ADR 0030): a change keeps the list of the requests that shaped it and who
  made them, and the intent history reads as "request → amendment".

### 4. The default goal of a change is to ship it

- A methodology declares its **ship goal** (`ship: deliver` at the top level; `sdlc` names `deliver`, whose
  precondition is `applied`). A change created from an accepted proposal gets that goal unless the proposal
  names another one (a question-only request, e.g. an impact analysis, names its goal explicitly).
- After acceptance, the intake process hands over to the methodology's agent for the ship goal
  (`goap-scheduler/start` on the change, the existing sub-agent mechanism): the requester does not have to
  ask twice. No trigger to write per methodology.
- A methodology without a ship goal keeps today's behaviour (the goal the intent loop selected).

### 5. Participants and "what is expected of me"

- **Participants** of a change, derived and stored as facts of its log: the requester(s) (§3), the owner unit
  and its members (`OrgUnit`), the principals who acted on it (journal), and people explicitly added. Roles:
  `owner` (may amend the intent, abandon, ship when the policy allows), `contributor`, `reviewer`, `watcher`.
  ABAC keeps deciding what each may do (ADR 0020); the participant list says **who is concerned**.
- A pending human task gets an **addressee**: a principal, a role or a unit (`HumanTask.assignee`), computed
  from the action (`human` actions may declare `assignee: requester | owner | role:<r> | unit:<u>`) and, for
  approvals, from the principals holding the permission (the initiator excluded under the four-eyes rule, so
  the requester is not shown an approval button they cannot use).
- A server-side **work list**: `ListMyWork` (engine + graph) returns, for the caller, every item that waits
  on them, whatever produced it:
  - human tasks and approvals addressed to them;
  - proposals of their conversations;
  - open questions and decision points where they are the decider or the ratifier;
  - changes they own that are `merge_pending` or ready to ship (§6);
  - amendments waiting for their ruling.
- **Notifications** are the change log's entries addressed to a participant, persisted server-side and read
  by the web (the bell becomes the work list plus a feed), not rebuilt from the live stream in each browser.

### 6. One readable state per change: "where it is, who it waits on"

A computed **stage** shown everywhere a change appears (chat card, list, header):
`proposed → formulated → in progress → in review → ready to ship → shipped` (or `abandoned`), plus
**waiting on** (who or what: a person, a role, an agent, a merge to resolve) and the **next step** (from the
process plan when a process drives it). "Ready to ship" is a checklist (impacts reviewed, no node left in an
editable state, no open sub-change, no pending decision, permission held), so *Apply* is never disabled
without saying why.

### 7. Web: a workspace for people who ask and contribute, a studio for authors

- **Workspace** (default on arrival): *Home* = the work list (§5), my changes with their stage (§6), and the
  assistant in the centre. Conversations list on the left. The chat shows a **proposal card** (§3) and a
  **change card** (title, stage, waiting on, open) for the bound change, and handles every pending kind (or
  links to where it is handled).
- **Studio** (methodologies, domains, algorithms, adapters, access, tokens, the Test panel): the current IDE,
  one switch away, for authors and administrators.
- The change page opens on a **Summary** (intent and its history of requests and amendments, stage, waiting on,
  participants, next step), then *Work* (tasks and decisions), *Content* (impacts), *History* (audit). Flows,
  options, raw IDs, JSON and the "blackboard check" move to an *Advanced* section.
- Creating a change by hand asks for title, intent and unit; namespace, baseline, branch and methodology are
  defaulted from the unit and shown under *Advanced*. Starting an agent on an existing change is available
  from the change page (the engine already accepts `changeId` on `StartProcess`).
- Confirmations on irreversible moves (*Apply*, *Select option*, which rejects the others); vocabulary in
  plain words in the workspace ("ship" for apply, "alternative" for option, "decision" for decision point).
- A login flow (OIDC) instead of pasting a Bearer token; dates and numbers in the browser's locale (not the
  hard-coded `fr-FR` of `web/src/lib/api.ts`).

### 8. Fixes independent of the rest

- `ApplyChange` on the graph service checks `change:apply` for the caller (today only the lifecycle moves are
  authorized; the approval gate of the methodology can be bypassed through the RPC).
- `goap-scheduler/attach` on the caller's own process must not go through `Engine.AttachChange` from inside
  `Run` (lock held, stale save); it becomes an effect applied by the run loop itself.
- `internal/enginesvc` `ListProcesses`: the store error is checked after the loop, not before it.

## Phasing

| Phase | Content | Main code |
|---|---|---|
| **1 — the path** | Built-in intake with deferred binding (§1), conversation turns on the engine, proposal pending kind with accept / edit / switch / reject (§3), `request` fact, ship goal and hand-over (§4), clarification as a human task, fixes of §8. Chat: proposal card, change card, follow-ups in the same conversation. | `pkg/engine` (`selectTarget`, `AttachChange`, new pending kind), `proto/goap/engine/v1`, `methodologies/`, `pkg/methodology` (`ship`), `web/src/lib/stores/assistant.svelte.ts`, `views/assistant/` |
| **2 — who** | Participants and roles (§5), task addressees, `ListMyWork`, persisted notifications, Home view. | `pkg/graph/changelog.go`, `pkg/engine`, `internal/enginesvc`, `internal/graphsvc`, `web/src/lib/stores/notifications.svelte.ts` |
| **3 — readability** | Stage / waiting on / next step (§6), workspace vs studio, change page Summary and Advanced, plain vocabulary, confirmations, login, locale. | `web/src/lib/shell/`, `views/editors/ChangeTab.svelte`, `components/NewChangeForm.svelte` |
| **4 — matching quality** | Change intents in the node index, ranking by unit and similarity (§2), amendments ruled by the owner as decision points. | `pkg/index`, `internal/indexersvc`, `pkg/graph/decision.go` |

## Consequences

- The assistant becomes the entry point it is meant to be: one conversation per piece of work, nothing
  written before the requester agrees, and the change knows who asked what.
- The eager creation of changes remains for triggers and explicit starts; assistant requests no longer leave
  orphan drafts.
- New contract surface: engine `SendMessage` (conversation turn), pending kind `proposal` with its accept RPC,
  `HumanTask.assignee`, `ListMyWork`, methodology `ship`. Schema: participants and notifications are entries
  of `change_log` (no new table for them); the index gains change intents (both SQL dialects).
- The workspace hides the machinery (flows, options, baselines) without removing it: authors keep the studio,
  and every advanced pane stays one click away.

## Open questions

1. Should an amendment by a non-owner always go through a decision point, or may the unit's policy let any
   participant amend the intent?
2. A request that matches two changes (or should split into two): propose a parent change with sub-changes
   (ADR 0016), or two proposals?
3. Does a pure question ("what does this break?") need a change at all? Design rule "pure questions about the
   world state need none" suggests the intake answers it with read-only tools and proposes a change only when
   the answer calls for acting.
