# ADR 0013 — Domains: the object part, one per namespace

**Status**: accepted, being implemented · **Date**: 2026-09

## Context

A methodology is the **active part** of the model (agents, actions, conditions, goals, triggers: what is done). A
domain is the **object part** (what is worked on): node types, link types, lifecycles, algorithms. A `Requirement` is
a `Requirement` whatever methodology works on it, so the object part is defined once and shared, and it evolves on its
own lifecycle rather than with the methodologies.

## Decision

1. **A domain is a registry entity** with the lifecycle of a methodology (draft → published → archived, immutable once
   published), stored in the graph (ADR 0023), served by the `registry.v1` `*Domain*` RPCs, guarded by the ABAC
   resource `domain` (role `methodologist`). Saving a draft is a change like any other.
2. **One domain per namespace.** A domain's name is the namespace its nodes live in (`alm`, `organisation`,
   `platform`; ADR 0015), and the prefix of the references to its types (ADR 0012). The meta-domains `methodology` and
   `domain` are built in and type the definitions themselves (ADR 0023).
3. **A methodology references types, not a domain.** It names the qualified types and link types it works on
   (`alm@Requirement`) and may use several domains. There is no `domainRef` and no embedded `domain:` section. When a
   methodology is saved or published, every qualified reference is resolved against the domain version in force
   (the latest published one): `expects.produce.nodeType`, `expects.link.type`, type literals in CEL, builtin
   `params.linkTypes`. A methodology is published only on published domains.
4. **Domain publication is checked against its users.** A new domain version is refused when a published methodology
   would break on it (a type or link type it references disappears). A domain in use cannot be deleted or archived.
5. The version in force is the latest published one; publishing it changes what every change is judged by from the
   next baseline on (ADR 0012 §2). Nothing is projected.

## Consequences

- The IDE has a Domain explorer and editor; a methodology no longer picks a domain, its references say which ones it
  uses.
- A domain holds algorithms (ADR 0018); a methodology holds none.
- Moving the data of a domain to another namespace is a data migration, not a rename: the namespace is part of every
  type reference.
