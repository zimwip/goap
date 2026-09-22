# ADR 0004 — L'application du change est une action planifiable protégée par permission

**Statut** : accepté · **Date** : 2026-09

## Contexte
Les actions n'écrivent que dans le change (ADR 0001) ; le domaine n'est transformé qu'à l'application
(`ApplyChange`). Il fallait décider qui déclenche cette application : un acte manuel hors moteur, ou le
moteur lui-même.

## Décision
- L'application est une action `builtin: graph.apply` de la méthodologie, avec des préconditions (en
  général `reviewed: true`) et l'effet `applied: true` (`change.status == "applied"`).
- Une action peut déclarer une `permission` (ici `change:apply`). Le moteur l'évalue pour
  l'**initiateur** du processus :
  - permission détenue → exécution automatique ;
  - sinon → tâche d'**approbation** ; `ApproveAction` par une personne habilitée exécute l'action avec
    son identité (`approvedBy`), un refus désactive l'action pour ce processus.
- Les permissions sont décidées par `authz.Authorizer` : politique de rôles statique aujourd'hui,
  `IamService.CheckPermission` au jalon M2.

## Conséquences
- Chaque méthodologie décide si l'application est automatique, soumise à approbation, ou absente
  (goal qui s'arrête à la revue).
- La validation reste traçable dans le processus (étape, approbateur, commentaire de refus).
- L'application peut échouer en conflit si le référentiel a évolué depuis la baseline de référence :
  l'étape est en erreur et l'action est retentée puis désactivée comme toute autre action.
