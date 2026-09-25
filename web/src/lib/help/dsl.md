# Script action DSL (JavaScript / Go)

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
