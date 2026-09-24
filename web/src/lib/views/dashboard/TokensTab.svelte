<script lang="ts">
  // Token consumption dashboard: overall, over time, by model / agent / action,
  // the runs that consume the most (sub-agents included) and the most expensive calls.
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { provideActions } from '../../shell/workbench.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { engine, errorMessage, formatDate, type Process } from '../../api';
  import { computeStats, type Slice } from '../../tokenStats';

  let { tab }: { tab: Tab } = $props();

  const RANGES: [string, string, number][] = [
    ['24h', 'Last 24 hours', 86_400_000],
    ['7d', 'Last 7 days', 7 * 86_400_000],
    ['30d', 'Last 30 days', 30 * 86_400_000],
    ['all', 'All history', 0],
  ];

  let processes = $state<Process[]>([]);
  let loading = $state(true);
  let error = $state('');
  let range = $state('7d');
  let sort = $state<'total' | 'input' | 'output' | 'calls'>('total');
  let loadedAt = $state(Date.now());

  async function load() {
    loading = true;
    try {
      processes = (await engine.listProcesses({})).processes ?? [];
      loadedAt = Date.now();
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void load();
  });

  provideActions(
    () => tab.id,
    () => [{ id: 'refresh', label: 'Refresh', icon: 'refresh', disabled: loading, run: load }],
  );

  const since = $derived.by(() => {
    const ms = RANGES.find((r) => r[0] === range)?.[2] ?? 0;
    return ms ? loadedAt - ms : 0;
  });
  const stats = $derived(computeStats(processes, since, loadedAt));
  const runs = $derived([...stats.topRuns].sort((a, b) => b[sort] - a[sort]).slice(0, 15));

  const n = (v: number) => Math.round(v).toLocaleString('en-US');
  const compact = (v: number) => (v >= 1e6 ? `${(v / 1e6).toFixed(1)}M` : v >= 1e3 ? `${(v / 1e3).toFixed(1)}k` : String(Math.round(v)));
  const pct = (v: number, of: number) => (of ? Math.round((v / of) * 100) : 0);

  // --- time chart -----------------------------------------------------------------------
  const CH = { w: 720, h: 190, l: 44, r: 8, t: 8, b: 24 };
  const maxBucket = $derived(Math.max(1, ...stats.buckets.map((b) => b.input + b.output)));
  const bw = $derived((CH.w - CH.l - CH.r) / Math.max(1, stats.buckets.length));
  const y = (v: number) => CH.h - CH.b - (v / maxBucket) * (CH.h - CH.t - CH.b);
  const labelEvery = $derived(Math.max(1, Math.ceil(stats.buckets.length / 8)));

  function heavy(r: { ratio: number }) {
    return r.ratio >= 3 && stats.runs >= 4;
  }

  function open(id: string) {
    openTab({ kind: 'run', params: { id } }, { pin: true });
  }
</script>

<div class="editor-page">
  <div class="editor-head">
    <Icon name="coins" size={18} />
    <h2>Token usage</h2>
    <span class="grow"></span>
    <select aria-label="Period" bind:value={range}>
      {#each RANGES as [v, l] (v)}<option value={v}>{l}</option>{/each}
    </select>
  </div>
  {#if error}<div class="alert">{error}</div>{/if}
  {#if loading && !processes.length}
    <p class="empty">Loading…</p>
  {:else if stats.calls === 0}
    <p class="empty">No token consumption in this period.</p>
  {:else}
    <div class="kpis">
      <div class="kpi"><span class="v">{compact(stats.total)}</span><span class="l">tokens in total</span></div>
      <div class="kpi"><span class="v">{compact(stats.input)}</span><span class="l">input ({pct(stats.input, stats.total)}%)</span></div>
      <div class="kpi"><span class="v">{compact(stats.output)}</span><span class="l">output ({pct(stats.output, stats.total)}%)</span></div>
      <div class="kpi"><span class="v">{n(stats.runs)}</span><span class="l">runs</span></div>
      <div class="kpi"><span class="v">{compact(stats.avgPerRun)}</span><span class="l">avg / run (median {compact(stats.medianPerRun)})</span></div>
      <div class="kpi"><span class="v">{n(stats.calls)}</span><span class="l">LLM calls{stats.errors ? ` · ${stats.errors} in error` : ''}</span></div>
    </div>

    <section class="card">
      <h3>Consumption over time <span class="legend"><i class="sw in"></i>input <i class="sw out"></i>output</span></h3>
      <svg viewBox={`0 0 ${CH.w} ${CH.h}`} class="chart" role="img" aria-label="Tokens per period, input and output">
        {#each [0, 0.5, 1] as f (f)}
          <line x1={CH.l} x2={CH.w - CH.r} y1={y(maxBucket * f)} y2={y(maxBucket * f)} class="grid" />
          <text x={CH.l - 6} y={y(maxBucket * f) + 4} text-anchor="end" class="axis">{compact(maxBucket * f)}</text>
        {/each}
        {#each stats.buckets as b, i (b.key)}
          {@const x = CH.l + i * bw}
          <g>
            <rect {x} y={y(b.input + b.output)} width={Math.max(1, bw - 2)} height={y(b.output) - y(b.input + b.output)} class="in" />
            <rect {x} y={y(b.output)} width={Math.max(1, bw - 2)} height={CH.h - CH.b - y(b.output)} class="out" />
            <title>{b.label}: {n(b.input)} in · {n(b.output)} out</title>
          </g>
          {#if i % labelEvery === 0}<text x={x + bw / 2} y={CH.h - 8} text-anchor="middle" class="axis">{b.label}</text>{/if}
        {/each}
      </svg>
    </section>

    <div class="cols">
      {#each [['By model', stats.byModel], ['By agent', stats.byAgent], ['By action', stats.byAction]] as [title, rows] (title)}
        <section class="card">
          <h3>{title}</h3>
          <ul class="bars">
            {#each (rows as Slice[]).slice(0, 8) as s (s.key)}
              <li title={`${n(s.input)} in · ${n(s.output)} out · ${n(s.calls)} calls`}>
                <span class="name">{s.key}</span>
                <span class="track"><span class="fill" style={`width:${pct(s.total, stats.total)}%`}></span></span>
                <span class="val">{compact(s.total)} <span class="hint">{pct(s.total, stats.total)}%</span></span>
              </li>
            {/each}
          </ul>
        </section>
      {/each}
    </div>

    <section class="card">
      <div class="row">
        <h3 class="grow">Runs that consume the most</h3>
        <span class="hint">Sub-agents are counted in their parent run.</span>
      </div>
      <div class="scroll">
        <table>
          <thead>
            <tr>
              <th>Run</th><th>Agent</th><th>Status</th><th>Started</th>
              {#each [['input', 'Input'], ['output', 'Output'], ['total', 'Total'], ['calls', 'Calls']] as [k, l] (k)}
                <th class="num"><button type="button" class="th" class:on={sort === k} onclick={() => (sort = k as typeof sort)}>{l}{sort === k ? ' ↓' : ''}</button></th>
              {/each}
              <th>Share</th>
            </tr>
          </thead>
          <tbody>
            {#each runs as r (r.id)}
              <tr class="clickable">
                <td>
                  <button type="button" class="link" onclick={() => open(r.id)}>{r.title}</button>
                  {#if r.subAgents}<span class="hint"> +{r.subAgents} sub-agent{r.subAgents > 1 ? 's' : ''}</span>{/if}
                </td>
                <td>{r.agent}</td>
                <td><StatusBadge status={r.status} /></td>
                <td>{formatDate(r.createdAt)}</td>
                <td class="num">{n(r.input)}</td>
                <td class="num">{n(r.output)}</td>
                <td class="num"><strong>{n(r.total)}</strong></td>
                <td class="num">{n(r.calls)}</td>
                <td class="share">
                  <span class="track"><span class="fill" style={`width:${pct(r.total, stats.topRuns[0]?.total ?? 1)}%`}></span></span>
                  <span class="hint">{pct(r.total, stats.total)}%</span>
                  {#if heavy(r)}<span class="heavy" title="Well above the average run">{r.ratio.toFixed(1)}× avg</span>{/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>

    <section class="card">
      <h3>Most expensive LLM calls</h3>
      <div class="scroll">
        <table>
          <thead><tr><th>Run</th><th>Action</th><th>Model</th><th class="num">Input</th><th class="num">Output</th><th class="num">Total</th></tr></thead>
          <tbody>
            {#each stats.topCalls as c, i (i)}
              <tr>
                <td><button type="button" class="link" onclick={() => open(c.processId)}>{c.title}</button></td>
                <td>{c.action}{c.step >= 0 ? ` #${c.step + 1}` : ''}</td>
                <td><code>{c.model}</code></td>
                <td class="num">{n(c.input)}</td>
                <td class="num">{n(c.output)}</td>
                <td class="num"><strong>{n(c.total)}</strong></td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>
    <p class="hint">Computed from the runs visible to you. Global quotas per model are set in Platform settings.</p>
  {/if}
</div>

<style>
  .editor-head select {
    width: auto;
  }
  .kpis {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
    gap: 0.6rem;
    margin-bottom: 0.8rem;
  }
  .kpi {
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    padding: 0.55rem 0.8rem;
    display: grid;
  }
  .kpi .v {
    font-size: 1.5rem;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
  }
  .kpi .l {
    color: var(--muted);
    font-size: 0.82rem;
  }
  .chart {
    width: 100%;
    height: auto;
    max-height: 260px;
  }
  .chart .grid {
    stroke: var(--border);
    stroke-dasharray: 3 4;
  }
  .axis {
    fill: var(--muted);
    font-size: 10px;
  }
  .in {
    fill: var(--accent);
  }
  .out {
    fill: var(--ok);
  }
  .legend {
    font-size: 0.8rem;
    font-weight: 400;
    color: var(--muted);
    margin-left: 0.6rem;
  }
  .sw {
    display: inline-block;
    width: 10px;
    height: 10px;
    border-radius: 2px;
    margin: 0 0.25rem 0 0.5rem;
  }
  .sw.in {
    background: var(--accent);
  }
  .sw.out {
    background: var(--ok);
  }
  .cols {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
    gap: 0 1rem;
    align-items: start;
  }
  .bars {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.4rem;
  }
  .bars li {
    display: grid;
    grid-template-columns: minmax(70px, 1fr) 1.4fr auto;
    gap: 0.5rem;
    align-items: center;
  }
  .bars .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-family: var(--mono);
    font-size: 0.85rem;
  }
  .bars .val {
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  .track {
    display: inline-block;
    height: 8px;
    border-radius: 4px;
    background: var(--surface-2);
    overflow: hidden;
    min-width: 60px;
    width: 100%;
  }
  .fill {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .scroll {
    overflow-x: auto;
  }
  td.num,
  th.num {
    text-align: right;
    font-variant-numeric: tabular-nums;
  }
  .share {
    min-width: 170px;
  }
  .share .track {
    width: 90px;
    margin-right: 0.4rem;
  }
  .heavy {
    margin-left: 0.4rem;
    border: 1px solid var(--warn);
    color: var(--warn);
    border-radius: 999px;
    padding: 0 0.4rem;
    font-size: 0.75rem;
    white-space: nowrap;
  }
  .th {
    border: none;
    background: none;
    padding: 0;
    min-height: 0;
    font: inherit;
    color: inherit;
    text-transform: inherit;
  }
  .th.on {
    color: var(--accent);
  }
  .link {
    border: none;
    background: none;
    padding: 0;
    min-height: 0;
    color: var(--accent);
    text-decoration: underline;
    font-weight: 500;
  }
</style>
