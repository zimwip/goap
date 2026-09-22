# ADR 0005 — Contrôle d'accès ABAC avec Casbin

**Statut** : accepté · **Date** : 2026-09 · Remplace la politique de rôles statique introduite avec l'ADR 0004.

## Contexte
Les décisions d'accès dépendent d'attributs, pas seulement de rôles : organisation de la ressource
(multi-tenant), propriétaire (séparation des tâches : on n'approuve pas son propre changement), type de
méthodologie… Les règles doivent être administrables sans redéploiement.

## Décision
- **Casbin** avec un modèle ABAC : `p = sub_rule, obj_type, act, eft`, matcher
  `(type ou *) && (action ou *) && eval(sub_rule)`, effet *allow sauf deny*.
- Les règles sont des expressions sur `r.sub` (Principal), `r.obj` (Resource) et `r.act`, avec les
  fonctions `hasRole`, `hasAnyRole`, `isAnonymous`.
- Politiques persistées dans le schéma `iam` (`casbin_rule`, adaptateur pgx maison), administrées par
  `IamService` ; les autres services interrogent `CheckPermission` via `authz.Authorizer`.
- En mode tout-en-un (`goap-dev`), l'enforcer est en mémoire dans le processus.

## Conséquences
- Une seule sémantique d'autorisation pour tous les services ; règles modifiables à chaud.
- Chaque décision coûte un appel RPC à iam ; un cache de décisions pourra être ajouté si nécessaire.
- Les politiques par défaut ne sont insérées que si la table est vide : les faire évoluer sur une base
  existante passe par l'API d'administration (ou une migration).
- Les en-têtes d'identité (`X-Goap-*`) ne sont fiables que si les services ne sont joignables que via la gateway.
