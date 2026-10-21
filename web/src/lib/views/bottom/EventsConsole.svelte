<script lang="ts">
  // "Events" console: the platform event stream as it comes (ADR 0053), every kind the caller may see.
  import Icon from '../../shell/Icon.svelte';
  import { live, clearEvents, processes, type EventRow } from '../../stores/live.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { formatTime, shortId } from '../../api';
  import { rowClick } from '../../actions';
  import { knownJobs, openJob } from '../../stores/jobs.svelte';

  let kind = $state('');
  let filter = $state('');
  let follow = $state(true);
  let scroller = $state<HTMLDivElement>();

  const KINDS = ['process', 'change', 'node', 'baseline', 'job', 'model', 'methodology', 'domain', 'connector', 'conversation', 'preferences'];

  const q = $derived(filter.trim().toLowerCase());
  const rows = $derived(
    live.events.filter(
      (e) =>
        (!kind || e.kind === kind) &&
        (!q || `${e.type} ${e.id} ${e.changeId} ${e.processId} ${e.agent} ${e.methodology} ${e.actor} ${e.detail}`.toLowerCase().includes(q)),
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

  /** The type without its kind: `node.written` reads `written` in the kind's column. */
  const what = (e: EventRow): string => (e.type.startsWith(`${e.kind}.`) ? e.type.slice(e.kind.length + 1) : e.type);

  /** Opens what a row is about: the run, the change, the job. */
  function open(e: EventRow): void {
    const job = e.kind === 'job' ? knownJobs.get(e.id) : undefined;
    if (e.processId) openTab({ kind: 'run', params: { id: e.processId } });
    else if (e.kind === 'change' && e.id) openTab({ kind: 'change', params: { id: e.id } });
    else if (job) openJob(job);
    else if (e.changeId) openTab({ kind: 'change', params: { id: e.changeId } });
  }
  const opens = (e: EventRow): boolean => !!(e.processId || e.changeId || (e.kind === 'change' && e.id) || (e.kind === 'job' && knownJobs.has(e.id)));
</script>

<div class="console-tools">
  <select aria-label="Event kind" bind:value={kind} data-no-pin>
    <option value="">All kinds</option>
    {#each KINDS as k (k)}<option value={k}>{k}</option>{/each}
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
  use:rowClick={(row) => {
    const e = rows.find((r) => String(r.seq) === row.dataset.seq);
    if (e) open(e);
  }}
>
  {#if rows.length}
    <table class="console-table">
      <thead><tr><th>Time</th><th>Kind</th><th>Event</th><th>About</th><th>By</th><th>Detail</th></tr></thead>
      <tbody>
        {#each rows as e (e.seq)}
          <tr class:clickable={opens(e)} data-row data-seq={e.seq}>
            <td>{formatTime(e.time)}</td>
            <td class="k">{e.kind}</td>
            <td><span class="t t-{what(e)}">{what(e)}</span></td>
            <td title={e.processId || e.id}>
              {#if e.processId}
                <button type="button" class="link" onclick={() => openTab({ kind: 'run', params: { id: e.processId } })}>{title(e.processId)}</button>
              {:else}
                {shortId(e.id)}
              {/if}
            </td>
            <td>{e.agent || e.actor}</td>
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
  .t-completed,
  .t-ended,
  .t-applied {
    color: var(--ok);
  }
  .t-failed,
  .t-lost,
  .t-quota {
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
  .t-log,
  .k {
    color: var(--muted);
  }
</style>
