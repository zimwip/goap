# ADR 0173 — Counters of a change

**Status**: accepted, implemented · **Date**: 2026-10 · **Builds on**: ADR 0024 (change impacts), ADR 0029 (event-sourced
impacts), ADR 0102 (commit and merge), ADR 0103 (sub-changes), ADR 0172 (deleting and restoring a node)

## Context

A statistic of the changes (how many nodes a change created, modified, deleted; how many links it wrote) had to be
derived from each change's log: fold the impacts, then read every version the change committed and the version it
started from to compare their links. Nothing answered it cheaply, and no store query could sum it over the changes of
a namespace.

The graph knows the answer at the one moment the versions are written: the commit has the version it writes, its
outgoing links, and the version the impact started from with its own (read in one batch already, ADR 0137).

## Decision

1. **The effect of an impact is recorded at commit.** `domain.EffectOf(pre, preLinks, post, postLinks)` says what a
   version does to its node over the version the change started from (the impact's `Pre`; nothing for a creation):
   `kind` `created` | `modified` | `deleted` (the version is a tombstone) | `restored` (the version it started from was
   one, ADR 0172), the properties set, changed or unset, whether its lifecycle state moved, and its outgoing links
   added, removed or changed (a link is its type and its target node, as `DiffShapes` compares them). `requalified`
   marks a creation the graph took as the modification of an existing node (an `impact.requalified` entry of the log,
   ADR 0172). The commit (`prepareChangeImpacts`) computes it for each version it writes and carries it on the
   `committed` event (`ImpactEvent.Effect`), the fold puts it on the impact (`ChangeImpact.Effect`, column
   `change_impact.effect`); an integration into another branch records none and keeps the one folded. The merge of a
   sub-change records, for each node it gives its parent, the effect over the parent's own pre (`recordInParent`: the
   sub-change's effect when both start from the same version, else read again, `effectOver`).
2. **The counters are a fold of the impacts.** `domain.StatsOf(impacts)` sums the main flow's impacts into
   `domain.ChangeStats`: the committed nodes by effect (`created`, `modified`, `deleted`, `restored`, `requalified`
   among them), the properties, lifecycle moves and links they changed, and the impacts not committed yet by review
   (`proposed`, `accepted`, `rejected`). They are **net per node**: an impact counts once, by the effect of its last
   commit over the version the change started from, so a node created then edited by the change is one creation, and
   an impact withdrawn (and reverted) no longer counts.
3. **The header keeps the committed counters.** Each commit and each merge of a sub-change into its parent writes
   `ChangeStats.Committed()` on the change (`Change.Stats`, column `change.stats`, a JSON object in both dialects), so a
   reader of change headers (a list of changes, a SQL query: `stats->>'created'`, `json_extract(stats, '$.created')`)
   has them with no fold. The graph statistics sum them per namespace over the root changes (measure
   `changeCounters`; a sub-change is counted in its parent once merged).
4. **Read.** `Change.stats` (proto `ChangeStats`) answers the fold of the impacts when the change is read with them
   (the pending impacts counted), else the counters of its header; `ChangeImpact.effect` (proto `ImpactEffect`) the
   effect of an impact. The change tab shows them under its facts (`ChangeCounters.svelte`, `web/src/lib/changeStats.ts`),
   the graph statistics tab the namespace's sums.

## Consequences

- A commit reads nothing more: the version an impact started from and its links are the ones the commit reads already.
- The counters of a node committed several times are those of its last commit over the start of the change, not a sum
  of the commits: what the change did, not how it got there.
- A version a three-way merge change writes (`graph.merge`) records no effect; such an impact counts by its intent.
