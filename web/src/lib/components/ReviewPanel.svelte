<script lang="ts">
  // The reviews of a change (ADR 0080): a reviewer builds a review up (a global comment, one entry per change impact with
  // its own comment and outcome) and submits it, which reviews every impact at once, all or none. Open reviews come
  // first; an open review is changed by its author (or an administrator); submitted and discarded ones are read-only.
  // The per-row Accept / Reject of the impacts stay as a shortcut for a single impact. Nothing is read again after a
  // write: the answer is put over what the change shows until the platform stream brings the new version.
  import { graph, errorMessage, formatDate, shortId, type Change, type ChangeImpact, type ReviewEdit, type ReviewRecord } from '../api';
  import { MAIN_SCOPE } from '../changeScope';
  import { awaitingImpacts, canSubmit, foldReviews, mayEdit, overlay, reviewOrder, submitProblems, tally, OUTCOMES } from '../reviews';
  import { can, me } from '../stores/session.svelte';
  import { confirmDialog } from '../shell/confirmState.svelte';
  import StatusBadge from './StatusBadge.svelte';

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
  let picked = $state<Record<string, string>>({});

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

  const open = () => run('open', () => graph.reviewOpen(change.id ?? '', '', scope || MAIN_SCOPE));
  const update = (r: ReviewRecord, edit: ReviewEdit) => run(`${r.key}:update`, () => graph.reviewUpdate(change.id ?? '', r.key, edit));
  const setComment = (r: ReviewRecord, comment: string) => comment.trim() !== (r.comment ?? '') && update(r, { comment });
  const setEntry = (r: ReviewRecord, id: string, patch: { comment?: string; outcome?: string }) => update(r, { entries: [{ changeImpactId: id, ...patch }] });
  const remove = (r: ReviewRecord, id: string) => update(r, { remove: [id] });
  async function add(r: ReviewRecord, ids: string[]) {
    if (ids.length && (await update(r, { add: ids }))) picked[r.key] = '';
  }
  const submit = (r: ReviewRecord) => run(`${r.key}:submit`, () => graph.reviewSubmit(change.id ?? '', r.key));
  async function discard(r: ReviewRecord) {
    if (await confirmDialog({ title: 'Discard this review?', message: `${r.key} is never submitted: it stays in the log, final. Its impacts await review again.`, confirmLabel: 'Discard', danger: true })) {
      await run(`${r.key}:discard`, () => graph.reviewDiscard(change.id ?? '', r.key));
    }
  }

  /** the impacts a review may still take: those awaiting review, in no other open review, not yet in it */
  const addable = (r: ReviewRecord) => {
    const taken = new Set((r.entries ?? []).map((e) => e.changeImpactId));
    return awaitingImpacts(nodes, all, scope, r.key).filter((n) => !taken.has(n.id ?? ''));
  };
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
    <article class="review" class:final={!isOpen} aria-label="Review {r.key}">
      <header>
        <span class="mono key">{r.key}</span>
        <StatusBadge status={r.status} />
        <span class="hint">by {r.by || '—'}{r.submittedAt ? ` · submitted ${formatDate(r.submittedAt)}` : ''}</span>
        <span class="hint tally">{r.entries?.length ?? 0} impact(s){isOpen ? '' : ` · ${t.accept} accepted · ${t.reject} rejected`}</span>
      </header>

      {#if mine}
        <label class="field">
          <span>Review comment</span>
          <textarea rows="2" value={r.comment ?? ''} placeholder="Global comment, kept on the review of every impact" onchange={(e) => setComment(r, (e.currentTarget as HTMLTextAreaElement).value)}></textarea>
        </label>
      {:else if r.comment}
        <p class="global">{r.comment}</p>
      {/if}

      {#if r.entries?.length}
        <table class="reg">
          <thead>
            <tr><th>Impact</th><th>Comment</th><th>Outcome</th>{#if !isOpen}<th>Impact now</th>{/if}{#if mine}<th></th>{/if}</tr>
          </thead>
          <tbody>
            {#each r.entries as e (e.changeImpactId)}
              {@const n = byId.get(e.changeImpactId)}
              <tr>
                <td>
                  <button type="button" class="link mono" title="Open the impact" onclick={() => onopenimpact?.(e.changeImpactId)}>{label(e.changeImpactId)}</button>
                  {#if n?.type}<div class="hint">{n.type}</div>{/if}
                </td>
                <td>
                  {#if mine}
                    <input type="text" value={e.comment ?? ''} placeholder="Comment on this impact" aria-label="Comment on {label(e.changeImpactId)}" onchange={(ev) => setEntry(r, e.changeImpactId, { comment: (ev.currentTarget as HTMLInputElement).value })} />
                  {:else}
                    {e.comment || '—'}
                  {/if}
                </td>
                <td class="outcome">
                  {#if mine}
                    {#each OUTCOMES as o (o.id)}
                      <label class="radio {o.id}">
                        <input type="radio" name="outcome-{r.key}-{e.changeImpactId}" value={o.id} checked={e.outcome === o.id} disabled={!!busy} onchange={() => setEntry(r, e.changeImpactId, { outcome: o.id })} />
                        {o.label}
                      </label>
                    {/each}
                  {:else}
                    <span class="verdict {e.outcome}">{e.outcome === 'accept' ? 'accepted' : e.outcome === 'reject' ? 'rejected' : '—'}</span>
                  {/if}
                </td>
                {#if !isOpen}<td><StatusBadge status={outcomeOf(r, e.changeImpactId)} /></td>{/if}
                {#if mine}
                  <td><button type="button" class="small" disabled={!!busy} aria-label="Remove {label(e.changeImpactId)} from the review" onclick={() => remove(r, e.changeImpactId)}>Remove</button></td>
                {/if}
              </tr>
            {/each}
          </tbody>
        </table>
      {:else if isOpen}
        <p class="empty">No impact in this review: pick the impacts that await review.</p>
      {/if}

      {#if mine}
        {@const free = addable(r)}
        <div class="row add">
          <select bind:value={picked[r.key]} aria-label="Impacts awaiting review" disabled={!free.length}>
            <option value="">{free.length ? '— add an impact awaiting review —' : 'no impact awaits review'}</option>
            {#each free as n (n.id)}<option value={n.id}>{n.key}{n.type ? ` (${n.type})` : ''}</option>{/each}
          </select>
          <button type="button" class="small" disabled={!!busy || !picked[r.key]} onclick={() => add(r, [picked[r.key]])}>Add</button>
          <button type="button" class="small" disabled={!!busy || !free.length} onclick={() => add(r, free.map((n) => n.id ?? ''))}>Add all ({free.length})</button>
        </div>
        <div class="row actions">
          <button type="button" class="primary" disabled={!!busy || !canSubmit(r)} title={submitProblems(r).join('\n')} onclick={() => submit(r)}>Submit review</button>
          <button type="button" disabled={!!busy} onclick={() => discard(r)}>Discard</button>
          {#if !canSubmit(r) && r.entries?.length}<span class="hint">{submitProblems(r).length} thing(s) to settle before it can be submitted</span>{/if}
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
  .add,
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-top: 8px;
    align-items: center;
  }
  .add select {
    width: auto;
    flex: 1 1 220px;
  }
  .add button,
  .actions button {
    flex: none;
  }
</style>
