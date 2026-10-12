# ADR 0003 — Version-to-version links, outgoing links carried by the source

**Status**: accepted · **Date**: 2026-09

## Context
Links connect exact versions. A baseline must remain immutable, and a change to a
node must signal the elements that depend on it.

## Decision
- A link belongs to a baseline if both its endpoints are in it (at the link's versions).
- **Outgoing links are part of the source node's version**: adding or removing an outgoing link
  of an existing node creates a new version of that node.
- When a node gets a new version (written from its draft on the change's branch when the change is committed, then
  merged into the target branch, ADR 0079, ADR 0102), it **carries its outgoing links forward**;
  **incoming** links from unmodified nodes stay on the old version and become
  **suspect** (`SuspectLinks`): this is the model's native impact signal.

## Consequences
- Baselines are immutable without explicitly storing the full set of links.
- A property change on a requirement makes its tests and components suspect, to be reviewed in a
  later change (or the same one, by including them).
