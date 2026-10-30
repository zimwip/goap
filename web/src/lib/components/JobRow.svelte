<script lang="ts">
  // One job (ADR 0149): what it is, how far it is, its counters, its error, and what can be done with it (cancel while
  // it goes, save the file it wrote, open the change it created). A click on it opens its detail (`JobDialog`, shown
  // by the status bar): its facts, its parameters and its whole log.
  import type { Job } from '../api';
  import { errorMessage } from '../api';
  import Icon from '../shell/Icon.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import { openTab } from '../shell/tabs.svelte';
  import { notify } from '../shell/workbench.svelte';
  import { cancelJob, downloadJobResult, openJob } from '../stores/jobs.svelte';
  import { elapsed, fraction, isActive, jobChange, jobTitle, kindLabel, pendingRequest, remaining, size, statsLine } from '../jobView';

  let {
    job,
    now = Date.now(),
    compact = false,
    showBy = false,
    detail = '',
    onopen,
  }: {
    job: Job;
    now?: number;
    compact?: boolean;
    showBy?: boolean;
    detail?: string;
    /** called when the person opens the detail (the status bar closes its popover) */
    onopen?: () => void;
  } = $props();

  const part = $derived(fraction(job));
  const active = $derived(isActive(job));
  const asked = $derived(pendingRequest(job));
  const change = $derived(jobChange(job));
  const stats = $derived(statsLine(job));
  const left = $derived(remaining(job, now));
  let busy = $state(false);

  function open() {
    openJob(job);
    onopen?.();
  }

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
  <button type="button" class="open" title="Show the detail of this job" onclick={open}>
  <span class="head">
    <strong class="title">{jobTitle(job)}</strong>
    <StatusBadge status={job.status ?? ''} />
  </span>
  {#if detail}<span class="hint line">{detail}</span>{/if}
  {#if active}
    <span class="bar" role="progressbar" aria-label="Progress" aria-valuemin="0" aria-valuemax="100" aria-valuenow={part === undefined ? undefined : Math.round(part * 100)}>
      <span class:indeterminate={part === undefined} style:width={part === undefined ? undefined : `${part * 100}%`}></span>
    </span>
  {/if}
  {#if job.progress}<span class="hint line">{job.progress}{part !== undefined && active ? ` (${Math.round(part * 100)} %)` : ''}</span>{/if}
  {#if stats && !compact}<span class="hint line">{stats}</span>{/if}
  {#if job.error}<span class="alert small line">{job.error}</span>{/if}
  {#if job.cancelRequested && active}<span class="hint line">Stopping…</span>{/if}
  {#if asked}<span class="asked line">Waiting for an answer: {asked.form.title || 'a question'}</span>{/if}
  </button>
  <div class="foot">
    <span class="hint">
      {#if !compact}{kindLabel(job.kind)}{#if job.target} · {job.target}{/if} · {/if}{#if showBy && job.by}{job.by} · {/if}{job.status === 'queued' ? 'waiting for a worker' : elapsed(job, now)}{left ? ` · ${left}` : ''}
    </span>
    <span class="acts">
      <button type="button" class="ghost small" title="Its facts, its parameters and what the run said it did" onclick={open}>
        <Icon name="list" size={13} /> Details
      </button>
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
</div>

<style>
  .job {
    display: grid;
    gap: 0.2rem;
    min-width: 0;
  }
  .open {
    all: unset;
    display: grid;
    gap: 0.2rem;
    min-width: 0;
    cursor: pointer;
    border-radius: 4px;
  }
  .open:hover .title {
    text-decoration: underline;
  }
  .open:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
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
    display: block;
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
  .asked {
    color: var(--accent);
    font-weight: 600;
    font-size: 0.82rem;
  }
</style>
