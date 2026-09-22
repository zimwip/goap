# ADR 0002 — Conditions exprimées en CEL

**Statut** : accepté · **Date** : 2026-09

## Contexte
Les préconditions / effets GOAP sont des booléens nommés. Il faut les calculer à partir de l'état
du changement, lequel référence des éléments de domaine. Le langage doit être déclaratif (YAML),
sûr (exécuté côté serveur), rapide et vérifiable au chargement.

## Options
1. Code Go enregistré (souple, mais pas déclaratif ni déployable sans build).
2. JSONPath / JMESPath (pas de quantificateurs typés, erreurs peu explicites).
3. **CEL** (typé, non Turing-complet, macros `all`/`exists`/`filter`, borne de coût, utilisé par
   Kubernetes/Envoy).
4. Rego (puissant mais orienté politique, plus lourd).

## Décision
CEL (`cel.dev/cel-go`). Variables : `change`, `items`, `impacts`, `proposals`, `decisions`,
`artifacts`, `vars`. Les références de domaine sont hydratées (`type`, `key`, `props`, `out`, `in`,
`latest`). Une expression en erreur rend la condition **inconnue** : elle ne satisfait aucune
précondition. Les `expects` des actions sont compilés en CEL.

## Conséquences
- Validation des méthodologies à la publication (compilation + type bool).
- Pas d'appel réseau pendant l'évaluation ; la profondeur de navigation est limitée à l'hydratation
  (voisinage direct). Des fonctions de parcours pourront être ajoutées si nécessaire.
