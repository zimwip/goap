# ADR 0158 — Modes of the front, and tasks in the context of their change

**Status**: accepted (first step implemented) · **Date**: 2026-10

## Context

Every read of the graph names its view (ADR 0157): the working change on its option, else the head of main. The front
still mixes several activities in one place. A person can open several change editors, edit a node of a change that is
not the working one, and browse the history and the definitions from the same shell. Human tasks are reached through
the change tab or a run, not as what drives a person's attention.

A task is work on a change. It only makes sense in the view of that change.

## Decision (proposed)

1. **Tasks are first-class and drive attention.** They are listed at three levels:
   - **My tasks**, across every open change: the tasks a person accepted, and the ones addressed only to them, which
     count as accepted.
   - **The tasks of the working change**: the change selected in the header.
   - **The tasks of a change open in an editor**, which need not be the working change.

   A task can be selected from any list. Working on a task switches to change mode with the task's change as the
   working change, so the task is always done in the view of its change.

2. **The front has modes**, switched from a header button:
   - **Project mode**: navigate the project, its open changes, their scope and their tasks (the tasks of every change
     of the project). Several change editors may be open, read only. This mode may relate to a methodology like
     `release-based`.
   - **Change mode**: work in the working change. The button is enabled only when a change is selected. **This is the
     only mode that modifies the graph**, as a task does: every write is a write of the working change. It relates to a
     methodology like `sdlc`.
   - **Navigation mode** (administrators): the graph and its history, the Baseline explorer, diffs and versions.
   - **Administration mode** (administrators): domains, methodologies, projects and the organisation.

   Project mode and change mode stay connected: a change is opened from the project, and its tasks are listed there.

3. **Open question**: whether a methodology declares the mode it belongs to (project or change).

## Consequences

- One rule for writes: only change mode writes, in the view of the working change. Editors of other changes are read
  only.
- The task lists need a read of the tasks of a person across changes (`GetChangeAsks` is per change today, ADR 0117).
- Navigation and administration leave the everyday shell, which shows less to people who are not administrators.

## Implementation (first step)

- The header switch and the modes (`web/src/lib/shell/mode.svelte.ts`, `ModeSwitch.svelte`): each mode shows its left
  tools (`TOOL_MODES`); change mode is enabled only while a change is worked in, and a page opens in it when one is;
  navigation and administration are offered to administrators only.
- The Tasks tool (`views/nav/TasksExplorer.svelte`): my tasks, the working change's, those of the changes open in an
  editor, the project's in project mode. The engine reads the asks of the open changes at once (`ListAsks`,
  `Engine.Asks`).
- Picking a task works on it: its change becomes the working change, change mode, the change tab opened on the task.

## Implementation (second step): who holds a task

- Every task the person may act on is shown: *Available to you* lists, in every mode, the tasks they may answer that
  nobody holds and they did not turn down, besides *My tasks*.
- Accepting a task is shared, held by the engine on the pending task of the run (`HumanTask.Assignee`, `AssignedBy`,
  `AssignedAt`, `Declined`; `engine/engine/assign.go`): `AcceptTask` makes it the caller's when they may answer it and
  nobody holds it; `RejectTask` gives it back (its holder) or declines it (one it is offered to), and it is no longer
  offered to them; while it is held, only its holder submits or approves it, besides whoever may reassign it. Each
  decision is a `task` record of the journal of the change.
- Reassignment: `AssignTask` gives the task to a person who may answer it (judged as that person on the project of the
  run), or takes it back; it needs `task:assign` on the project of the run, which the default rules grant the
  administrators and owners of that project or of a project above it (`isProjectAdmin` over the project chain) and the
  platform administrators. `ListTaskCandidates` keeps the people who may answer it among those given (the web offers
  the users of the organisation).
- A dedicated screen per task (`views/editors/TaskTab.svelte`, tab `task`, opened from the Tasks tool and from the strip
  of the change): its change, whom it is for, who holds it and who gave it, accept, give back or turn down with a
  reason, answer in place; the assignment section shows only with `mayAssign`.

Not yet: the editors of a change other than the working one, or outside change mode, are not read only; the
editors of navigation and administration still open from anywhere (search, commands, links); a notification to the
person given a task; the conditions, unblock and decision asks are not held (anyone they are for answers them).
