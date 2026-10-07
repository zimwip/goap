# ADR 0086 — The contextual helper

**Status**: accepted, implemented (model gateway, web) · **Date**: 2026-10 · Builds on ADR 0021 (model configuration as
graph data) and the catalog policy of the gateway (roles, quotas, token usage).

## Context

A person filling a form (the properties of a node, the fields of a change) sometimes wants a suggestion for a value. The
assistant answers plain-language requests by running a change; that is too heavy, and too durable, for "what would a good
title be here". The helper is the light counterpart: hidden, ephemeral, tied to the form in front of the user.

## Decision

- **Who, why, how, what**: the helper answers none of the questions of the platform's mental model; it is a user
  interface aid over the model gateway (*with what*). It opens no change, runs no agent, writes nothing to the graph,
  the change log or the journal, and keeps nothing: closing it discards the discussion. A value it proposes reaches the
  graph only if the person accepts it into the form and then goes through the form's own save, as any edit.
- **One stateless RPC**, `ModelService.Suggest` (`internal/modelgw/suggest.go`). Request: the context (the tab as
  `{kind, params}`, the subject as an opaque string, the text selected on the page, the fields of the form with id,
  label, type, enum values, description, current value as JSON text and `readOnly`), the small discussion (`user` /
  `assistant` turns, resent each time) and an optional instruction. Response: a short message, the proposals
  `{fieldId, value (JSON text), rationale}` and the token usage.
- **The model is the `helper` alias**, called through the gateway as the calling principal (`Service.Complete`): the
  availability, the role allow-list and the quota of its model apply and its tokens are counted, as for any `model:use`.
  When the caller has no `helper` alias (not configured, disabled, or its model reserved to other roles) the call is
  refused with `FailedPrecondition`. The alias itself, and its protection, belong to the model configuration, not to this
  ADR.
- **The prompt asks for JSON only and for the given field ids only**; the context travels as data in the system prompt.
  The server **parses defensively** (fences and prose around the object tolerated, an answer that is no JSON becomes the
  message with no proposal) and **drops** a proposal for an unknown id, a read-only field, a second proposal of a field, a
  value larger than `MaxProposalBytes`, or a value that does not fit the type of its field (string, number, boolean, date
  `YYYY-MM-DD`, enum member, array; `json` and untyped take any JSON).
- **Limits** (`ErrInvalid`, `InvalidArgument`, beyond): `MaxSuggestFields` 60, `MaxSuggestMessages` 12,
  `MaxSuggestContextBytes` 32 KiB, `MaxSuggestMessageBytes` 4 KiB (a message and the instruction); field ids are set and
  unique.
- **Web** (`web/src/lib/helper/`): forms register their fields with the Svelte action `use:assistField={{id, label,
  type, enum, get, set, readOnly}}` (`fields.svelte.ts`: element, metadata and setter, per tab, removed on destroy);
  `context.ts` collects the active tab, its subject, the selection and the registered fields within the same limits;
  `helper.svelte.ts` holds the state in memory only (open, messages, proposals, loading, error; never `localStorage`);
  `HelperBubble.svelte` is the popup anchored to the field of the proposal (position recomputed on scroll and resize,
  arrow on the field, a floating panel when the field is not visible or no side has room, `placement.ts`) with Accept
  (calls the field's setter, then the next proposal or closes), Reject (closes and discards) and a comment box (resends
  the context and the discussion, replaces the proposals). It is a non-modal dialog, labelled, with Escape, a focus
  trap and focus restored on close. Opened by the palette command "Ask the helper about this form" or `Ctrl+.`, offered
  only when the `helper` alias is available and the active tab shows a field it may fill; it closes when the tab changes.
  Node forms (`NodePropertyForm.svelte`) register their attributes; a setter writes the form's draft, so the person still
  reviews and saves.

## Not done

Other forms (change creation, definition editors) register no field yet; `helperEnabled` of the web reads the available
aliases of the caller.
