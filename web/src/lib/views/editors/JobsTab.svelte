<script lang="ts">
  // "Jobs" tab (ADR 0149): the long runs of the person (imports from a source, graph exports and imports), live from the
  // job stream: progress, counters, errors, cancel, the file a job wrote and the change it created. An administrator
  // may also look at every person's jobs (read when asked, then followed for their own).
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import JobRow from '../../components/JobRow.svelte';
  import { errorMessage, jobService, type Job } from '../../api';
  import { can } from '../../stores/session.svelte';
  import { jobList, jobStream, knownJobs } from '../../stores/jobs.svelte';
  import { isActive } from '../../jobView';
  import { jobSubject } from '../../importSources';

  let { tab }: { tab: Tab } = $props();
  void tab;

  let everyone = $state(false);
  let others = $state<Job[]>([]);
  let error = $state('');
  let now = $state(Date.now());
  let filter = $state<'all' | 'active' | 'ended'>('all');

  const list = $derived.by(() => {
    const mine = jobList();
    if (!everyone) return mine;
    const ids = new Set(mine.map((j) => j.id));
    // a job of the caller is the live one
    const rest = others.filter((j) => !ids.has(j.id)).map((j) => knownJobs.get(j.id ?? '') ?? j);
    return [...mine, ...rest].sort((a, b) => Date.parse(b.createdAt ?? '') - Date.parse(a.createdAt ?? ''));
  });
  const shown = $derived(list.filter((j) => filter === 'all' || (filter === 'active') === isActive(j)));
  const running = $derived(list.some(isActive));

  $effect(() => {
    if (!running) return;
    now = Date.now();
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });

  async function loadEveryone() {
    try {
      others = (await jobService.list({ all: true, limit: 200 })).jobs ?? [];
      error = '';
    } catch (e) {
      error = errorMessage(e);
    }
  }

  $effect(() => {
    if (everyone) void loadEveryone();
    else others = [];
  });
</script>

<div class="editor-page">
  <div class="editor-head">
    <Icon name="clock" size={18} />
    <h2>Jobs</h2>
  </div>
  <section class="card">
    <p class="hint">
      The long runs you started: imports from a source, graph exports and imports. They go on when you close the page; a finished job keeps
      its result for a week.
    </p>
    <div class="bar">
      <div class="seg" role="radiogroup" aria-label="Show">
        {#each [['all', 'All'], ['active', 'In progress'], ['ended', 'Ended']] as [v, l] (v)}
          <button type="button" role="radio" aria-checked={filter === v} class:on={filter === v} onclick={() => (filter = v as typeof filter)}>{l}</button>
        {/each}
      </div>
      <span class="grow"></span>
      {#if can.administer}
        <label class="check"><input type="checkbox" bind:checked={everyone} /> Every person's jobs</label>
        {#if everyone}<button type="button" class="ghost small" title="Read every person's jobs again" onclick={() => void loadEveryone()}><Icon name="refresh" size={13} /></button>{/if}
      {/if}
    </div>
    {#if error}<div class="alert">{error}</div>{/if}
    {#if jobStream.error && !jobStream.connected}<div class="alert warn">The job stream is not connected: {jobStream.error}</div>{/if}
    {#if !jobStream.loaded && !jobStream.error}
      <p class="hint">Loading…</p>
    {:else if !shown.length}
      <p class="hint">{filter === 'active' ? 'No job in progress.' : 'No job yet.'}</p>
    {:else}
      <ul class="jobs">
        {#each shown as j (j.id)}
          <li><JobRow job={j} {now} showBy={everyone} detail={jobSubject(j)} /></li>
        {/each}
      </ul>
    {/if}
  </section>
</div>

<style>
  .bar {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.5rem;
  }
  .grow {
    flex: 1;
  }
  .seg {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }
  .seg button {
    border: none;
    border-radius: 0;
    background: transparent;
    font-weight: 500;
    padding: 0.2rem 0.6rem;
    min-height: 0;
  }
  .seg button.on {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .jobs {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .jobs li {
    padding: 0.55rem 0.2rem;
    border-top: 1px solid var(--border);
  }
</style>
