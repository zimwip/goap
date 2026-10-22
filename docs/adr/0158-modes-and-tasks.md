# ADR 0158 — Modes of the front, and tasks in the context of their change

**Status**: proposed · **Date**: 2026-10

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
