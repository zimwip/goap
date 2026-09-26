# ADR 0018 — Algorithms: the DSL as a generic capability, plugged into the domain

**Status**: accepted · **Date**: 2026-09 · Extends ADR 0007 (sandbox), ADR 0013 (shared domain), ADR 0014 (lifecycle).

## Context

Dynamic code (JavaScript / Go, `pkg/dsl`) was only usable in the `script` actions of a methodology.
The object part of the model, the **domain**, stayed declarative: a property could not be validated
beyond its name, a lifecycle transition could only be judged by a CEL guard or `requires`, and nothing
could run when a transition was taken. Making the domain customizable meant changing the platform.

## Decision

1. **The DSL is a generic capability tied to a usage.** A *usage* fixes where a script runs, the
   context object (`ctx`) it sees and what its result means. The script engine of `pkg/dsl`
   (goja / yaegi, `runScript`) is shared; only the context differs. Fixed list (`algo.Usage`):

   | Usage | Context | Go entry point | Meaning |
   |---|---|---|---|
   | `action` | `Ctx` (blackboard, domain, LLM, agents, tools) | `Run(ctx *dsl.Ctx) error` | writes to the change |
   | `property_validator` | `ValidatorCtx` | `Run(ctx *dsl.ValidatorCtx) error` | accepts or rejects one property value |
   | `transition_guard` | `GuardCtx` | `Run(ctx *dsl.GuardCtx) error` | allows or refuses a lifecycle transition |
   | `transition_action` | `TransitionCtx` | `Run(ctx *dsl.TransitionCtx) error` | sets / removes properties of the node that moved |

   | `adapter` | `AdapterCtx` (`tool()`, `args()`, `param()`, `call(operation, args)`) | `Run(ctx *dsl.AdapterCtx) error` | implements the tools of an MCP with the operations of a connector (ADR 0019) |

   The first three algorithm contexts are **pure**: no LLM, tool or sub-agent call, no blackboard. They
   reject with `ctx.fail(message)`, by throwing / returning an error, or (JavaScript) by returning
   `false` or a string. A JavaScript algorithm is the *body* of `function (ctx)`, so it may `return`.
2. **Action code stays in the agent declaration.** The `action` usage has no algorithms: an action
   keeps its inline `code`. The other three usages are *pluggable*.
3. **Three levels, all in the domain** (`Schema.algorithms`, `Schema.algorithmInstances`):
   - **algorithm type** = the usage (fixed, above);
   - **algorithm** = a named script of a type, in `javascript` or `go`, with a dynamic list of typed
     **parameters** (`string`, `number`, `boolean`, `regex`, `enum`, `strings`, `json`; required,
     default, enum values) that the script reads with `ctx.param(name)` to alter its behaviour
     (e.g. a regex validator takes the regular expression);
   - **algorithm instance** = an algorithm with its parameter values (checked against the
     declarations, defaults filled in, regexes compiled).
4. **Plugs** (instances only, checked for existence and type when the domain is saved):
   `NodeType.validators: [{property, instance}]` (own or inherited property) and
   `Transition.guards: [instance]` / `Transition.actions: [instance]`. **The list order is the call
   order**; validators of a supertype run before those of its subtypes; algorithm guards run after the
   CEL guard and `requires`, actions only once every transition of the change is accepted.
5. **Evaluation** follows ADR 0014: the NodeType nodes of the graph embed the plugged instances
   *resolved* (`validators`, and `guardAlgos` / `actionAlgos` inside the embedded lifecycle: instance
   name, algorithm, language, code, parameter values), so a change is judged by the model of its
   reference baseline and republishing a domain updates them in place (`Sync`, `validators` is a
   patched key). Property validators run when items are added (early feedback: create, and update on
   base + patch) and when the change is applied on the target graph (created and updated nodes; also
   again after a transition action). Transition guards and actions run when the change is applied.
   A transition action's changes are written into the version the transition produced
   (`Tx.SetNodeProps`); when a node moves several times in one change, only its last transition is
   checked, and so is the only one whose actions run.
6. **Storage**: proto `registry.v1` (`Algorithm`, `AlgorithmParam`, `AlgorithmInstance`,
   `NodeType.validators`, `LifecycleTransition.guards/actions`, `Domain.algorithms/algorithm_instances`);
   PostgreSQL `domain_algorithm` and `domain_algorithm_instance` (JSON definitions, migration 0010),
   validators in the node type `meta`, guards / actions in the lifecycle definition; SQLite keeps the
   whole domain as one JSON document. **Only shared domains** carry algorithms: a methodology that
   embeds its domain is refused if it declares or plugs any.
7. **IDE**: an *Algorithms* section manages the algorithms and instances of a domain draft (editor,
   parameter table, instance value forms, *try it* through `RegistryService.RunAlgorithm`, which
   runs an algorithm on a sample input without storing anything); the domain editor plugs instances.

   The `adapter` usage is the exception: it is declared in the library like the others, with `mcp` and
   `connector` and `secret` parameters (never readable by the code), but it is **not plugged** into a node type or a
   lifecycle: organisational units instantiate it (ADR 0019). It calls the connector, so it is bounded by 30 s and
   32 calls, and is run by the MCP hub, not by the graph service.

## Consequences

- Algorithms are versioned and published with their domain (immutable once published, ADR 0013).
  `domains/alm.yaml` ships examples: `regex-match`, `not-blank` (Go), `max-length`,
  `children-in-states`, `stamp-date`.
- Algorithms run **in the process that judges the change** (graph service), not in a per-process
  sandbox: they are pure and their interpreters expose no file, network or process access (Go:
  stdlib subset; JavaScript: no `require`) and are bounded by a 5 s timeout. A Go algorithm that
  loops forever cannot be killed (its goroutine is abandoned, as for actions); JavaScript is
  interrupted. Running them in the sandbox pool (ADR 0007) is the next step if untrusted domain
  authors are expected.
- Property validators are not run on nodes that a change merely moves, links or deletes, nor on
  `merge_node` results; existing invalid data is never rejected retroactively.
- Not done: algorithms shared across domains, algorithms for other extension points (link
  validators, conditions), a dry run of the plugs against existing nodes when a domain is published,
  running the PostgreSQL migration in the automated tests without `GOAP_TEST_PG_DSN`.
