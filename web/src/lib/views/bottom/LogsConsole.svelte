<script lang="ts">
  // "Logs" console: "log" event lines and step logs.
  import Icon from '../../shell/Icon.svelte';
  import { live, clearLogs, processes } from '../../stores/live.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { formatTime, shortId } from '../../api';
  import { rowClick } from '../../actions';

  const LEVELS = ['debug', 'info', 'warn', 'error'];
  let minLevel = $state('debug');
  let processId = $state('');
  let filter = $state('');
  let follow = $state(true);
  let scroller = $state<HTMLDivElement>();

  const rank = (l: string) => Math.max(0, LEVELS.indexOf(l));
  const q = $derived(filter.trim().toLowerCase());
  const procs = $derived([...new Set(live.logs.map((l) => l.processId).filter(Boolean))]);
  const rows = $derived(
    live.logs.filter(
      (l) =>
        rank(l.level) >= rank(minLevel) &&
        (!processId || l.processId === processId) &&
        (!q || `${l.message} ${l.action}`.toLowerCase().includes(q)),
    ),
  );

  $effect(() => {
    void rows.length;
    if (follow && scroller) scroller.scrollTop = scroller.scrollHeight;
  });

  function name(pid: string) {
    const p = processes.get(pid);
    return p?.agent ? `${p.agent} · ${shortId(pid)}` : shortId(pid);
  }
</script>

<div class="console-tools">
  <select aria-label="Minimum level" bind:value={minLevel} data-no-pin>
    {#each LEVELS as l (l)}<option value={l}>≥ {l}</option>{/each}
  </select>
  <select aria-label="Process" bind:value={processId} data-no-pin>
    <option value="">All processes</option>
    {#each procs as p (p)}<option value={p}>{name(p)}</option>{/each}
  </select>
  <input type="search" placeholder="Filter…" aria-label="Filter logs" bind:value={filter} data-no-pin />
  <label class="check"><input type="checkbox" bind:checked={follow} /> Follow</label>
  <span class="grow"></span>
  <span class="hint">{rows.length} / {live.logs.length}</span>
  <button type="button" class="ghost small" title="Clear" aria-label="Clear logs" onclick={clearLogs}><Icon name="clear" size={13} /></button>
</div>
<div
  class="console-scroll"
  bind:this={scroller}
  use:rowClick={(row) => row.dataset.pid && openTab({ kind: 'run', params: { id: row.dataset.pid } })}
>
  {#if rows.length}
    <table class="console-table">
      <thead><tr><th>Time</th><th>Level</th><th>Process</th><th>Action</th><th>Message</th></tr></thead>
      <tbody>
        {#each rows as l (l.key)}
          <tr class:clickable={!!l.processId} data-row data-pid={l.processId}>
            <td>{formatTime(l.time)}</td>
            <td><span class="lvl lvl-{l.level}">{l.level}</span></td>
            <td title={l.processId}>
              {#if l.processId}
                <button type="button" class="link" onclick={() => openTab({ kind: 'run', params: { id: l.processId } })}>{name(l.processId)}</button>
              {/if}
            </td>
            <td>{l.action}{l.step !== undefined ? ` #${l.step + 1}` : ''}</td>
            <td class="wrap">{l.message}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p class="console-empty">No log lines.</p>
  {/if}
</div>
