<script lang="ts">
  // Where a run stands in the steps of its process (ADR 0035 §3): each step done, skipped, active, waiting for
  // someone, ready, to do (with what it still needs) or blocked; nested processes unfold in place. Renders nothing
  // for a run that does not follow a process.
  import ProcessProgress from './ProcessProgress.svelte';
  import { engine, type ProcessProgress as Progress, type StepProgress } from '../api';
  import { processes } from '../stores/live.svelte';

  let {
    processId,
    onopen,
    nested = false,
  }: {
    processId: string;
    /** opens a run (a sub-agent of a step) */
    onopen?: (id: string) => void;
    nested?: boolean;
  } = $props();

  let progress = $state<Progress | undefined>();
  // refreshed whenever the run changes (live stream)
  const live = $derived(processes.get(processId));
  const version = $derived(`${live?.status ?? ''}:${live?.updatedAt ?? ''}:${live?.steps?.length ?? 0}`);

  $effect(() => {
    void version;
    const ctl = new AbortController();
    engine
      .getProcessProgress(processId, ctl.signal)
      .then((r) => (progress = r.progress))
      .catch(() => (progress = undefined));
    return () => ctl.abort();
  });

  const LABEL: Record<string, string> = {
    done: 'done',
    skipped: 'not needed',
    active: 'in progress',
    waiting: 'waiting',
    ready: 'ready',
    todo: 'to do',
    blocked: 'blocked',
  };
  const pct = $derived(progress?.total ? Math.round(((progress.done ?? 0) * 100) / progress.total) : 0);

  function waitingText(s: StepProgress): string {
    if (s.waiting === 'approval') return `an approval${s.permission ? ` (${s.permission})` : ''}`;
    if (s.waiting === 'input') return 'a person to do it';
    return s.waiting ?? '';
  }
</script>

{#snippet step(s: StepProgress)}
  <li class="step {s.state}">
    <div class="line">
      <span class="chip {s.state}">{LABEL[s.state ?? ''] ?? s.state}</span>
      <strong>{s.name}</strong>
      {#if s.description}<span class="hint ell">{s.description}</span>{/if}
      {#if s.target}<span class="hint mono">{s.method} {s.target}{s.chosen ? ` → ${s.chosen}` : ''}</span>{/if}
    </div>
    {#if s.state === 'waiting'}
      <div class="note">Waiting for {waitingText(s)}.</div>
    {:else if s.state === 'todo' && s.missing?.length}
      <div class="note">Needs: {#each s.missing as m, i (m)}{i ? ', ' : ''}<code>{m}</code>{/each}</div>
    {/if}
    {#if s.childProcessIds?.length}
      <div class="note">
        {#each s.childProcessIds as c (c)}
          <button type="button" class="link" onclick={() => onopen?.(c)}>{processes.get(c)?.agent || 'sub-agent'}</button>
        {/each}
      </div>
      {#if s.method === 'process' && (s.state === 'active' || s.state === 'waiting')}
        <div class="nested"><ProcessProgress processId={s.childProcessIds[s.childProcessIds.length - 1]} {onopen} nested /></div>
      {/if}
    {/if}
    {#if s.steps?.length}
      <ul class="tree">
        {#each s.steps as c (c.path)}{@render step(c)}{/each}
      </ul>
    {/if}
  </li>
{/snippet}

{#if progress}
  <section class="progress" class:card={!nested} aria-label="Process progress">
    <div class="head">
      <h3 class="grow">
        {nested ? 'Nested process' : 'Process'} <code>{progress.process}</code>
        <span class="hint">{progress.done ?? 0}/{progress.total ?? 0} steps</span>
      </h3>
    </div>
    <div class="bar" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={pct}><span style="width: {pct}%"></span></div>
    {#if progress.error}<p class="hint">{progress.error}</p>{/if}
    <ul class="tree root">
      {#each progress.steps ?? [] as s (s.path)}{@render step(s)}{/each}
    </ul>
  </section>
{/if}

<style>
  .head {
    display: flex;
    align-items: center;
  }
  .head h3 {
    margin: 0 0 4px;
  }
  .grow {
    flex: 1;
  }
  .bar {
    height: 6px;
    background: var(--border);
    border-radius: 3px;
    overflow: hidden;
    margin-bottom: 8px;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--ok);
  }
  .tree {
    list-style: none;
    margin: 0;
    padding-left: 18px;
  }
  .tree.root {
    padding-left: 0;
  }
  .step {
    margin: 3px 0;
  }
  .line {
    display: flex;
    gap: 8px;
    align-items: baseline;
    min-width: 0;
  }
  .ell {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }
  .note {
    margin-left: 6.5em;
    font-size: 0.9em;
    color: var(--muted);
  }
  .nested {
    margin: 4px 0 4px 18px;
    padding-left: 8px;
    border-left: 2px solid var(--border);
  }
  .chip {
    display: inline-block;
    min-width: 6em;
    text-align: center;
    font-size: 0.8em;
    border-radius: 8px;
    padding: 0 6px;
    border: 1px solid var(--border);
    color: var(--muted);
    flex-shrink: 0;
  }
  .chip.done {
    color: var(--ok);
    border-color: var(--ok);
  }
  .chip.skipped {
    opacity: 0.7;
  }
  .chip.active {
    color: var(--info);
    border-color: var(--info);
  }
  .chip.waiting {
    color: var(--warn);
    border-color: var(--warn);
    background: var(--warn-soft);
  }
  .chip.ready {
    color: var(--accent);
    border-color: var(--accent);
  }
  .chip.blocked {
    color: var(--danger);
    border-color: var(--danger);
    background: var(--danger-soft);
  }
</style>
