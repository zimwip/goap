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
the action, atomically; an action that errors writes nothing. The `#nN` references returned by
writes designate change impacts declared within the same execution.

## Reading

| JavaScript | Go | Return |
|---|---|---|
| `ctx.intent()` / `ctx.goal()` / `ctx.agent()` / `ctx.action()` | `Intent()`… | `string` |
| `ctx.param(name)` / `ctx.var(name)` | `Param(name)` / `Var(name)` | JSON value |
| `ctx.items(kind)` (`""` = all) | `Items(kind)` | `Item[]` |
| `ctx.changeImpacts()` | `ChangeImpacts()` | `ChangeImpact[]` |
| `ctx.options()` | `Options()` | `Option[]` |
| `ctx.decisionPoints()` | `DecisionPoints()` | `DecisionPoint[]` |
| `ctx.node(key)` | `Node(key)` | `Node` (reference baseline) |
| `ctx.nodes(type)` (`""` = all) | `Nodes(type)` | `Node[]` |
| `ctx.links(key, direction, type)` (`"out"`/`"in"`, `""` = all) | `Links(…)` | `Link[]` |

`Item` (artifact, decision): `{id, kind, type, status, data, producedBy}` — `Node`: `{id, version, key, type, props}` —
`Link`: `{id, type, from, to}` (`from`/`to`: `{id, version, key, type}`).

`ChangeImpact`: `{id, key, type, intent, rationale, review, planned, pre, post, landed, links}` — the node the change reads, modifies
or creates ([ADR 0024](adr/0024-change-impacts.md)); `pre` / `post` / `landed` are `Node`s or `null`, `planned` is set while no
version is written, `links` are the outgoing links of the version written.

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
| `ctx.impactNode(key, rationale)` | the change acts on a baseline node, and why → `"#nN"` (a change impact with no version yet) |
| `ctx.createNode(type, key, rationale)` | the change creates a node → `"#nN"` |
| `ctx.writeNode(node, {props, state, links, removeLinks, retire})` | write the next version of the node of a change impact on the change branch (`node`: key or `#nN`; `links`: `[{type, to}]`, `to` a node key or a `#nN` already written; `props` merged; `state` a lifecycle state) |
| `ctx.reviewNode(node, accept, comment)` | accept or reject a change impact; the comment is mandatory |
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
  const t = ctx.createNode("TestCase", "TST-" + n.key, "verifies " + n.key);
  ctx.writeNode(t, { props: { title: "Verify " + n.pre.props.title }, links: [{ type: "verifies", to: n.key }] });
}
```

```go
// Go: same action
package action

import "github.com/zimwip/goap/pkg/dsl"

func Run(ctx *dsl.Ctx) error {
	for _, n := range ctx.ChangeImpacts() {
		if n.Type != "Requirement" || n.Pre == nil {
			continue
		}
		t := ctx.CreateNode("TestCase", "TST-"+n.Key, "verifies "+n.Key)
		ctx.WriteNode(t, map[string]any{"props": map[string]any{"title": "Verify " + n.Key}})
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
| `property_validator` | `nodeTypes[].attributes[].validators: [instance]` (also on link type attributes) | accepts / rejects the value of an attribute, when a node is created or modified |
| `node_validator` | `nodeTypes[].validators: [instance]` | accepts / rejects a node as a whole (rules across attributes), after the attribute validators |
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
