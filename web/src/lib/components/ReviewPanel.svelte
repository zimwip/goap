<script lang="ts">
  // The reviews of a change (ADR 0080): a reviewer builds a review up (a global comment, one entry per change impact with
  // its own comment and outcome) and submits it, which reviews every impact at once, all or none. Open reviews come
  // first; an open review is changed by its author (or an administrator); submitted ones are read-only; a discarded one leaves the list (the change log keeps it).
  // A new review is prefilled with every impact awaiting a review; the open review is one table of those impacts, a box to
  // include or remove each (a removed row stays, greyed, to be included again), its comment and outcome inline. Nothing is read again after a
  // write: the answer is put over what the change shows until the platform stream brings the new version.
  import { graph, errorMessage, formatDate, shortId, type Change, type ChangeImpact, type ReviewEdit, type ReviewRecord } from '../api';
  import { MAIN_SCOPE } from '../changeScope';
  import { awaitingImpacts, canSubmit, foldReviews, mayEdit, overlay, reviewOrder, reviewRows, staleEntries, submitProblems, tally, OUTCOMES } from '../reviews';
  import { can, me } from '../stores/session.svelte';
  import { confirmDialog } from '../shell/confirmState.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import { assistField } from '../assist/registry.svelte';

  let {
    change,
    nodes,
    scope = MAIN_SCOPE,
    closed = false,
    onopenimpact,
  }: {
    change: Change;
    /** the change impacts the scope sees (with the review of that flow) */
    nodes: ChangeImpact[];
    /** the flow the panel is the view of: 'main' or an option id */
    scope?: string;
    closed?: boolean;
    /** opens the impacts pane on an impact */
    onopenimpact?: (id: string) => void;
  } = $props();

  const flowOf = (f: string | undefined) => (!f || f === MAIN_SCOPE ? '' : f);
  /** the answers of the writes, until the stream brings their version */
  let local = $state<Record<string, ReviewRecord>>({});
  let busy = $state('');
  let error = $state('');

  const all = $derived(overlay(foldReviews(change.items ?? []), local));
  const reviews = $derived(reviewOrder(all.filter((r) => flowOf(r.flow) === flowOf(scope))));
  const openCount = $derived(reviews.filter((r) => r.status === 'open').length);
  const byId = $derived(new Map(nodes.map((n) => [n.id ?? '', n])));
  const label = (id: string) => byId.get(id)?.key || shortId(id);
  const editable = (r: ReviewRecord) => !closed && mayEdit(r, me(), can.administer);

  async function run(what: string, fn: () => Promise<{ review?: ReviewRecord }>): Promise<boolean> {
    busy = what;
    error = '';
    try {
      const r = (await fn()).review;
      if (r) local[r.key] = r;
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      busy = '';
    }
  }

  /** opens a review prefilled with every impact awaiting review (two calls: the review, then its entries) */
  async function open() {
    const ids = awaitingImpacts(nodes, all, scope).map((n) => n.id ?? '');
    let key = '';
    const ok = await run('open', async () => {
      const res = await graph.reviewOpen(change.id ?? '', '', scope || MAIN_SCOPE);
      key = res.review?.key ?? '';
      return res;
    });
    if (ok && key && ids.length) await run(`${key}:update`, () => graph.reviewUpdate(change.id ?? '', key, { add: ids }));
  }
  const update = (r: ReviewRecord, edit: ReviewEdit) => run(`${r.key}:update`, () => graph.reviewUpdate(change.id ?? '', r.key, edit));
  const setComment = (r: ReviewRecord, comment: string) => comment.trim() !== (r.comment ?? '') && update(r, { comment });
  const setEntry = (r: ReviewRecord, id: string, patch: { comment?: string; outcome?: string }) => update(r, { entries: [{ changeImpactId: id, ...patch }] });
  const include = (r: ReviewRecord, id: string, on: boolean) => update(r, on ? { add: [id] } : { remove: [id] });
  const setAll = (r: ReviewRecord, outcome: string) =>
    update(r, { entries: rowsOf(r).filter((x) => x.included).map((x) => ({ changeImpactId: x.id, outcome })) });
  const setEntryComment = (r: ReviewRecord, id: string, comment: string, was: string) => comment.trim() !== was.trim() && setEntry(r, id, { comment });
  /** the entries the impacts no longer await are taken out first, so that the submit does not review them */
  async function submit(r: ReviewRecord) {
    const stale = staleEntries(nodes, r);
    if (stale.length && !(await update(r, { remove: stale }))) return;
    await run(`${r.key}:submit`, () => graph.reviewSubmit(change.id ?? '', r.key));
  }
  async function discard(r: ReviewRecord) {
    if (await confirmDialog({ title: 'Discard this review?', message: `${r.key} is dropped from the list and never submitted; only the audit trail keeps it. Its impacts await review again.`, confirmLabel: 'Discard', danger: true })) {
      await run(`${r.key}:discard`, () => graph.reviewDiscard(change.id ?? '', r.key));
    }
  }

  const rowsOf = (r: ReviewRecord) => reviewRows(nodes, all, r);
  const staleOf = (r: ReviewRecord) => new Set(staleEntries(nodes, r));
  /** what the review of the impact made in this review says (the impact may have been reviewed again since) */
  function outcomeOf(r: ReviewRecord, id: string): string {
    const rs = byId.get(id)?.reviews ?? [];
    for (let i = rs.length - 1; i >= 0; i--) if (rs[i].reviewId === r.key) return rs[i].status ?? '';
    return byId.get(id)?.review ?? '';
  }
</script>

<section class="card">
  <h3>
    Reviews <span class="hint">{openCount} open · {reviews.length} in all</span>
    {#if !closed}
      <button type="button" class="small primary" disabled={!!busy} onclick={open}>New review</button>
    {/if}
  </h3>
  {#if error}<div class="alert">{error}</div>{/if}
  {#if !reviews.length}
    <p class="empty">No review yet. A review gathers impacts, each with its own comment and outcome, and reviews them all when it is submitted.</p>
  {/if}

  {#each reviews as r (r.key)}
    {@const isOpen = r.status === 'open'}
    {@const mine = editable(r)}
    {@const t = tally(r)}
    {@const rows = rowsOf(r)}
    {@const stale = staleOf(r)}
    <article class="review" class:final={!isOpen} aria-label="Review {r.key}">
      <header>
        <span class="mono key">{r.key}</span>
        <StatusBadge status={r.status} />
        <span class="hint">by {r.by || '—'}{r.submittedAt ? ` · submitted ${formatDate(r.submittedAt)}` : ''}</span>
        <span class="hint tally">{(r.entries?.length ?? 0) - staleOf(r).size} impact(s){isOpen ? '' : ` · ${t.accept} accepted · ${t.reject} rejected`}</span>
      </header>

      {#if mine}
        <label class="field">
          <span>Review comment</span>
          <textarea
            rows="2"
            value={r.comment ?? ''}
            placeholder="Global comment, kept on the review of every impact"
            onchange={(e) => setComment(r, (e.currentTarget as HTMLTextAreaElement).value)}
            use:assistField={{ id: `review_global_comment:${r.key}`, label: `Comment of review ${r.key}`, type: 'string', get: () => r.comment ?? '', set: (v) => void setComment(r, String(v)) }}
          ></textarea>
        </label>
      {:else if r.comment}
        <p class="global">{r.comment}</p>
      {/if}

      {#if rows.length}
        <table class="reg">
          <thead>
            <tr>
              {#if mine}<th class="narrow">In</th>{/if}
              <th>Impact</th>
              <th>Comment</th>
              <th>
                Outcome
                {#if mine}
                  <span class="bulk">
                    <button type="button" class="small" disabled={!!busy || !rows.some((x) => x.included)} onclick={() => setAll(r, 'accept')}>Accept all</button>
                    <button type="button" class="small" disabled={!!busy || !rows.some((x) => x.included)} onclick={() => setAll(r, 'reject')}>Reject all</button>
                  </span>
                {/if}
              </th>
              {#if !isOpen}<th>Impact now</th>{/if}
            </tr>
          </thead>
          <tbody>
            {#each rows as x (x.id)}
              {@const n = x.node}
              <tr class:off={isOpen && !x.included}>
                {#if mine}
                  <td class="narrow">
                    <input type="checkbox" checked={x.included} disabled={!!busy} aria-label="Include {label(x.id)} in the review" title={x.included ? 'Remove from the review' : 'Include in the review'} onchange={(ev) => include(r, x.id, (ev.currentTarget as HTMLInputElement).checked)} />
                  </td>
                {/if}
                <td>
                  <button type="button" class="link mono" title="Open the impact" onclick={() => onopenimpact?.(x.id)}>{label(x.id)}</button>
                  <div class="hint">{[n?.type, n?.intent].filter(Boolean).join(' · ')}</div>
                  {#if n?.rationale}<div class="hint" title={n.rationale}>{n.rationale}</div>{/if}
                </td>
                <td>
                  {#if mine}
                    <input type="text" value={x.comment} disabled={!x.included} placeholder="Comment on this impact" aria-label="Comment on {label(x.id)}" onchange={(ev) => setEntryComment(r, x.id, (ev.currentTarget as HTMLInputElement).value, x.comment)}
                      use:assistField={{ id: `review_entry_comment:${r.key}:${x.id}`, label: `Comment on ${label(x.id)}`, type: 'string', readOnly: !x.included, get: () => x.comment, set: (v) => void setEntryComment(r, x.id, String(v), x.comment) }}
                    />
                  {:else}
                    {x.comment || '—'}
                  {/if}
                </td>
                <td class="outcome">
                  {#if mine}
                    {#each OUTCOMES as o (o.id)}
                      <label class="radio {o.id}">
                        <input type="radio" name="outcome-{r.key}-{x.id}" value={o.id} checked={x.outcome === o.id} disabled={!!busy || !x.included} onchange={() => setEntry(r, x.id, { outcome: o.id })} />
                        {o.label}
                      </label>
                    {/each}
                  {:else}
                    <span class="verdict {x.outcome}">{x.outcome === 'accept' ? 'accepted' : x.outcome === 'reject' ? 'rejected' : '—'}</span>
                  {/if}
                </td>
                {#if !isOpen}<td><StatusBadge status={outcomeOf(r, x.id)} /></td>{/if}
              </tr>
            {/each}
          </tbody>
        </table>
      {:else if isOpen}
        <p class="empty">No impact awaits a review.</p>
      {/if}
      {#if mine && stale.size}
        <p class="hint">{stale.size} entr{stale.size > 1 ? 'ies' : 'y'} no longer await{stale.size > 1 ? '' : 's'} a review (reviewed elsewhere): hidden, and left out when the review is submitted.</p>
      {/if}

      {#if mine}
        {@const problems = submitProblems(r, stale)}
        <div class="row actions">
          <button type="button" class="primary" disabled={!!busy || !canSubmit(r, stale)} title={problems.join('\n')} onclick={() => submit(r)}>Submit review</button>
          <button type="button" disabled={!!busy} onclick={() => discard(r)}>Discard</button>
          {#if problems.length}<span class="hint">{problems.length} thing(s) to settle before it can be submitted</span>{/if}
        </div>
      {:else if isOpen}
        <p class="hint">Only {r.by || 'its author'} (or an administrator) changes this review.</p>
      {/if}
    </article>
  {/each}
</section>

<style>
  .review {
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 10px 12px;
    margin-top: 10px;
  }
  .review.final {
    opacity: 0.85;
  }
  header {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    margin-bottom: 6px;
  }
  .key {
    font-weight: 600;
  }
  .tally {
    margin-left: auto;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 2px;
    margin-bottom: 8px;
  }
  .global {
    white-space: pre-wrap;
    margin: 0 0 8px;
  }
  .reg {
    width: 100%;
    border-collapse: collapse;
  }
  .reg th,
  .reg td {
    text-align: left;
    padding: 3px 6px;
    border-bottom: 1px solid var(--border);
    vertical-align: top;
  }
  .reg td input[type='text'] {
    width: 100%;
  }
  .outcome {
    white-space: nowrap;
  }
  .radio {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    margin-right: 10px;
  }
  .radio.reject input {
    accent-color: var(--danger);
  }
  .verdict.accept {
    color: var(--ok);
  }
  .verdict.reject {
    color: var(--danger);
  }
  button.link {
    background: none;
    border: none;
    padding: 0;
    color: var(--accent);
    cursor: pointer;
    text-align: left;
  }
  .reg tr.off td {
    opacity: 0.5;
  }
  .reg tr.off td:first-child {
    opacity: 1;
  }
  .narrow {
    width: 1%;
  }
  .bulk {
    margin-left: 8px;
    font-weight: normal;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-top: 8px;
    align-items: center;
  }
  .actions button {
    flex: none;
  }
</style>
