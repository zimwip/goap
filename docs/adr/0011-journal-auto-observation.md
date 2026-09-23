# ADR 0011 — Journal d'exécution, méthodologie dans le domaine, auto-observation

**Statut** : accepté · **Date** : 2026-09

## Contexte
L'exécution d'un change doit être **traçable et auditable de bout en bout** : pas seulement les items du
blackboard, mais aussi chaque tick de planification, chaque exécution d'action (LLM ou formelle), chaque
appel de modèle ou d'outil, chaque décision humaine, reliés aux items qu'ils ont produits. Ces données
permettent une **auto-observation** : un agent de la plateforme analyse les exécutions pour **améliorer
les méthodologies elles-mêmes**. Pour que ses propositions soient des changes comme les autres, la
méthodologie doit être **modélisée dans le domaine** en tant qu'élément versionné. OpenTelemetry reste la
source de vérité des durées et des points durs techniques.

## Décision

### 1. Journal d'exécution (axe change)
- Chaque change porte un **journal** d'`ExecutionRecord` (`pkg/domain/execution.go`), écrit par le moteur :
  `process.started`, `tick` (état du monde, plan, action choisie, `replanned`, conditions inconnues),
  `action` (action, spécialisation exécutée, type, effets tenus, items produits, tokens, appels de modèle et
  d'outils, sortie, erreur, attente humaine / sous-agent), `approval` (décideur, décision), `process.ended`
  (statut, totaux, actions désactivées).
- Chaque enregistrement porte `methodology@version`, agent, planificateur, but, `traceId` / `spanId`
  (lien vers le span OpenTelemetry). Les items du blackboard portent `execution` : l'enregistrement qui les
  a produits (provenance jusqu'aux appels LLM).
- Stockage par le service graph (mémoire, PostgreSQL, SQLite), ordre d'écriture ; RPC `RecordExecutions` /
  `ListExecutions`. L'écriture est « au mieux » : une panne du journal est journalisée, jamais bloquante.

### 2. La méthodologie comme élément versionné du domaine
- `pkg/metamodel` projette chaque méthodologie publiée sur des nœuds (`Methodology`, `Agent`, `Action`,
  `Goal`, `Condition`, `Trigger`, `NodeType`, clés `M:<méthodologie>[/<type>/<nom>]`) et des liens
  (`contains`, `uses`, `pursues`, `has`, `requires`, `achieves`, `specializes`, `extends`).
- La projection passe par un **change appliqué sur main** : chaque publication crée de nouvelles versions
  des éléments modifiés (liens portés par la version source, ADR 0003). L'historique du graphe explique
  l'évolution de la méthodologie, et un enregistrement du journal désigne la version exacte exécutée.
- Le registry reste la source de vérité de la définition ; le graphe en est la projection versionnée
  (synchronisée au démarrage et à chaque publication).

### 3. Agent d'auto-observation (`methodology-improvement`)
- Déclenché à la fin d'un processus racine (`process.completed`, `process.failed`, `process.stuck`) ;
  l'événement est exposé au processus (`vars.event`).
- `analyze_run` (`observe.analyze`) : journal du run et de ses sous-agents, items produits, **spans
  OpenTelemetry** de la trace (API Jaeger, `GOAP_TRACE_QUERY_URL`) → rapport de coût (`cost_report`) :
  statistiques par action et **constats** — boucles, exécutions sans effets, actions désactivées, action
  LLM coûteuse, **action LLM systématisable** (sortie régulière), replanifications, actions et spans lents.
- `propose_improvements` (abstraite) spécialisée par des **règles** (`observe.propose`, par défaut) ou par
  un **LLM** (`vars.llm_review`) : propositions sur les nœuds de la méthodologie — spécialisation par un
  script d'une action LLM systématisable, modèle `fast`, coût, actions d'un agent, **demande d'outil MCP**
  (`ToolRequest`) pour un point lent.
- Revue humaine (décisions), puis `draft_methodology` (`methodology.draft`, permission
  `methodology:write`) : les propositions acceptées sont appliquées à la définition publiée et enregistrées
  comme **nouvelle version brouillon** (version patch suivante), à publier depuis l'éditeur.

## Conséquences
- Le journal grossit avec les exécutions : rétention / archivage à prévoir (M1).
- Les constats sont heuristiques et paramétrables (`observe.Thresholds`) ; le LLM peut les affiner.
- Les demandes d'outils MCP ne modifient pas la méthodologie : elles alimentent le backlog du connecteur MCP.
- La boucle est fermée : exécuter → observer → proposer → revoir → publier → exécuter la nouvelle version.
