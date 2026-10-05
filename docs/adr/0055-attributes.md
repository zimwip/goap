# ADR 0055 — Attributes, enums and node validators in the domain

Status: accepted

## Context

A node type declared its properties as bare names. Everything the interface needs to display and edit a
node (label, type, widget, enum values, sections, which property is the name) lived nowhere, and the
validators of a type named their property by string (`validators: [{property, instance}]`). The registry
defines the whole model of a domain: what a node carries, how it is checked and how it moves belong in it.

plm-core, the reference, defines an *attribute* (code, label, data type, widget, enum, default, section,
order, tooltip, name flag) with its own validators, and enumerations next to the node types.

## Decision

1. **Attribute** (`def.Attribute`): `name` (the code), `label`, `description`, `type` (`string`,
   `number`, `boolean`, `date`, `enum`, `json`; empty: untyped), `widget` (`text`, `textarea`, `dropdown`,
   `checkbox`, `date`; empty: the usual one of the type), `enum`, `default`, `section`, `order`, `tooltip`,
   `asName`, and `validators`: the `property_validator` instances plugged on it, in call order. YAML accepts
   a bare name as shorthand. `NodeType.Attributes` and `LinkType.Attributes` replace `properties`.
2. **Enum** (`Schema.Enums`): a named, ordered list of `{value, label}` of the domain; an enum attribute
   names one.
3. **Node validator**: a new usage `node_validator` (`dsl.NodeValidatorCtx`: `node`, `param`, `fail`)
   plugged on a node type (`NodeType.Validators`, instance names): rules across attributes. The old
   `{property, instance}` list is gone.
4. **Resolution** (`typecat`): a type's attributes are its ancestors' first; a subtype redefining one by
   name replaces it in place. Validators accumulate in call order: supertype first, and for each type its
   attributes' validators then its node validators. Enum values are resolved from the declaring domain.
5. **Checks**: the domain refuses unknown or duplicate attributes, unknown types or widgets, an enum attribute
   without (or with an unknown) enum, a non-enum attribute naming one, more than one `asName`, and instances
   plugged with the wrong usage. On commit the graph checks the type of each declared value and enum
   membership (`graph.checkAttributeValue`, through `TypeCatalog.AttributeChecks`); an untyped attribute and
   an empty value are accepted (requiring a value is a validator's job); extra properties stay allowed. The
   validators of link type attributes are checked by the domain but not yet run when a link is written.
6. **Registry and web**: the contract carries `Attribute`, `Enum`, `AttributeInfo` (resolved: where it is
   inherited from, enum values); `ListTypes` returns them. In the IDE each node type, link type, lifecycle
   and enum has its own tab; the Domains explorer lists, under each version, node types (attributes with
   their validators, node validators), link types (attributes), enums, lifecycles (states, transitions with
   their guards and actions) and algorithms. The node editor lays a node out by section and order and edits
   each value with its widget; the attribute with `asName` is the display name.

Lifecycles, transitions and link types also carry a `description` (states, node types, enums and algorithms already did), so that the whole model of a domain is documented in the registry. A lifecycle's `restInEditable` (ADR 0048) now travels through the registry contract too.

No migration: the model starts from this shape.

## Consequences

- The type, enum and widget of a value are defined once, in the registry, and the interface follows.
- Typed validation is a floor under the validator algorithms, not a replacement.
- Per-state attribute rules (visible / required / editable per lifecycle state) and role-based attribute
  views, which plm-core has, are not part of this decision.
