# GOAP — atelier web (IDE)

Interface Svelte 5 + Vite + TypeScript de la plateforme GOAP, organisée comme un
IDE (à la VS Code) : conception des méthodologies (agents, actions, conditions,
objectifs, domaine), tests d'intention, suivi en direct des exécutions, revue des
changements, référentiels, déclencheurs et politiques d'accès. Un **assistant**
conversationnel permet aux non-spécialistes de formuler une demande en langage
courant.

## Disposition

```
┌──────────────────────────────── En-tête ─────────────────────────────────┐
│ GOAP Atelier   [ Rechercher (Ctrl+P) — « > » : commandes ]   ● en direct 👤 │
├──────────────────────────────── Barre d'outils ──────────────────────────┤
│ actions de l'éditeur actif (Enregistrer, Valider, Publier…)   globales    │
├──┬──────────────┬──────────────────────────────────────┬──────────────┬──┤
│A │ Navigation   │ Onglets d'édition                    │ Panneau droit│A │
│c │ (outil de la │                                      │ (Tester,     │c │
│t │  barre       │  éditeur de l'onglet actif           │  Propriétés, │t │
│. │  d'activité) ├──────────────────────────────────────┤  Aide DSL)   │. │
│  │              │ Console : Événements · Journaux ·    │              │  │
│  │              │ Problèmes · Tokens                   │              │  │
├──┴──────────────┴──────────────────────────────────────┴──────────────┴──┤
│ ● Plateforme OK   flux en direct      2 exécutions en cours  👤 dev  🔔 3 │
└──────────────────────────────── Barre d'état ────────────────────────────┘
```

- **En-tête** : recherche d'objets (onglets ouverts, méthodologies, agents,
  exécutions, référentiels, changements) ; préfixe `>` pour la palette de
  commandes. Menu utilisateur : identité (`WhoAmI`), jeton d'accès (stocké dans
  `localStorage`, clé `goap.token`, envoyé en `Authorization: Bearer …`), thème
  (système / clair / sombre).
- **Barre d'outils** : actions contextuelles de l'éditeur actif (méthodologie :
  Enregistrer, Valider, Publier, Exporter, Nouvelle version, Recharger,
  Supprimer / Archiver ; exécution : Actualiser, Trace, Changement, Parent…),
  plus « Nouveau test d'intention » et les bascules des panneaux.
- **Barre d'activité gauche** → panneau de navigation (redimensionnable,
  repliable) :
  - **Assistant** : conversation en langage courant (aussi disponible en onglet) ;
  - **Méthodologies** : méthodologie → version → Agents / Actions / Conditions /
    Objectifs / Domaine (compteurs de problèmes, ● modifications, `+` pour ajouter) ;
  - **Exécutions** : processus regroupés par statut, sous-agents imbriqués sous
    leur parent ;
  - **Déclencheurs** : état des déclencheurs des agents publiés (`ListTriggers`),
    bouton « Déclencher » (`FireTrigger`) qui ouvre l'exécution créée ;
  - **Référentiel** : baselines → nœuds par type ;
  - **Changements** ; **Accès** (politiques ABAC).
- **Zone d'édition à onglets** : chaque objet s'ouvre dans un onglet. Un clic
  simple ouvre un **aperçu** (titre en italique) remplacé par la sélection
  suivante ; un onglet devient **épinglé** par double clic (sur l'onglet ou dans
  l'explorateur) ou dès qu'on modifie son contenu ; l'icône d'épingle le
  désépingle. ● signale des modifications non enregistrées ; clic milieu ferme ;
  glisser-déposer pour réordonner. Les onglets sont mémorisés (`localStorage`).
- **Console** (redimensionnable, repliable) : **Événements** (flux
  `WatchEvents` en direct, clic → exécution), **Journaux** (filtre par niveau et
  processus), **Problèmes** (validation du brouillon actif, clic → champ
  concerné), **Tokens** (un appel LLM par ligne : heure, processus, agent,
  action, modèle, entrée / sortie, durée, totaux).
- **Barre d'activité droite** : **Tester** (méthodologie et agent facultatifs,
  référentiel, intention → « Envoyer » = `StartProcess` ; candidats de
  l'identification, question de clarification et réponse ; ouvre l'exécution),
  **Propriétés** (détails de l'objet sélectionné), **Aide DSL** (`docs/dsl.md`).
- **Barre d'état** : santé de la plateforme (`GET /api/status` toutes les 15 s,
  détail des services au clic), mes exécutions en cours (`ListProcesses`
  `{mine, rootsOnly, statuses}` + flux ; liste au clic), identité et
  **notifications** (fin, échec, blocage, saisie ou approbation attendue,
  clarification, lancement par un déclencheur ; 100 dernières, mémorisées).

Sous 1024 px de large, les panneaux latéraux se superposent à l'éditeur.

## Éditeurs

- **Méthodologie** : général et domaine (types de nœuds et de liens), contenu.
- **Agent** : nom, description, exemples, planificateur (`goap`, `utility`,
  `hybrid`), actions admissibles et objectifs (liste vide = tous), et
  **déclencheurs** (événement ou planification cron UTC, filtre CEL, objectif,
  intention, cible, rôles, activé).
- **Action** : tous les champs ; `script` : éditeur de code (CodeMirror)
  JavaScript / Go avec coloration et complétion de l'API `ctx.` ; `llm` : éditeur
  de prompt ; `utility` : expression CEL numérique.
- **Condition** (expression CEL), **Objectif**.

Les agents, actions, conditions et objectifs sont édités dans le **brouillon**
en mémoire de leur méthodologie@version, partagé par tous ses onglets :
enregistrer depuis n'importe lequel enregistre la méthodologie entière
(`SaveMethodology`). Le brouillon est validé automatiquement après chaque
modification. Un renommage met à jour les références (pre / effects, agents,
déclencheurs). Les versions publiées ou archivées sont en lecture seule.

- **Exécution** (en direct via `WatchEvents`, repli sur `GetProcess` toutes les
  2 s si le flux échoue) : statut, agent, planificateur, objectif, initiateur,
  « déclenché par », totaux (tokens, appels LLM / outils), lien **Trace** vers
  Jaeger, parent / sous-agents, plan, état du monde, étapes (usage, appels LLM
  et outils, journaux, sandbox, sous-agents), tâche en attente (saisie,
  approbation, attente d'un sous-agent) et dialogue d'intention.
- **Changement**, **Référentiel**, **Politiques d'accès**, **Import YAML**.

## Assistant

Pour les utilisateurs non spécialistes : cartes des agents disponibles avec
leurs exemples de demandes, saisie libre → `StartProcess` sans méthodologie
(identification parmi tous les agents publiés, sur le référentiel le plus
récent — modifiable dans les réglages). La conversation suit l'exécution en
direct avec des messages lisibles (description des actions), pose les questions
de clarification sous forme de boutons, affiche les tâches humaines et les
approbations dans la conversation, suit les sous-agents, et résume le résultat
(artefacts, impacts, propositions, tokens, « Voir le détail »). L'historique est
conservé dans le navigateur ; « Nouvelle conversation » le réinitialise.

## Raccourcis

| Raccourci                    | Action                                   |
| ---------------------------- | ---------------------------------------- |
| `Ctrl`/`Cmd` + `S`           | enregistrer l'éditeur actif              |
| `Ctrl`/`Cmd` + `W`, `Alt`+`W` | fermer l'onglet (`Alt`+`W` si le navigateur intercepte `Ctrl`+`W`) |
| `Ctrl`/`Cmd` + `J`           | afficher / masquer la console            |
| `Ctrl`/`Cmd` + `B`           | afficher / masquer la navigation         |
| `Ctrl`/`Cmd` + `P` (ou `K`)  | rechercher ; `>` pour les commandes      |
| `Ctrl` + `Espace`            | complétion dans l'éditeur de code        |
| `Ctrl` + `Entrée`            | envoyer l'intention (Tester)             |
| double clic                  | épingler un onglet / ouvrir épinglé      |
| clic milieu                  | fermer un onglet                         |
| flèches                      | naviguer dans les arbres, onglets et barres d'activité ; redimensionner un séparateur focalisé |

## Démarrage

Prérequis : Node.js ≥ 20.19 (ou ≥ 22.12) et une passerelle GOAP accessible
(ou `go run ./cmd/goap-dev` à la racine du dépôt).

```sh
cd web
npm install
npm run dev          # http://localhost:5173
```

En développement, Vite relaie `/goap.*` (RPC Connect) et `/api` (état de la
plateforme) vers la passerelle, par défaut `http://localhost:8080` :

```sh
GOAP_GATEWAY_URL=http://passerelle:8080 npm run dev
```

Variables de build facultatives :

| Variable               | Rôle                                                    |
| ---------------------- | ------------------------------------------------------- |
| `VITE_GOAP_BASE_URL`   | base des URL de la passerelle (défaut : relatives)       |
| `VITE_GOAP_JAEGER_URL` | base de Jaeger pour les liens « Trace » (défaut `http://localhost:16686`) |

## Scripts

| Commande          | Rôle                                            |
| ----------------- | ----------------------------------------------- |
| `npm run dev`     | serveur de développement avec proxy             |
| `npm run build`   | build de production dans `dist/`                |
| `npm run check`   | vérification des types (`svelte-check`)         |
| `npm run preview` | sert le build localement                        |

## Protocole

- RPC unaires : Connect en JSON (`POST /{package.Service}/{Method}`), sans
  génération de code (`src/lib/api.ts`). Les `int64` arrivent en chaînes (proto3
  JSON) : `int()` les convertit.
- Flux serveur `EngineService.WatchEvents` (`src/lib/stream.ts`) : `fetch` +
  `ReadableStream`, `Content-Type: application/connect+json`, enveloppes Connect
  (1 octet d'indicateurs + longueur 32 bits big-endian + JSON ; indicateur
  `0x02` = fin de flux, erreur éventuelle), `AbortController`, reconnexion avec
  délai exponentiel. Le serveur n'envoie les en-têtes qu'avec le premier
  événement : un flux inactif reste « en attente », ce qui est normal.

## Organisation

- `src/lib/shell/` : mini-framework de l'IDE — registre des vues
  (`registerView({id, zone: 'left'|'right'|'bottom'|'editor', title, icon, component})`),
  disposition persistée (`layout.svelte.ts`), onglets d'aperçu / épinglés
  (`tabs.svelte.ts`), actions contextuelles et mises en évidence
  (`workbench.svelte.ts`), composants de la coquille (en-tête, barres, panneaux,
  onglets, console, barre d'état).
- `src/lib/views/` : vues enregistrées (`index.ts`) — `nav/` (explorateurs),
  `editors/` (onglets), `bottom/` (console), `right/` (outils), `assistant/`.
- `src/lib/stores/` : brouillons des méthodologies, catalogues, données vivantes
  (événements, processus, journaux, tokens), identité, notifications, état de la
  plateforme, assistant.
- `src/lib/api.ts`, `src/lib/stream.ts` : client Connect (unaire et flux).
- `src/lib/methodologyForm.ts` : modèle d'édition d'une méthodologie ↔ message proto.
- `src/lib/codemirror.ts`, `src/lib/dsl.ts` : éditeur de code et complétion du DSL.
- `src/lib/help/dsl.md` : **copie** de `docs/dsl.md` embarquée au build (le
  conteneur de développement ne monte que `web/`) — à resynchroniser quand
  `docs/dsl.md` change.
- `src/lib/components/` : composants réutilisés (formulaires de tâche humaine,
  approbation, lignes de conditions, frise des étapes…).

CodeMirror est chargé à la demande (morceau séparé du bundle).

## Version de TypeScript

TypeScript est volontairement contraint à `^6` : `svelte-check` (4.7) déclare
`typescript: ^5.0.0 || ^6.0.0` en dépendance pair. Ne passer à TypeScript 7
qu'une fois une version de `svelte-check` compatible publiée.
