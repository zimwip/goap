# ADR 0001 — Le blackboard est l'axe *change* du graphe

**Statut** : accepté · **Date** : 2026-09

## Contexte
Embabel utilise un blackboard en mémoire contenant des objets typés. Pour des méthodologies
d'entreprise, le travail d'un agent doit être persistant, auditable, partageable entre humains et
agents, et relié au référentiel qu'il modifie.

## Décision
Le blackboard d'un processus est un **ChangeSet** stocké par le service graph. Ses éléments
(`impact`, `proposal`, `decision`, `artifact`) référencent des **versions exactes** de nœuds du
graphe de référence. Les actions n'écrivent que des ChangeItems ; le graphe de domaine n'est modifié
que par `ApplyChange`, qui produit une nouvelle baseline.

## Conséquences
- Traçabilité complète (provenance `producedBy` / `derivedFrom`) et reprise possible d'un processus.
- Plusieurs processus / humains peuvent contribuer au même changement.
- Le moteur doit relire (hydrater) le blackboard à chaque cycle : coût réseau accepté, un cycle
  correspondant à un appel LLM ou une action humaine.
