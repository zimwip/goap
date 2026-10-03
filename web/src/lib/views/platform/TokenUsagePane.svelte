<script lang="ts">
  // "Token usage" section of the settings modal: the caller's own consumption (overall, over
  // time, by model / agent / action, the runs that consume the most, the most expensive calls).
  // Platform administrators can switch to the consumption of the whole platform, with the quotas.
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { openSettings } from '../../shell/settingsState.svelte';
  import { closeUsage } from '../../shell/usageState.svelte';
  import { models, errorMessage, formatDate, type CatalogModel } from '../../api';
  import { hasAnyRole, me } from '../../stores/session.svelte';
  import { live, processes as known, refreshProcesses } from '../../stores/live.svelte';
  import { prefs } from '../../stores/preferences.svelte';
  import { computeStats, type Dim, type Slice } from '../../tokenStats';

  const RANGES: [string, string, number][] = [
    ['24h', 'Last 24 hours', 86_400_000],
    ['7d', 'Last 7 days', 7 * 86_400_000],
    ['30d', 'Last 30 days', 30 * 86_400_000],
    ['all', 'All history', 0],
  ];

  let loading = $state(true);
  let error = $state('');
  let range = $state(prefs.values.usagePeriod);
  let sort = $state<'total' | 'input' | 'output' | 'calls'>('total');
  let loadedAt = $state(Date.now());
  let catalog = $state<CatalogModel[]>([]);
  let quotaError = $state('');
  const isAdmin = $derived(hasAnyRole('admin'));
  // only administrators may look beyond their own consumption
  let scope = $state<'mine' | 'platform'>(prefs.values.usageScope);
  const platform = $derived(isAdmin && scope === 'platform');

  async function load() {
    loading = true;
    try {
      await refreshProcesses();
      error = live.processesError;
      // global quotas are administered (and readable) by platform admins only
      if (platform) {
        try {
          catalog = (await models.listCatalog()).models ?? [];
          quotaError = '';
        } catch (e) {
          quotaError = errorMessage(e);
        }
      }
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void platform;
    void load();
  });

  // The processes are the store the platform event stream keeps: the figures follow the runs as they go.
  const processes = $derived([...known.values()].filter((p) => platform || p.initiator?.subject === me()));
  $effect(() => {
    void live.events.length;
    loadedAt = Date.now();
  });

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
  const DIMS: [Dim, string][] = [['model', 'By model'], ['agent', 'By agent'], ['action', 'By action']];
  const PALETTE = ['#4f7fd8', '#e0873a', '#3fa672', '#a96ad0', '#d9534f', '#2fa3b8', '#c2a02c'];
  const OTHER = '#8a8f98';
  const MAX_SERIES = PALETTE.length;
  let dim = $state<Dim>('model');
  // stacked series: the biggest values of the axis over the period, the rest merged as "other"
  const series = $derived.by(() => {
    const tot = new Map<string, number>();
    for (const b of stats.buckets) for (const [k, v] of Object.entries(b.by[dim])) tot.set(k, (tot.get(k) ?? 0) + v.input + v.output);
    const ranked = [...tot.entries()].sort((a, b) => b[1] - a[1]).map(([k]) => k);
    const shown = ranked.slice(0, ranked.length > MAX_SERIES ? MAX_SERIES - 1 : MAX_SERIES);
    const list = shown.map((key, i) => ({ key, color: PALETTE[i] }));
    if (ranked.length > shown.length) list.push({ key: 'other', color: OTHER });
    return list;
  });
  // per period, one stack for the input and one for the output, side by side
  const stacks = $derived(
    stats.buckets.map((b) => {
      const shown = new Set(series.map((s) => s.key));
      const stack = (side: 'input' | 'output') => {
        let acc = 0;
        const segs = series.map((s) => {
          const v =
            s.key === 'other' && !b.by[dim]['other']
              ? Object.entries(b.by[dim]).reduce((a, [k, x]) => a + (shown.has(k) ? 0 : x[side]), 0)
              : (b.by[dim][s.key]?.[side] ?? 0);
          const seg = { ...s, v, from: acc };
          acc += v;
          return seg;
        });
        return { segs, total: acc };
      };
      return { b, input: stack('input'), output: stack('output') };
    }),
  );
  const maxBucket = $derived(Math.max(1, ...stacks.map((s) => Math.max(s.input.total, s.output.total))));
  const bw = $derived((CH.w - CH.l - CH.r) / Math.max(1, stats.buckets.length));
  const y = (v: number) => CH.h - CH.b - (v / maxBucket) * (CH.h - CH.t - CH.b);
  const labelEvery = $derived(Math.max(1, Math.ceil(stats.buckets.length / 8)));

  const num = (v: string | number | undefined) => Number(v ?? 0) || 0;
  const quotas = $derived(
    catalog
      .filter((m) => m.enabled && num(m.quotaTokens) > 0)
      .map((m) => ({ m, quota: num(m.quotaTokens), used: num(m.usedTokens), pct: (num(m.usedTokens) / num(m.quotaTokens)) * 100 }))
      .sort((a, b) => b.pct - a.pct),
  );
  const unmetered = $derived(catalog.filter((m) => m.enabled && num(m.quotaTokens) === 0 && num(m.usedTokens) > 0));
  const RESET: Record<string, string> = { day: 'resets daily (UTC)', month: 'resets monthly (UTC)', total: 'never resets' };

  function heavy(r: { ratio: number }) {
    return r.ratio >= 3 && stats.runs >= 4;
  }

  function open(id: string) {
    closeUsage();
    openTab({ kind: 'run', params: { id } }, { pin: true });
  }
</script>

<div class="pane">
  <div class="head">
    {#if isAdmin}
      <select aria-label="Scope" bind:value={scope}>
        <option value="mine">My consumption</option>
        <option value="platform">Whole platform</option>
      </select>
    {:else}
      <span class="hint">Your own consumption.</span>
    {/if}
    <span class="grow"></span>
    <button type="button" class="small refresh" disabled={loading} onclick={load}><Icon name="refresh" size={13} />Refresh</button>
    <select aria-label="Period" bind:value={range}>
      {#each RANGES as [v, l] (v)}<option value={v}>{l}</option>{/each}
    </select>
  </div>
  {#if error}<div class="alert">{error}</div>{/if}
  {#if platform}
    <section class="card">
      <div class="row">
        <h3 class="grow">Quota usage</h3>
        <button type="button" class="small" onclick={() => { closeUsage(); openSettings('catalog'); }}>Manage quotas</button>
      </div>
      {#if quotaError}
        <p class="alert">{quotaError}</p>
      {:else if quotas.length === 0}
        <p class="empty">No model has a global quota. Set one in Models & quotas.</p>
      {:else}
        <ul class="quotas">
          {#each quotas as q (q.m.provider + '/' + q.m.model)}
            <li>
              <code class="qname">{q.m.provider}/{q.m.model}</code>
              <span class="qbar" class:warn={q.pct >= 80 && q.pct < 100} class:full={q.pct >= 100} role="progressbar" aria-valuenow={Math.min(100, Math.round(q.pct))} aria-valuemin="0" aria-valuemax="100" aria-label={`Quota of ${q.m.model}`}>
                <span style={`width:${Math.min(100, q.pct)}%`}></span>
              </span>
              <span class="qval">{compact(q.used)} / {compact(q.quota)} <span class="hint">{Math.round(q.pct)}%</span></span>
              <span class="hint qreset">{RESET[q.m.quotaPeriod ?? 'month'] ?? ''}</span>
              {#if q.pct >= 100}<span class="heavy full">exhausted</span>{:else if q.pct >= 80}<span class="heavy">near limit</span>{/if}
            </li>
          {/each}
        </ul>
      {/if}
      {#if unmetered.length}
        <p class="hint">Without quota: {unmetered.map((m) => `${m.provider}/${m.model} (${compact(num(m.usedTokens))})`).join(', ')}.</p>
      {/if}
      <p class="hint">Quotas count the tokens of the current period across all users; the period figures above cover the selected range only.</p>
    </section>
  {/if}
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
      <div class="row">
        <h3 class="grow">Consumption over time</h3>
        <select aria-label="Aggregation axis" bind:value={dim}>
          {#each DIMS as [v, l] (v)}<option value={v}>{l}</option>{/each}
        </select>
      </div>
      <div class="legend">
        {#each series as s (s.key)}<span class="lg"><i class="sw" style={`background:${s.color}`}></i>{s.key}</span>{/each}
        <span class="lg note">left bar: input · right bar (lighter): output</span>
      </div>
      <svg viewBox={`0 0 ${CH.w} ${CH.h}`} class="chart" role="img" aria-label={`Tokens per period, stacked ${dim}`}>
        {#each [0, 0.5, 1] as f (f)}
          <line x1={CH.l} x2={CH.w - CH.r} y1={y(maxBucket * f)} y2={y(maxBucket * f)} class="grid" />
          <text x={CH.l - 6} y={y(maxBucket * f) + 4} text-anchor="end" class="axis">{compact(maxBucket * f)}</text>
        {/each}
        {#each stacks as st, i (st.b.key)}
          {@const x = CH.l + i * bw}
          <g>
            {#each [['input', st.input, 0], ['output', st.output, 1]] as [side, stk, k] (side as string)}
              {#each (stk as typeof st.input).segs as g (g.key)}
                {#if g.v > 0}<rect x={x + (k as number) * (bw / 2)} y={y(g.from + g.v)} width={Math.max(1, bw / 2 - 1.5)} height={y(g.from) - y(g.from + g.v)} fill={g.color} class:out={side === 'output'}><title>{st.b.label} · {side} · {g.key}: {n(g.v)}</title></rect>{/if}
              {/each}
            {/each}
            <title>{st.b.label}: {n(st.b.input)} in · {n(st.b.output)} out</title>
          </g>
          {#if i % labelEvery === 0}<text x={x + bw / 2} y={CH.h - 8} text-anchor="middle" class="axis">{st.b.label}</text>{/if}
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
    <p class="hint">{platform ? 'Computed from every run of the platform.' : 'Computed from the runs you started.'}</p>
  {/if}
</div>

<style>
  .pane {
    display: grid;
    gap: 0.8rem;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .grow {
    flex: 1;
  }
  .refresh {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    white-space: nowrap;
  }
  .head select {
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
  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: 0.2rem 0.9rem;
    font-size: 0.8rem;
    color: var(--muted);
    margin: 0.4rem 0;
  }
  .out {
    opacity: 0.6;
  }
  .note {
    margin-left: auto;
  }
  .lg {
    display: inline-flex;
    align-items: center;
    max-width: 16rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .row select {
    width: auto;
  }
  .sw {
    display: inline-block;
    width: 10px;
    height: 10px;
    border-radius: 2px;
    margin-right: 0.3rem;
    flex: none;
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
  .quotas {
    list-style: none;
    margin: 0 0 0.5rem;
    padding: 0;
    display: grid;
    gap: 0.5rem;
  }
  .quotas li {
    display: grid;
    grid-template-columns: minmax(140px, 1.2fr) 2fr auto minmax(110px, auto) auto;
    gap: 0.6rem;
    align-items: center;
  }
  .qname {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .qbar {
    height: 10px;
    border-radius: 5px;
    background: var(--surface-2);
    overflow: hidden;
  }
  .qbar span {
    display: block;
    height: 100%;
    background: var(--ok);
  }
  .qbar.warn span {
    background: var(--warn);
  }
  .qbar.full span {
    background: var(--danger);
  }
  .qval {
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  .qreset {
    font-size: 0.8rem;
  }
  .heavy.full {
    border-color: var(--danger);
    color: var(--danger);
  }
  @media (max-width: 800px) {
    .quotas li {
      grid-template-columns: 1fr auto;
    }
    .qbar {
      grid-column: 1 / -1;
    }
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
