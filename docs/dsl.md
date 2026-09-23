# DSL des actions script (JavaScript / Go)

Les actions `kind: script` sont écrites en **JavaScript** (interprété par goja) ou en **Go**
(interprété par yaegi) et s'exécutent dans le **sandbox** du processus (`goap-runner`).
Le moteur injecte un objet `ctx` : la même API existe dans les deux langages
(`camelCase` en JavaScript, `PascalCase` en Go).

Les écritures sont **tamponnées** et n'arrivent sur le blackboard (le change) qu'à la fin de
l'action, de façon atomique ; une action en erreur n'écrit rien. Les références `#pN` renvoyées par les
écritures désignent les items créés dans la même exécution.

## Lecture

| JavaScript | Go | Retour |
|---|---|---|
| `ctx.intent()` / `ctx.goal()` / `ctx.agent()` / `ctx.action()` | `Intent()`… | `string` |
| `ctx.param(name)` / `ctx.var(name)` | `Param(name)` / `Var(name)` | valeur JSON |
| `ctx.items(kind)` (`""` = tous) | `Items(kind)` | `Item[]` |
| `ctx.impacts()` / `ctx.proposals()` | `Impacts()` / `Proposals()` | `Item[]` |
| `ctx.node(key)` | `Node(key)` | `Node` (baseline de référence) |
| `ctx.nodes(type)` (`""` = tous) | `Nodes(type)` | `Node[]` |
| `ctx.links(key, direction, type)` (`"out"`/`"in"`, `""` = tous) | `Links(…)` | `Link[]` |

`Item` : `{id, kind, type, status, target, data, op, node, link, producedBy}` (`link` : `{type, from, to}` d'une proposition de lien, extrémités = clé de nœud ou `@<itemId>` d'un nœud proposé, réutilisables dans `proposeLink`) — `Node` : `{id, version, key, type, props}` —
`Link` : `{id, type, from, to}` (`from`/`to` : `{id, version, key, type}`).

## Écriture (sur le change)

| JavaScript | Effet |
|---|---|
| `ctx.addImpact(key, reason)` | impact direct sur un nœud de la baseline |
| `ctx.proposeNode(type, key, props)` | proposition de création → `"#pN"` |
| `ctx.proposeUpdate(key, props)` | nouvelle version d'un nœud |
| `ctx.proposeDelete(key)` | suppression |
| `ctx.proposeLink(from, type, to)` | lien (`from` / `to` : clé de nœud ou `#pN`) |
| `ctx.addArtifact(type, data)` | donnée libre (rapport…) |
| `ctx.decide(itemId, accept, comment)` | décision sur une proposition |

## Appels (via le moteur : autorisés, tracés, comptés)

| JavaScript | Effet |
|---|---|
| `ctx.llm(prompt)` | complétion texte (modèle `default`) |
| `ctx.complete({model, system, prompt, json, maxTokens})` | `{text, json, inputTokens, outputTokens, model}` |
| `ctx.runAgent(name, intent)` | exécute un sous-agent sur le même change → `{status, goal, processId}` ; si le sous-agent attend un humain, l'action est suspendue puis rejouée quand il se termine |
| `ctx.callTool(name, args)` | outil MCP (`serveur/outil`) |
| `ctx.log(msg)` / `ctx.warn(msg)` | journal (console de l'IDE) |

## Exemples

```js
// JavaScript : un cas de test par exigence impactée
for (const i of ctx.impacts()) {
  if (i.target.type !== "Requirement") continue;
  const t = ctx.proposeNode("TestCase", "TST-" + i.target.key, { title: "Vérifier " + i.target.props.title });
  ctx.proposeLink(t, "verifies", i.target.key);
}
```

```go
// Go : même action
package action

import "github.com/zimwip/goap/pkg/dsl"

func Run(ctx *dsl.Ctx) error {
	for _, i := range ctx.Impacts() {
		if i.Target == nil || i.Target.Type != "Requirement" {
			continue
		}
		t := ctx.ProposeNode("TestCase", "TST-"+i.Target.Key, map[string]any{"title": "Vérifier " + i.Target.Key})
		ctx.ProposeLink(t, "verifies", i.Target.Key)
	}
	return nil
}
```
