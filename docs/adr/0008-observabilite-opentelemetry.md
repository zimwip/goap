# ADR 0008 — Observabilité OpenTelemetry, un processus = une trace

**Statut** : accepté · **Date** : 2026-09

## Contexte
Il faut suivre l'ensemble des appels et surtout chaque appel LLM et chaque outil utilisé, avec les
tokens consommés, et les rattacher au processus, à l'agent et à l'action.

## Décision
- OpenTelemetry (SDK Go), export OTLP vers un **collector** (Jaeger pour les traces, Prometheus pour les
  métriques, Grafana pour la visualisation) ; configuration par les variables `OTEL_*` standard.
- Instrumentation : Echo, Connect (`otelconnect`, parent distant de confiance entre services internes),
  pgx, NATS (contexte dans les en-têtes), proxy de la gateway.
- Spans métier : `process <agent>` (le `traceparent` est persisté dans le processus pour que les
  exécutions en arrière-plan, reprises et sous-agents restent dans la même trace), `action <nom>`,
  `chat <modèle>` (conventions GenAI) dans le model gateway, `execute_tool <nom>`.
- Attribution des appels LLM par **baggage** (`goap.process.id`, `goap.agent`, `goap.action`,
  `goap.methodology`) posé par le moteur et lu par le model gateway ; l'identifiant de processus n'est pas
  mis dans les métriques (cardinalité).
- Les compteurs (tokens, appels) sont également persistés dans le processus pour l'IDE.

## Conséquences
- Une trace peut durer longtemps (processus en attente d'un humain) : acceptable pour Jaeger, à surveiller
  pour l'échantillonnage (tail sampling dans le collector si nécessaire).
