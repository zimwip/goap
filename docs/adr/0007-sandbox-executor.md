# ADR 0007 — Exécution sandboxée des actions : un sandbox par processus, provisioner par environnement

**Statut** : accepté · **Date** : 2026-09

## Contexte
Les actions `script` exécutent du code saisi par les méthodologues (JavaScript, Go). Il ne doit jamais
tourner dans le moteur ni pouvoir atteindre l'hôte, le réseau interne ou des secrets. La plateforme se
déploie en Docker Compose (dev), Kubernetes et potentiellement bare metal.

## Options
1. Interpréteurs dans le moteur (sandbox « langage » seule) — simple, mais une faille d'interpréteur
   compromet le moteur et ses secrets.
2. Un conteneur **par action** — isolation forte mais coût de démarrage (~1 s) à chaque action.
3. Un sandbox **par processus** réutilisé pour ses actions, derrière une abstraction de provisioner.

## Décision
Option 3 :
- `engine.Sandboxes` (Acquire / Release par processus) implémenté par `sandbox.Pool` sur un
  `Provisioner` (`process`, `docker`, `kubernetes` ; `inproc` pour les tests) choisi par `GOAP_SANDBOX`.
- Le sandbox exécute `goap-runner` (SandboxService). Les opérations qui sortent du script passent par le
  RuntimeService du moteur avec un **jeton par job** : le sandbox n'a ni identité, ni secret, ni accès
  au graphe ou au model gateway.
- Défense en profondeur : interpréteurs sans fichiers / réseau / processus + isolation du conteneur ou
  du pod (rootfs en lecture seule, pas de capabilities, non-root, limites, réseau interne, gVisor optionnel).
- Sandboxes arrêtés à la fin du processus ou après inactivité.

## Conséquences
- Le moteur pilote une infrastructure (API Docker via proxy restreint, ou droits `pods` limités à un
  namespace dédié sur Kubernetes).
- Latence : démarrage du sandbox au premier script d'un processus ; les appels DSL sortants font un aller-retour
  réseau vers le moteur.
- Une boucle infinie dans un script Go interprété ne peut pas être interrompue : le job échoue au timeout et
  le sandbox (jetable) est recyclé avec le processus.
