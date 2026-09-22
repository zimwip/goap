# GOAP — plateforme agentique de méthodologies d'entreprise

GOAP permet de déployer des **méthodologies d'entreprise** (analyse d'impact, gestion d'exigences,
revues…) sous forme de définitions déclaratives, exécutées par un moteur agentique inspiré
d'[Embabel](https://github.com/embabel/embabel-agent) : **boucle d'intention**, **planification GOAP (A\*)**,
exécution d'actions (LLM, humain, code, outils MCP) et **replanification** après chaque action.

Le blackboard du moteur est **l'axe *change*** d'un graphe de connaissance versionné dont
**l'axe *domaine*** porte le contenu de référence. Les conditions sont des expressions
[CEL](https://cel.dev) sur l'état du changement, hydraté avec les éléments de domaine qu'il référence.

👉 **Lire d'abord : [docs/architecture.md](docs/architecture.md)** · décisions : [docs/adr](docs/adr)

## Démarrage rapide

```bash
# 1. Tout-en-un, en mémoire, avec données de démo (aucune dépendance)
make dev                      # API Connect sur http://localhost:8080
make web                      # UI sur http://localhost:5173 (autre terminal)

# 2. Pile complète : postgres, nats, vault, services, web
ANTHROPIC_API_KEY=... make up # sans clé : fournisseur LLM « fake »
```

Sans clé API, le fournisseur `fake` renvoie des réponses vides : les actions LLM échouent à produire
leurs effets, sont désactivées, et le planificateur se replie sur les actions humaines — pratique pour
observer la replanification.

Exemple d'appel (protocole Connect en JSON, via la gateway) :

```bash
curl -s localhost:8080/goap.graph.v1.GraphService/ListBaselines -H 'Content-Type: application/json' -d '{}'
curl -s localhost:8080/goap.engine.v1.EngineService/StartProcess -H 'Content-Type: application/json' \
  -d '{"methodology":"impact-analysis","baselineId":"<id>","intent":"Le PSP passe en API v2 : qu'"'"'est-ce que ça casse ?"}'
```

## Services

| Service | Port (compose) | Rôle |
|---|---|---|
| gateway | 8080 | point d'entrée, authentification (none / HS256 + jetons de dev), routage Connect |
| graph | 8081 | axe domaine (nœuds versionnés, liens version-à-version, baselines) + axe change |
| registry | 8082 | méthodologies structurées en base (brouillon → publiée), import/export YAML |
| engine | 8083 | processus agentiques : intention → planification → exécution |
| modelgw | 8084 | passerelle LLM multi-fournisseurs (Anthropic, OpenAI-compatible, fake) |
| iam | 8086 | contrôle d'accès ABAC (Casbin), politiques en base |
| mcp | — | squelette (API définie, non implémentée) |

## Développement

```bash
make tools      # buf + plugins protoc
make generate   # proto/ -> gen/
make test       # tests unitaires
make test-pg    # tests du dépôt PostgreSQL du graphe
make lint
```

Stack : Go 1.26 · Echo · connect-rpc · NATS JetStream · PostgreSQL · Vault · Svelte 5.
