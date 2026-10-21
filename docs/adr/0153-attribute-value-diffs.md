# ADR 0153 — Diffs of attribute values

**Status**: accepted; implemented · **Date**: 2026-10

## Context

Many places show what changed in a value: the diff of a change impact, the comparison of two options, the history of a
node, the comparison of two baselines, the resolution of a merge conflict, the node tab within a working change. Each
wrote `old → new` itself, a long value cut to 80 characters with a *more* button. A long text, a JSON value or the
source text of a code attribute (ADR 0152) changed by one line read as two walls of text side by side, and nothing said
which line changed.

## Decision

1. **An attribute names its diff** (`def.Attribute.Diff`, YAML / JSON `diff`, proto `registry.v1.Attribute.diff`, the
   *Diff* select of the attribute editor; the domain refuses an unknown one): `value` (the two values whole, the old one
   struck out), `words` (the words added and removed marked within the text), `lines` (line by line), `json` (indented
   JSON, keys sorted, line by line) or `code` (line by line, highlighted in the language the value names; the front
   matter compared apart, a change of the language shown as `language: c → go`). Empty: the usual one of the type,
   `def.Attribute.DefaultDiff` (twin `defaultDiff` in `web/src/lib/valueDiff.ts`): `code` for a code attribute or a code
   widget, `json` for json, `value` for number, boolean, date and enum, `lines` for a text area, `words` otherwise.
2. **One component shows every diff of values**, `ValueDiff.svelte` (pure logic in `web/src/lib/valueDiff.ts`: a Myers
   edit script, word and line diffs, folds, side-by-side rows). Given the two values and the attribute (or the node
   type and the property, read in the type catalogue), it shows a short change on one line, and a longer one as a diff
   of its lines, unified or side by side (one choice for the page, remembered), line numbers on both sides, the edited
   lines paired with their words marked (not for code, highlighted instead: `highlightLines` of `codemirror.ts`, the
   tags of the editor's style), the unchanged lines more than three away from a change folded and opened on a click.
   An absent side is a value added or removed. It replaces the hand-written diffs of `ImpactDiffDialog`,
   `CompareDialog`, `NodeHistory`, `BaselineCompare`, `MergeResolver` (each side against the common base) and the
   *diff* of a changed property on the node tab. A new place showing a change of a value uses it.

## Consequences

- A domain chooses how its long texts read in a review: a description diffed by words, a specification by lines.
- The server never computes a diff of values: the attribute only names how the web shows it.
