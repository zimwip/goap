<script lang="ts">
  import { untrack } from 'svelte';
  // "Token usage" section of the settings modal, built on the gateway's ledger of LLM calls (ADR 0089): the caller's
  // own consumption (overall, over time, by model / alias / source / agent / action, the runs that consume the most,
  // the most expensive calls); assistant, helper and indexer calls are in it too. Platform administrators can switch
  // to the consumption of the whole platform (by subject too), with the quotas.
  import { can } from '../../stores/session.svelte';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { openSettings } from '../../shell/settingsState.svelte';
  import { closeUsage } from '../../shell/usageState.svelte';
  import { models, errorMessage, formatDate, int, shortId, type CatalogModel, type LLMCall, type UsageFilter, type UsageGroup, type UsageSummaryRow } from '../../api';
  import { processes as known } from '../../stores/live.svelte';
  import { prefs } from '../../stores/preferences.svelte';
  import { usage, attachUsage, setUsagePlatform, usageSubject } from '../../stores/usage.svelte';
  import { bucketsOf, runsOf, slicesOf, sourceLabel, stepOf, topCalls, totalsOf, type Dim, type Slice } from '../../tokenStats';

  const RANGES: [string, string, number][] = [
    ['24h', 'Last 24 hours', 86_400_000],
    ['7d', 'Last 7 days', 7 * 86_400_000],
    ['30d', 'Last 30 days', 30 * 86_400_000],
    ['all', 'All history', 0],
  ];

  interface Loaded {
    by: Record<Dim, UsageSummaryRow[]>;
    series: UsageSummaryRow[];
    hourly: boolean;
    from: number;
    to: number;
    calls: LLMCall[];
  }

  let loading = $state(true);
  let error = $state('');
  let range = $state(prefs.values.usagePeriod);
  let sort = $state<'total' | 'input' | 'output' | 'calls'>('total');
  let data = $state<Loaded | null>(null);
  let catalog = $state<CatalogModel[]>([]);
  let quotaError = $state('');
  const isAdmin = $derived(can.administer);
  // only administrators may look beyond their own consumption
  const platform = $derived(isAdmin && usage.platform);
  let epoch = 0;

  $effect(() => {
    const wantPlatform = can.administer && prefs.values.usageScope === 'platform';
    return untrack(() => {
      setUsagePlatform(wantPlatform);
      return attachUsage();
    });
  });

  async function load() {
    const mine = ++epoch;
    loading = true;
    const ms = RANGES.find((r) => r[0] === range)?.[2] ?? 0;
    const to = Date.now();
    const from = ms ? to - ms : 0;
    const hourly = !!ms && ms <= 2 * 86_400_000;
    const filter: UsageFilter = { subject: usageSubject(), from: from ? new Date(from).toISOString() : undefined };
    const dims: [Dim, UsageGroup][] = [['model', 'model'], ['alias', 'alias'], ['source', 'source'], ['agent', 'agent'], ['action', 'action'], ['process', 'process'], ['subject', 'subject']];
    try {
      const [sums, series, list] = await Promise.all([
        Promise.all(dims.map(([, g]) => models.usageSummary(filter, g))),
        models.usageSummary(filter, hourly ? 'hour' : 'day'),
        models.listUsage({ ...filter, limit: 5000 }),
      ]);
      if (mine !== epoch) return;
      const by = {} as Record<Dim, UsageSummaryRow[]>;
      dims.forEach(([d], i) => (by[d] = sums[i].rows ?? []));
      data = { by, series: series.rows ?? [], hourly, from, to, calls: list.calls ?? [] };
      error = '';
    } catch (e) {
      if (mine === epoch) error = errorMessage(e);
    }
    // global quotas are administered (and readable) by platform admins only
    if (platform) {
      try {
        catalog = (await models.listCatalog()).models ?? [];
        quotaError = '';
      } catch (e) {
        quotaError = errorMessage(e);
      }
    }
    if (mine === epoch) loading = false;
  }

  // The figures follow the ledger: a batch of new calls (the feed polls while the pane is open) reloads them.
  $effect(() => {
    void range;
    void platform;
    void usage.version;
    const t = setTimeout(() => void load(), 400);
    return () => clearTimeout(t);
  });

  const totals = $derived(totalsOf(data?.by.model));
  const buckets = $derived(data ? bucketsOf(data.series, data.hourly, data.from, data.to) : []);
  const BREAKDOWNS = $derived<[Dim, string][]>([
    ['model', 'By model'],
    ['alias', 'By alias'],
    ['source', 'By source'],
    ['agent', 'By agent'],
    ['action', 'By action'],
    ...(platform ? ([['subject', 'By subject']] as [Dim, string][]) : []),
  ]);
  const slices = $derived((d: Dim): Slice[] => slicesOf(d, data?.by[d]));
  const runInfo = $derived(runsOf(data?.by.process));
  const runs = $derived([...runInfo.runs].sort((a, b) => b[sort] - a[sort]).slice(0, 15));
  const top = $derived(topCalls(data?.calls ?? []));

  const n = (v: number) => Math.round(v).toLocaleString('en-US');
  const compact = (v: number) => (v >= 1e6 ? `${(v / 1e6).toFixed(1)}M` : v >= 1e3 ? `${(v / 1e3).toFixed(1)}k` : String(Math.round(v)));
  const pct = (v: number, of: number) => (of ? Math.round((v / of) * 100) : 0);

  // --- time chart -----------------------------------------------------------------------
  const CH = { w: 720, h: 190, l: 44, r: 8, t: 8, b: 24 };
  const maxBucket = $derived(Math.max(1, ...buckets.map((b) => Math.max(b.input, b.output))));
  const bw = $derived((CH.w - CH.l - CH.r) / Math.max(1, buckets.length));
  const y = (v: number) => CH.h - CH.b - (v / maxBucket) * (CH.h - CH.t - CH.b);
  const labelEvery = $derived(Math.max(1, Math.ceil(buckets.length / 8)));

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
    return r.ratio >= 3 && runInfo.runs.length >= 4;
  }

  function open(id: string) {
    closeUsage();
    openTab({ kind: 'run', params: { id } }, { pin: true });
  }
  const title = (id: string) => known.get(id)?.title || known.get(id)?.goal || shortId(id);
</script>

<div class="pane">
  <div class="head">
    {#if isAdmin}
      <select aria-label="Scope" value={platform ? 'platform' : 'mine'} onchange={(e) => setUsagePlatform(e.currentTarget.value === 'platform')}>
        <option value="mine">My consumption</option>
        <option value="platform">Whole platform</option>
      </select>
    {:else}
      <span class="hint">Your own consumption.</span>
    {/if}
    <span class="grow"></span>
    <button type="button" class="small refresh" disabled={loading} onclick={() => void load()}><Icon name="refresh" size={13} />Refresh</button>
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
  {#if loading && !data}
    <p class="empty">Loading…</p>
  {:else if totals.calls === 0}
    <p class="empty">No token consumption in this period.</p>
  {:else}
    <div class="kpis">
      <div class="kpi"><span class="v">{compact(totals.total)}</span><span class="l">tokens in total</span></div>
      <div class="kpi"><span class="v">{compact(totals.input)}</span><span class="l">input ({pct(totals.input, totals.total)}%)</span></div>
      <div class="kpi"><span class="v">{compact(totals.output)}</span><span class="l">output ({pct(totals.output, totals.total)}%)</span></div>
      <div class="kpi"><span class="v">{n(runInfo.runs.length)}</span><span class="l">runs</span></div>
      <div class="kpi"><span class="v">{compact(runInfo.avg)}</span><span class="l">avg / run</span></div>
      <div class="kpi"><span class="v">{n(totals.calls)}</span><span class="l">LLM calls{totals.errors ? ` · ${totals.errors} in error` : ''}</span></div>
    </div>

    <section class="card">
      <div class="row">
        <h3 class="grow">Consumption over time</h3>
        <span class="hint">{data?.hourly ? 'per hour' : 'per day'}, UTC</span>
      </div>
      <div class="legend">
        <span class="lg"><i class="sw" style="background:var(--accent)"></i>input</span>
        <span class="lg"><i class="sw out" style="background:var(--accent)"></i>output</span>
      </div>
      <svg viewBox={`0 0 ${CH.w} ${CH.h}`} class="chart" role="img" aria-label="Tokens per period">
        {#each [0, 0.5, 1] as f (f)}
          <line x1={CH.l} x2={CH.w - CH.r} y1={y(maxBucket * f)} y2={y(maxBucket * f)} class="grid" />
          <text x={CH.l - 6} y={y(maxBucket * f) + 4} text-anchor="end" class="axis">{compact(maxBucket * f)}</text>
        {/each}
        {#each buckets as b, i (b.key)}
          {@const x = CH.l + i * bw}
          <g>
            {#if b.input > 0}<rect x={x} y={y(b.input)} width={Math.max(1, bw / 2 - 1.5)} height={y(0) - y(b.input)} fill="var(--accent)"><title>{b.label} · input: {n(b.input)}</title></rect>{/if}
            {#if b.output > 0}<rect x={x + bw / 2} y={y(b.output)} width={Math.max(1, bw / 2 - 1.5)} height={y(0) - y(b.output)} fill="var(--accent)" class="out"><title>{b.label} · output: {n(b.output)}</title></rect>{/if}
            <title>{b.label}: {n(b.input)} in · {n(b.output)} out · {n(b.calls)} calls</title>
          </g>
          {#if i % labelEvery === 0}<text x={x + bw / 2} y={CH.h - 8} text-anchor="middle" class="axis">{b.label}</text>{/if}
        {/each}
      </svg>
    </section>

    <div class="cols">
      {#each BREAKDOWNS as [dim, title] (dim)}
        <section class="card">
          <h3>{title}</h3>
          <ul class="bars">
            {#each slices(dim).slice(0, 8) as s (s.key)}
              <li title={`${n(s.input)} in · ${n(s.output)} out · ${n(s.calls)} calls${s.errors ? ` · ${n(s.errors)} in error` : ''}`}>
                <span class="name">{s.label}</span>
                <span class="track"><span class="fill" style={`width:${pct(s.total, totals.total)}%`}></span></span>
                <span class="val">{compact(s.total)} <span class="hint">{pct(s.total, totals.total)}%</span></span>
              </li>
            {/each}
          </ul>
        </section>
      {/each}
    </div>

    <section class="card">
      <div class="row">
        <h3 class="grow">Runs that consume the most</h3>
        <span class="hint">Each run counts its own calls; its sub-agents are listed on their own.</span>
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
            {#each runs as r (r.key)}
              {@const p = known.get(r.key)}
              <tr class="clickable">
                <td><button type="button" class="link" onclick={() => open(r.key)}>{title(r.key)}</button></td>
                <td>{p?.agent ?? ''}</td>
                <td>{#if p?.status}<StatusBadge status={String(p.status)} />{/if}</td>
                <td>{formatDate(p?.createdAt)}</td>
                <td class="num">{n(r.input)}</td>
                <td class="num">{n(r.output)}</td>
                <td class="num"><strong>{n(r.total)}</strong></td>
                <td class="num">{n(r.calls)}</td>
                <td class="share">
                  <span class="track"><span class="fill" style={`width:${pct(r.total, runInfo.runs[0]?.total ?? 1)}%`}></span></span>
                  <span class="hint">{pct(r.total, totals.total)}%</span>
                  {#if heavy(r)}<span class="heavy" title="Well above the average run">{r.ratio.toFixed(1)}× avg</span>{/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>

    <section class="card">
      <div class="row">
        <h3 class="grow">Most expensive LLM calls</h3>
        <span class="hint">among the latest 5,000 calls of the period</span>
      </div>
      <div class="scroll">
        <table>
          <thead><tr><th>Source</th><th>Run</th><th>Action</th><th>Model</th><th class="num">Input</th><th class="num">Output</th><th class="num">Total</th></tr></thead>
          <tbody>
            {#each top as c (c.seq)}
              <tr>
                <td>{sourceLabel(c.source)}</td>
                <td>{#if c.processId}<button type="button" class="link" onclick={() => open(c.processId ?? '')}>{title(c.processId)}</button>{/if}</td>
                <td>{c.action ?? ''}{c.processId ? ` #${stepOf(c) + 1}` : ''}</td>
                <td><code>{c.model ?? ''}</code></td>
                <td class="num">{n(int(c.inputTokens))}</td>
                <td class="num">{n(int(c.outputTokens))}</td>
                <td class="num"><strong>{n(int(c.inputTokens) + int(c.outputTokens))}</strong></td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>
    <p class="hint">{platform ? 'From the ledger of LLM calls of the whole platform.' : 'From the ledger of your LLM calls: runs, assistant, helper.'}</p>
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
  .lg {
    display: inline-flex;
    align-items: center;
    max-width: 16rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
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
