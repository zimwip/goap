# DSL (JavaScript / Go): script actions and domain algorithms

The DSL is a generic capability: the same JavaScript (goja) / Go (yaegi) engine runs the code of a
**usage**, and the usage decides the `ctx` object the code sees. This page describes the `action` usage
(first part) and the **algorithm** usages of the domain (last part, [ADR 0018](adr/0018-algorithms.md)).

## Script actions

`kind: script` actions are written in **JavaScript** (interpreted by goja) or in **Go**
(interpreted by yaegi) and run in the process's **sandbox** (`goap-runner`).
The engine injects a `ctx` object: the same API exists in both languages
(`camelCase` in JavaScript, `PascalCase` in Go).

Writes are **buffered** and only reach the blackboard (the change) at the end of
the action, atomically; an action that errors writes nothing. The `#pN` references returned by
writes designate items created within the same execution.

## Reading

| JavaScript | Go | Return |
|---|---|---|
| `ctx.intent()` / `ctx.goal()` / `ctx.agent()` / `ctx.action()` | `Intent()`… | `string` |
| `ctx.param(name)` / `ctx.var(name)` | `Param(name)` / `Var(name)` | JSON value |
| `ctx.items(kind)` (`""` = all) | `Items(kind)` | `Item[]` |
| `ctx.impacts()` / `ctx.proposals()` | `Impacts()` / `Proposals()` | `Item[]` |
| `ctx.node(key)` | `Node(key)` | `Node` (reference baseline) |
| `ctx.nodes(type)` (`""` = all) | `Nodes(type)` | `Node[]` |
| `ctx.links(key, direction, type)` (`"out"`/`"in"`, `""` = all) | `Links(…)` | `Link[]` |

`Item`: `{id, kind, type, status, target, data, op, node, link, producedBy}` (`link`: `{type, from, to}` of a link proposal, endpoints = node key or `@<itemId>` of a proposed node, reusable in `proposeLink`) — `Node`: `{id, version, key, type, props}` —
`Link`: `{id, type, from, to}` (`from`/`to`: `{id, version, key, type}`).

## Writing (to the change)

| JavaScript | Effect |
|---|---|
| `ctx.addImpact(key, reason)` | direct impact on a baseline node |
| `ctx.proposeNode(type, key, props)` | creation proposal → `"#pN"` |
| `ctx.proposeUpdate(key, props)` | new version of a node |
| `ctx.proposeDelete(key)` | deletion |
| `ctx.proposeTransition(key, state)` | move a node to a lifecycle state (reopen it before editing, leave the editable states before the change is applied) |
| `ctx.proposeLink(from, type, to)` | link (`from` / `to`: node key or `#pN`) |
| `ctx.addArtifact(type, data)` | free-form data (report…) |
| `ctx.decide(itemId, accept, comment)` | decision on a proposal |

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
for (const i of ctx.impacts()) {
  if (i.target.type !== "Requirement") continue;
  const t = ctx.proposeNode("TestCase", "TST-" + i.target.key, { title: "Verify " + i.target.props.title });
  ctx.proposeLink(t, "verifies", i.target.key);
}
```

```go
// Go: same action
package action

import "github.com/zimwip/goap/pkg/dsl"

func Run(ctx *dsl.Ctx) error {
	for _, i := range ctx.Impacts() {
		if i.Target == nil || i.Target.Type != "Requirement" {
			continue
		}
		t := ctx.ProposeNode("TestCase", "TST-"+i.Target.Key, map[string]any{"title": "Verify " + i.Target.Key})
		ctx.ProposeLink(t, "verifies", i.Target.Key)
	}
	return nil
}
```

## Algorithms (domain)

A domain declares **algorithms** (a script of a fixed type, with typed parameters) and **instances**
(an algorithm with parameter values). Instances are plugged where the type is allowed; the list order
is the call order. Action code is not an algorithm: it stays in the action declaration.

| Type | Plugged in | Result |
|---|---|---|
| `property_validator` | `nodeTypes[].validators: [{property, instance}]` | accepts / rejects a property value, when a node is created or modified |
| `transition_guard` | `lifecycles[].transitions[].guards: [instance]` | allows / refuses the transition, when the change is applied |
| `transition_action` | `lifecycles[].transitions[].actions: [instance]` | changes properties of the node that moved, once the transition is accepted |

Parameter types: `string`, `number`, `boolean`, `regex`, `enum` (`values`), `strings` (list), `json`.
The script reads its values with `ctx.param(name)`.

A **JavaScript** algorithm is the body of a function of `ctx` (it may `return`); a **Go** algorithm
declares `func Run(ctx *dsl.<Usage>Ctx) error`. Algorithms are pure: no LLM, tool, agent or blackboard
access. They reject with `ctx.fail(message)` (several messages allowed), by throwing / returning an
error, or (JavaScript) by returning `false` or a message string.

| JavaScript | Go | Available in |
|---|---|---|
| `ctx.param(name)` | `Param(name)` | all |
| `ctx.instance()` / `ctx.algorithm()` | `Instance()` / `Algorithm()` | all |
| `ctx.fail(message)` | `Fail(message)` | all (in a transition action it aborts the application of the change) |
| `ctx.log(msg)` / `ctx.warn(msg)` | `Log(msg)` / `Warn(msg)` | all |
| `ctx.property()` / `ctx.value()` | `Property()` / `Value()` | validator (`value()` is `null` when absent) |
| `ctx.node()` | `Node()` | all: `{id, version, key, type, state, props}`, as it will be after the change |
| `ctx.children()` | `Children()` | guard, action: the nodes a document contains (`Node[]`) |
| `ctx.change()` | `Change()` | guard, action: `{id, title, intent, methodology, goal}` |
| `ctx.transition()` | `Transition()` | guard, action: `{name, from, to}` |
| `ctx.setProp(name, value)` / `ctx.removeProp(name)` | `SetProp(name, value)` / `RemoveProp(name)` | action |

```js
// property_validator "regex-match": params pattern (regex, required), message (string)
const v = ctx.value();
if (v === undefined || v === null || v === "") return;
if (!new RegExp(ctx.param("pattern")).test(String(v))) {
  ctx.fail(ctx.param("message") || (ctx.property() + " must match " + ctx.param("pattern")));
}
```

```go
// transition_action "stamp": records who and when
package action

import "github.com/zimwip/goap/pkg/dsl"

func Run(ctx *dsl.TransitionCtx) error {
	ctx.SetProp("approvedInChange", ctx.Change().Title)
	return nil
}
```
