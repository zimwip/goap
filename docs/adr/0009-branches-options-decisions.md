# ADR 0009 — Branches de versions, options d'analyse, boucles de décision et merge

**Statut** : accepté · **Date** : 2026-09 · Étend l'ADR 0003 (versionnement) et le scénario de conflit
(merge validé par un humain, rebase, replanification).

## Contexte
1. Deux changes concurrents peuvent modifier les mêmes nœuds : un conflit doit produire une **opération de
   merge** en attente de validation humaine, qui peut rendre caduques des actions du change et relancer la
   planification dans le contexte post-merge.
2. Une version de nœud doit pouvoir être créée **dans plusieurs branches en parallèle**, à la manière de git.
3. L'analyse d'une modification explore **plusieurs options**, chacune dans sa branche, les **compare** puis
   **décide** ; une décision impossible doit produire son **pourquoi** et un **objectif d'analyse
   additionnel**, jusqu'à ce que la décision puisse être entérinée ; l'option retenue est **mergée** sur la
   branche principale.

## Décision

### 1. Versions et branches
Chaque version de nœud porte :

| Champ | Rôle |
|---|---|
| `version` | entier croissant **par nœud**, toutes branches confondues (identité `NODE@v7`) |
| `branch` | nom de la branche (`main` par défaut) |
| `parents` | version(s) d'origine : une pour `revise` / `derive`, deux pour un merge |
| `reason` | `create` · `revise` (successeur sur la même branche) · `derive` (départ d'une branche parallèle) · `merge` |

```
REQ-1  v1(main) ── v2(main, revise) ─────────────── v6(main, merge ← v2 + v5)
          └────── v3(opt-a, derive) ── v4(opt-a, revise)
          └────── v5(opt-b, derive)
```

- Une **branche** est un objet `{name, parentBranch, forkBaseline, headBaseline, origin (change / option), status: open | merged | abandoned}`.
  Les baselines appartiennent à une branche et forment un DAG (`parents`).
- La règle de l'ADR 0003 est inchangée : les liens sortants appartiennent à la version source ; une version
  dérivée reporte ses liens sortants dans sa branche, et les liens entrants depuis d'autres branches deviennent suspects.
- « Dernière version » se lit **par branche** (`latest(node, branch)`). L'ancêtre commun de deux versions
  (remontée des `parents`) est la base du merge à 3 voies.

### 2. Conflits et merge (déjà retenu)
- Détection au plus tôt : quand une branche avance (apply, merge), les changes et options ouverts qui
  s'appuient sur des versions dépassées passent `diverged`.
- Item `merge` par proposition en conflit : `{base (ancêtre commun), theirs, ours, fusion proposée,
  clés en conflit}` ; la fusion est calculée (`graph.rebase`, 3 voies par propriété) ou proposée par un agent.
- Le processus est interrompu entre deux actions (`pending.kind = merge`) ; validation humaine (ABAC
  `change:merge`) ; puis **rebase** : baseline déplacée, items remplacés `superseded`, contexte post-change
  exposé (`change.rebases`) ; la boucle OODA replanifie ce qui est devenu caduc.

### 3. Options : une branche par hypothèse
Un change peut ouvrir des **options** : `{id, name, hypothesis, branch, status: exploring | evaluated | selected | rejected}`.

- Les **impacts** (analyse) restent au niveau du change et sont partagés ; les **propositions** portent
  l'option à laquelle elles appartiennent.
- Une option est **matérialisée** sur sa branche (`derive` depuis la baseline du change) quand une analyse
  a besoin du graphe résultant (propagation, simulation, contrôles) ; sinon elle reste à l'état de propositions.
- L'exploration d'une option est un **sous-processus** (sous-agent) travaillant sur l'option : il peut
  appliquer ses propositions sur la branche de l'option sans toucher `main`.
- La **comparaison** est un artefact `comparison` (critères × options, scores, argumentaire) produit par
  une action (LLM, script) ; conditions CEL disponibles : `options`, `options.all(o, o.status == "evaluated")`…

### 4. Décisions et boucles de décision
La décision est un **point de décision** sur le blackboard : `{question, options, critères, status:
open | blocked | decided, décideur, justification}`.

```
            ┌──────────────► explorer les options ──► comparer ──┐
            │                                                     ▼
   questions ouvertes ◄── « impossible de décider : pourquoi » ◄── décider ──► décision entérinée
   (goal d'analyse)                                                              │
            └── sous-agent d'analyse (identifié par l'intention = le pourquoi)   ▼
                                                                          merge de l'option sur main
```

- Le décideur (humain, ou agent avec ratification humaine selon la méthodologie) peut répondre
  **« indécidable »** avec un **pourquoi** : cela crée des items `question` (ouverts) sur le blackboard.
- Conditions de plateforme : `open_questions` / `no_open_questions`. L'action de décision exige
  `no_open_questions` ; une action générique `investigate` a pour effet de répondre aux questions : elle
  lance un **sous-agent** dont l'intention est la question — l'identification choisit l'agent d'analyse
  adapté, dans cette méthodologie ou dans une autre (axe multi-méthodologique).
- La réponse (artefact `answer` lié à la question) ferme la question ; le monde change ; le planificateur
  revient naturellement à la décision. Pas de pile de goals explicite : la boucle émerge du blackboard.
- Garde-fous : nombre maximal de tours et budget (tokens, durée) par point de décision ; au-delà,
  escalade vers un humain.

### 5. Entérinement et merge
Une fois la décision prise : l'option retenue passe `selected`, sa branche est **mergée sur `main`**
(flux du §2, avec validation humaine des conflits), les autres options passent `rejected` et leurs
branches `abandoned` (conservées pour l'audit : on sait ce qui a été envisagé et pourquoi ce n'a pas été retenu).
Le change continue ensuite vers son application ou sa release.

## Conséquences
- Le modèle de version gagne `branch`, `parents`, `reason` ; `latest` devient par branche ; les requêtes
  « dernière version » et la détection de liens suspects prennent la branche en paramètre.
- Le blackboard gagne les items `merge`, `option`, `decision point`, `question`, `answer` et le statut
  `superseded` ; l'IDE gagne une vue de comparaison d'options, un diff à 3 voies et une vue du graphe de versions.
- L'appel de sous-agents doit pouvoir cibler une autre méthodologie (`runAgent("méthodologie/agent", …)`).

## Décisions complémentaires (validées)

1. **Numérotation** : entier croissant par nœud + nom de branche.
2. **Matérialisation des options** : paresseuse (à la demande d'une analyse).
3. **Décideur** : un **agent de la méthodologie** prend la décision avec une **confiance** ; en dessous du
   seuil déclaré par le point de décision, la décision devient une action humaine en attente (ratification).
4. **Budget** : fixé pendant la phase d'analyse, **au niveau du change** (tokens, étapes, durée). Le moteur
   décompte la consommation ; les conditions exposent `budget` (`remaining`, `ratio`, `low`, `exhausted`).
   Quand le budget est bas, les méthodologies basculent sur des chemins **accélérés, sous-optimaux**
   (décider avec l'information disponible au lieu d'investiguer) ; épuisé, les boucles de décision sont
   closes de force par le décideur, et au besoin par un humain.
5. **Multi-méthodologie = spécialisation** : une action peut être **abstraite** et avoir des
   **spécialisations** (même rôle, règles différentes : `build` en C, en Java, en shell), déclarées dans la
   même méthodologie ou dans d'autres (`specializes: "<méthodologie>/<action>"`), avec une **garde** CEL
   (`when`) et une priorité. Le planificateur raisonne sur l'action abstraite ; à l'exécution, le moteur
   choisit la spécialisation applicable la plus prioritaire.
