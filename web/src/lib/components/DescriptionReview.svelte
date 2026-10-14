<script lang="ts">
  // The rating of the description of a change and its thread (ADR 0110): a reactive step of the methodology rates the
  // title and the intent each time they change or the person answers it here. The field it rates is rendered inside
  // (`children`) and a bubble floats by it while the rating waits to be processed (`pending`: not accepted yet),
  // showing the latest rating, its findings and the wording it suggests (Apply edits the description, which the step
  // rates again), and the thread; an answer is a comment item of the change, which wakes the same run of the rater.
  // Once accepted, or hidden by the person until the rating or its thread moves, it folds into the badge.
  import type { Snippet } from 'svelte';
  import { graph, errorMessage, type Change } from '../api';
  import { MAX_SCORE, assessmentOf, bubbleKey, commentItem, pending, raterRun, reviewState, threadOf } from '../descriptionReview';
  import { processes } from '../stores/live.svelte';
  import { openTab } from '../shell/tabs.svelte';

  let {
    change,
    closed = false,
    onchange,
    onapply,
    children,
  }: { change: Change; closed?: boolean; onchange?: () => void; onapply?: (intent: string) => void; children?: Snippet } = $props();

  const rating = $derived(assessmentOf(change));
  const thread = $derived(threadOf(change.items));
  const st = $derived(reviewState(rating));
  const run = $derived(rating ? raterRun(processes.values(), change, rating.item) : undefined);
  const working = $derived(run?.status === 'running');
  const key = $derived(bubbleKey(rating, thread));
  // open: asked by the person; hidden: the key of what the person closed while it was pending
  let open = $state(false);
  let hidden = $state('');
  const shown = $derived(open || (pending(st, working) && hidden !== key));

  function close() {
    open = false;
    hidden = key;
  }
  const toggle = () => (shown ? close() : (open = true));
  let answer = $state('');
  let busy = $state(false);
  let error = $state('');

  const LABEL: Record<typeof st, string> = {
    none: 'not rated yet',
    stale: 'the description changed since its rating',
    awaiting: 'your remark awaits an answer',
    accepted: 'accepted',
    refused: 'to be improved',
  };

  async function send() {
    const text = answer.trim();
    if (!text || !change.id) return;
    busy = true;
    error = '';
    try {
      await graph.addItems(change.id, [commentItem(text)]);
      answer = '';
      onchange?.();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<div class="anchor">
  <div class="field">{@render children?.()}</div>
  {#if rating || thread.length > 0}
    <button
      type="button"
      class="bubble {st}"
      aria-expanded={shown}
      title="Rating of the description: {LABEL[st]}"
      onclick={toggle}
    >
      <span aria-hidden="true">💬</span>
      {#if rating}<strong>{rating.score}/{MAX_SCORE}</strong>{/if}
      <span>{LABEL[st]}{working ? ' · rating…' : ''}</span>
    </button>
    {#if shown}
      <div
        class="panel {st}"
        role="dialog"
        aria-label="Rating of the description"
        tabindex="-1"
        onkeydown={(e) => e.key === 'Escape' && (e.stopPropagation(), close())}
      >
        <header>
          {#if rating}
            <strong>{rating.score}/{MAX_SCORE}</strong>
            {#if rating.verdict}<span class="verdict">{rating.verdict}</span>{/if}
          {:else}
            <strong>Rating</strong>
          {/if}
          <span class="state">{LABEL[st]}{working ? ' · rating…' : ''}</span>
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
              <span class="hint">Suggested description</span>
              <blockquote>{rating.suggestion}</blockquote>
              {#if !closed && onapply && rating.suggestion.trim() !== (change.intent ?? '').trim()}
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
  .bubble.accepted {
    border-color: var(--ok);
    background: var(--ok-soft);
  }
  .bubble.refused,
  .bubble.stale,
  .bubble.awaiting {
    border-color: var(--warn);
    background: var(--warn-soft);
  }
  /* the bubble floats under its badge, over what follows the field, its tail pointing at the badge */
  .panel {
    position: absolute;
    top: calc(100% + 8px);
    right: 0;
    z-index: 30;
    width: min(34rem, 100%);
    max-height: 60vh;
    overflow: auto;
    padding: 0.6rem 0.8rem;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 0.6rem;
    box-shadow: var(--shadow-pop);
  }
  .panel.refused,
  .panel.stale,
  .panel.awaiting {
    border-color: var(--warn);
  }
  .panel.accepted {
    border-color: var(--ok);
  }
  .panel::before {
    content: '';
    position: absolute;
    top: -7px;
    right: 2.2rem;
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
