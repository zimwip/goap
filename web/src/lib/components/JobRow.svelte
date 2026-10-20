<script lang="ts">
  // One job (ADR 0149): what it is, how far it is, its counters, its error, and what can be done with it (cancel while
  // it goes, save the file it wrote, open the change it created), and its feedback, the lines its run said.
  import type { Job } from '../api';
  import { errorMessage } from '../api';
  import Icon from '../shell/Icon.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import { openTab } from '../shell/tabs.svelte';
  import { notify } from '../shell/workbench.svelte';
  import { cancelJob, downloadJobResult } from '../stores/jobs.svelte';
  import { elapsed, fraction, isActive, jobChange, jobTitle, kindLabel, logLines, remaining, size, statsLine } from '../jobView';

  let {
    job,
    now = Date.now(),
    compact = false,
    showBy = false,
    detail = '',
  }: { job: Job; now?: number; compact?: boolean; showBy?: boolean; detail?: string } = $props();

  const part = $derived(fraction(job));
  const active = $derived(isActive(job));
  const change = $derived(jobChange(job));
  const stats = $derived(statsLine(job));
  const left = $derived(remaining(job, now));
  const lines = $derived(compact ? [] : logLines(job));
  let busy = $state(false);
  let showLog = $state(false);
  let logBox = $state<HTMLElement | undefined>();

  // the newest line in view while the log is open
  $effect(() => {
    void lines.length;
    if (showLog && logBox) logBox.scrollTop = logBox.scrollHeight;
  });

  async function cancel() {
    busy = true;
    try {
      await cancelJob(job);
    } catch (e) {
      notify(errorMessage(e), 'error');
    } finally {
      busy = false;
    }
  }

  async function download() {
    busy = true;
    try {
      await downloadJobResult(job);
    } catch (e) {
      notify(errorMessage(e), 'error');
    } finally {
      busy = false;
    }
  }
</script>

<div class="job" class:compact>
  <div class="head">
    <strong class="title" title={job.kind}>{jobTitle(job)}</strong>
    <StatusBadge status={job.status ?? ''} />
  </div>
  {#if detail}<div class="hint line">{detail}</div>{/if}
  {#if active}
    <div class="bar" role="progressbar" aria-label="Progress" aria-valuemin="0" aria-valuemax="100" aria-valuenow={part === undefined ? undefined : Math.round(part * 100)}>
      <span class:indeterminate={part === undefined} style:width={part === undefined ? undefined : `${part * 100}%`}></span>
    </div>
  {/if}
  {#if job.progress}<div class="hint line">{job.progress}{part !== undefined && active ? ` (${Math.round(part * 100)} %)` : ''}</div>{/if}
  {#if stats && !compact}<div class="hint line">{stats}</div>{/if}
  {#if job.error}<div class="alert small line">{job.error}</div>{/if}
  {#if job.cancelRequested && active}<div class="hint line">Stopping…</div>{/if}
  <div class="foot">
    <span class="hint">
      {#if !compact}{kindLabel(job.kind)}{#if job.target} · {job.target}{/if} · {/if}{#if showBy && job.by}{job.by} · {/if}{job.status === 'queued' ? 'waiting for a worker' : elapsed(job, now)}{left ? ` · ${left}` : ''}
    </span>
    <span class="acts">
      {#if lines.length}
        <button type="button" class="ghost small" aria-expanded={showLog} title="What the run said it did" onclick={() => (showLog = !showLog)}>
          <Icon name="list" size={13} /> {showLog ? 'Hide log' : `Log (${lines.length})`}
        </button>
      {/if}
      {#if change}
        <button type="button" class="ghost small" title="Open the change {change.title}" onclick={() => openTab({ kind: 'change', params: { id: change.id } }, { pin: true })}>
          <Icon name="branch" size={13} /> Change
        </button>
      {/if}
      {#if job.status === 'done' && job.result?.name}
        <button type="button" class="ghost small" disabled={busy} title="Save {job.result.name} ({size(job.result.size)})" onclick={() => void download()}>
          <Icon name="download" size={13} /> Download
        </button>
      {/if}
      {#if active && !job.cancelRequested}
        <button type="button" class="ghost small" disabled={busy} title="Cancel this job" onclick={() => void cancel()}>
          <Icon name="stop" size={13} /> Cancel
        </button>
      {/if}
    </span>
  </div>
  {#if showLog && lines.length}
    <ol class="log" bind:this={logBox} aria-label="Log of the job">
      {#each lines as l, i (i)}
        <li><span class="at">{l.at}</span> {l.text}</li>
      {/each}
    </ol>
  {/if}
</div>

<style>
  .job {
    display: grid;
    gap: 0.2rem;
    min-width: 0;
  }
  .head,
  .foot {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    min-width: 0;
  }
  .title {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .foot .hint {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .line {
    overflow-wrap: anywhere;
  }
  .compact .line {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .acts {
    display: inline-flex;
    gap: 0.2rem;
  }
  .acts button {
    display: inline-flex;
    align-items: center;
    gap: 0.2rem;
  }
  .log {
    list-style: none;
    margin: 0.2rem 0 0;
    padding: 0.3rem 0.45rem;
    max-height: 14rem;
    overflow: auto;
    background: var(--neutral-soft);
    border-radius: 4px;
    font-family: var(--mono, monospace);
    font-size: 0.78rem;
  }
  .log li {
    overflow-wrap: anywhere;
  }
  .log .at {
    color: var(--muted);
  }
  .bar {
    height: 4px;
    border-radius: 2px;
    background: var(--neutral-soft);
    overflow: hidden;
    position: relative;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width 0.3s ease;
  }
  .bar .indeterminate {
    position: absolute;
    width: 30%;
    animation: slide 1.2s ease-in-out infinite;
  }
  @keyframes slide {
    from {
      left: -30%;
    }
    to {
      left: 100%;
    }
  }
</style>
