# DSL (JavaScript): script actions and domain algorithms

The DSL is a generic capability: the same JavaScript engine runs the code of a
**usage**, and the usage decides the `ctx` object the code sees. This page describes the `action` usage
(first part) and the **algorithm** usages of the domain (last part, [ADR 0018](adr/0018-algorithms.md)).

## Script actions

`kind: script` actions are written in **JavaScript** and run in the process's **sandbox** (`goap-runner`).
The engine injects a `ctx` object (methods in `camelCase`).

Two engines run the same JavaScript ([ADR 0134](adr/0134-javascript-in-webassembly.md)), chosen by `GOAP_JS_ENGINE`:
`goja` (the default, interpreted in the Go process) or `wasm` (QuickJS compiled to WebAssembly, run by wazero in the
process, with no cgo: memory capped, stopped for real at the deadline). Either compiles a script once, when it is
saved (the check of an algorithm) or at its first run, and keeps it by the hash of its source. With `wasm` the code
runs in strict mode (an undeclared variable is an error) and the values cross into Go as JSON: a number is a
`float64`, a Go struct an object named by its json tags.

Writes are **buffered** and only reach the blackboard (the change) at the end of
the action, atomically; an action that errors writes nothing. The `#nN` references returned by
writes designate change impacts declared within the same execution.

## Reading

| JavaScript | Return |
|---|---|
| `ctx.intent()` / `ctx.goal()` / `ctx.agent()` / `ctx.action()` | `string` |
| `ctx.param(name)` / `ctx.var(name)` | JSON value |
| `ctx.items(type)` (`""` = all) | `Item[]` |
| `ctx.changeImpacts()` | `ChangeImpact[]` |
| `ctx.options()` | `Option[]` |
| `ctx.decisionPoints()` | `DecisionPoint[]` |
| `ctx.node(key)` | `Node` (reference baseline) |
| `ctx.nodes(type)` (`""` = all) | `Node[]` |
| `ctx.links(key, direction, type)` (`"out"`/`"in"`, `""` = all) | `Link[]` |

The code of a **human action with a form** ([ADR 0115](adr/0115-forms-of-human-actions.md)) runs when a person submits
it, with the checked values of the form in `ctx.var("form")` (by field name: a `node` field's key, a `link` field's
`[{from, type, to}]`...); what it returns is the submission.

`Item` (artifact, decision): `{id, type, target, status, data, producedBy}` (`type` the item type, `target` what the item is about within it: the type of an artifact, the name of a signal) — `Node`: `{id, version, key, type, props}` —
`Link`: `{id, type, from, to}` (`from`/`to`: `{id, version, key, type}`).

`ChangeImpact`: `{id, key, type, intent, rationale, review, planned, drafted, pre, post, committed, links}` — the node the change reads, modifies
or creates ([ADR 0024](adr/0024-change-impacts.md)); `pre` / `post` / `committed` are `Node`s or `null`, `planned` is set while the
node has no draft (only the impact of an existing node), `drafted` while the change holds a draft of it: `post` is then the draft
(`version` 0: a node has no version until the change commits, [ADR 0079](adr/0079-drafts-and-versions-at-commit.md)), `links` are the
outgoing links of the draft.

`Option`: `{id, name, hypothesis, status, active, evaluation}` — an option of the change, a hypothesis explored on a flow of
its own ([ADR 0032](adr/0032-branches-as-pointers-baselines-as-deltas.md) §6); `status` is `exploring`, `evaluated`,
`selected` or `rejected`, `active` marks the option the change works on.

`DecisionPoint`: `{id, question, options, criteria, policy, status, questions, option}` — a question
the change must settle ([ADR 0009](adr/0009-branches-options-decisions.md) §4); `status` is `open`, `blocked` (open
questions), `ratifying` (an agent's ruling waits for a person), `escalated` (only a person rules it) or `decided`
(`option` is the option chosen); `questions`: `[{id, point, text, status, answer}]`; `policy`: the policy values of
the point (with the platform's policy: `decider`, `threshold`, `maxRounds`, `rounds`, `deadline`).

## Writing (to the change)

| JavaScript | Effect |
|---|---|
| `ctx.addArtifact(type, data)` | free-form data (report…) |
| `ctx.addItem(type, data)` | an item of a declared item type (`risk`, `action`, `hypothesis`, `assessment`…, ADR 0128): a new version of the record of its `data.key` |
| `ctx.impactNode(key, rationale)` | the change acts on a baseline node, and why → `"#nN"` (a change impact with no version yet) |
| `ctx.impactNodeCreate(type, key, rationale)` | the change creates a node → `"#nN"`; the node is created (one `created` event, with its properties) by the first `writeNode` of the reference, or bare when the script ends; any other operation on it creates it first |
| `ctx.impactNodePlan(type, key, rationale)` | the change will create a node, not written yet → `"#nN"`: a change impact of intent created with no draft (an impact analysis planning what is new, green field included); a later `writeNode` of its key, in this script or another action, creates it; a change holding a planned creation does not merge (create or withdraw it) |
| `ctx.impactNodeMerge(sources, type, key, rationale)` | merge the nodes `sources` (keys or `#nN`) into a new node, from the side of their parents: each parent loses the links to the sources and gains one to the new node, the sources stay as they are, their impacts carry the parent's impact as `via` ([ADR 0077](adr/0077-impact-node-operations.md)) → `"#nN"` of the new node (with its draft) |
| `ctx.impactNodeSplit(source, [{type, key, rationale, props}])` | split a node into new ones, from the side of its parents → the `#nN` of each new node, in order |
| `ctx.writeNode(node, {props, links, removeLinks})` | edit the **draft** of the node of a change impact (`node`: key or `#nN`; the first write checks the node out; no version is written until the change commits, [ADR 0079](adr/0079-drafts-and-versions-at-commit.md); `links`: `[{type, to}]`, `to` a node key or a `#nN` already written; `props` merged; `removeLinks` ids of the draft's links; a lifecycle state is `ctx.impactNodeTransition`). A write never reviews |
| `ctx.impactNodeReview(node, accept, comment)` | accept or reject a change impact; the comment is mandatory; an explicit operation (an edit never reviews); an acceptance runs the checks of a version on the draft of the impact and writes nothing ([ADR 0079](adr/0079-drafts-and-versions-at-commit.md)) |
| `ctx.impactNodeReviewWithReserve(node, derogation, comment)` | accept a change impact with a reserve ([ADR 0075](adr/0075-verification-derogation-criticality.md)): `derogation` is the key of an open derogation in force; the operation is a review with `NodeOp.Reserve` set; the graph's review policy still applies (the reviewer is not the producer) |
| `ctx.openDecision(question, {options, criteria, decider, threshold, maxRounds, maxDuration})` | open a decision point (`options`: names or ids, none = the open options; every other key is a policy value, handed to the graph's decision policy) → `"#dN"` |
| `ctx.decide(point, option, confidence, justification)` | rule a point decided (`point`: id, `#dN` or `""` for the only pending one; `option`: name or id; `confidence` 0 to 1): below the point's threshold (the platform's policy) the ruling waits for a person |
| `ctx.undecidable(point, justification, questions)` | rule a point undecidable: why, and the questions to answer first (they block it) |
| `ctx.answer(questionId, answer)` | answer an open question of a decision point |

The decision calls are applied after the change impacts; a script is an agent: its rulings may need a ratification,
and it cannot ratify one. The change impact calls need a change with a branch of its own; they are applied in order when the action ends. On a flow
branch (a relaunched step, ADR 0025) `changeImpacts()` shows the change impacts of the flow, the stale ones of the relaunched steps
are not there, and what the script declares, writes and reviews stays on the flow until it is adopted.

## Calls (via the engine: authorized, traced, counted)

| JavaScript | Effect |
|---|---|
| `ctx.llm(prompt)` | text completion (model `default`) |
| `ctx.complete({model, system, prompt, json, maxTokens})` | `{text, json, inputTokens, outputTokens, model}` |
| `ctx.runAgent(name, intent)` | runs a sub-agent on the same change → `{status, goal, processId}`; if the sub-agent is waiting on a human, the action is suspended then replayed when it completes |
| `ctx.callTool(name, args)` | MCP tool (`server/tool`) |
| `ctx.log(msg)` / `ctx.warn(msg)` | log (IDE console) |

## Examples

```js
// JavaScript: one test case per impacted requirement
for (const n of ctx.changeImpacts()) {
  if (n.type !== "Requirement" || !n.pre) continue;
  const t = ctx.impactNodeCreate("TestCase", "TST-" + n.key, "verifies " + n.key);
  ctx.writeNode(t, { props: { title: "Verify " + n.pre.props.title }, links: [{ type: "verifies", to: n.key }] });
}
```

## Algorithms (domain)

A domain declares **algorithms** (a script of a fixed type, with typed parameters) and **instances**
(an algorithm with parameter values). Instances are plugged where the type is allowed; the list order
is the call order. Action code is not an algorithm: it stays in the action declaration.

| Type | Plugged in | Result |
|---|---|---|
| `property_validator` | `nodeTypes[].attributes[].validators: [instance]` | accepts / rejects the value of an attribute, when the draft is accepted (its accepted review) and when the change commits (a draft may be incomplete; the attributes, their types and enums are checked on every edit, ADR 0076, 0079) |
| `node_validator` | `nodeTypes[].validators: [instance]` | accepts / rejects a node as a whole (rules across attributes), after the attribute validators, at the same moments |
| `link_validator` | `linkTypes[].validators: [instance]` | accepts / rejects a link as a whole, with the node it leaves and the node it reaches (rules across both ends and the link's properties), after the validators of the link's attributes (which run too, ADR 0100), at the same moments |
| `transition_guard` | `lifecycles[].transitions[].guards: [instance]` | allows / refuses the transition, when it is taken (`ImpactNodeTransition`, ADR 0076) |
| `transition_action` | `lifecycles[].transitions[].actions: [instance]` | changes properties of the node that moved, when the transition is taken |

Parameter types: `string`, `number`, `boolean`, `regex`, `enum` (`values`), `strings` (list), `json`.
The script reads its values with `ctx.param(name)`.

An algorithm is the body of a function of `ctx` (it may `return`). Algorithms are pure: no LLM, tool,
agent or blackboard access. They reject with `ctx.fail(message)` (several messages allowed), by throwing,
or by returning `false` or a message string.

| JavaScript | Available in |
|---|---|
| `ctx.param(name)` | all |
| `ctx.instance()` / `ctx.algorithm()` | all |
| `ctx.fail(message)` | all (in a transition action it aborts the transition) |
| `ctx.log(msg)` / `ctx.warn(msg)` | all |
| `ctx.property()` / `ctx.value()` | validator (`value()` is `null` when absent) |
| `ctx.node()` | all: `{id, version, key, type, state, props}`, as it will be after the change |
| `ctx.link()` | link validator: `{type, props}` of the link checked |
| `ctx.from()` / `ctx.to()` | link validator: the nodes the link leaves and reaches (`Node`, as they will be); a target that cannot be resolved skips the validator |
| `ctx.children()` | guard, action: the nodes a document contains (`Node[]`) |
| `ctx.change()` | guard, action: `{id, title, intent, methodology, goal}` |
| `ctx.transition()` | guard, action: `{name, from, to}` |
| `ctx.impact()` | guard: the impact of the node in the change, `{intent, review}` ([ADR 0076](adr/0076-checkout-working-versions.md): a transition that requires a review checks it); a CEL guard also sees `change` (`id`, `title`, `intent`, `methodology`, `goal`, `state`, `status`) and `draft` (`key`, `type`, `state` it goes to, `props`, `owner`, `links`, `base` version it was checked out from, `new`), [ADR 0079](adr/0079-drafts-and-versions-at-commit.md) |
| `ctx.setProp(name, value)` / `ctx.removeProp(name)` | action |

```js
// property_validator "regex-match": params pattern (regex, required), message (string)
const v = ctx.value();
if (v === undefined || v === null || v === "") return;
if (!new RegExp(ctx.param("pattern")).test(String(v))) {
  ctx.fail(ctx.param("message") || (ctx.property() + " must match " + ctx.param("pattern")));
}
```

```js
// link_validator "same-project": a requirement refines a requirement of its own project
if (ctx.from().props.project !== ctx.to().props.project) {
  ctx.fail(ctx.link().type + " must stay inside a project: " + ctx.from().key + " -> " + ctx.to().key);
}
```

```js
// transition_action "stamp": records who and when
ctx.setProp("approvedInChange", ctx.change().title);
```
