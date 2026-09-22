# ADR 0006 — Méthodologies stockées en base, YAML en import/export

**Statut** : accepté · **Date** : 2026-09

## Contexte
Les méthodologies doivent être éditables depuis le frontend et administrables en base. Stocker le
source YAML rendait l'édition fragile (texte libre) et l'administration SQL impossible.

## Décision
- Stockage **relationnel normalisé** dans le schéma `registry` : un en-tête `methodology`
  (nom, version, statut, auteur, dates) et une table par section (types de nœuds, types de liens,
  conditions, actions, objectifs) ordonnée par `position`. Les champs à structure variable (pré/effets,
  `expects`, `params`) sont en `jsonb`.
- Cycle de vie : `draft` → `published` (immuable, validé) → `archived`. Le moteur n'exécute que la dernière
  version publiée ; les versions publiées sont mises en cache par nom@version dans le moteur.
- Validation détaillée (`methodology.Validate`) : liste d'anomalies avec chemin de champ, utilisée par
  l'éditeur ; un brouillon invalide peut être enregistré, pas publié.
- YAML conservé comme format d'échange : `ImportMethodology` / `ExportMethodology`, et import des
  fichiers `methodologies/*.yaml` au démarrage (versions absentes uniquement).
- Écritures soumises à l'ABAC (ressource `methodology`, actions `write`, `publish`, `delete`).

## Conséquences
- Le frontend édite un modèle structuré (formulaires) plutôt que du texte.
- Modifier une méthodologie publiée impose une nouvelle version : les processus en cours restent
  cohérents avec la version qui les a démarrés (à figer explicitement dans le processus au jalon M1).
