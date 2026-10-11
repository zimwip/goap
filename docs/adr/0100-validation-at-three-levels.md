# ADR 0100 — Validation at three levels: attribute, link, node

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0018 (algorithms), 0055 (attributes), 0079 (drafts
and versions at landing).

## Context

A `reasoning` model alias with no target landed and was silently dropped by the reader of the aliases: nothing in the
platform said an alias needs a target, so the rule lived in the code that read the nodes. Meanwhile the domain had two
levels of validation (the property validators of an attribute, the node validators of a node type, ADR 0018), the
validators of a link's attributes were declared but never run, and a rule that spans a link and both its ends had no
place at all.

## Decision

Rules about the data live in the domain that defines it, at three levels, all blocking when a draft is accepted and when
the change lands (`Graph.checkVersion`, ADR 0079):

1. **Attribute**: `property_validator` instances plugged on an attribute of a node type or of a link type (the latter
   now run).
2. **Link**: `link_validator` instances plugged on a link type (`linkTypes[].validators`, `def.LinkType.Validators`).
   Context `LinkValidatorCtx` (`pkg/dsl`): `param`, `link()` (`{type, props}`), `from()`, `to()`, `fail` / `log` /
   `warn`. They run after the validators of the link's attributes.
3. **Node**: `node_validator` instances plugged on a node type, after the attribute validators.

Readers never define these rules: a reader of the graph (the model gateway's alias snapshot, the access snapshot) takes
what it is given and the domain says what is valid. The Settings dialog is only an accelerator that fills the forms; it
holds no rule. The alias rule is the `alias-target` validator of `domains/builtin/platform.yaml`, plugged on the alias
type. The registry carries the new field (`LinkType.validators`), the algorithm editor and the "Try it" run know the
usage, and the link type editor plugs instances.

## Consequences

- A link validator is pure: both ends are given and it reads nothing else; a link whose target cannot be resolved (not
  in the change, not stored) is skipped rather than refused.
- Draft edits never validate; only the accept of a review and the landing do (an incomplete draft stays editable).
- Not done: "the target names an existing model" is a rule across an alias and the model catalogue; it waits for a link
  from the alias to its model, then a link validator.
