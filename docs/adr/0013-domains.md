# ADR 0013 — Domains: the object part, one per namespace

**Status**: accepted, implemented · **Date**: 2026-09

## Context

A methodology is the **active part** of the model (agents, actions, conditions, goals, triggers: what is done). A
domain is the **object part** (what is worked on): node types, link types, lifecycles, algorithms. A `Requirement` is
a `Requirement` whatever methodology works on it, so the object part is defined once and shared, and it evolves on its
own lifecycle rather than with the methodologies.

## Decision

1. **A domain is a registry entity** with the lifecycle of a methodology (draft → published → archived, immutable once
   published), kept in the registry's database (ADR 0023), served by the `registry.v1` `*Domain*` RPCs, guarded by
   the ABAC resource `domain` (role `methodologist`). A domain is the definition of a graph, not graph data: saving a
   draft is no change of the graph.
2. **One domain per namespace.** A domain's name is the namespace its nodes live in (`alm`, `platform`,
   `organisation`; ADR 0015), and the prefix of the references to its types (ADR 0012). **Adding a namespace is
   creating and publishing a domain** in the domain editor: its name (lowercase letters, digits, `-`, `_`) becomes the
   namespace, and methodologies can target it. The domains shipped with the code (ADR 0012 §4),
   `methodology`, `organisation` and `platform`, are shown in the domain editor, read-only: they are initialised at
   startup and change with the code.
3. **A methodology names its target namespace, not a domain.** `namespace: alm` is the namespace its changes act on
   (ADR 0015 §2), hence the domain whose nodes it creates and modifies; it replaces `domainRef`, and there is no
   embedded `domain:` section. Its references to types and link types are qualified (`alm@Requirement`); they may
   point to other domains for what it reads or links to (`organisation@OrgUnit`), but the nodes it writes are of its
   target namespace. When a methodology is saved or published, every qualified reference is resolved against the
   domain version in force (the latest published one): `expects.produce.nodeType`, `expects.link.type`, type literals
   in CEL, builtin `params.linkTypes`. A methodology is published only on published domains.
4. **Domain publication is checked against its users.** A new domain version is refused when a published methodology
   would break on it (a type or link type it references disappears). The version in force of a domain in use cannot be archived.
5. The version in force is the latest published one; publishing it applies to the changes checked afterwards
   (ADR 0012 §2). Nothing is projected.

## Consequences

- The IDE has a Domain explorer and editor for every namespace (the frozen ones read-only); a methodology picks
  its target namespace instead of a domain.
- A domain holds algorithms (ADR 0018); a methodology holds none.
- Moving the data of a domain to another namespace is a data migration, not a rename: the namespace is part of every
  type reference.
