<script lang="ts">
  // The assessment of a subject of a change and its thread (ADR 0110): an action of the methodology rates the
  // description (title and intent), the title, the intent, an impact or an item, and the person answers it here. The
  // field it rates is rendered inside (`children`) and its badge sits next to it; a bubble floats by the badge while
  // the assessment waits to be processed (`pending`: not accepted yet, or `processed` as the caller judges it, e.g. an
  // impact decided by its review), showing the latest rating, its findings, the wording it suggests (Apply, when the
  // caller can apply it) and the thread; an answer is a comment item of the change, which wakes the run of the rater.
  // Once processed, or hidden by the person until the rating or its thread moves, it folds into the badge. A compact
  // bubble (rows of a table) never opens by itself: its badge is coloured while pending, the bubble opens on a click.
  // The bubble is positioned on the viewport, so a scrolling table does not clip it.
  import type { Snippet } from 'svelte';
  import { graph, errorMessage, type Change } from '../api';
  import { DESCRIPTION, MAX_SCORE, assessmentOf, bubbleKey, commentItem, pending, raterRun, reviewState, threadOf, type Subject } from '../descriptionReview';
  import { processes } from '../stores/live.svelte';
  import { openTab } from '../shell/tabs.svelte';

  let {
    change,
    subject = DESCRIPTION,
    label = 'the description',
    compact = false,
    processed,
    closed = false,
    onchange,
    onapply,
    value,
    children,
  }: {
    change: Change;
    /** the subject key, or the keys naming it (an item by its record key and its id) */
    subject?: Subject;
    /** what the subject is, in words */
    label?: string;
    /** a row of a table: the bubble opens on a click only */
    compact?: boolean;
    /** whether the assessment is processed, when the caller knows better than its score (an impact decided) */
    processed?: boolean;
    closed?: boolean;
    onchange?: () => void;
    /** applies the suggested wording to the subject; no Apply button without it */
    onapply?: (text: string) => void;
    /** the text of the subject now: no Apply when the suggestion says the same */
    value?: string;
    children?: Snippet;
  } = $props();

  const rating = $derived(assessmentOf(change, subject));
  const thread = $derived(threadOf(change.items, subject));
  const st = $derived(reviewState(rating));
  const due = $derived(processed !== undefined ? !processed && !!rating : st !== 'accepted' && st !== 'none');
  const run = $derived(rating ? raterRun(processes.values(), change, rating.item) : undefined);
  const working = $derived(run?.status === 'running');
  const key = $derived(bubbleKey(rating, thread));
  // open: asked by the person; hidden: the key of what the person closed while it was pending
  let open = $state(false);
  let hidden = $state('');
  const shown = $derived(open || (!compact && pending(st, working, processed) && hidden !== key));

  // the bubble floats under the badge, on the viewport (a table or a pane scrolling under it does not clip it)
  let badge = $state<HTMLElement>();
  let pos = $state({ top: 0, left: 0, tail: 24 });
  const WIDTH = 544;
  function place() {
    if (!badge) return;
    const r = badge.getBoundingClientRect();
    const width = Math.min(WIDTH, window.innerWidth - 32);
    const left = Math.max(16, Math.min(r.right - width, window.innerWidth - width - 16));
    pos = { top: r.bottom + 8, left, tail: Math.max(12, Math.min(width - 24, r.left + r.width / 2 - left - 6)) };
  }
  $effect(() => {
    if (!shown) return;
    place();
    window.addEventListener('scroll', place, true);
    window.addEventListener('resize', place);
    return () => {
      window.removeEventListener('scroll', place, true);
      window.removeEventListener('resize', place);
    };
  });

  function close() {
    open = false;
    hidden = key;
  }
  const toggle = () => (shown ? close() : (open = true));
  let answer = $state('');
  let busy = $state(false);
  let error = $state('');

  const LABELS: Record<typeof st, string> = {
    none: 'not rated yet',
    stale: 'changed since its rating',
    awaiting: 'your remark awaits an answer',
    accepted: 'accepted',
    refused: 'to be improved',
  };
  const stateLabel = $derived(processed === undefined || !rating ? LABELS[st] : processed ? 'processed' : 'not processed yet');

  async function send() {
    const text = answer.trim();
    if (!text || !change.id) return;
    busy = true;
    error = '';
    try {
      await graph.addItems(change.id, [commentItem(text, subject)]);
      answer = '';
      onchange?.();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<div class="anchor" class:compact>
  {#if children}<div class="field">{@render children()}</div>{/if}
  {#if rating || thread.length > 0}
    <button
      type="button"
      class="bubble"
      class:due
      class:done={!!rating && !due}
      bind:this={badge}
      aria-expanded={shown}
      title="Assessment of {label}: {rating?.verdict || stateLabel}"
      onclick={toggle}
    >
      <span aria-hidden="true">💬</span>
      {#if rating}<strong>{rating.score}/{MAX_SCORE}</strong>{/if}
      {#if compact}
        {#if rating?.verdict}<span>{rating.verdict}</span>{/if}{#if working}<span>…</span>{/if}
      {:else}
        <span>{stateLabel}{working ? ' · rating…' : ''}</span>
      {/if}
    </button>
    {#if shown}
      <div
        class="panel"
        class:due
        class:done={!!rating && !due}
        style="top: {pos.top}px; left: {pos.left}px; --tail: {pos.tail}px"
        role="dialog"
        aria-label="Assessment of {label}"
        tabindex="-1"
        onkeydown={(e) => e.key === 'Escape' && (e.stopPropagation(), close())}
      >
        <header>
          {#if rating}
            <strong>{rating.score}/{MAX_SCORE}</strong>
            {#if rating.verdict}<span class="verdict">{rating.verdict}</span>{/if}
          {:else}
            <strong>Assessment</strong>
          {/if}
          <span class="state">of {label}</span>
          <span class="state">{stateLabel}{working ? ' · rating…' : ''}</span>
          <span class="spacer"></span>
          {#if run}<button type="button" class="link small" onclick={() => openTab({ kind: 'run', params: { id: run.id ?? '' } })}>Open the run</button>{/if}
          <button type="button" class="link small close" aria-label="Hide the rating" title="Hide until the rating changes" onclick={close}>✕</button>
        </header>
        {#if error}<div class="alert">{error}</div>{/if}
        {#if rating}
          {#if rating.findings.length > 0}
            <ul class="findings">
              {#each rating.findings as f, i (i)}<li>{#if f.severity}<span class="sev {f.severity}">{f.severity}</span>{/if} {f.text}</li>{/each}
            </ul>
          {/if}
          {#if rating.suggestion}
            <div class="suggestion">
              <span class="hint">Suggested wording</span>
              <blockquote>{rating.suggestion}</blockquote>
              {#if !closed && onapply && rating.suggestion.trim() !== (value ?? '').trim()}
                <button type="button" class="small" onclick={() => onapply?.(rating.suggestion)}>Apply</button>
              {/if}
            </div>
          {/if}
        {/if}
        {#if thread.length > 0}
          <ol class="thread">
            {#each thread as m (m.id)}
              <li class={m.kind}>
                <span class="who">{m.kind === 'comment' ? 'You' : `Rater · ${m.score}/${MAX_SCORE}`}</span>
                <span>{m.text}</span>
              </li>
            {/each}
          </ol>
        {/if}
        {#if !closed}
          <form onsubmit={(e) => (e.preventDefault(), send())}>
            <textarea rows="2" placeholder="Answer the rater, ask it to refine its rating…" bind:value={answer}></textarea>
            <button type="submit" class="primary small" disabled={busy || !answer.trim()}>Send</button>
          </form>
        {/if}
      </div>
    {/if}
  {/if}
</div>

<style>
  .anchor {
    position: relative;
    display: flex;
    gap: 0.6rem;
    align-items: flex-start;
  }
  .field {
    flex: 1;
    min-width: 0;
  }
  .bubble {
    flex: none;
    margin-top: 0.5rem;
    display: inline-flex;
    gap: 0.4rem;
    align-items: center;
    border-radius: 1rem;
    padding: 0.15rem 0.7rem;
    font-size: 0.85rem;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--text);
    cursor: pointer;
  }
  .anchor.compact {
    display: inline-flex;
    vertical-align: middle;
  }
  .compact .bubble {
    margin-top: 0;
    padding: 0 0.45rem;
    font-size: 0.78rem;
  }
  .bubble.done {
    border-color: var(--ok);
    background: var(--ok-soft);
  }
  .bubble.due {
    border-color: var(--warn);
    background: var(--warn-soft);
  }
  /* the bubble floats under its badge (placed on the viewport by place()), its tail pointing at the badge */
  .panel {
    position: fixed;
    z-index: 60;
    width: min(34rem, calc(100vw - 2rem));
    text-align: left;
    font-weight: normal;
    white-space: normal;
    max-height: 60vh;
    overflow: auto;
    padding: 0.6rem 0.8rem;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 0.6rem;
    box-shadow: var(--shadow-pop);
  }
  .panel.due {
    border-color: var(--warn);
  }
  .panel.done {
    border-color: var(--ok);
  }
  .panel::before {
    content: '';
    position: absolute;
    top: -7px;
    left: var(--tail, 24px);
    width: 12px;
    height: 12px;
    background: var(--surface);
    border-left: 1px solid;
    border-top: 1px solid;
    border-color: inherit;
    transform: rotate(45deg);
  }
  header {
    display: flex;
    gap: 0.4rem;
    align-items: baseline;
  }
  .spacer {
    flex: 1;
  }
  .state {
    font-size: 0.8rem;
    color: var(--muted);
  }
  .verdict {
    font-style: italic;
  }
  .findings {
    margin: 0.4rem 0;
    padding-left: 1.2rem;
  }
  .sev {
    font-size: 0.75rem;
    text-transform: uppercase;
    opacity: 0.8;
  }
  .suggestion blockquote {
    margin: 0.2rem 0 0.4rem;
    padding-left: 0.6rem;
    border-left: 3px solid var(--border);
    white-space: pre-wrap;
  }
  .thread {
    list-style: none;
    padding: 0;
    margin: 0.5rem 0;
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
  }
  .thread li {
    display: flex;
    flex-direction: column;
    padding: 0.3rem 0.5rem;
    border-radius: 0.5rem;
    max-width: 85%;
  }
  .thread li.comment {
    align-self: flex-end;
    background: var(--surface-2);
  }
  .thread li.assessment {
    align-self: flex-start;
    border: 1px solid var(--border);
  }
  .who {
    font-size: 0.75rem;
    opacity: 0.7;
  }
  form {
    display: flex;
    gap: 0.4rem;
    align-items: flex-end;
  }
  textarea {
    flex: 1;
  }
</style>
