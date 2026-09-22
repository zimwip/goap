# ADR 0003 — Liens version-à-version, liens sortants portés par la source

**Statut** : accepté · **Date** : 2026-09

## Contexte
Les liens relient des versions exactes. Une baseline doit rester immuable, et un changement sur un
nœud doit signaler les éléments qui en dépendent.

## Décision
- Un lien appartient à une baseline si ses deux extrémités y sont (aux versions du lien).
- Les **liens sortants font partie de la version du nœud source** : ajouter ou retirer un lien sortant
  d'un nœud existant crée une nouvelle version de ce nœud.
- À l'application d'un changement, un nœud qui change de version **reporte ses liens sortants** ;
  les liens **entrants** depuis des nœuds non modifiés restent sur l'ancienne version et deviennent
  **suspects** (`SuspectLinks`) : c'est le signal d'impact natif du modèle.

## Conséquences
- Les baselines sont immuables sans stocker explicitement l'ensemble des liens.
- Un changement de propriété d'une exigence rend suspects ses tests et composants, à revoir dans un
  changement ultérieur (ou dans le même, en les incluant).
