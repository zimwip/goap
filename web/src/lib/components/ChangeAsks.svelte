<script lang="ts">
  // What the change waits for from people (ADR 0117), as one strip above the panes of the change (ADR 0152): folded, it
  // counts what waits for the person looking at it and for whom else; unfolded, it lists what the person may answer,
  // answered in place (the panel of the run's task, the answer to a question), then what waits for someone else and
  // whom. Nothing waiting, or the asks unreadable: no strip. The engine gathers the asks and says who may answer each.
  import { tick, untrack } from 'svelte';
  import { engine, errorMessage, graph, shortId, type ChangeAsk, type Process } from '../api';
  import { processes, ingestProcess } from '../stores/live.svelte';
  import { me } from '../stores/session.svelte';
  import { stamp, keyOf } from '../flux/signals.svelte';
  import { addressee, answeredInPlace, askKind, attentionChips, groupAsks, waitingFor } from '../changeAsks';
  import PendingTask from './PendingTask.svelte';
  import { openTask } from '../stores/tasks.svelte';
  import { holderLabel } from '../tasks';
  import IntentDialogue from '../views/editors/IntentDialogue.svelte';

  let {
    changeId,
    closed = false,
    reviews = [],
    onreview,
    ondecisions,
    onopen,
    focus = '',
  }: {
    changeId: string;
    /** the ask picked from a list of tasks (ADR 0158): the strip opens on it */
    focus?: string;
    closed?: boolean;
    /** keys of the impacts awaiting their review */
    reviews?: string[];
    onreview?: () => void;
    ondecisions?: () => void;
    onopen: (processId: string, pin?: boolean) => void;
  } = $props();

  let asks = $state<ChangeAsk[]>([]);
  let error = $state('');
  let answers = $state<Record<string, string>>({});
  let busy = $state('');
  /** the strip unfolded (per tab, not remembered) */
  let open = $state(false);

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
      })
      .catch(() => {
        // the engine cannot say what the change waits for: show nothing of it rather than an error beside every pane
        if (!ctl.signal.aborted) asks = [];
      });
    return () => ctl.abort();
  });

  const groups = $derived(groupAsks(asks));
  // a task picked elsewhere opens the strip on it, once it is read, and shows it
  let shown = '';
  let root = $state<HTMLElement>();
  $effect(() => {
    if (!focus || focus === shown || !asks.some((a) => a.id === focus)) return;
    shown = focus;
    untrack(() => (open = true));
    void tick().then(() => root?.querySelector('.ask.focus')?.scrollIntoView({ block: 'start', behavior: 'smooth' }));
  });
  const subject = $derived(me());
  const chips = $derived(attentionChips(groups, closed ? 0 : reviews.length, subject));
  const forMe = $derived(chips.some((c) => c.mine));

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

{#if chips.length}
  <section class="asks" class:mine={forMe} aria-label="What the change waits for" bind:this={root}>
    <button type="button" class="strip" aria-expanded={open} onclick={() => (open = !open)}>
      <strong>Waiting on people</strong>
      {#each chips as c (c.label)}<span class="chip" class:mine={c.mine}>{c.label}</span>{/each}
      <span class="grow"></span>
      <span class="hint">{open ? 'Hide ▴' : 'Show ▾'}</span>
    </button>
    {#if open}
    <div class="panel">
    {#if groups.mine.length || (!closed && reviews.length)}
      <h3>Waiting for you <span class="count">{groups.mine.length + (closed ? 0 : reviews.length)}</span></h3>
      {#each groups.mine as a, i (a.id)}
        {@const run = runOf(a)}
        <details class="ask mine" class:focus={a.id === focus} open={i < 3 || a.id === focus}>
          <summary>
            <span class="kind">{askKind(a)}</span>
            <strong>{a.title || a.action}</strong>
            <span class="hint">
              {#if a.step}{a.step} · {/if}for {addressee(a, subject)}{#if holderLabel(a, subject)}{` · held by ${holderLabel(a, subject)}`}{/if}{#if waitingFor(a.since)}{` · waiting ${waitingFor(a.since)}`}{/if}
            </span>
            {#if a.processId}<button type="button" class="link small" title="Accept, turn down{a.mayAssign ? ', give to someone' : ''}" onclick={(e) => (e.preventDefault(), openTask({ ...a, changeId }, true))}>task</button>{/if}
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
            <span class="hint">for {addressee(a, subject)}{#if holderLabel(a, subject)}{` · held by ${holderLabel(a, subject)}`}{/if}{#if waitingFor(a.since)}{` · waiting ${waitingFor(a.since)}`}{/if}</span>
            {#if a.processId}<button type="button" class="link small" onclick={() => onopen(a.processId ?? '')}>run</button>{/if}
            {#if a.processId}<button type="button" class="link small" title="Who holds it{a.mayAssign ? ', give it to someone' : ''}" onclick={() => openTask({ ...a, changeId }, true)}>task</button>{/if}
          </li>
        {/each}
      </ul>
    {/if}
    {#if error}<p class="hint">Answering failed: {error}</p>{/if}
    </div>
    {/if}
  </section>
{/if}

<style>
  .asks {
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    margin: 0 0 0.6rem;
  }
  .asks.mine {
    border-color: var(--accent);
  }
  .ask.focus {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
    border-radius: var(--radius-sm);
  }
  .strip {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 8px;
    align-items: center;
    width: 100%;
    padding: 5px 10px;
    border: 0;
    border-radius: var(--radius);
    background: transparent;
    color: var(--text);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .asks.mine .strip {
    background: var(--accent-soft);
  }
  .strip .grow {
    flex: 1;
  }
  .chip {
    font-size: 0.85em;
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 8px;
    background: var(--surface);
    color: var(--muted);
    white-space: nowrap;
  }
  .chip.mine {
    border-color: var(--accent);
    color: var(--accent);
    font-weight: 600;
  }
  .panel {
    padding: 6px 12px 10px;
    border-top: 1px solid var(--border);
    max-height: 60vh;
    overflow-y: auto;
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
