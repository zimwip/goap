# GOAP — Plateforme agentique de méthodologies d'entreprise

> Document d'architecture — version 0.1 (fondation)
> Statut : **brouillon de travail**. Les sections marquées 🟡 sont des hypothèses à valider, 🟢 sont implémentées dans le socle initial.

## 1. Vision

GOAP est une plateforme agentique **généraliste** dans laquelle on déploie des **méthodologies d'entreprise**
(analyse d'impact, gestion d'exigences, revue d'architecture, conformité, onboarding…) sous forme de
définitions déclaratives. Une méthodologie décrit :

- un **modèle de domaine** (types de nœuds, types de liens) ;
- des **conditions** (prédicats sur l'état d'un changement) ;
- des **actions** (unités de travail : LLM, outil MCP, humain, code) avec préconditions, effets et coût ;
- des **objectifs** (goals) exprimés comme un ensemble de conditions à atteindre.

Le moteur d'exécution s'inspire d'[Embabel](https://github.com/embabel/embabel-agent) :
**planification GOAP** (Goal Oriented Action Planning, A\*) sur un **blackboard**, replanification après
chaque action (boucle OODA), et une **boucle d'intention** préalable qui transforme une demande en
langage naturel en objectif formel.

L'originalité de GOAP : le blackboard n'est pas un sac d'objets en mémoire, c'est **l'axe *change***
d'un graphe de connaissance versionné, dont l'autre axe, **l'axe *domaine***, décrit le contenu de référence.

## 2. Concepts fondamentaux

### 2.1 Le graphe à deux axes

```
                 AXE DOMAINE (contenu, versionné)
   ┌──────────────────────────────────────────────────────────┐
   │  NEED-4@v2 ◄──satisfies── REQ-12@v3 ◄──verifies── TST-7@v1│   baseline B1 (référence)
   │                              │                             │
   └──────────────────────────────┼─────────────────────────────┘
                                  │ référence (target)
                 AXE CHANGE (modification, = blackboard)
   ┌──────────────────────────────┼─────────────────────────────┐
   │ ChangeSet CR-42 (baseline = B1, intent = "…")              │
   │   ├─ impact   #i1 → REQ-12@v3   (direct)                   │
   │   ├─ impact   #i2 → TST-7@v1    (propagé depuis #i1)       │
   │   ├─ proposal #p1 update_node REQ-12 (base v3) → props'    │
   │   ├─ proposal #p2 add_link    TST-9(new) ─verifies→ #p1    │
   │   └─ decision #d1 accept #p1                               │
   └────────────────────────────────────────────────────────────┘
                                  │ apply
                                  ▼
                 baseline B2 (graphe d'arrivée) : REQ-12@v4, TST-9@v1 …
```

#### Axe domaine

| Concept | Description |
|---|---|
| **Node** | Élément de contenu typé (`Requirement`, `Service`, `Process`…). Identité stable `NodeID` + `Key` lisible (`REQ-12`). |
| **Version** | Chaque modification crée une nouvelle version immuable `NodeID@vN`. Une version peut être un *tombstone* (suppression). |
| **Link** | Relation typée **de version à version** : `REQ-12@v3 ─satisfies→ NEED-4@v2`. Un lien ne « suit » pas automatiquement les nouvelles versions : si `NEED-4` passe en v3, le lien devient **suspect** — c'est le signal d'impact natif du modèle. |
| **Baseline** | Ensemble cohérent `{NodeID → Version}` : un « commit » du graphe. Les liens d'une baseline sont ceux dont les deux extrémités sont dans la baseline. Toute modification part d'une baseline de référence et produit une baseline d'arrivée. |

Règle de versionnement ([ADR 0003](adr/0003-liens-version-a-version.md)) : les **liens sortants font partie
de la version du nœud source**. Ajouter/retirer un lien sortant crée une nouvelle version de la source ;
un nœud qui change de version reporte ses liens sortants ; les liens entrants depuis des nœuds non
modifiés restent sur l'ancienne version et deviennent **suspects**. Les baselines restent ainsi immuables.

#### Axe change

| Concept | Description |
|---|---|
| **ChangeSet** | Une demande de modification. Référence une baseline de départ, porte l'intention initiale et le goal retenu. C'est **le blackboard** d'un processus agentique. |
| **ChangeItem** | Élément du blackboard. `kind` ∈ `impact`, `proposal`, `decision`, `artifact`. Chaque item a une provenance (`producedBy` = action, `derivedFrom` = autres items). |
| **Impact** | Référence un nœud **du graphe de référence** (`NodeRef` version exacte) avec une raison. Point de départ de l'analyse. |
| **Proposal** | Modification proposée du **graphe d'arrivée** : `create_node`, `update_node`, `delete_node`, `add_link`, `remove_link`. Les extrémités de lien peuvent être un nœud existant (`NodeRef`) ou un nœud proposé (référence à un autre item). |
| **Decision** | Acceptation / rejet d'une proposition (humain ou agent). |
| **Artifact** | Donnée libre produite par une action (résumé, rapport, réponse d'outil). |

L'application d'un ChangeSet (`ApplyChange`) matérialise les propositions acceptées en nouvelles versions
de nœuds et en une nouvelle baseline. Le ChangeSet reste l'historique explicable de *pourquoi* le graphe a changé.

### 2.2 Correspondance avec Embabel

| Embabel | GOAP | Commentaire |
|---|---|---|
| Blackboard | **ChangeSet** (axe change) | Persisté, partagé, auditable ; référence des éléments du domaine. |
| Objet du blackboard | **ChangeItem** | Typé par `kind` + `type` sémantique. |
| Condition | **Condition** = expression [CEL](https://cel.dev) évaluée sur le blackboard *hydraté* avec les nœuds de domaine référencés | Voir §2.3. |
| Action (`@Action`) | **Action** de méthodologie (`llm`, `tool`, `human`, `builtin`) | Préconditions/effets = conditions nommées. L'**attendu** d'une action se traduit par un lien sur l'axe domaine (§2.4). |
| Goal (`@AchievesGoal`) | **Goal** = conjonction de conditions + valeur | |
| GOAP planner (A\*) | `pkg/goap` 🟢 | A\* sur l'espace des états booléens. |
| Autonomy / goal selection | **Boucle d'intention** `pkg/intent` 🟢 | Classement des goals + clarification tant que la confiance est insuffisante. |
| AgentProcess | **Process** `pkg/engine` 🟢 | Boucle observe → planifie → agit → replanifie. |

### 2.3 Conditions : expressions sur l'état du changement

Une condition est un prédicat **nommé** évalué sur le blackboard. On retient **CEL** (Common Expression
Language, `cel-go`) : non Turing-complet, typé, rapide, sandboxé, sérialisable dans une définition YAML.

Variables exposées à l'expression :

| Variable | Contenu |
|---|---|
| `change` | `{id, title, intent, status, goal, baseline}` |
| `items` | tous les ChangeItems |
| `impacts`, `proposals`, `decisions`, `artifacts` | items filtrés par `kind` |
| `vars` | variables libres du processus (réponses de clarification, paramètres) |

Chaque référence de domaine d'un item (`target`, `node.base`, extrémités de lien) est **hydratée** :
`{id, version, key, type, props, out: [{type, to}], in: [{type, from}], latest}`. Une condition peut donc
naviguer dans le domaine *de référence* sans appel réseau pendant l'évaluation (l'hydratation est faite
une fois par cycle par le moteur via le Graph Service).

Exemples :

```cel
// au moins un impact identifié
size(impacts) > 0

// chaque exigence impactée a une proposition de mise à jour
impacts.filter(i, i.target.type == "Requirement")
       .all(i, proposals.exists(p, p.op == "update_node" && p.node.base.id == i.target.id))

// aucun lien suspect : toute cible impactée est à sa dernière version
impacts.all(i, i.target.version == i.target.latest)
```

Le monde vu par le planificateur est **l'évaluation booléenne de toutes les conditions**
(`WorldState = {nom → vrai/faux}`). Une condition dont l'évaluation échoue (champ absent, erreur de type) est
**inconnue** et ne satisfait aucune précondition.

### 2.4 Actions et « attendu » sur l'axe domaine

Une action déclare :

- `pre` : conditions requises (`{nom: bool}`) ;
- `effects` : conditions que l'action est **censée** rendre vraies/fausses (utilisées pour planifier) ;
- `expects` (optionnel) : l'**attendu** exprimé comme un **motif de lien de domaine**. Exemple :
  « pour chaque impact sur un `Requirement`, produire une proposition de nœud `TestCase` liée par `verifies` ».

Un `expects` est **compilé en condition CEL** (`expect:<action>`) ajoutée automatiquement aux effets de l'action.
Ainsi le lien sur l'axe domaine est à la fois la **spécification** de l'action, son **critère de
réussite** et un **effet planifiable**. Après exécution, le moteur ré-évalue : si l'effet promis n'est pas
observé, l'étape est marquée `effectsMet=false` ; après 2 échecs l'action est **désactivée** pour ce
processus et le planificateur replanifie vers une autre action produisant le même effet (ex. repli
`identify_impacts` (LLM) → `select_impacts` (humain)). Si aucun plan n'existe, le processus passe en `stuck`.

Types d'exécuteurs :

| Kind | Exécution | Sortie |
|---|---|---|
| `llm` | Prompt (template Go) + contexte blackboard → Model Gateway, sortie JSON structurée | ChangeItems |
| `tool` | Appel d'un outil via le MCP Connector | Artifact (+ mapping optionnel vers items) |
| `human` | Crée une tâche ; le processus passe en `waiting` jusqu'à `SubmitHumanInput` | Items saisis |
| `builtin` | Fonction Go enregistrée : `graph.propagate` (propagation d'impact), `graph.apply` (application du change) | ChangeItems / nouvelle baseline |

Les sorties LLM/humaines utilisent un format d'entrée simplifié (`engine.ItemInput`) : les nœuds sont
désignés par leur **clé** (`REQ-1`), les items du même lot par `#ref`, les items existants par `@<id>` ;
le moteur les résout en `NodeRef` exacts de la baseline de référence.

### 2.5 Boucle d'intention

Avant toute planification :

1. l'utilisateur exprime une demande (« le fournisseur de paiement change d'API, qu'est-ce que ça casse ? ») ;
2. le **Ranker** (LLM via Model Gateway, ou lexical en dev) classe les goals de la méthodologie avec une confiance ;
3. si `confiance(top) ≥ seuil` et écart suffisant avec le second → goal retenu, `change.goal` renseigné ;
4. sinon → **question de clarification** (générée à partir des goals candidats), processus en `clarifying` ;
   la réponse est ajoutée à l'historique et on reboucle (max N tours) ;
5. le goal retenu peut exiger des **paramètres** (ex. le nœud de départ) : ils sont extraits dans `vars`.

### 2.6 Boucle d'exécution (Process)

```
        ┌─────────────┐
        │  intention  │── clarification ──► (attente utilisateur)
        └──────┬──────┘
               ▼
   ┌──► observer : hydrater blackboard, évaluer conditions ──► goal atteint ? ── oui ──► completed
   │           │ non
   │           ▼
   │    planifier : A*(WorldState, actions, goal) ── aucun plan ──► stuck
   │           │
   │           ▼
   │    agir : exécuter la 1re action du plan
   │           │   (human / approbation → waiting ; erreur → retry/failed)
   │           ▼
   └──── enregistrer items + step (événement NATS)
```

### 2.7 Application du changement et permissions

Les actions ne modifient jamais le domaine directement : elles alimentent le change, et la transformation
effective n'a lieu qu'à l'**application** du change ([ADR 0004](adr/0004-application-du-change.md)).
Cette application est elle-même une action planifiable, `builtin: graph.apply`, qui porte une **permission** :

```yaml
- name: apply_change
  kind: builtin
  builtin: graph.apply
  pre: {reviewed: true, applied: false}   # uniquement après la revue
  effects: {applied: true}                # applied: change.status == "applied"
  permission: change:apply
```

- Le processus mémorise son **initiateur** (identité propagée par la gateway : `X-Goap-Subject/Org/Roles`).
- Si l'initiateur détient la permission, l'action s'exécute automatiquement.
- Sinon, le processus passe en `waiting` avec une tâche d'**approbation** (`pending.kind = approval`).
  Une personne habilitée appelle `ApproveAction` : si elle approuve, l'action s'exécute avec son identité
  (`step.approvedBy`) ; si elle refuse, l'action est désactivée pour le processus et le planificateur
  cherche une autre voie (en général : `stuck`).
- Toute action peut porter une permission, pas seulement `graph.apply`.

Décision des permissions : `pkg/authz` avec une politique de rôles statique (`viewer`, `contributor`,
`methodologist`, `approver`, `admin`) en attendant `IamService.CheckPermission` (M2).

Replanifier à chaque pas rend le moteur robuste aux actions non déterministes (LLM) et aux modifications
concurrentes du blackboard (un humain peut ajouter un impact pendant l'exécution).

Décisions structurantes : [ADR 0001 — blackboard = axe change](adr/0001-blackboard-axe-change.md),
[ADR 0002 — conditions CEL](adr/0002-conditions-cel.md), [ADR 0003 — liens version-à-version](adr/0003-liens-version-a-version.md),
[ADR 0004 — application du change](adr/0004-application-du-change.md).

## 3. Architecture des composants

```
                          ┌──────────────┐
          navigateur ───► │  web (Svelte)│
                          └──────┬───────┘
                                 │ HTTPS (Connect JSON / REST)
                          ┌──────▼───────┐       ┌─────────┐
                          │   gateway    │──────►│  iam    │ (users, orgs, rôles, tokens)
                          │ (Echo, authN,│       └─────────┘
                          │  routage)    │
                          └──┬───┬───┬───┘
              connect-rpc    │   │   │
        ┌────────────────────┘   │   └─────────────────────┐
  ┌─────▼──────┐         ┌───────▼──────┐          ┌───────▼──────┐
  │  registry  │◄────────│    engine    │─────────►│    graph     │
  │ (méthodo-  │         │ (processus,  │          │ (domaine +   │
  │  logies)   │         │  planif, x N)│          │  change)     │
  └────────────┘         └──┬────────┬──┘          └──────────────┘
                            │        │
                    ┌───────▼──┐  ┌──▼──────────┐
                    │ modelgw  │  │ mcp         │──► serveurs MCP externes
                    │ (LLMs)   │  │ connector   │
                    └────┬─────┘  └─────────────┘
                         ▼
             Anthropic / OpenAI-compatible / Ollama…

  Transverse : PostgreSQL (schéma par service) · NATS JetStream (événements) · Vault (secrets)
```

| Service | Responsabilité | API | Persistance | Statut |
|---|---|---|---|---|
| **gateway** | Point d'entrée unique, authentification (JWT/OIDC), routage vers les services, CORS, rate-limit | Echo HTTP, reverse proxy Connect | — | 🟢 socle |
| **iam** | Utilisateurs, organisations (tenants), rôles, permissions, API keys | Connect `iam.v1` | `iam` | 🟡 à venir |
| **registry** | Stockage, validation et versionnement des méthodologies | Connect `registry.v1` | `registry` | 🟢 socle |
| **engine** | Boucle d'intention, planification, exécution des processus ; déployable en cluster | Connect `engine.v1` | `engine` | 🟢 socle (mémoire) |
| **graph** | Axe domaine (nœuds versionnés, liens, baselines) + axe change (ChangeSets, items, apply) | Connect `graph.v1` | `graph` | 🟢 |
| **modelgw** | Abstraction multi-fournisseurs / multi-modèles, alias (`default`, `fast`, `reasoning`), quotas, traces | Connect `model.v1` | `modelgw` (usage) | 🟢 socle |
| **mcp** | Registre et proxy de serveurs MCP ; expose les outils aux actions `tool` | Connect `mcp.v1` | `mcp` | 🟡 à venir |
| **vault** | Secrets (clés API LLM, credentials MCP, DSN) | HashiCorp Vault KV v2 | — | 🟢 dev mode |

### 3.1 Communication

- **Externe** : HTTP via Echo sur la gateway. Le frontend utilise le protocole **Connect en JSON**
  (`POST /goap.engine.v1.EngineService/StartProcess`), ce qui évite toute génération de code côté web.
- **Synchrone inter-services** : **connect-rpc** (HTTP/2 h2c en interne, HTTP/1.1 compatible). Contrats dans
  `proto/`, générés par `buf` dans `gen/`.
- **Asynchrone** : **NATS JetStream**. Conventions de sujets :

| Sujet | Émetteur | Contenu |
|---|---|---|
| `goap.process.<id>.started` / `.step` / `.waiting` / `.completed` / `.failed` | engine | `ProcessEvent` |
| `goap.change.<id>.item_added` / `.applied` | graph | `ChangeEvent` |
| `goap.registry.methodology.published` | registry | nom + version |
| `goap.engine.work` (work-queue) 🟡 | engine | tick de processus à exécuter (clustering) |

### 3.2 Clustering du moteur 🟡

Cible : l'état d'un processus est persisté (schéma `engine`) ; chaque pas d'exécution est déclenché par un
message sur un stream JetStream **work-queue** (`goap.engine.work`, clé = processId). N'importe quelle
réplique consomme le message, verrouille le processus (`SELECT … FOR UPDATE SKIP LOCKED` ou advisory lock),
exécute **un** pas, persiste, et republie un tick si le processus n'est pas terminal. Les pas sont
idempotents (clé `processId/stepIndex`). Le socle initial utilise un `ProcessStore` en mémoire derrière
une interface, remplaçable par l'implémentation PostgreSQL sans changer le moteur.

### 3.3 Persistance

- **Dev** : une instance PostgreSQL, **un schéma par service** (`graph`, `registry`, `engine`, `iam`,
  `modelgw`, `mcp`) et un rôle dédié par service (`deploy/postgres/init.sql`).
- **Prod** : une base (ou un cluster) par service ; seul le DSN change (`GOAP_DB_DSN`, lu depuis Vault).
- Migrations embarquées dans chaque service (`embed.FS`), appliquées au démarrage (verrou advisory).

Modèle `graph` (simplifié) :

```sql
node(id uuid, key text, type text, latest int)
node_version(node_id, version, props jsonb, deleted bool, change_id, created_at)  -- PK (node_id, version)
link(id uuid, type, from_id, from_version, to_id, to_version, props jsonb, change_id)
baseline(id uuid, name, parent_id, change_id, created_at)
baseline_entry(baseline_id, node_id, version)
change_set(id uuid, title, intent, status, baseline_id, goal, methodology, result_baseline_id, data jsonb)
change_item(id uuid, change_id, kind, type, status, target_id, target_version, payload jsonb,
            produced_by, derived_from uuid[], created_at)
```

> Pourquoi PostgreSQL et pas une base graphe ? Les parcours nécessaires (voisinage, propagation d'impact à
> profondeur bornée) s'expriment en CTE récursives ; le versionnement version-à-version et les baselines sont
> plus simples en relationnel ; un seul moteur à opérer. Une projection vers une base graphe reste possible
> via les événements NATS.

### 3.4 Sécurité

- La gateway valide le JWT (OIDC en prod, HS256 signé par un secret Vault en dev) et propage
  `X-Goap-Subject`, `X-Goap-Org`, `X-Goap-Roles` aux services (réseau interne uniquement).
- Permissions d'action : voir §2.7. Le moteur fait confiance aux en-têtes `X-Goap-*` : il ne doit être
  joignable que via la gateway (qui les écrase systématiquement).
- Chaque ressource (méthodologie, changeset, processus) appartient à une **organisation** : isolation multi-tenant
  par `org_id` dans toutes les tables (à ajouter avec le service IAM).
- Les secrets ne sont jamais en variables d'environnement en prod : `internal/platform/secrets` lit Vault
  (KV v2, auth token en dev / Kubernetes auth en prod) avec repli sur l'environnement en dev.

### 3.5 Déploiement

- `deploy/compose/docker-compose.yml` : postgres, nats (JetStream), vault (dev), tous les services, web.
- Une seule image multi-cible (`Dockerfile`, `ARG SERVICE`), binaire statique sur `distroless`.
- Kubernetes 🟡 : un chart Helm par service (ou kustomize) ; engine en `Deployment` scalable (HPA sur la
  profondeur du stream work-queue), NATS via le chart officiel, Vault Agent Injector.

## 4. Format d'une méthodologie

```yaml
name: impact-analysis
version: 1.0.0
description: Analyse d'impact d'un changement sur un référentiel d'exigences
domain:
  nodeTypes: [Need, Requirement, TestCase, Component]
  linkTypes:
    - {name: satisfies, from: Requirement, to: Need}
    - {name: verifies,  from: TestCase,    to: Requirement}
conditions:
  - name: has_impacts
    expr: size(impacts) > 0
  - name: impacts_propagated
    expr: artifacts.exists(a, a.type == "propagation")
actions:
  - name: identify_impacts
    kind: llm
    description: Identifier les nœuds directement impactés par l'intention
    pre: {has_impacts: false}
    effects: {has_impacts: true}
    cost: 2
    prompt: |
      ...
  - name: propose_test_updates
    kind: llm
    pre: {impacts_propagated: true}
    expects:
      forEach: impacts
      where: x.target.type == "Requirement"   # x = élément itéré
      produce: {op: create_node, nodeType: TestCase}
      link: {type: verifies}                  # direction: out (nouveau -> cible) par défaut
goals:
  - name: assess_impact
    description: Mesurer l'impact d'un changement sans rien modifier
    examples: ["qu'est-ce que ça casse", "quel est l'impact"]
    pre: {impacts_propagated: true}
```

Voir `methodologies/impact-analysis.yaml` pour l'exemple complet exécutable.

## 5. Organisation du dépôt

```
cmd/<service>/main.go        points d'entrée (gateway, registry, engine, graph, modelgw, mcp, iam)
cmd/goap-dev/                tout-en-un en mémoire pour le développement local
internal/platform/           config, logs, serveur HTTP/Connect, NATS, Postgres, secrets Vault
internal/<service>/          implémentation des handlers Connect d'un service
pkg/domain/                  modèle du graphe (axe domaine + axe change)
pkg/graph/                   Store (mémoire, PostgreSQL), apply, hydratation
pkg/goap/                    planificateur A*
pkg/condition/               compilation/évaluation CEL, compilation des `expects`
pkg/intent/                  boucle d'intention (Ranker lexical, Ranker LLM)
pkg/engine/                  processus, exécuteurs d'actions
pkg/methodology/             format de méthodologie, chargement YAML, validation
pkg/llm/                     contrat de complétion (implémenté par internal/modelgw)
proto/                       contrats connect-rpc (buf)
gen/                         code généré (commité)
methodologies/               méthodologies d'exemple
deploy/                      compose, init postgres, (k8s à venir)
web/                         frontend Svelte
docs/                        architecture, ADR
```

## 6. Feuille de route

| Étape | Contenu |
|---|---|
| **M0 — socle** 🟢 | Doc, modèle domaine/change, planificateur A\*, conditions CEL, boucle d'intention, moteur (mémoire), graph (mémoire + Postgres), registry, modelgw (fake + Anthropic + OpenAI-compatible), gateway, compose, UI minimale |
| **M1 — persistance moteur** | `ProcessStore` PostgreSQL, work-queue JetStream, reprise après crash, multi-réplique |
| **M2 — IAM** | organisations, utilisateurs, rôles, OIDC, isolation `org_id`, `CheckPermission` en remplacement de la politique de rôles statique |
| **M3 — MCP** | registre de serveurs MCP, découverte d'outils, actions `tool`, secrets MCP via Vault |
| **M4 — axe change avancé** | propagation d'impact (CTE récursive paramétrée par types de liens), liens suspects, diff de baselines, merge/rebase de changesets concurrents |
| **M5 — UX** | éditeur de méthodologies, visualisation du graphe et du plan, tâches humaines |
| **M6 — K8s** | charts Helm, HPA engine, observabilité (OpenTelemetry) |

## 7. Questions ouvertes

1. **Granularité des conditions** : faut-il des conditions *paramétrées* (par nœud) plutôt que globales ?
   GOAP classique raisonne sur des booléens globaux ; une condition par nœud ferait exploser l'espace d'états.
   Proposition : conditions globales quantifiées (`all`/`exists`) + actions qui itèrent en interne.
2. **Attendu d'action** : un `expects` suffit-il à exprimer tous les attendus, ou faut-il un vrai langage
   de motifs de graphe (type Cypher restreint) ?
3. **Concurrence sur une baseline** : deux ChangeSets partant de B1 — stratégie de fusion (rebase des propositions,
   détection de conflit sur `base version`) ?
4. **Coût des actions** : statique (déclaré) ou dynamique (tokens estimés, latence observée) ?
5. ~~**Décisions humaines** : validation obligatoire ou `apply` séparé ?~~ → tranché par l'[ADR 0004](adr/0004-application-du-change.md) :
   `apply` est une action planifiable conditionnée par la revue et protégée par une permission.
