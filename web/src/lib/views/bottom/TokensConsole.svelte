<script lang="ts">
  // "Tokens" console: one LLM call per line, read from the gateway's ledger (ADR 0089): whoever asked (engine,
  // assistant, helper, indexer…), with totals.
  import Icon from '../../shell/Icon.svelte';
  import { processes } from '../../stores/live.svelte';
  import { usage, attachUsage, clearUsage, setUsagePlatform } from '../../stores/usage.svelte';
  import { can } from '../../stores/session.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { openUsage } from '../../shell/usageState.svelte';
  import { formatDuration, formatTime, int, shortId, type LLMCall } from '../../api';
  import { rowClick } from '../../actions';
  import { openModelExchange } from '../../shell/modelExchangeState.svelte';
  import { callOf, hasExchange, sourceLabel, stepOf } from '../../tokenStats';

  let source = $state('');
  let processId = $state('');
  let model = $state('');
  let alias = $state('');
  let follow = $state(true);
  let scroller = $state<HTMLDivElement>();

  $effect(() => attachUsage());

  const platform = $derived(can.administer && usage.platform);
  const sources = $derived([...new Set(usage.rows.map((t) => t.source || 'other'))].sort());
  const procs = $derived([...new Set(usage.rows.map((t) => t.processId).filter(Boolean))] as string[]);
  const models = $derived([...new Set(usage.rows.map((t) => t.model).filter(Boolean))].sort() as string[]);
  const aliases = $derived([...new Set(usage.rows.map((t) => t.alias).filter(Boolean))].sort() as string[]);
  const rows = $derived(
    usage.rows.filter(
      (t) =>
        (!source || (t.source || 'other') === source) &&
        (!processId || t.processId === processId) &&
        (!model || t.model === model) &&
        (!alias || t.alias === alias),
    ),
  );
  const totals = $derived(
    rows.reduce(
      (acc, r) => ({
        input: acc.input + int(r.inputTokens),
        output: acc.output + int(r.outputTokens),
        duration: acc.duration + int(r.durationMs),
        errors: acc.errors + (r.error ? 1 : 0),
      }),
      { input: 0, output: 0, duration: 0, errors: 0 },
    ),
  );

  const n = (v: number) => v.toLocaleString('en-US');
  const cols = $derived(platform ? 11 : 10);

  $effect(() => {
    void rows.length;
    if (follow && scroller) scroller.scrollTop = scroller.scrollHeight;
  });

  function name(pid: string) {
    const p = processes.get(pid);
    return p?.title || shortId(pid);
  }
  const label = (r: LLMCall) => `${r.action || sourceLabel(r.source)}${r.processId ? ` #${stepOf(r) + 1}` : ''} · ${r.model ?? ''}`;
</script>

{#snippet prompt(r: LLMCall, value: string)}
  {#if hasExchange(r)}
    <button
      type="button"
      class="link num"
      title="Show the prompt and the answer"
      onclick={(e) => {
        e.stopPropagation();
        openModelExchange({ changeId: r.changeId ?? '', processId: r.processId ?? '', step: stepOf(r), call: callOf(r), label: label(r) });
      }}>{value}</button
    >
  {:else}{value}{/if}
{/snippet}

<div class="console-tools">
  {#if can.administer}
    <select aria-label="Scope" value={platform ? 'platform' : 'mine'} onchange={(e) => setUsagePlatform(e.currentTarget.value === 'platform')} data-no-pin>
      <option value="mine">My calls</option>
      <option value="platform">Whole platform</option>
    </select>
  {/if}
  <select aria-label="Source" bind:value={source} data-no-pin>
    <option value="">All sources</option>
    {#each sources as s (s)}<option value={s}>{sourceLabel(s)}</option>{/each}
  </select>
  <select aria-label="Process" bind:value={processId} data-no-pin>
    <option value="">All processes</option>
    {#each procs as p (p)}<option value={p}>{name(p)}</option>{/each}
  </select>
  <select aria-label="Model" bind:value={model} data-no-pin>
    <option value="">All models</option>
    {#each models as m (m)}<option value={m}>{m}</option>{/each}
  </select>
  <select aria-label="Alias" bind:value={alias} data-no-pin>
    <option value="">All aliases</option>
    {#each aliases as a (a)}<option value={a}>{a}</option>{/each}
  </select>
  <label class="check"><input type="checkbox" bind:checked={follow} /> Follow</label>
  <span class="grow"></span>
  <span class="totals">
    {rows.length} call{rows.length > 1 ? 's' : ''} · input <strong>{n(totals.input)}</strong> · output
    <strong>{n(totals.output)}</strong> · total <strong>{n(totals.input + totals.output)}</strong> · {formatDuration(totals.duration)}
    {#if totals.errors}· <span class="err">{totals.errors} in error</span>{/if}
  </span>
  <button type="button" class="small" onclick={() => openUsage()}>Dashboard</button>
  <button type="button" class="ghost small" title="Clear" aria-label="Clear calls" onclick={clearUsage}><Icon name="clear" size={13} /></button>
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
          <th>Time</th><th>Source</th>{#if platform}<th>Subject</th>{/if}<th>Process</th><th>Agent</th><th>Action</th><th>Model</th>
          <th class="num">Input</th><th class="num">Output</th><th class="num">Duration</th><th>Error</th>
        </tr>
      </thead>
      <tbody>
        {#each rows as r (r.seq)}
          <tr class:clickable={!!r.processId} data-row data-pid={r.processId ?? ''}>
            <td>{formatTime(r.at)}</td>
            <td title={r.kind}>{sourceLabel(r.source)}</td>
            {#if platform}<td>{r.subject ?? ''}</td>{/if}
            <td title={r.processId}>
              {#if r.processId}
                <button type="button" class="link" onclick={() => openTab({ kind: 'run', params: { id: r.processId ?? '' } })}>{name(r.processId)}</button>
              {/if}
            </td>
            <td>{r.agent ?? ''}</td>
            <td>{r.action ?? ''}{r.processId ? ` #${stepOf(r) + 1}` : ''}</td>
            <td title={[r.provider, r.alias && `alias ${r.alias}`].filter(Boolean).join(' · ')}>{r.model ?? ''}</td>
            <td class="num">{@render prompt(r, n(int(r.inputTokens)))}</td>
            <td class="num">{@render prompt(r, n(int(r.outputTokens)))}</td>
            <td class="num">{formatDuration(int(r.durationMs))}</td>
            <td class="err">{r.error ?? ''}</td>
          </tr>
        {/each}
      </tbody>
      <tfoot>
        <tr>
          <th colspan={cols - 4}>Total</th>
          <th class="num">{n(totals.input)}</th>
          <th class="num">{n(totals.output)}</th>
          <th class="num">{formatDuration(totals.duration)}</th>
          <th></th>
        </tr>
      </tfoot>
    </table>
  {:else if usage.error}
    <p class="console-empty err">{usage.error}</p>
  {:else}
    <p class="console-empty">{usage.loading ? 'Loading…' : 'No LLM calls recorded.'}</p>
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
