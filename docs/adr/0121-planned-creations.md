# ADR 0121 — Planned creations

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0024 (change impacts), ADR 0077 (impact node
operations), ADR 0079 (drafts and versions at landing), ADR 0109 (impact analysis in passes)

## Context

The impact analysis of `sdlc` names, layer by layer, the existing nodes a change concerns (a `modified` impact,
planned until its node is checked out) and propagates them. On a green field nothing exists: every pass found nothing,
and what the change will create appeared only when the specification and the design wrote it. The analysis could not
say "this change needs a new requirement, a new function, a new component", and the person validating it saw an empty
list. A new node had no proposal (ADR 0077): `ProposeImpact` refused the intent `created`, `ImpactNodeCreate` gave the
node its draft at once, with content the analysis does not have yet.

## Decision

1. **A creation may be planned.** `ProposeImpact` takes an impact of intent `created` with a key, a type and a
   rationale, and no draft: a *planned creation* (`ChangeImpact.Planned()`, `planned` / `hasPost` in CEL). It is
   refused when the reference baseline holds the key or the change already acts on it in the flow.
2. **Writing it fulfils it.** `ImpactNodeCreate` of the same key (and type), or `ImpactNodeCheckout` of it, gives the
   planned impact its draft: a `checkedOut` event on the same impact (with the properties and links of the creation;
   a checkout starts bare), so a flow fulfils it as it edits any impact. It lands like any creation (version 1).
3. **It holds the merge.** A planned creation modifies nothing: it is not accepted, and `unsettled` reports it ("planned,
   not created yet: create or withdraw it"). The work that follows writes it or withdraws it.
4. **Actions plan with `op: plan`.** The engine applies `{"op":"plan","type","key","rationale"}` (DSL
   `ctx.impactNodePlan(type, key, rationale)`); a later `write` of the key checks it out (the draft), a link to it
   creates it bare.
5. **`sdlc` 0.8.3.** Each pass of the impact analysis plans what its layer needs that does not exist yet; the
   specification, `allocate_functions` and the design write the planned keys (or withdraw them), `check_design` flags a
   planned element still unwritten; the validation of the analysis accepts an analysis made of planned creations only,
   or of none when the summaries of the passes say why.

## Consequences

- A cancel of a fulfilled planned creation drops the impact like any creation cancelled (it does not go back to
  planned).
- `requirements_traced` (created requirements all written and traced) stays false while a planned requirement is
  unwritten: the gap shows instead of being skipped.
