<script lang="ts">
  // The rating of the description of a change and its thread (ADR 0110): a reactive step of the methodology rates the
  // title and the intent each time they change or the person answers it here. The bubble shows the latest rating,
  // its findings and the wording it suggests (Apply edits the description, which the step rates again), and the
  // thread; an answer is a comment item of the change, which wakes the same run of the rater.
  import { graph, errorMessage, type Change } from '../api';
  import { MAX_SCORE, assessmentOf, commentItem, raterRun, reviewState, threadOf } from '../descriptionReview';
  import { processes } from '../stores/live.svelte';
  import { openTab } from '../shell/tabs.svelte';

  let { change, closed = false, onchange, onapply }: { change: Change; closed?: boolean; onchange?: () => void; onapply?: (intent: string) => void } =
    $props();

  const rating = $derived(assessmentOf(change));
  const thread = $derived(threadOf(change.items));
  const st = $derived(reviewState(rating));
  const run = $derived(rating ? raterRun(processes.values(), change, rating.item) : undefined);
  const working = $derived(run?.status === 'running');
  let open = $state(false);
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

{#if rating || thread.length > 0}
  <div class="review">
    <button
      type="button"
      class="bubble {st}"
      aria-expanded={open}
      title="Rating of the description: {LABEL[st]}"
      onclick={() => (open = !open)}
    >
      <span aria-hidden="true">💬</span>
      {#if rating}<strong>{rating.score}/{MAX_SCORE}</strong>{/if}
      <span>{LABEL[st]}{working ? ' · rating…' : ''}</span>
    </button>
    {#if open}
      <section class="panel" aria-label="Rating of the description">
        {#if error}<div class="alert">{error}</div>{/if}
        {#if rating}
          <p>
            <strong>{rating.score}/{MAX_SCORE}</strong>
            {#if rating.verdict}<span class="verdict">{rating.verdict}</span>{/if}
            {#if run}<button type="button" class="link small" onclick={() => openTab({ kind: 'run', params: { id: run.id ?? '' } })}>Open the run</button>{/if}
          </p>
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
        <ol class="thread">
          {#each thread as m (m.id)}
            <li class={m.kind}>
              <span class="who">{m.kind === 'comment' ? 'You' : `Rater · ${m.score}/${MAX_SCORE}`}</span>
              <span>{m.text}</span>
            </li>
          {/each}
        </ol>
        {#if !closed}
          <form onsubmit={(e) => (e.preventDefault(), send())}>
            <textarea rows="2" placeholder="Answer the rater, ask it to refine its rating…" bind:value={answer}></textarea>
            <button type="submit" class="primary small" disabled={busy || !answer.trim()}>Send</button>
          </form>
        {/if}
      </section>
    {/if}
  </div>
{/if}

<style>
  .review {
    margin: 0.25rem 0 0.5rem;
  }
  .bubble {
    display: inline-flex;
    gap: 0.4rem;
    align-items: center;
    border-radius: 1rem;
    padding: 0.15rem 0.7rem;
    font-size: 0.85rem;
    border: 1px solid var(--border);
    background: var(--surface-1, transparent);
    cursor: pointer;
  }
  .bubble.accepted {
    border-color: var(--ok, #2e7d32);
  }
  .bubble.refused,
  .bubble.stale {
    border-color: var(--warn, #b26a00);
  }
  .panel {
    margin-top: 0.4rem;
    padding: 0.6rem 0.8rem;
    border: 1px solid var(--border);
    border-radius: 0.5rem;
    max-width: 48rem;
  }
  .verdict {
    margin-left: 0.4rem;
    font-style: italic;
  }
  .findings {
    margin: 0.3rem 0;
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
    background: var(--surface-2, rgba(127, 127, 127, 0.12));
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
