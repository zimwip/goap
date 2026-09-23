# ADR 0013 — Shared Domain: object part apart from the methodologies

**Status**: accepted · **Date**: 2026-09

## Context

A methodology embedded its own `domain:` (node types, link types). Every methodology therefore
redefined `Need`, `Requirement`, `Component`… with slightly different properties, the graph got one
`NodeType` node per methodology (`M:<meth>/nodetype/<name>`) for what is the same concept, and the
domain could only change by publishing a new version of a methodology together with its agents,
actions and goals.

A methodology is the **active part** of the model (agents, actions, conditions, goals, triggers: what
is done). The domain is the **object part** (what is worked on): a `Need` is a `Need` whatever
methodology it comes from.

## Decision

1. **Domain is a registry entity** with the lifecycle of a methodology (draft → published → archived,
   immutable once published), stored on its own (`domain*` tables, `registry.v1` `*Domain*` RPCs, ABAC
   resource `domain`, role `methodologist`). Editing it involves **no change, impact or proposal**.
2. **A methodology references a domain** with `domainRef: <name>[@<version>]` (unpinned: latest
   published). Embedding a `domain:` stays valid; embedding and referencing are exclusive.
3. **Consistency is checked at save / publish**: the domain is resolved (`Methodology.Resolve`) and the
   methodology validated against it — `expects.produce.nodeType`, `expects.link.type`, type literals in CEL
   (`"X" in p.node.types`, `.target.type == "X"`, `.link.type == "x"`), builtin `params.linkTypes`.
   A methodology is published only on a published domain. A domain version is refused when a published
   methodology that follows the latest version would break, and cannot be deleted or archived while a
   methodology is pinned to it (or follows it as the latest published version).
4. **Graph**: the `NodeType` nodes of a domain are keyed `D:<domain>/nodetype/<name>`, one per type,
   shared by every methodology and data node using the domain. The methodology's root node carries its
   `domainRef`; `metamodel.TypeNamespace` resolves where a methodology's types live, so
   `Supertypes`, `BackfillInstanceOf`, `LinkToType`, `CreateObject`, `ApplyNodeTypes` and the engine's
   item resolver work the same for embedded and shared domains. ADR 0012 still holds: synchronization only
   creates missing NodeTypes and `extends` links, it never overwrites or deletes them.
5. A domain publication re-projects the methodologies (`goap.registry.domain.published`).

## Consequences

- No data migration: existing methodologies keep their embedded domain. Moving them onto a shared domain
  (merging the differing `Requirement` / `Component` definitions) is a separate, deliberate step; types
  already projected under `M:<meth>/nodetype/*` stay in the graph (ADR 0012: never deleted).
- The IDE has a Domain explorer and editor; a methodology picks its domain (and version) in its tab.
- The engine client asks the registry for a resolved methodology (`resolve_domain`); an unpinned reference
  is cached by methodology version, so a newer domain version reaches a running engine at its next start.
- Existing policy stores are seeded once: add the `domain` rules (see `pkg/authz/casbin.go`) to a
  database created before this ADR.
