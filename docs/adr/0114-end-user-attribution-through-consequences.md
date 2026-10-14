# ADR 0114 — The end user travels from one consequence to the next

**Status**: accepted, implemented (see "Not done") · **Date**: 2026-10 · Extends ADR 0089 (the ledger of LLM calls),
ADR 0036 §3 (transverse methodologies, companion runs), ADR 0110 (reactive activities), ADR 0043 (who acts)

## Context

The ledger of the model gateway (ADR 0089) records every LLM call under the subject of its principal. What a person
does directly is theirs (the assistant, the helper, a run they start, its sub-agents). What their action **sets off**
is not: an edit of a change raises `change.edited`, a trigger or a reactive activity starts a run acting as
`system:trigger:<key>` or `system:companion:<methodology>/<activity>`, that run calls a model, writes items, raises
further events, starts sub-agents... All of it landed under platform subjects, so a person's "My tokens" and the
header's consumption of the day showed a fraction of what their work cost, and nothing linked the indirect calls to
the person whose action caused them.

## Decision

### 1. A principal names the end user it acts for

`authz.Principal.OnBehalfOf` is the person a **platform service** (a `system:` principal) acts for; `EndUser()` is the
subject of a person, the `OnBehalfOf` of a service (`""` when it acts for nobody). `For(user)` gives a service the end
user it acts for; a person is unchanged: a person always acts for themselves, so an `OnBehalfOf` on a person is
ignored. It **attributes**, it never grants: authorization reads the subject and its roles, as before.

The identity headers carry it (`identity.HeaderOnBehalfOf`, `X-Goap-On-Behalf-Of`, written by `SetHeaders` and the hub
client, read by `FromHeaders` for a `system:` subject only); the gateway deletes it from every incoming request.

### 2. It is carried from one consequence to the next

- **Change events**: `domain.ChangeEvent.EndUser` is the end user of the writer (`graphsvc.Handler.publish`,
  `engine.EventingGraph`), so it crosses NATS with the event; `TriggerEventOf` keeps it (`TriggerEvent.EndUser`, and
  `endUser` in the activation of the event, `vars.event.endUser`). A process event names it through the process.
- **Triggers**: the run a trigger starts on an event acts as the trigger `For` the event's end user (the one who
  raised a change event, else the end user of the process the event comes from).
- **Companion runs and reactions** (ADR 0036 §3, 0110): started `For` the end user of the event; run again in place,
  restarted by a superseding event or taken from the inbox, they act for the end user of that event.
- **Sub-agents and other consequences of a run**: a process started by a platform service that names no end user takes
  the one of its parent (`ParentID`), else of the process that caused it (`Cause`). A run acting as its initiator
  (`ProcessRef.Actor`) carries it to everything it calls: the model gateway, the graph (so its own events), the MCP hub.
- **Persistent instances and relaunches** keep the initiator they act for (ADR 0112: the execution acts for whoever
  asks for it now).

`Process.Initiator.OnBehalfOf` keeps it on the run (proto `engine.v1.Principal.on_behalf_of`; the run tab shows
*On behalf of*; `ListProcesses(mine)` and the Processes explorer list the runs on the caller's behalf too).

### 3. The ledger attributes every call

`llm_call.end_user` (migration `0007_llm_call_end_user.sql` in both dialects; earlier rows: the subject when it is not
a platform service) is the principal's `EndUser()` when the call is recorded (`Call.EndUser`, proto `LLMCall.end_user`).
`UsageFilter.EndUser` selects the calls attributed to a person, `group_by: end_user` groups them. A caller who does not
administer the platform reads the calls attributed to them (their own and those made on their behalf; the end user of
the filter is forced to them, the subject only narrows: to themselves or to a service, another person is refused) and
the exchanges of those calls; a platform service acting for nobody reads its own.

### 4. One consumption per person

The web asks the ledger for the calls attributed to the person (`usageScope()`: `{endUser: me}`, administrators the
whole platform): the Tokens console (a *Via* column naming the service a call was made through,
`tokenStats.viaOf` / `actorLabel`), the Token usage pane (an *On your behalf* figure, a *Who acted* breakdown, *Via*
in the most expensive calls; *By end user* for the platform) and the header's consumption of the day (grouped by
subject: the tooltip says how much was made on the person's behalf, `tokenStats.directSplit`).

## Consequences

- A person's consumption is what their actions cost, directly or through what they set off; a platform service still
  appears as the subject of its calls, so who acted and for whom are both kept.
- The chain stops at the first consequence that names no end user: work the platform does by itself (a timer, a
  methodology published, the indexer) is attributed to nobody, as before.

## Not done

- The indexer's embeddings of the nodes a change wrote are not attributed (one batch serves several changes).
- The quotas stay per model and period; there is no per-person quota on attributed usage.
- A trigger fired by a timer, or a person's edit raised from another tool that does not identify them, has no end user.
