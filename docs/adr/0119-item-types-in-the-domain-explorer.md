# ADR 0119 — Item types in the domain explorer

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0036 (risks and actions), ADR 0065 (item kinds
registry), ADR 0110 (assessments)

## Context

A change holds facts next to its impacts: change items, each of a kind (`artifact`, `risk`, `assessment`…). The kinds
are code: the graph knows the core ones (`ChangeItem.Validate`), a use case registers its own
(`domain.RegisterItemKind`, ADR 0065). Nothing showed them: a person designing a methodology saw node types, link
types, enums and lifecycles in the domain explorer, but had no place to read which kinds of item a change may hold,
what they are, and what writing one asks. The word "kind" also stood apart from the "types" of the explorer, and an item's `type` named something else (what an artifact is).

## Decision

1. **The catalogue.** `domain.ItemTypes()` lists the item types the process accepts: the core types (described in
   `contracts/domain/itemtypes.go`) and the registered ones, with their origin (`core`, else the use case: `risk`, `assess`,
   `verify`), a description (`domain.DescribeItemType`, called by the use case next to `RegisterItemType`) and the
   permission they ask of their writer (`RequireItemPermission`). RPC `GraphService.ListItemTypes` serves it to any
   identified caller (static metadata of the process).
2. **The explorer.** The domain explorer ends with an *Item types* group (filtered with the domains); an entry opens a
   read-only `itemtype` tab: name, origin, description, where the change tab shows its items, the permission it asks.
   Item types are not domain data: they change with the platform code, not through a domain version.
3. **One name everywhere.** What was the item's `kind` is its **type**, in the code, the proto, the JSON, CEL, the
   DSL and the web (`ChangeItem.Type`, `domain.ItemType`, `domain.TypeArtifact`, `risk.TypeRisk`, `RegisterItemType`;
   proto `ChangeItem.type`). What was its `type`, what the item is about within its type (what an artifact is, the
   name of a signal), is its **target** (`ChangeItem.Target`, `target` in JSON and proto). The process a signal is
   addressed to, formerly `target`, is its **recipient** (`ChangeItem.Recipient`, `recipient`; the `signal` tool of
   `goap-change` takes `target` and `recipient`, its `note` tool `target`). An item a model or a script writes is
   `{"type":"artifact","target":"design","data":{...}}`, a change impact operation `{"type":"changeImpact",...}`;
   in CEL, `artifacts.exists(a, a.target == "design")` and `event.items.exists(i, i.type == "risk")`. The database
   starts from zero (no stored item to migrate) and the shipped methodologies, evals and prompts are rewritten.

## Consequences

- A use case registering an item type describes it, or it shows with no description.
- A methodology written for the former names (`"kind"` in an item, `a.type` on an artifact in CEL or a script) must
  be rewritten: `kind` → `type`, the item's `type` → `target`.
