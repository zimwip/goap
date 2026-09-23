<script lang="ts">
  // "Tokens" console: one LLM call per line, with totals.
  import Icon from '../../shell/Icon.svelte';
  import { live, clearTokens, processes } from '../../stores/live.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { formatDuration, formatTime, shortId } from '../../api';
  import { rowClick } from '../../actions';

  let processId = $state('');
  let model = $state('');
  let follow = $state(true);
  let scroller = $state<HTMLDivElement>();

  const procs = $derived([...new Set(live.tokens.map((t) => t.processId))]);
  const models = $derived([...new Set(live.tokens.map((t) => t.model).filter(Boolean))].sort());
  const rows = $derived(live.tokens.filter((t) => (!processId || t.processId === processId) && (!model || t.model === model)));
  const totals = $derived(
    rows.reduce(
      (acc, r) => ({
        input: acc.input + r.input,
        output: acc.output + r.output,
        duration: acc.duration + r.durationMs,
        errors: acc.errors + (r.error ? 1 : 0),
      }),
      { input: 0, output: 0, duration: 0, errors: 0 },
    ),
  );

  const n = (v: number) => v.toLocaleString('en-US');

  $effect(() => {
    void rows.length;
    if (follow && scroller) scroller.scrollTop = scroller.scrollHeight;
  });

  function name(pid: string) {
    const p = processes.get(pid);
    return p?.title || shortId(pid);
  }
</script>

<div class="console-tools">
  <select aria-label="Process" bind:value={processId} data-no-pin>
    <option value="">All processes</option>
    {#each procs as p (p)}<option value={p}>{name(p)}</option>{/each}
  </select>
  <select aria-label="Model" bind:value={model} data-no-pin>
    <option value="">All models</option>
    {#each models as m (m)}<option value={m}>{m}</option>{/each}
  </select>
  <label class="check"><input type="checkbox" bind:checked={follow} /> Follow</label>
  <span class="grow"></span>
  <span class="totals">
    {rows.length} call{rows.length > 1 ? 's' : ''} · input <strong>{n(totals.input)}</strong> · output
    <strong>{n(totals.output)}</strong> · total <strong>{n(totals.input + totals.output)}</strong> · {formatDuration(totals.duration)}
    {#if totals.errors}· <span class="err">{totals.errors} in error</span>{/if}
  </span>
  <button type="button" class="ghost small" title="Clear" aria-label="Clear calls" onclick={clearTokens}><Icon name="clear" size={13} /></button>
</div>
<div
  class="console-scroll"
  bind:this={scroller}
  use:rowClick={(row) => row.dataset.pid && openTab({ kind: 'run', params: { id: row.dataset.pid } })}
>
  {#if rows.length}
    <table class="console-table">
      <thead>
        <tr>
          <th>Time</th><th>Process</th><th>Agent</th><th>Action</th><th>Model</th>
          <th class="num">Input</th><th class="num">Output</th><th class="num">Duration</th><th>Error</th>
        </tr>
      </thead>
      <tbody>
        {#each rows as r (r.key)}
          <tr class="clickable" data-row data-pid={r.processId}>
            <td>{formatTime(r.time)}</td>
            <td title={r.processId}>
              <button type="button" class="link" onclick={() => openTab({ kind: 'run', params: { id: r.processId } })}>{name(r.processId)}</button>
            </td>
            <td>{r.agent}</td>
            <td>{r.action} #{r.step + 1}</td>
            <td title={r.provider}>{r.model}</td>
            <td class="num">{n(r.input)}</td>
            <td class="num">{n(r.output)}</td>
            <td class="num">{formatDuration(r.durationMs)}</td>
            <td class="err">{r.error}</td>
          </tr>
        {/each}
      </tbody>
      <tfoot>
        <tr>
          <th colspan="5">Total</th>
          <th class="num">{n(totals.input)}</th>
          <th class="num">{n(totals.output)}</th>
          <th class="num">{formatDuration(totals.duration)}</th>
          <th></th>
        </tr>
      </tfoot>
    </table>
  {:else}
    <p class="console-empty">No LLM calls recorded (they appear alongside run steps).</p>
  {/if}
</div>

<style>
  .totals {
    font-size: 0.92em;
    color: var(--muted);
  }
  .totals strong {
    color: var(--text);
    font-variant-numeric: tabular-nums;
  }
  .err {
    color: var(--danger);
  }
  tfoot th {
    position: sticky;
    bottom: 0;
    background: var(--surface-2);
    font-family: var(--mono);
    text-transform: none;
    color: var(--text);
  }
</style>
