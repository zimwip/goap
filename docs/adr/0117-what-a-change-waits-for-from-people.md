# ADR 0117 — What a change waits for from people: the asks at the top of the change

**Status**: accepted, step 1 implemented (see "Next steps") · **Date**: 2026-10 · Extends ADR 0034 (manual steps),
ADR 0035 §2 (roles of the steps), ADR 0043 (roles on projects), ADR 0009 §4 (decision points), ADR 0112 (current state
of a change), ADR 0115 (forms of human actions)

## Context

The change holds everything about a piece of work, but what it needs from a person was buried in its runs. A human
action, an approval, a stuck run, a relaunched flow to adopt, a merge or a rebase to settle are the pending task of a
process (`Process.Pending`), and the only screen that showed it and let a person answer it was the tab of that run. The
change tab showed where the steps stand (ADR 0112), not that someone had to act. The notifications of the web only
reached the initiator of the run, never the people holding the role the task is addressed to. The questions of the
decision points, the points only a person may rule and the impacts awaiting their review were each in their own pane.

Several profiles collaborate on a change: the requester, the responsible role of a step, the accountable role that
approves, the reviewers. Each opens the change and must see at once what is theirs to do, and what waits for whom.

## Decision

### 1. The asks of a change are a read model of the engine

`Engine.ChangeAsks(ctx, changeID)` (`engine/engine/asks.go`, RPC `engine.v1.GetChangeAsks`, read like the change itself:
a personal change is its subject's only) gathers what the change waits for from people, the oldest first; nothing is
stored, the tasks stay where they are:

- each run of the change waiting on a person: its pending task (`input`, `approval`, `condition`, `unblock`, `flow`,
  `board`, `merge`, `rebase`) or its intent question (`clarification`). A run waiting for a sub-agent (`agent`) or for
  the decision on another run's flow (`relaunched`, carried by that run) asks nothing;
- the open questions of the decision points (`question`), and the points only a person settles (`decision`: escalated,
  reserved to a person, or a ruling to ratify).

An ask (`engine.Ask`, proto `ChangeAsk`) carries its kind, a title (the task's description, else the step's, else the
action), the methodology's instructions (`detail`), the run, agent, step and action, when the change started waiting
(`since`: the start of the step waiting, else the last update of the run; when the question was asked; the ruling to
ratify), and **whom it is addressed to**:

- `input`: the roles that may run the action (`runRoles`: the action's, else its agent's) and the responsible role of
  the step;
- `approval`: the roles the approval waits for (`HumanTask.Roles`) and the accountable role of the step;
- `condition` / `unblock`: the accountable role of the step the run carries out;
- otherwise, or when no role is declared, the **owner**: the person the run works for (its initiator, `EndUser()`);
- decision points: anyone working on the change (addressing them is a next step).

### 2. Whether the caller may answer is the rule of the call answering it

`mayAnswer` is computed for the caller with the very checks the call answering the ask makes, so the web never offers
what will be refused: `Engine.maySubmit` (the responsible role of the step, the roles of the action; extracted from
`submit`), `Engine.mayApprove` (extracted from `Approve`), `Engine.mayUnblock`; the handler adds the permission it asks
on the run (`submit` for an input, a merge, a rebase or a clarification, `decide_flow`, `relaunch` for the board,
`answerPermission`). A question or a decision is answerable by whoever works on the change, as the graph rules today.

### 3. The change tab opens on what is asked

`ChangeAsks.svelte`, in a column on the right of the change tab, beside every pane (the change and what it waits for are seen together; the column is resized by its edge, its width kept with the layout of the workbench (`layout.asksWidth`, 480px by default), folds to a strip carrying the count of the asks for the person, and goes above the panes when the tab is narrow):

- **Waiting for you**: each ask the person may answer, with its kind, its title, its step, whom it is addressed to and
  for how long the change waits, answered in place: the panel of the run's task (`PendingTask.svelte`, the switch
  over the pending task kinds the run tab used, now shared by both: the form of ADR 0115, the approval, the unblock,
  the merge and rebase panels…), the intent dialogue of a clarification, an answer field for a question; a decision is
  ruled in the Decisions pane (a button opens it). The impacts awaiting their review join the list (they used to be an
  alert at the bottom of the overview).
- **Waiting for others**: one line per ask, with the role or the person it waits for.

The run stays one click away from each ask. The
pure helpers are `web/src/lib/changeAsks.ts` (grouping, addressee, labels, waiting time).

## Consequences

- A person opening a change sees what is theirs to do first, and answers it without leaving the change.
- The checks of who may submit or approve live in one place each (`maySubmit`, `mayApprove`), used by the calls and by
  the read model alike.
- The asks are read again when a run of the change moves (live stream) or the change does (its stamp), like the
  current state.

## Next steps

These were proposed with this ADR and are not done yet:

1. **Addressing by role beyond the change**: notifications to the holders of the role an ask is addressed to (not only
   the initiator), and a "My asks" inbox across the changes of the active project, fed by the same model.
2. **The thread of the change**: one timeline shared by the profiles working on a change, mixing the messages of the
   people and of the agents (change items, a generalisation of the `comment` kind of ADR 0110, with `@role` / `@person`
   mentions and a sub-thread per ask), the asks as they are raised and settled, and the milestones of the runs; read by
   the agents in their brief.
3. **Agents in the thread**: an LLM action asking a role a question instead of failing or getting stuck, taking an ask
   ("I am on it") and handing it over to a person, activities reacting to the messages (`on:`, ADR 0110).
