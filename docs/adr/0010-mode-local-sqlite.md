# ADR 0010 — Mode local sans conteneur (SQLite)

**Statut** : accepté · **Date** : 2026-09

## Contexte
La pile complète (compose : PostgreSQL, NATS, Vault, services, observabilité) est lourde pour travailler sur
une méthodologie ou sur l'IDE. `goap-dev` (tout-en-un en mémoire) perd tout à l'arrêt : graphe, méthodologies
éditées, politiques, processus en attente d'une action humaine.

## Décision
- `goap-dev` choisit son stockage par `GOAP_STORE` : `memory` (défaut) ou `sqlite`, un **fichier unique**
  (`GOAP_SQLITE_PATH`, défaut `.goap/goap.db`) partagé par le graphe, le registry, l'IAM et le moteur.
- Pilote **`modernc.org/sqlite`** (Go pur, sans cgo) : rien à installer hors Go (et Node pour l'IDE).
- Chaque composant a ses migrations SQLite (`migrations_sqlite/`), suivies par composant dans
  `schema_migrations` ; mêmes interfaces de stockage que PostgreSQL (`graph.Repo`, `registrysvc.Store`,
  adaptateur Casbin, `engine.Store`) et mêmes tests (le graphe, le registry et l'IAM tournent sur mémoire,
  SQLite et PostgreSQL).
- Le graphe garde le modèle normalisé (versions, liens, baselines, branches) ; les méthodologies et les
  processus sont des documents JSON (interrogeables avec les fonctions JSON de SQLite).
- Une seule connexion en écriture (SQLite n'a qu'un écrivain), WAL, clés étrangères actives.
- Au démarrage, les processus restés `running` sont marqués `failed` (pas de work-queue à reprendre) ;
  les processus `waiting` / `clarifying` reprennent normalement.
- `goap-dev` sert l'IDE compilé (`GOAP_WEB_DIR`, défaut `web/dist`) avec repli SPA ; `make devlocal`
  recompile l'IDE si ses sources ont changé puis lance le tout sur http://localhost:8080.
- Les scripts s'exécutent dans le processus (`GOAP_SANDBOX=inproc`) ou en sous-processus
  (`GOAP_SANDBOX=process`), sans docker.

## Conséquences
- SQLite reste un mode de **développement** : pas de clustering, un seul processus ; la production reste
  sur PostgreSQL (une base par service).
- Toute évolution de schéma se fait dans les deux dialectes (`migrations/` et `migrations_sqlite/`).
