<script lang="ts">
  // What the change waits for from people (ADR 0117), at the top of its overview: what the person looking at it may
  // answer, answered in place (the panel of the run's task, the answer to a question), then what waits for someone
  // else and whom. The engine gathers the asks and says who may answer each; the runs stay one click away.
  import { engine, errorMessage, graph, shortId, type ChangeAsk, type Process } from '../api';
  import { processes, ingestProcess } from '../stores/live.svelte';
  import { me } from '../stores/session.svelte';
  import { stamp, keyOf } from '../flux/signals.svelte';
  import { addressee, answeredInPlace, askKind, groupAsks, waitingFor } from '../changeAsks';
  import PendingTask from './PendingTask.svelte';
  import IntentDialogue from '../views/editors/IntentDialogue.svelte';

  let {
    changeId,
    closed = false,
    reviews = [],
    onreview,
    ondecisions,
    onopen,
    oncount,
  }: {
    changeId: string;
    closed?: boolean;
    /** keys of the impacts awaiting their review */
    reviews?: string[];
    onreview?: () => void;
    ondecisions?: () => void;
    onopen: (processId: string, pin?: boolean) => void;
    /** how many asks the person may answer, reviews included */
    oncount?: (n: number) => void;
  } = $props();

  let asks = $state<ChangeAsk[]>([]);
  let error = $state('');
  let answers = $state<Record<string, string>>({});
  let busy = $state('');

  // the runs of the change move (live stream) or the change itself does: read the asks again
  const version = $derived(
    [...processes.values()]
      .filter((p) => p.changeId === changeId)
      .map((p) => `${p.id}:${p.status}:${p.updatedAt}`)
      .join('|'),
  );
  $effect(() => {
    void version;
    void stamp(keyOf.change(changeId));
    const ctl = new AbortController();
    engine
      .getChangeAsks(changeId, ctl.signal)
      .then((r) => {
        asks = r.asks ?? [];
        error = '';
      })
      .catch((e: unknown) => {
        if (!ctl.signal.aborted) error = errorMessage(e);
      });
    return () => ctl.abort();
  });

  const groups = $derived(groupAsks(asks));
  const subject = $derived(me());
  $effect(() => oncount?.(groups.mine.length + (closed ? 0 : reviews.length)));

  const runOf = (a: ChangeAsk): Process | undefined => (a.processId ? processes.get(a.processId) : undefined);

  async function answer(a: ChangeAsk) {
    const text = answers[a.id ?? '']?.trim();
    if (!text || !a.question) return;
    busy = a.id ?? '';
    error = '';
    try {
      await graph.answerQuestion(changeId, a.question, text);
      answers[a.id ?? ''] = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }
</script>

{#if groups.mine.length || groups.others.length || (!closed && reviews.length) || error}
  <section class="card asks" aria-label="What the change waits for">
    {#if groups.mine.length || (!closed && reviews.length)}
      <h3>Waiting for you <span class="count">{groups.mine.length + (closed ? 0 : reviews.length)}</span></h3>
      {#each groups.mine as a, i (a.id)}
        {@const run = runOf(a)}
        <details class="ask mine" open={i < 3}>
          <summary>
            <span class="kind">{askKind(a)}</span>
            <strong>{a.title || a.action}</strong>
            <span class="hint">
              {#if a.step}{a.step} · {/if}for {addressee(a, subject)}{#if waitingFor(a.since)} · waiting {waitingFor(a.since)}{/if}
            </span>
            {#if a.processId}<button type="button" class="link small" onclick={(e) => (e.preventDefault(), onopen(a.processId ?? ''))}>run {run?.agent || shortId(a.processId)}</button>{/if}
          </summary>
          {#if a.detail && a.kind !== 'input'}<p class="hint">{a.detail}</p>{/if}
          {#if a.kind === 'question'}
            {#if a.detail}<p class="hint">About: {a.detail}</p>{/if}
            <div class="answer">
              <textarea rows="2" placeholder="Your answer" bind:value={answers[a.id ?? '']} disabled={closed}></textarea>
              <button type="button" class="primary small" disabled={closed || busy === a.id || !answers[a.id ?? '']?.trim()} onclick={() => answer(a)}>Answer</button>
            </div>
          {:else if a.kind === 'decision'}
            <button type="button" class="small" onclick={() => ondecisions?.()}>Rule it in Decisions</button>
          {:else if a.kind === 'clarification' && run}
            <IntentDialogue process={run} onupdate={ingestProcess} />
          {:else if answeredInPlace(a) && run?.pending}
            <PendingTask process={run} ondecided={ingestProcess} {onopen} />
          {:else if a.processId}
            <p class="hint">Open the run to answer it.</p>
          {/if}
        </details>
      {/each}
      {#if !closed && reviews.length}
        <div class="ask mine review">
          <span class="kind">Review</span>
          <strong>{reviews.length} change impact{reviews.length > 1 ? 's' : ''} await{reviews.length > 1 ? '' : 's'} their review</strong>
          <span class="hint">{reviews.join(', ')}</span>
          <button type="button" class="small" onclick={() => onreview?.()}>Review them</button>
        </div>
      {/if}
    {/if}
    {#if groups.others.length}
      <h4>Waiting for others <span class="count">{groups.others.length}</span></h4>
      <ul class="others">
        {#each groups.others as a (a.id)}
          <li>
            <span class="kind">{askKind(a)}</span>
            {a.title || a.action}
            <span class="hint">for {addressee(a, subject)}{#if waitingFor(a.since)} · waiting {waitingFor(a.since)}{/if}</span>
            {#if a.processId}<button type="button" class="link small" onclick={() => onopen(a.processId ?? '')}>run</button>{/if}
          </li>
        {/each}
      </ul>
    {/if}
    {#if error}<p class="hint">What the change waits for is unavailable: {error}</p>{/if}
  </section>
{/if}

<style>
  .asks {
    border-left: 3px solid var(--accent);
  }
  h3,
  h4 {
    display: flex;
    gap: 8px;
    align-items: center;
    margin: 0 0 6px;
  }
  h4 {
    margin-top: 10px;
    color: var(--muted);
  }
  .count {
    font-size: 0.8em;
    border-radius: 8px;
    padding: 0 6px;
    background: var(--accent-soft);
    color: var(--accent);
  }
  .ask {
    margin: 6px 0;
    padding: 4px 0;
    border-top: 1px solid var(--border);
  }
  .ask summary,
  .ask.review {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: baseline;
    cursor: pointer;
  }
  .ask.review {
    cursor: default;
  }
  .kind {
    font-size: 0.8em;
    border-radius: 8px;
    padding: 0 6px;
    border: 1px solid var(--border);
    color: var(--muted);
    white-space: nowrap;
  }
  .mine .kind {
    border-color: var(--warn);
    color: var(--warn);
  }
  .answer {
    display: flex;
    gap: 8px;
    align-items: flex-end;
    margin-top: 4px;
  }
  .answer textarea {
    flex: 1;
  }
  .others {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .others li {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: baseline;
    padding: 2px 0;
  }
</style>
