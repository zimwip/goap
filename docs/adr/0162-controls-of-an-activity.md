# ADR 0162 — Controls of an activity: rules in the domain, conditions used as controls, a measure that answers with the rule broken

**Status**: accepted, step 1 implemented · **Date**: 2026-10 ·
Builds on ADR 0034 (processes and steps), ADR 0050 (methods), ADR 0075 (vetos, objectives, derogations), ADR 0109
(conditions derived from the context, scoped actions), ADR 0120 (references between elements are links), ADR 0152
(code texts), ADR 0157 (every read names its view), ADR 0160 (read queries in Cypher).

## Context

An activity has two kinds of input. What it **consumes** (a specification, a component) is transformed into what it
produces; the methodology already says it with `requires` / `produces`. What **governs** it is not consumed: the
coding rules, the architecture decisions, the conventions of the organisation shape the result without being changed
by the activity (the *control* of IDEF0's ICOM). A coding agent given a specification should also be given the rules
that apply to what it writes, and should learn, cheaply, which of them its result breaks so that it can fix it.

Two things are distinct and live in different places:

- **The method** is a transformation applicable to any organisation. It carries the general quality rules and says
  *how* the quality conditions are applied: list the architecture decisions, follow them, verify them.
- **The values of the rules** (this ADR, that coding rule) are domain content: an organisation produces them within a
  project, because they apply locally. They are nodes of the domain (`alm`), written through changes like any other.

So the control of an activity is local to the step: the rules may be the *product* of another process (which sees
them as consumables) and the *control* of this one. An activity enters with a question (what am I doing, on what
subject) and leaves with the control function that applies to it.

Collecting the rules is an exercise of its own. Finding no rule after collecting them is a valid answer: it means
there is no rule, not that nobody looked.

## Decision

### 1. Rules are domain content (`alm` 1.8.0)

`alm@Rule` (`statement`, the rule in one imperative sentence, its name; `rationale`; `severity` of the enum
`rule-severity`: `veto` (default), `objective`, `advice`; `technology`; `check`, a code text, ADR 0152, whose language
is `cypher` or `shell`; `fix`, how to fix a violation in one line), extended by `alm@ArchitectureDecision`
(`context`, `consequences`) and `alm@CodingRule`. A rule is linked to what it governs by `alm@governs` (from a rule,
to any node) and to the rule it replaces by `alm@supersedes`. Lifecycle `rule`: `draft` (not committable),
`proposed`, `accepted`, `superseded`, `retired`. The severities are those of ADR 0075: a veto blocks, an objective
needs a derogation, an advice is noted.

### 2. A condition is a control by its use

A condition is never marked as a control. It stays an ordinary condition of the methodology, here the one saying that
the rules were collected: the output of the activity that collects them. What makes it a control is its use by an
activity: an action or a step (of a process or a method) lists it among its `controls`, and that use says which
rules govern the activity (`methodology.Control`):

```yaml
conditions:
  - name: architecture_decided
    description: The architecture decisions of the change are collected (there may be none)
    expr: 'artifacts.exists(a, a.target == "architecture")'   # the collection was done
processes:
  - name: delivery
    steps:
      - {name: design, action: decide}                         # produces architecture_decided
      - name: build
        action: implement
        controls:
          - condition: architecture_decided                    # waited for
            types: [alm@ArchitectureDecision]                  # the rules of the domain it covers
            via: alm@governs                                   # only those linked to the subject of the execution
            where: 'self.state == "accepted"'                  # CEL: self the rule, subject the subject, change the change
            rules:                                             # the general rules of the method itself
              - {id: DOC, statement: Every component says what it is for}
```

- The **condition** says the rules were collected; the same condition is the output of one activity and the control
  of another, as a condition is the output of an activity and the input of another (ADR 0120).
- The **use** says which rules it covers: the nodes of `types` (a subtype is not one of them unless named), linked to
  the subject through `via` when given, kept by `where`; and the general rules the methodology writes. A use covers
  rules (types, rules or both); its condition is about the change (no scope, no aggregate).
- Two activities may use one condition with different coverage (the coding rules for the build, the architecture
  decisions for the design review).

### 3. Stored as `controlled_by`

Stored, a use is the link `methodology@controlled_by` from the activity to the condition node (ADR 0120), carrying
`types`, `via`, `where` and `rules` as its attributes, as `requires` / `produces` carry their value. The condition
node holds nothing of it. **An activity waits for its controls by default**: the compiler merges their conditions into
the entry of the step (and of its sub-steps) and the `pre` of the action, so nothing is planned before the rules are
collected. The definition is not modified.

### 4. The control function of an execution

`Compiled.ControlsOf(action, paths...)` gives the controls of an execution: the action's, the one it implements, then
each step it runs for with the steps containing it. `Engine.ControlOf(change, action, step, subject)` resolves them
in the view of the change: each control is **established** (its condition holds: its rules are given, possibly none)
or **pending** (not collected yet: no rule is known). A run records the rules it was given on its step
(`Step.Controls`: `ADR-1@3` for a version of a rule of the domain, `<condition>/<id>` for a general rule,
`<condition>?` for a pending control), so what governed a result stays traceable.

### 5. Feedforward: one line per rule

An llm action is given a `CONTROLS` section in its system prompt: one line per rule, its id, severity and statement,
`(not measured)` when it has no check, `none` when the rules were collected and none governs the work, and the
controls not collected yet. Never the rationale nor the check: the cheapest form an agent can act upon.

### 6. Feedback: a measure that answers with the rule broken

The builtin `control.check` measures the rules of the controls of its run, for its subject, and costs no model call:

- `cypher`: a read query (ADR 0160) over the view of the run, `subject` bound to the subject when the query names it;
  each row is a violation, the column `at` saying where, `msg` what.
- `shell`: a command in the workspace of the change (`workspace/bash`, in the directory of the subject); SARIF on its
  output, else its `file:line: message` lines, else a failed exit is one violation.

The report is an artifact (`control_report` by default, `params.artifact`): the controls and their state, the rules
checked and unmeasured, the violations (rule, severity, where, what, the fix; at most ten per rule, the rest counted),
the checks that failed, the vetos and objectives broken, `ok`. `params.revisionOf` counts the artifacts of a target for
the subject (its code changes) as the revision of the report, so a condition tells a report current. The output of
the step is one line per violation, `ADR-1 [veto] CMP-2: depends on another component → go through an interface`:
what an agent reads to fix its work.

## As built (step 1)

- `domains/alm.yaml` 1.8.0; `lib/domains/builtin/methodology.yaml` 1.17.0 (`Activity.controls`, link type
  `controlled_by` with `types`, `via`, `where`, `rules`).
- `engine/methodology/control.go` (`Control`, written as a condition name or in full; `ControlRule`, `ControlCheck`,
  `ControlInfo`, `ControlsOf`, `compileControls`), `engine/condition/control.go` (`RuleFilter`), `refs.go`
  (`controlled_by`, shape `refUses`: a list of objects naming a condition, the rest of each on its link), proto
  `registry.v1.Control` in `Action.controls` / `Step.controls`, `registrysvc` conversions, web types.
- `engine/engine/control.go` (`ControlSet`, `ControlOf`, `controlsOf` in `runAction`, the prompt section),
  `engine/engine/controlcheck.go` (`ControlCheck`, `contracts/builtins.ControlCheck`), `graphapi.Port.QueryView`
  (`lib/graphclient` over RPC).
- Tests: `engine/methodology/control_test.go`, `platform/registrysvc/refs_test.go` (`TestControlsAreLinks`), `engine/engine/controlcheck_test.go`,
  `platform/e2e/engine/control_test.go` (a control pending then established, the rules of the subject only, a
  violation found by a query with its fix; collected and empty).

## Not yet

- `sdlc`: the build controlled by the architecture decisions and coding rules of its components, a `verify_rules`
  step with `control.check` (`mcps: [workspace]`, `revisionOf: code_change`) and the conditions `rules_checked` /
  `rules_respected` in the build gate.
- The rules of the project chain only (a filter on the project of the rule and its ancestors) and the rules
  superseded left out by default.
- An opt-out of the wait (`wait: false`) on a use that advises only.
- A `judge` check (a model rating a rule a query cannot measure), cached by the fingerprint of the subject.
- The open count aware of derogations (ADR 0075) and the gate items reading the report.
- An MCP `control` (`rules`, `check`) so that an agent measures its work inside its own loop.
- Human tasks showing their controls; the web editing `controls` (`StepEditor.svelte`, the action tab); the compiled view (`ViewStep`) and the flow drawing them.

## Consequences

- The methodology stays generic: it names types and link types of a domain and writes only the general rules; the
  rules themselves are produced, reviewed and versioned as domain content by the organisations of the project.
- An activity never runs before its rules are collected, and an empty collection does not block it.
- What governed a result is recorded on the run, by version of the rule.
- The measure is deterministic and costs no token; the agent reads only the rules broken and how to fix them.
