# ADR 0115 — Forms of human actions: a declared form, its code, an extensible widget registry

**Status**: accepted, first part implemented (see "Not done") · **Date**: 2026-10 · Extends ADR 0034 (manual steps),
ADR 0018 / DSL (script code), ADR 0076 / 0079 (change impacts, drafts), ADR 0092 (screens of the web)

## Context

A human action (`kind: human`) suspends its process until a person submits items (`Engine.Submit`). What the person
saw was generic: `HumanTaskForm.svelte` guessed a screen from the **name** of the action (`select…` → a list of
checkboxes declaring impacts, `review…` → accept / reject the pending impacts, anything else → the node lifecycle
editor), and a JSON text area for everything else. A methodology could say what a person does (`instructions`), and
restrict the node types (`nodeTypes`), but not what they fill in, nor what happens with it. A screen that asks
"pick the requirement, link it to the applications that realize it, say why" had to be either a raw JSON input or new
code in the web.

## Decision

### 1. A human action declares a form

`Action.Form` (`form:` in YAML, `methodology.Action.Form`, a `form.Form` of the leaf package `pkg/form`; proto
`registry.v1.Action.form` and `engine.v1.HumanTask.form`, both a `google.protobuf.Struct`; attribute `form` of
`methodology@Action`): what the person sees and fills in.

```yaml
- name: link_requirement
  kind: human
  form:
    title: Link the requirement to what realizes it
    submit: Link
    fields:
      - {name: requirement, widget: node, label: Requirement, required: true,
         constraints: {types: [alm@Requirement]}}
      - {name: realizedBy, widget: link, label: Realized by,
         constraints: {from: requirement, linkTypes: [alm@realized_by], toTypes: [alm@Application], max: 5}}
      - {name: why, widget: textarea, label: Why, required: true}
```

A form is `title`, `description`, `submit` (label of the button), `artifact` (type of the artifact recording the
values when the action has no code, default the action name) and ordered `fields`. A field is `name` (the key of its
value, `[A-Za-z][A-Za-z0-9_]*`, unique), `widget`, `label`, `help`, `placeholder`, `section` (fields of a section
are grouped, in order of first appearance), `required`, `default` and `constraints` (an object whose keys the widget
declares). The form is data of the methodology: versioned with it, compiled with it, edited with it.

### 2. Widgets are a registry, built-in and extensible

A **widget** is a name, a value kind (`string`, `number`, `boolean`, `list`, `node`, `links`, `none` for a display
widget) and the **constraints** it accepts, each with a type (`string`, `number`, `boolean`, `strings`, `field`: the
name of another field of the form). `pkg/form` holds the registry (`form.Register`, `Lookup`, `All`; a duplicate
name panics), with the built-in widgets:

| Widget | Value | Constraints |
|---|---|---|
| `text`, `textarea` | string | `minLength`, `maxLength`, `pattern` |
| `number` | number | `min`, `max`, `step`, `integer` |
| `boolean` | boolean | |
| `date` | string (`YYYY-MM-DD`) | `min`, `max` |
| `select` | string | `options` (required) |
| `multiselect` | list of strings | `options` (required), `min`, `max` |
| `node` | node key, or a list with `multiple` | `types`, `states`, `namespace`, `inChange`, `multiple`, `min`, `max` |
| `link` | `[{from, type, to}]` | `from` (a `node` field, or a fixed key `fromKey`), `linkTypes`, `toTypes`, `toStates`, `min`, `max` |
| `markdown` | none (shows `constraints.text`) | `text` |

**Constraints limit the choices**: a `node` field offers only the nodes of those types (a subtype qualifies), states
and namespace, only the nodes the change touches with `inChange`; a `link` field offers only those link types and
targets. They are checked twice: by the web (what it offers) and by the engine (what it accepts, below), so a
submission made outside the web obeys them too.

The registry is extensible on both sides with the same name: a Go package registers the server half (constraints and
the check of a value, `form.Widget.Check`), the web registers the component (`registerWidget` in
`web/src/lib/forms/registry.ts`). A widget the web does not know is shown as a JSON input of its value with its
constraints listed; a widget the server does not know is a compile issue of the methodology (`form.CheckForm`, path
`actions[i].form.fields[j]`), as is an unknown or ill-typed constraint, a duplicate or invalid name, a `select`
without options, a `link` whose `from` names no `node` field.

### 3. A submission is the values of the form, checked by the engine

`SubmitHumanInput` takes `form` (a `Struct`, the values by field name) next to `items`. `Engine.SubmitForm`
validates them against the form of the pending task (`form.Validate`: required, value kind, constraints; node keys
resolved against the reference baseline of the change and its impacts through `form.Env`, with their types, the
supertypes of the type catalogue and their states), refuses with `ErrInvalid` and the list of the field issues
otherwise, then turns them into items:

- **with code** (`language` + `code` on the human action, the DSL of script actions, docs/dsl.md): the code runs in
  the sandbox of the process with the values as `ctx.var("form")` and returns items and change impact operations,
  exactly as a script action does; it decides what the values mean;
- **without code**: one artifact of type `form.artifact` (default: the action name) with the values as data, and
  for every `link` field the change impact operations adding the links (the source declared as modified when the
  change does not hold it yet, then written with the links): a form without code still changes the graph through its
  links.

Either way the items are added as the submission of the step (the record of its run, ADR 0109), as `Submit` does:
the roles of the step and of the action are checked first.

### 4. The web renders the declared form

`web/src/lib/forms/`: `registry.ts` (the widget registry: name → component and its constraints), `FormRenderer.svelte`
(sections, labels, help, required marks, the issues of each field, the submit button), `validate.ts` (the same checks
as the engine, minus the node lookups, run before sending), `widgets/` (the built-in ones). The node and link widgets
take a `NodeSource` (the nodes they may offer: for a human task, the reference baseline of the change and the nodes
its impacts write) and filter it by their constraints, with a search box. `HumanTaskForm.svelte` renders the declared
form when the task has one and keeps the former heuristics otherwise. The renderer knows no process: it takes a
`form`, the values, a `NodeSource` and an `onsubmit`, so any page can use it.

## Consequences

- A methodology describes the screen of each human action; the guess on the name of the action stays only for the
  actions that declare no form.
- Constraints are part of the definition, so the web and the engine agree on what is allowed.
- A form without code changes the graph only through links and an artifact; anything else (creating a node,
  setting a property, opening a decision point) is the code's job, with the DSL every script already has.

## Not done

- The form editor of the methodology (drag and drop of widgets from a palette, field properties, constraints with
  pickers from the type catalogue, a live preview with `FormRenderer`) and **patterns** (ready-made forms: "pick and
  link", "review the impacts", "qualify an impact") to start from.
- Conditional fields (`visible` / `requiredWhen` in CEL over the other values) and repeated groups.
- Standard pages using `FormRenderer` with a form declared by a node type (an `form:` on a node type of a domain, for
  its creation dialog), and the assistant filling a form (a screen tool per declared form, ADR 0092).
- Widgets beyond the built-in set (a node creation widget with its attributes, a decision point, a file).
