<script lang="ts">
  // Status bar: my jobs (ADR 0149: imports, exports, live from the job stream), my running runs and token usage. Platform health/stream,
  // identity, settings and notifications live in the header (one place
  // for each, no duplicates).
  import Icon from './Icon.svelte';
  import Popover from './Popover.svelte';
  import { openTab } from './tabs.svelte';
  import { myActiveRuns, myRunsState } from '../stores/status.svelte';
  import { shortId } from '../api';
  import StatusBadge from '../components/StatusBadge.svelte';
  import JobRow from '../components/JobRow.svelte';
  import { activeJobs, jobList, onJobEnded, startJobs } from '../stores/jobs.svelte';
  import { fraction, jobTitle } from '../jobView';
  import { notify } from './workbench.svelte';

  let runsOpen = $state(false);
  let jobsOpen = $state(false);
  let now = $state(Date.now());

  // the job stream lives as long as the shell
  $effect(() => startJobs());
  // a job that ends says so, wherever the person is
  $effect(() =>
    onJobEnded((j) => {
      if (j.status === 'done') notify(`${jobTitle(j)}: done.`, 'ok');
      else if (j.status === 'failed') notify(`${jobTitle(j)} failed: ${j.error || 'no reason given'}`, 'error');
    }),
  );

  const jobs = $derived(activeJobs());
  // the recent ones under the running ones, in the list
  const recentJobs = $derived(jobList().slice(0, 8));
  const jobsPart = $derived.by(() => {
    const parts = jobs.map(fraction);
    return parts.length && parts.every((p) => p !== undefined) ? parts.reduce((a, b) => a! + b!, 0)! / parts.length : undefined;
  });

  // Elapsed-time clock (only while a list is open).
  $effect(() => {
    if (!runsOpen && !jobsOpen) return;
    now = Date.now();
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });

  const runs = $derived(myActiveRuns());
  const running = $derived(runs.some((p) => p.status === 'running'));

  function elapsed(iso: string | undefined): string {
    if (!iso) return '';
    const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
    if (s < 60) return `${s} s`;
    if (s < 3600) return `${Math.floor(s / 60)} min ${s % 60} s`;
    return `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`;
  }

  function openJobs() {
    openTab({ kind: 'jobs', params: {} }, { pin: true });
    jobsOpen = false;
  }

  function openRun(id: string) {
    openTab({ kind: 'run', params: { id } }, { pin: true });
    runsOpen = false;
  }
</script>

<footer class="status" aria-label="Status bar">
  <span class="grow"></span>

  <div class="item-wrap">
    <button type="button" class="item" aria-haspopup="dialog" aria-expanded={jobsOpen} onclick={() => (jobsOpen = !jobsOpen)} title="My jobs: imports and exports">
      {#if jobs.length}<span class="spin" aria-hidden="true"></span>{:else}<Icon name="clock" size={12} />{/if}
      {jobs.length ? `${jobs.length} job${jobs.length > 1 ? 's' : ''}` : 'Jobs'}
      {#if jobs.length && jobsPart !== undefined}
        <span class="mini" aria-hidden="true"><span style:width={`${jobsPart * 100}%`}></span></span>
        {Math.round(jobsPart * 100)} %
      {/if}
    </button>
    <Popover bind:open={jobsOpen} label="My jobs" width="420px">
      <div class="pop-head">
        <strong>My jobs</strong>
        <span class="grow"></span>
        <button type="button" class="ghost small" onclick={openJobs}>Open all</button>
      </div>
      {#if recentJobs.length}
        <ul class="list">
          {#each recentJobs as j (j.id)}
            <li class="job-li"><JobRow job={j} {now} compact /></li>
          {/each}
        </ul>
      {:else}
        <p class="pad hint">No job yet. Imports from a source and graph exports run here.</p>
      {/if}
    </Popover>
  </div>

  <div class="item-wrap">
    <button type="button" class="item" aria-haspopup="dialog" aria-expanded={runsOpen} onclick={() => (runsOpen = !runsOpen)} title="My running runs">
      {#if running}<span class="spin" aria-hidden="true"></span>{:else}<Icon name="play" size={12} />{/if}
      {runs.length ? `${runs.length} run${runs.length > 1 ? 's' : ''} in progress` : 'No runs in progress'}
    </button>
    <Popover bind:open={runsOpen} label="My running runs" width="380px">
      <div class="pop-head"><strong>My running runs</strong></div>
      {#if myRunsState.error}<p class="pad alert">{myRunsState.error}</p>{/if}
      {#if runs.length}
        <ul class="list">
          {#each runs as p (p.id)}
            <li>
              <button type="button" class="row-btn" onclick={() => openRun(p.id ?? '')}>
                <span class="main">
                  <strong>{p.agent || p.title || shortId(p.id)}</strong>
                  <span class="hint">{p.goal ? `goal ${p.goal}` : 'goal to be determined'}</span>
                </span>
                <StatusBadge status={p.status} />
                <span class="hint el">{elapsed(p.createdAt)}</span>
              </button>
            </li>
          {/each}
        </ul>
      {:else}
        <p class="pad hint">No runs in progress.</p>
      {/if}
    </Popover>
  </div>
</footer>

<style>
  .status {
    display: flex;
    align-items: stretch;
    height: 22px;
    flex: none;
    background: var(--chrome-2);
    color: var(--text);
    border-top: 1px solid var(--border);
    font-size: 12px;
    padding: 0 0.3rem;
    position: relative;
    z-index: 30;
  }
  .item-wrap {
    position: relative;
    display: flex;
  }
  .item {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0 0.5rem;
    min-height: 0;
    border: none;
    border-radius: 0;
    background: transparent;
    color: inherit;
    font-size: 12px;
    font-weight: 500;
    white-space: nowrap;
  }
  button.item:hover:not(:disabled) {
    background: var(--hover);
  }
  button.item:focus-visible {
    outline: 1px solid currentColor;
    outline-offset: -2px;
  }
  .spin {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    border: 2px solid currentColor;
    border-right-color: transparent;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .pop-head {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.45rem 0.6rem;
    border-bottom: 1px solid var(--border);
    position: sticky;
    top: 0;
    background: var(--surface);
  }
  .pad {
    padding: 0.4rem 0.6rem;
    margin: 0;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0.2rem 0;
  }
  .row-btn {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    border: none;
    border-radius: 0;
    background: none;
    text-align: left;
    font-weight: 400;
    padding: 0.3rem 0.6rem;
    min-height: 0;
  }
  .row-btn:hover:not(:disabled) {
    background: var(--hover);
  }
  .main {
    flex: 1;
    min-width: 0;
    display: grid;
  }
  .main .hint {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--muted);
  }
  .el {
    white-space: nowrap;
  }
  .grow {
    flex: 1;
  }
  .job-li {
    padding: 0.4rem 0.6rem;
    border-top: 1px solid var(--border);
  }
  .job-li:first-child {
    border-top: none;
  }
  .mini {
    display: inline-block;
    width: 48px;
    height: 4px;
    border-radius: 2px;
    background: var(--neutral-soft);
    overflow: hidden;
  }
  .mini span {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width 0.3s ease;
  }
</style>
