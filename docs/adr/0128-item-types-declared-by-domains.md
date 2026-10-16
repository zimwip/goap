# ADR 0128 — Item types declared by domains

**Status**: accepted, implemented · **Date**: 2026-10 · Supersedes decisions 1 and 2 of ADR 0119 (the catalogue of the
process, the read-only group of the explorer) · Builds on ADR 0012 (domains), ADR 0055 (attributes), ADR 0065 (item
type registry)

## Context

ADR 0119 listed the item types a change may hold in a group of their own at the end of the domain explorer: they were
code (the core types of `contracts/domain`, the registrations of `contracts/risk`, `contracts/assess`, `contracts/verify`), not domain data.
A person reading the explorer expects a type to belong to a domain, next to its node types, link types, enums and
lifecycles; and a domain had no way to say which facts a change on it holds (a test result, a measure) without a
change of the platform code.

## Decision

1. **A domain declares its item types.** `itemTypes: [{name, description, attributes}]` on a domain
   (`def.ItemType`, `def.Schema.ItemTypes`; proto `registry.v1.Domain.item_types`, `ItemType`; `DomainSummary.item_type_count`).
   A name is a lower camel or snake case identifier (`def.ItemTypeNameRE`), unique in its domain (`checkItemTypes`)
   and across the domains in force (`typecat.New` refuses a second declaration, so the domain service refuses to validate or
   publish a domain whose item type another domain declares, at path `itemTypes`). An item's `type` stays a bare name:
   an item belongs to a change, not to a namespace. The attributes (ADR 0055) describe the keys of the data of its items.
2. **The built-in ones are in the methodology meta-domain** (1.12.0): the change mechanism (`artifact`, `decision`,
   `decision_point`, `flow`, `review`, `signal`, `transition`) and the platform use cases (`risk`, `action`,
   `derogation`, `assessment`, `comment`, `verification`): what the actions of a methodology write on a change and its
   conditions read. `typecat.TestCodeItemTypesAreDeclared` checks every type the code knows (`domain.CoreItemTypes`,
   `domain.RegisteredItemTypes`) is declared by a built-in domain.
3. **The code gives a meaning, the domain the existence.** `domain.RegisterItemType` (a validator) and
   `RequireItemPermission` stay with the use cases; `DescribeItemType`, the origin and `domain.ItemTypes()` are gone.
   `ChangeItem.Validate` checks the structure and runs the registered validator, and no longer refuses an unknown type:
   the graph does (`Graph.checkItem`, at every item it stores: `AddItems`, the items of a new flow, the review records,
   the decision and transition facts, and `ValidateBoard`), with its catalogue (untyped graph: the built-in domains):
   a type no domain in force declares is `ErrInvalid`, and the keys of the data a type declares as attributes are
   checked for their type and enum; the other keys stay free (an item's data is open).
4. **The catalogue.** `typecat.Catalog.ItemType` / `ItemTypes` / `ItemAttributeChecks` (`graph.TypeCatalog`),
   `Graph.ItemTypes`; RPC `GraphService.ListItemTypes` answers the types in force with their domain and version and
   what this process adds (`permission`, `subject_field`, `code`: a core type or a registered validator).
5. **The explorer.** Each domain version has an *Item types* group (add, remove, open), and the `itemtype` tab is a
   part of the domain draft like an enum (`ItemTypeTab.svelte`, params `name`, `version`, `uid`, `it`; form
   `ItemTypeForm` of `web/src/lib/domainForm.ts`): name, description, the attributes of the data (`AttributeEditor`),
   and, read from `ListItemTypes` when this version is the one in force, what the code adds and where the change tab
   shows the items. The group at the end of the explorer is gone.

## Consequences

- A domain can give its changes new facts without a line of code: published, its item types are accepted by the graph,
  their data checked against their attributes; they show in the raw list of the Artifacts pane.
- A use case registering a validator must have its type declared by a built-in domain (the test fails otherwise).
- Removing an item type from a domain version stops new items of it; the stored ones stay readable.
- `registry.v1.Domain` now carries the enums of the domain (`DomainToPB` dropped them: an edited domain lost them on
  its way to the web).
