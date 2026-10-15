# ADR 0119 — Item types in the domain explorer

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0036 (risks and actions), ADR 0065 (item kinds
registry), ADR 0110 (assessments)

## Context

A change holds facts next to its impacts: change items, each of a kind (`artifact`, `risk`, `assessment`…). The kinds
are code: the graph knows the core ones (`ChangeItem.Validate`), a use case registers its own
(`domain.RegisterItemKind`, ADR 0065). Nothing showed them: a person designing a methodology saw node types, link
types, enums and lifecycles in the domain explorer, but had no place to read which kinds of item a change may hold,
what they are, and what writing one asks. The word "kind" also stood apart from the "types" of the explorer.

## Decision

1. **The catalogue.** `domain.ItemKinds()` lists the kinds the process accepts: the core kinds (described in
   `pkg/domain/itemkinds.go`) and the registered ones, with their origin (`core`, else the use case: `risk`, `assess`,
   `verify`), a description (`domain.DescribeItemKind`, called by the use case next to `RegisterItemKind`) and the
   permission they ask of their writer (`RequireItemPermission`). RPC `GraphService.ListItemKinds` serves it to any
   identified caller (static metadata of the process).
2. **The explorer.** The domain explorer ends with an *Item types* group (filtered with the domains); an entry opens a
   read-only `itemtype` tab: name, origin, description, where the change tab shows its items, the permission it asks.
   Item types are not domain data: they change with the platform code, not through a domain version.
3. **The name.** People read **item type**, the word the explorer uses for node and link types. The code, the proto
   and the YAML keep `kind` (`ChangeItem.Kind`, `ItemKind`, `kind:` of an item): `ChangeItem.Type` already holds the
   sub-type of an item (the type of an artifact, the name of a signal), so renaming the field would merge two notions
   and break every stored item and methodology for a word.

## Consequences

- A use case registering a kind describes it, or it shows with no description.
- In the web, "type" of an item still names its sub-type where an item is shown (an artifact's type); the explorer is
  the only place naming the kind, as its item type.
