# ADR 0110 — Reactive activities: reviewing the description of a change, rating it, discussing it

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0036 §3 (companion runs, `on:`), 0106 (`on.from`), 0109
(conditions derived from the context, the history of runs)

## Context

A change is accepted only if its description (`Change.Intent`, with its title) says clearly what is wanted. Today
nothing looks at it: a person writes it, nobody rates it, and a reviewer finds out at the merge that it is vague. We
want:

1. the system to notice that the description was edited;
2. the methodology of the change to run, on that event, a review action that rates the description and can propose a
   better wording;
3. the rating to sit on the change and gate it (a condition of the methodology);
4. the feedback shown to the person as a comment bubble next to the description, in which they can answer the
   reviewing agent so that it refines its answer; the agent's run is an ordinary run (journal, ledger, prompts in the
   log, Progress), kept and run again, not a chat on the side.

What exists: companion runs (ADR 0036 §3) already are "a process run on a change, again for each event it subscribes
to, in place, with `vars.event`, an inbox while it works", but only for **transverse** methodologies (`appliesTo`).
Triggers (`event:` + `target: event_change`) start a **new** run per event, so the discussion would be spread over
runs. `UpdateChange` journals a `change.updated` entry with the edited fields but publishes no trigger event.

## Decision

### 1. An event for an edited header: `change.edited`

`graphsvc` publishes `change.edited` (`events.ChangeEdited`, in `events.All`, `TRIGGER_EVENTS`) when `UpdateChange`
changed a field of the header, with `event.fields` = `{<field>: {from, to}}` (`title`, `intent`, `goal`, `data`
keys). A filter selects on it: `"intent" in event.fields`. The edit itself stays the `change.updated` log entry (who,
when), so the history (ADR 0109) and the PROV export already see it.

### 2. Every activity may react to events: `on:`

The trigger is generalised to every activity of a methodology: a **process**, a **step** (of a process or of a method),
a **method** and an **action** may declare `on:` (the `Subscription` of ADR 0036 §3: `event`, `filter`, `from`, `self`
by default). Such an activity is **reactive**: on every matching event of a change of its own methodology (or of a
change related to it, `from: integrated | integrator | dependency`), the engine runs it on that change as a companion
run, one per change and activity, kept and run again in place for each event (the mechanism of `Engine.accompany`,
`rerun`, inbox, generalised from transverse processes to any activity). What is run:

| Activity | Agent | Goal |
|---|---|---|
| process `p` | `p` | `p` |
| step `p/s` (or `m/s` of a method) | the process (method) | the step path: its exit (ADR 0097) |
| method `m` | `m` (its generated agent) | `m` |
| action `a` | a generated agent `on.<a>` admitting `a` only | `on.<a>`: the effects of `a` |

The event says **when to look**, the conditions say **whether there is work** (ADR 0109): a run woken while the exit
of its activity holds ends at once. So the exit of a reactive activity must be derived from what the event changes
(here: "the current description is rated and the last remark of the thread is answered"), never from the history of
its own run alone; the compiler warns when a reactive activity's exit is only its `step:` condition.

A reactive activity stays an ordinary activity otherwise: a reactive step is still a step of its process, planned
towards the goal like the others; `on:` adds runs, it removes none. The methodology-level `on:` of a transverse
methodology is unchanged (its processes, alongside the changes of the methodologies it applies to). An item produced
by the actions of a reaction does not wake it again.

```yaml
processes:
  - name: description-review
    steps:
      - name: rate
        action: rate-description             # llm, writes the assessment below
        done: {description_reviewed: true}
        on:
          - event: change.created
          - event: change.edited
            filter: '"intent" in event.fields || "title" in event.fields'
          - event: change.item_added         # the person answered in the bubble
            filter: 'event.items.exists(i, i.kind == "comment" && i.data.about == "intent")'
```

The run is a standard `Process` (trigger `companion:<methodology>/<activity path>`): its journal, model calls, prompts
and history are those of any run.

**An event that supersedes the work in flight** (`supersede: true` on a subscription, proto `Subscription.supersede`).
An event arriving while the run is at work waits in its inbox by default: the run ends, then runs again for it. That is
wrong when the event makes the work in flight pointless: the person edits the description, the rating starts, the
person edits it again before the answer, and the answer would be about a text that is gone. An event of a subscription
flagged `supersede` interrupts the run instead (`Engine.restartFor`, `pkg/engine/companion.go`, in the background so the
publisher of the event never waits): its sub-processes (the agent of the step it carries out, a task waiting for a
person) are cancelled, its run in flight is interrupted (its model and tool calls end with their context, as for
`Cancel`) and lets go without failing, then the run starts again for the event, with what it waited for, what it tried
(`Disabled`) and its inbox dropped: it reads the change as it is now, which carries every earlier event. A run a person
cancelled stays cancelled. `sdlc` 0.7.4 flags its `change.edited` subscription of `description_review`; a comment in the
thread still waits for the rating in flight. The flag is honoured by the `on:` of a transverse methodology as well.

### 3. The rating is an assessment item of the change

A built-in use case, `pkg/assess` (registered like `pkg/risk`: `assess.Register()`), with an item kind `assessment`:
each item is the whole record of its `data.key` (the subject), the latest one in effect is the rating.

```text
{key: "description", score: 0..5, verdict: "clear"|"unclear"|"insufficient",
 findings: [{text, severity}], suggestion: "<proposed wording>", reply: "<what the rater says to the person>",
 rated: "<the text rated>"}
```

It is content produced by an action (ADR 0109 §7), not a fact asserting a condition: the conditions **derive** from it
and from the change as it is now. The CEL variable `assessments` gives the latest rating of each subject with two
derived flags: `current` (the subject still reads as rated: for `description`, the title, a blank line and the
intent) and `answered` (no `comment` about the subject came after the rating). A library `assessments` (ADR 0064):

- `description_rated`: a current rating of the description;
- `description_reviewed`: rated and answered, the exit of the rating step: an edit or a remark makes it false again,
  so the event that woke the step finds work to do;
- `description_accepted`: a current rating with `score >= condition.AcceptableScore` (3).

Editing the description makes every one of them false by itself (the rated text no longer matches), nothing to
invalidate. A methodology gates on them where it wants (a step's `pre`, a lifecycle guard, the guardian of the merge).

### 4. Reformulation is a proposal, the person edits

The rater never writes the description: `suggestion` is shown in the bubble with **Apply**, an ordinary
`UpdateChange` by the person (who acts stays the person; the edit raises `change.edited`, the step rates again).

### 5. The bubble is the thread of the subject

A person answers with an item of kind `comment` (`{about: "description", text}`); the rater answers with its next
rating (`reply`). The thread of a subject is its comments and ratings in the order of the log (`assess.Thread`). A
comment raises `change.item_added`, which wakes the same run of the rating step (filter on the comment); the brief of
the LLM action (`pkg/brief`) carries the latest rating and the thread, so it refines rather than starts over. The
discussion is part of the change (journaled, PROV), not of the conversation service (ADR 0085), and the rater's run is
an ordinary run (Progress, journal, prompts), reachable from the bubble.

### 6. Any subject can be rated: impacts and items, by fingerprint

A rating is about a **subject** (`data.key` of the assessment, `data.about` of a comment), whose text `assess.Text`
reads from the blackboard of the change as it is now:

| Subject | Text |
|---|---|
| `description` | the title, a blank line, the intent |
| `title`, `intent` | that field |
| `impact:<node key>` | the node as the change has it (key, type, intent, state, properties, outgoing links), canonical JSON |
| `item:<record key or id>` | the latest version of the record (kind, type, data), canonical JSON |

`rated` holds the text rated **or its fingerprint** (`assess.Fingerprint`: FNV-1a 32 bits, 8 hex digits; the web's
`fingerprint` is its twin). The brief prints the fingerprint of each subject (`SUBJECT description fp=…`, `fp=` on the
impacts and artifacts, the current one on each `ASSESSMENT`), so a rater copies 8 characters, not a node. `current`
holds while the subject reads as rated; a subject the change no longer has is not current. `assess.Assessments` /
`Of` take the blackboard (the nodes of the impacts are hydrated there).

### 7. Events of the methodology

A methodology declares its own events next to the standard ones (`events.All`):

```yaml
events:
  - {name: spec.ready, description: The spec is written}
actions:
  - {name: write_spec, kind: llm, effects: {spec_written: true}, emits: [spec.ready]}
  - {name: notify, kind: human, effects: {notified: true}, on: [{event: spec.ready}]}
```

- **Declaring**: a name of lower case words separated by dots (`methodology.EventNameRE`), none of the standard
  families (`change.`, `process.`, `step.`, `methodology.`), declared once. An `on` (of the methodology or of an
  activity) and an event trigger may follow a standard event or a declared one, nothing else: the compiler refuses an
  unknown event (`checkEvent`), whatever its origin; following another methodology's event means declaring it too.
- **Raising**: an event is raised by a **signal item** of its name (`domain.KindSignal`, `Type` = the name) on the
  change: an action lists it in `emits` and the engine adds the signal once the action reached its effects
  (`Engine.emitEvents`, `data {action, step}`, in the step's items); a person, a script or an LLM may add one too
  (`goap-change/signal`). `TriggerManager.Handle` turns the signals of a `change.item_added` into one event per name
  (`engine.DeclaredEvents`, the signals in `event.items`; a standard name is never raised this way), then runs the
  reactions, the companions, the relays and the triggers on it like any event, so it crosses NATS as the
  `change.item_added` it comes from. The items of a reaction, and the events they raise, do not wake it again.
- **Checking the composition**: `CompositionIssues` checks every subscription now (the methodology's and its
  activities', `Methodology.AllSubscriptions`): an event followed `from: integrated | integrator` that its source does
  not declare is a warning with its fix.

Proto `Methodology.events` (`EventDef`), `Action.emits`; attributes `events` of `methodology@MethodologyVersion` and
`emits` of `methodology@Action`.

### 8. The editor of `on:`

The methodology editor edits the `on` of the methodology, of each process, step, method and action with one form
(`SubscriptionsEditor.svelte`): the event is picked among the standard events and those the methodology declares
(two groups), an event it does not know is flagged at once, `from` (self, integrated, integrator, dependency) and the
CEL filter beside it, the registry's issues under each row. The methodology tab declares the events, the action tab
ticks what the action emits, the triggers of an agent list the declared events too. `fromForm` reports the unknown
events, the badly named declarations and the unknown emits with the compiler's paths
(`activityEventIssues`, `web/src/lib/subscriptions.ts`).

### 9. Every activity is carried out by an agent: the rating is a planned method

An activity is always carried out by an agent, autonomous or human. The rating of the description is therefore a
**method** planned by GOAP (`description_rating` in `sdlc`, the step `description_review/rate` naming its capability),
whose pool holds an llm action (`rate_description`, cost 1) and a human one (`rate_description_by_hand`, cost 6,
roles `business_analyst` / `product_owner`), both reaching `description_reviewed`. The planner takes the llm action
first; when it cannot run or keeps failing (disabled after `Engine.MaxFailures`), it plans the human one, a task for a
person, who rates the same way (an `assessment` item). The fallback exists without being preferred: its cost says so.
A reaction owns the actions of the methods its capability steps run (`ownActions`), so their ratings do not wake it
again; the bubble finds the run from the step that wrote the rating, up to the companion run (`raterRun`).

### 10. As implemented

- `pkg/events.ChangeEdited`, `domain.ChangeEvent.Fields`, `domain.EditedFields`; published by
  `graphsvc.Handler.UpdateChange` and `engine.EventingGraph.UpdateChange`; `TriggerEvent.Fields`, `event.fields`.
- `pkg/methodology`: `On` on `Process`, `Step`, `Method`, `Action` (YAML, proto, attribute `on` of
  `methodology@Activity`); `compileReactions` / `Compiled.Reactions()` (`reaction.go`): event known, filter compiles,
  `from` valid, a reactive action declares effects and is not a specialization, none in a transverse methodology
  (it subscribes at its own level), a warning for a reactive step whose exit is only its own run.
- `pkg/engine`: `Engine.react` from `Accompany` (the change's own methodology, `from: self`) and from `relay` (the
  related changes); `companionRun` shared with the transverse processes.
- `pkg/assess`, CEL `assessments` (conditions and lifecycle guards), library `assessments`, `pkg/brief` (`ASSESSMENT`,
  `THREAD`, the intent up to 1500 runes).
- `methodologies/sdlc.yaml` 0.7.1: process `description_review`, step `rate` (method `description_rating`, goap: `rate_description` llm first, `rate_description_by_hand` human fallback),
  reactive on `change.created`, `change.edited` (title or intent) and comments on the description; the analysis step
  of `software_delivery` waits for `description_accepted` (the run waits for a condition it does not establish, and is
  tried again when the change moves).
- Web: `DescriptionReview.svelte` under the intent in the change tab (score, state, findings, suggestion with Apply,
  thread, answer, link to the run), `web/src/lib/descriptionReview.ts`; `change.edited` in `TRIGGER_EVENTS`.

## Consequences

- Any activity of any methodology can react to the events of its changes (description, title, items, transitions),
  with runs that are kept and discussed, not only the processes of transverse ones.
- The rating is derived content: a new description is never "accepted" by an old rating.
- `assessment` / `comment` rate any subject of the change: its description, an impact, an item.
- A methodology names the moments of its own work (`spec.ready`) and its activities follow them like the standard
  events, checked at compile and across the composition.
