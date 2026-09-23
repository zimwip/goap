<script lang="ts">
  // "Events" console: live WatchEvents stream.
  import Icon from '../../shell/Icon.svelte';
  import { live, clearEvents, processes } from '../../stores/live.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { formatTime, shortId } from '../../api';
  import { rowClick } from '../../actions';

  let type = $state('');
  let filter = $state('');
  let follow = $state(true);
  let scroller = $state<HTMLDivElement>();

  const TYPES = ['started', 'intent', 'step', 'waiting', 'completed', 'stuck', 'failed', 'log'];
  const LABELS: Record<string, string> = {
    started: 'started',
    intent: 'intent',
    step: 'step',
    waiting: 'waiting',
    completed: 'completed',
    stuck: 'stuck',
    failed: 'failed',
    log: 'log',
  };

  const q = $derived(filter.trim().toLowerCase());
  const rows = $derived(
    live.events.filter(
      (e) =>
        (!type || e.type === type) &&
        (!q || `${e.processId} ${e.agent} ${e.methodology} ${e.detail} ${e.type}`.toLowerCase().includes(q)),
    ),
  );

  $effect(() => {
    void rows.length;
    if (follow && scroller) scroller.scrollTop = scroller.scrollHeight;
  });

  function title(pid: string): string {
    const p = processes.get(pid);
    return p?.title || shortId(pid);
  }
</script>

<div class="console-tools">
  <select aria-label="Event type" bind:value={type} data-no-pin>
    <option value="">All types</option>
    {#each TYPES as t (t)}<option value={t}>{LABELS[t]}</option>{/each}
  </select>
  <input type="search" placeholder="Filter…" aria-label="Filter events" bind:value={filter} data-no-pin />
  <label class="check"><input type="checkbox" bind:checked={follow} /> Follow</label>
  <span class="grow"></span>
  <span class="hint">{rows.length} / {live.events.length}</span>
  <span class="hint">{live.status === 'open' ? '● connected' : live.status === 'connecting' ? '● waiting' : live.status === 'stopped' ? 'stopped' : `reconnecting…${live.error ? ` (${live.error})` : ''}`}</span>
  <button type="button" class="ghost small" title="Clear" aria-label="Clear events" onclick={clearEvents}><Icon name="clear" size={13} /></button>
</div>
<div
  class="console-scroll"
  bind:this={scroller}
  use:rowClick={(row) => row.dataset.pid && openTab({ kind: 'run', params: { id: row.dataset.pid } })}
>
  {#if rows.length}
    <table class="console-table">
      <thead><tr><th>Time</th><th>Type</th><th>Process</th><th>Agent</th><th>Step / action</th></tr></thead>
      <tbody>
        {#each rows as e (e.seq)}
          <tr class:clickable={!!e.processId} data-row data-pid={e.processId}>
            <td>{formatTime(e.time)}</td>
            <td><span class="t t-{e.type}">{LABELS[e.type] ?? e.type}</span></td>
            <td title={e.processId}>
              {#if e.processId}
                <button type="button" class="link" onclick={() => openTab({ kind: 'run', params: { id: e.processId } })}>{title(e.processId)}</button>
              {/if}
            </td>
            <td>{e.agent}</td>
            <td class="wrap">{#if e.level}<span class="lvl lvl-{e.level}">{e.level}</span> {/if}{e.detail}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p class="console-empty">No events received yet.</p>
  {/if}
</div>

<style>
  .t {
    font-weight: 600;
  }
  .t-completed {
    color: var(--ok);
  }
  .t-failed {
    color: var(--danger);
  }
  .t-stuck,
  .t-waiting {
    color: var(--warn);
  }
  .t-started,
  .t-intent {
    color: var(--info);
  }
  .t-log {
    color: var(--muted);
  }
</style>
