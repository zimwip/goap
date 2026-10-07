# ADR 0094 — The assistant reads a methodology, filtered and small

**Status**: accepted, implemented (server side) · **Date**: 2026-10 · Builds on ADR 0023 (methodologies are graph
nodes), 0087 (the assistant), 0090 (`list_agents`, applicable methodologies), 0092 (screen tools).

## Context

`list_methodologies` gives an overview (name, description, a few goal examples). To explain how a methodology works
("which steps does the delivery process have?", "what does this action do?", "who may run this agent?") the assistant
needs the definition itself, but a methodology holds hundreds of elements, prompts and scripts: giving it whole would
drown the context and the budget of the turn. The result of a tool has to be as small as the question allows.

## Decision

- **Who, why, how, what**: a read of the *how*, as the caller, for the person's questions. It changes nothing, runs
  nothing, asks no confirmation and calls neither the graph nor the engine; it is a server tool counted in the per-round
  tool-call cap (`MaxToolCalls`) like the others. It is the seventh server tool; `list_methodologies` stays the overview.
- **Scope**: the methodologies applicable to the active project (`Projects.ApplicableMethodologies`, the rule of
  `list_methodologies`, `list_agents` and `create_change`), at most 30; `methodology` names one of them, any other name is
  refused with the message `create_change` gives. The definitions are read through the `Methodologies` port, as the
  caller (`Registry.Methodology`, the compile cache). The declared definition only: what the compiler generates for
  processes and methods (agents, actions, goals) is not listed, the processes and methods are.
- **Arguments** (all optional, unknown ones refused): `methodology`; `kind` (`agent | action | goal | process | step |
  role | method | trigger | condition | library`; a step is named by its path `<process or method>/<step>/<sub-step>`,
  a trigger belongs to its agent, a library is an imported condition library); `name` (exact or glob `*` `?`,
  case-insensitive; a step also matches its own last segment); `q` (case-insensitive text in name, description and
  examples); `parent` (the process / method / step of a step, the agent of an action, goal or trigger); `fields`
  (projection; a name that is not a field of the kind is refused listing the valid ones, the fields are those of the
  definition's JSON, plus `kind`, `parents`, `how` for a step and `actionKind` for the kind of an action); `detail`
  (`names`: name and kind; `summary`: name, kind, a description cut to one line of 160 bytes and the few defining
  fields of the kind, lists cut to 8; `full`: the definition, strings cut at 2000 bytes, the sub-steps of a process,
  method or step as a list of paths); `limit` (default 20, max 50, clamped) and `offset`.
- **Result**: `{methodology?, kind?, total, returned, offset, truncated, next?: {offset}, counts?, hint?, items}`.
  `methodology` is set when one is queried (items carry their `methodology` otherwise). With no `kind` and one
  methodology, `counts` gives the matches per kind, and when no filter at all is given the default detail is `names`: the
  overview. The model zooms: overview, then a kind with a name or `q`, then `summary`, then `full` of one element.
- **Caps**: the JSON of a whole result is at most 6 KiB (`MaxQueryBytes`): items that would pass it are dropped,
  `truncated` is true, `next.offset` continues after the last one returned and `hint` says to narrow with kind / name /
  q / parent, to project with `fields`, or to paginate. `detail: full` returns at most 3 items (`MaxFullItems`): more
  is clamped, `truncated` and a hint say so. With `fields`, the count cap does not apply but the byte cap does. A first
  item over the cap alone returns none and a hint to project.
- **Code**: the engine is pure and unit-tested (`internal/assistantsvc/methodquery.go`, `RunQuery(Query,
  []*methodology.Methodology)`): the valid fields come from the struct tags of `pkg/methodology`, nothing is parsed or
  compiled again; the tool glue (arguments, scope, registry port) is `methodtool.go`.
- **Prompt**: start narrow (kind / name / q), prefer `names` / `summary`, project with `fields`, paginate instead of
  asking for full lists, explain a methodology from the results only.

## Consequences

- The assistant can answer questions on a methodology at a cost proportional to the question; a wide question costs
  several small calls rather than one big one, within the 4 rounds of a turn.
- No secret or other tenant's data is exposed: only definitions of methodologies the project applies, which carry no
  secret by design (secrets are references, adapters are not part of them), read with the caller's rights.
- The field names are the JSON names of `pkg/methodology`: a new field of a definition is queryable with no change here
  (its `summary` stays the list of defining fields in `summaryFields`).

## Not done

Conditions imported from a library are listed by name only; the compile issues of a methodology and the CEL expressions
of the generated conditions (steps, states) are not exposed; no query across methodologies the project does not apply.
