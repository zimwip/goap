<script lang="ts">
  // "My processes" tool (ADR 0031): lists the caller's own processes regardless of whether they
  // have a change yet — the only click-path into a process that never attaches to one.
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { engine, errorMessage, formatDate, shortId, type Process, type ProcessStatus } from '../../api';
  import { openTab } from '../../shell/tabs.svelte';
  import { ingestProcess, onLiveEvent } from '../../stores/live.svelte';

  let items = $state<Process[]>([]);
  let loading = $state(false);
  let error = $state('');
  let filter = $state('');
  let status = $state<ProcessStatus | ''>('');

  const STATUSES: (ProcessStatus | '')[] = ['', 'clarifying', 'running', 'waiting', 'completed', 'stuck', 'failed', 'superseded'];

  async function load() {
    loading = true;
    try {
      const ps = (await engine.listProcesses({ mine: true, statuses: status ? [status] : undefined })).processes ?? [];
      for (const p of ps) ingestProcess(p);
      items = [...ps].sort((a, b) => (b.createdAt ?? '').localeCompare(a.createdAt ?? ''));
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void status;
    void load();
    const t = setInterval(() => void load(), 15_000);
    const off = onLiveEvent(() => void load());
    return () => {
      clearInterval(t);
      off();
    };
  });

  const q = $derived(filter.trim().toLowerCase());
  const shown = $derived(
    items.filter((p) => !q || `${p.title ?? ''} ${p.methodology ?? ''} ${p.agent ?? ''} ${p.goal ?? ''}`.toLowerCase().includes(q)),
  );

  function open(p: Process) {
    openTab({ kind: 'run', params: { id: p.id ?? '' } }, { pin: true });
  }
</script>

<div class="explorer">
  <div class="tools">
    <input type="search" placeholder="Filter…" aria-label="Filter processes" bind:value={filter} data-no-pin />
    <select aria-label="Filter by status" bind:value={status}>
      {#each STATUSES as s (s)}
        <option value={s}>{s || 'all statuses'}</option>
      {/each}
    </select>
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}
      ><Icon name="refresh" size={14} /></button
    >
  </div>
  {#if error}<div class="alert small">{error}</div>{/if}
  {#if !loading && !error && !items.length}
    <p class="empty pad">No processes yet. Start one from the Assistant or a methodology's agent.</p>
  {/if}
  <ul class="list">
    {#each shown as p (p.id)}
      <li>
        <div class="line1">
          <button type="button" class="link name" onclick={() => open(p)} title="Open">{p.title || `Run ${shortId(p.id)}`}</button>
          <span class="grow"></span>
          <StatusBadge status={p.status} />
        </div>
        <div class="line2">
          {p.methodology || '—'}{#if p.agent} / <code>{p.agent}</code>{/if}
          {#if !p.changeId}
            · <span class="warn" title="Not attached to any change yet">no change</span>
          {/if}
        </div>
        <div class="line2">{formatDate(p.createdAt)}</div>
      </li>
    {/each}
  </ul>
</div>

<style>
  .explorer {
    padding-bottom: 1rem;
  }
  .tools {
    display: flex;
    gap: 2px;
    padding: 0 0.5rem 0.4rem;
    position: sticky;
    top: 0;
    background: var(--chrome);
    z-index: 1;
  }
  .tools input {
    min-height: 24px;
    height: 24px;
    margin-right: 0.2rem;
    flex: 1;
  }
  .tools select {
    min-height: 24px;
    height: 24px;
    font-size: 0.82rem;
  }
  .tools button {
    padding: 0.1rem 0.3rem;
  }
  .pad {
    padding: 0.4rem 0.8rem;
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .list li {
    padding: 0.35rem 0.7rem;
    border-bottom: 1px solid var(--border);
  }
  .line1 {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }
  .name {
    font-weight: 600;
  }
  .line2 {
    font-size: 0.88em;
    color: var(--muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .warn {
    color: var(--warn);
  }
</style>
