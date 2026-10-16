# ADR 0018 — Algorithms: the DSL as a generic capability, plugged into the domain

**Status**: accepted, implemented; amended by ADR 0055 (property validators plugged on attributes, node validators),
ADR 0061 (the adapter leaves `contracts/algo` for `lib/adapter`), ADR 0079 (validation on drafts, moves checked when taken)
and ADR 0100 (link validators) · **Date**: 2026-09 · Extends ADR 0007 (sandbox), ADR 0013 (domains), ADR 0014
(lifecycle).

## Context

Dynamic code (JavaScript / Go, `lib/dsl`) was only usable in the `script` actions of a methodology.
The object part of the model, the **domain**, stayed declarative: a property could not be validated
beyond its name, a lifecycle transition could only be judged by a CEL guard or `requires`, and nothing
could run when a transition was taken. Making the domain customizable meant changing the platform.

## Decision

1. **The DSL is a generic capability tied to a usage.** A *usage* fixes where a script runs, the
   context object (`ctx`) it sees and what its result means. The script engine of `lib/dsl`
   (goja / yaegi, `runScript`) is shared; only the context differs. Fixed list (`algo.Usage`, plus the adapter):

   | Usage | Context | Go entry point | Meaning |
   |---|---|---|---|
   | `action` | `Ctx` (blackboard, domain, LLM, agents, tools) | `Run(ctx *dsl.Ctx) error` | writes to the change |
   | `property_validator` | `ValidatorCtx` | `Run(ctx *dsl.ValidatorCtx) error` | accepts or rejects one property value |
   | `node_validator` | `NodeValidatorCtx` | `Run(ctx *dsl.NodeValidatorCtx) error` | accepts or rejects a node as a whole (ADR 0055) |
   | `link_validator` | `LinkValidatorCtx` | `Run(ctx *dsl.LinkValidatorCtx) error` | accepts or rejects a link with both its ends (ADR 0100) |
   | `transition_guard` | `GuardCtx` | `Run(ctx *dsl.GuardCtx) error` | allows or refuses a lifecycle transition |
   | `transition_action` | `TransitionCtx` | `Run(ctx *dsl.TransitionCtx) error` | sets / removes properties of the node that moved |
   | `adapter` | `AdapterCtx` (`tool()`, `args()`, `param()`, `call(operation, args)`) | `Run(ctx *dsl.AdapterCtx) error` | implements the tools of an MCP with the operations of a connector (ADR 0019) |

   The pluggable contexts (`ValidatorCtx`, `NodeValidatorCtx`, `LinkValidatorCtx`, `GuardCtx`, `TransitionCtx`) are **pure**: no LLM, tool or sub-agent call, no blackboard. They
   reject with `ctx.fail(message)`, by throwing / returning an error, or (JavaScript) by returning
   `false` or a string. A JavaScript algorithm is the *body* of `function (ctx)`, so it may `return`.
2. **Action code stays in the agent declaration.** The `action` usage has no algorithms: an action
   keeps its inline `code`. The validator, guard and transition action usages are *pluggable*.
3. **Three levels, all in the domain** (`Schema.algorithms`, `Schema.algorithmInstances`):
   - **algorithm type** = the usage (fixed, above);
   - **algorithm** = a named script of a type, in `javascript` or `go`, with a dynamic list of typed
     **parameters** (`string`, `number`, `boolean`, `regex`, `enum`, `strings`, `json`; required,
     default, enum values) that the script reads with `ctx.param(name)` to alter its behaviour
     (e.g. a regex validator takes the regular expression);
   - **algorithm instance** = an algorithm with its parameter values (checked against the
     declarations, defaults filled in, regexes compiled).
4. **Plugs** (instances only, checked for existence and type when the domain is saved): the attributes of a node
   type or a link type carry their property validators (`attributes[].validators`, ADR 0055), a node type its node
   validators (`NodeType.validators`), a link type its link validators (`LinkType.validators`, ADR 0100), and
   `Transition.guards: [instance]` / `Transition.actions: [instance]`. **The list order is the call
   order**; validators of a supertype run before those of its subtypes, and for each type its attributes' before its
   node validators; algorithm guards run after the CEL guard and `requires`, actions after the guards.
5. **Evaluation** follows ADR 0014: the type catalogue (ADR 0012 §2) resolves the plugged instances
   (`validators`, and `guardAlgos` / `actionAlgos` of the lifecycle transitions: instance name, algorithm, language,
   code, parameter values), and publishing a domain version applies to the changes checked afterwards. This first
   ran the validators when a version was written on the change branch and the guards and actions when the change was
   applied. Since ADR 0079 a node has no version during a change: the validators run on the draft when its impact is
   accepted and again when the change lands (`Graph.checkVersion`, ADR 0100), never on a draft edit; the guards and
   actions of a transition run when the move is taken on the draft (`ImpactNodeTransition`), the properties an action
   sets being written into the draft by the move's `transitioned` event (and validated again); at landing each move
   recorded in the log is replayed against the lifecycle (`walkTransitions`), not guarded again.
6. **Storage**: proto `registry.v1` (`Algorithm`, `AlgorithmParam`, `AlgorithmInstance`,
   `NodeType.validators`, `LifecycleTransition.guards/actions`, `Domain.algorithms/algorithm_instances`), stored
   with the domain version in the registry's database (ADR 0023).
   **Only domains** carry algorithms, a methodology never does.
7. **IDE**: the *Domains* explorer lists, under each domain version, an *Algorithms* group (beside node types and link types, by usage, instances underneath) that manages the algorithms and instances of a domain draft (editor,
   parameter table, instance value forms, *try it* through `RegistryService.RunAlgorithm`, which
   runs an algorithm on a sample input without storing anything); the domain editor plugs instances.

   The `adapter` usage is the exception: it is **not declared in a domain** (it is no `algo.Usage`; `lib/adapter`
   since ADR 0061, its context still `dsl.AdapterCtx`): it is a `platform@AdapterDef` node, changed through a change, with `mcp`, `connector` and `secret`
   parameters (never readable by the code), and it is **not plugged** into a node type or a
   lifecycle: organisational units instantiate it (ADR 0019). It calls the connector, so it is bounded by 30 s and
   32 calls (`dsl.AdapterTimeout`, `MaxAdapterCalls`), and is run by the MCP hub, not by the graph service.

## Consequences

- Algorithms are versioned and published with their domain (immutable once published, ADR 0023).
  `domains/alm.yaml` ships examples: `regex-match`, `not-blank` (Go), `max-length`,
  `children-in-states`, `stamp-date`.
- Algorithms run **in the process that judges the change** (graph service), not in a per-process
  sandbox: they are pure and their interpreters expose no file, network or process access (Go:
  stdlib subset; JavaScript: no `require`) and are bounded by a 5 s timeout. A Go algorithm that
  loops forever cannot be killed (its goroutine is abandoned, as for actions); JavaScript is
  interrupted. Running them in the sandbox pool (ADR 0007) is the next step if untrusted domain
  authors are expected.
- Existing invalid data is never rejected retroactively: only the drafts a change accepts and lands are validated.
- Not done: algorithms shared across domains, algorithms for other extension points (conditions), a dry run of the
  plugs against existing nodes when a domain is published.
