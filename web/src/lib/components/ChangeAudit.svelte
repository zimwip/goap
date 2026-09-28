<script lang="ts">
  // Audit of a change: its log (ADR 0030: facts, journal records with the scheduling, impact events, in one order),
  // drawn as a flow of events: flow branches fork from the main flow, run side by side (alternatives to compare), and
  // are merged back or dropped. The sources, the flow and the action run are filtered by the server on the columns of
  // the log; the text on the entries shown. Each entry expands to its full record; the trail exports as CSV, and the
  // entries shown as JSON.
  import {
    graph,
    decodeLogEntry,
    executionsFromLog,
    errorMessage,
    formatDate,
    formatDuration,
    formatInt,
    int,
    shortId,
    JAEGER_URL,
    type Change,
    type ExecutionRecord,
    type LogEntry,
  } from '../api';
  import {
    AUDIT_SOURCES,
    JOURNAL_TYPES,
    SOURCE_TYPES,
    buildTrail,
    flowLanes,
    flowParents,
    processSummaries,
    runLabel,
    sourceOfType,
    trailCSV,
    type AuditEntry,
    type AuditSource,
  } from '../auditTrail';
  import { loadRaw, save } from '../shell/storage';
  import { makeContext } from '../items';
  import CallsDetail from './CallsDetail.svelte';
  import StatusBadge from './StatusBadge.svelte';

  let {
    change,
    process = $bindable(''),
    run = $bindable(''),
    onrun,
  }: {
    /** reloaded whenever the change is */
    change: Change;
    /** restricts the trail to one process (its record kinds still filtered by the sources below) */
    process?: string;
    /** restricts the trail to everything done by one action run */
    run?: string;
    /** open a process */
    onrun?: (process: string) => void;
  } = $props();

  // filters: sources (all by default, remembered), flow, process, action run: sent to the server; text: on the
  // entries shown
  const KEY = 'goap.ide.audit.sources.v2';
  const stored = loadRaw(KEY);
  let sources = $state<AuditSource[]>(Array.isArray(stored) ? (stored as AuditSource[]) : AUDIT_SOURCES.map((s) => s.id));
  $effect(() => save(KEY, sources));
  let flow = $state('*');
  let filter = $state('');
  let newestFirst = $state(false);
  let open = $state<string[]>([]);

  let log = $state<LogEntry[]>([]);
  let counts = $state<Record<string, number>>({});
  // the whole change, whatever the filters: the action runs (to name them) and the flow events (forks, merges)
  let runLog = $state<LogEntry[]>([]);
  let flowLog = $state<LogEntry[]>([]);
  // the whole execution journal of the change, to group by process (the process filter and its summary)
  let journalLog = $state<LogEntry[]>([]);
  let error = $state('');
  let loading = $state(false);

  const query = $derived.by(() => {
    const types = sources.flatMap((s) => (s === 'change' ? [] : SOURCE_TYPES[s]));
    return {
      changeId: change.id ?? '',
      types: types.length ? types : ['none.'],
      ...(flow === '*' ? {} : { flows: [flow || 'main'] }),
      ...(run ? { execution: run } : {}),
      ...(process ? { processIds: [process] } : {}),
    };
  });

  $effect(() => {
    const q = query;
    void change;
    if (!q.changeId) return;
    const ctrl = new AbortController();
    loading = true;
    Promise.all([
      graph.listChangeLog(q, ctrl.signal),
      graph.listChangeLog({ changeId: q.changeId, types: ['journal.action'] }, ctrl.signal),
      graph.listChangeLog({ changeId: q.changeId, types: ['fact.flow'] }, ctrl.signal),
      graph.listChangeLog({ changeId: q.changeId, types: JOURNAL_TYPES }, ctrl.signal),
    ])
      .then(([l, r, f, j]) => {
        log = l.entries ?? [];
        counts = l.counts ?? {};
        runLog = r.entries ?? [];
        flowLog = f.entries ?? [];
        journalLog = j.entries ?? [];
        error = '';
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) error = errorMessage(e);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) loading = false;
      });
    return () => ctrl.abort();
  });

  const parents = $derived(flowParents(flowLog));
  // the creation of the change belongs to the main flow, and to no action run
  const withChange = $derived(sources.includes('change') && (flow === '*' || flow === '') && !run);
  const trail = $derived(buildTrail(change, log, parents, withChange));
  const runs = $derived(new Map(runLog.map((l) => [l.id ?? '', decodeLogEntry<ExecutionRecord>(l)])));
  const records = $derived([...runs.values()]);
  /** the whole flow lifecycle, for the legend and the lanes */
  const flowTrail = $derived(buildTrail(change, flowLog, parents, false));
  /** one summary per process of the change: agent / methodology / goal and totals, for the process filter */
  const processGroups = $derived(processSummaries(executionsFromLog(journalLog)));
  const ctx = $derived(makeContext([], change.items ?? []));
  function itemLabel(id: string): string {
    const it = ctx.items.get(id);
    return it ? `${it.kind ?? 'item'}${it.type ? ` ${it.type}` : ''}` : shortId(id);
  }

  const q = $derived(filter.trim().toLowerCase());
  const shown = $derived.by(() => {
    const out = trail.filter((e) => !q || `${e.label} ${e.subject} ${e.summary} ${e.by} ${runLabel(runs.get(e.execution))}`.toLowerCase().includes(q));
    return newestFirst ? out.reverse() : out;
  });
  const count = (s: AuditSource) =>
    s === 'change' ? 1 : Object.entries(counts).reduce((n, [t, c]) => n + (sourceOfType(t) === s ? c : 0), 0);
  const flows = $derived([...parents.keys()]);

  // ---- the flow of events: one lane per flow branch -------------------------------------------------------
  const LANE = 16;
  const PAD = 10;
  const DOT = 13; // y of the dot, from the top of the row
  const BEND = 10;
  const HUES = [28, 145, 340, 265, 175, 300, 100, 0, 200, 60];
  const lanes = $derived(flowLanes(shown));
  const laneWidth = $derived(PAD * 2 + (lanes.count - 1) * LANE);
  const flowOrder = $derived([...parents.keys()]);
  const colorOf = (f: string) => (f ? `hsl(${HUES[Math.max(0, flowOrder.indexOf(f)) % HUES.length]} 62% 50%)` : 'var(--accent)');
  const xOf = (f: string) => PAD + (lanes.lane.get(f) ?? 0) * LANE;
  /** the flows of the change and how they ended */
  const flowStates = $derived.by(() => {
    const m = new Map<string, string>();
    for (const e of flowTrail) {
      if (e.fork) m.set(e.fork, 'open');
      if (e.merge) m.set(e.merge, 'adopted');
      if (e.end) m.set(e.flow, 'discarded');
    }
    return m;
  });

  interface Segment {
    flow: string;
    top: number | null; // null: from the top of the row
    bottom: number | null; // null: to the bottom of the row
  }
  /** the lines crossing row i, the curves joining a forked / merged flow to its parent, and the dot */
  function rowGraph(e: AuditEntry, i: number) {
    const segs: Segment[] = [];
    const joined = e.fork ?? e.merge ?? '';
    for (const [f, [a, b]] of lanes.span) {
      if (i < a || i > b || a === b) continue;
      let top: number | null = i > a ? null : DOT;
      let bottom: number | null = i < b ? null : DOT;
      if (f === joined) {
        // the curve reaches the lane BEND px towards the rest of the flow
        if (i === a) top = DOT + BEND;
        if (i === b) bottom = DOT - BEND;
      }
      segs.push({ flow: f, top, bottom });
    }
    let curve = '';
    if (joined) {
      const [a] = lanes.span.get(joined) ?? [i, i];
      const dir = i === a ? 1 : -1;
      const x1 = xOf(e.flow);
      const x2 = xOf(joined);
      curve = `M${x1},${DOT} Q${x2},${DOT} ${x2},${DOT + dir * BEND}`;
    }
    return { segs, curve, joined, x: xOf(e.flow) };
  }

  const totals = $derived.by(() => {
    const actions = records.filter((r) => r.kind === 'action');
    return {
      actions: actions.length,
      modelCalls: actions.reduce((n, r) => n + (r.modelCalls?.length ?? 0), 0),
      toolCalls: actions.reduce((n, r) => n + (r.toolCalls?.length ?? 0), 0),
      inputTokens: records.reduce((n, r) => n + int(r.inputTokens), 0),
      outputTokens: records.reduce((n, r) => n + int(r.outputTokens), 0),
      people: [...new Set(trail.map((e) => e.by).filter(Boolean))].sort(),
    };
  });

  function toggle(key: string) {
    open = open.includes(key) ? open.filter((k) => k !== key) : [...open, key];
  }

  function toggleSource(s: AuditSource) {
    sources = sources.includes(s) ? sources.filter((x) => x !== s) : [...sources, s];
  }

  function download(name: string, type: string, body: string) {
    const url = URL.createObjectURL(new Blob([body], { type }));
    const a = document.createElement('a');
    a.href = url;
    a.download = name;
    a.click();
    URL.revokeObjectURL(url);
  }
  const base = $derived(`change-${shortId(change.id)}-audit`);
  const exportCSV = () => download(`${base}.csv`, 'text/csv', trailCSV(shown, runs));
  const exportJSON = () =>
    download(
      `${base}.json`,
      'application/json',
      JSON.stringify({ change: change.id, query, entries: log.map((l) => ({ ...l, payload: JSON.parse(l.payload ?? '{}') })) }, null, 2),
    );

  const json = (x: unknown) => JSON.stringify(x, null, 2);
</script>

<div class="audit">
  <div class="stats" aria-label="Totals">
    <span><strong>{trail.length}</strong> entries</span>
    <span><strong>{totals.actions}</strong> action runs</span>
    <span><strong>{totals.modelCalls}</strong> model calls</span>
    {#if totals.toolCalls}<span><strong>{totals.toolCalls}</strong> tool calls</span>{/if}
    <span><strong>{formatInt(totals.inputTokens)} → {formatInt(totals.outputTokens)}</strong> tokens</span>
    <span>actors <strong>{totals.people.join(', ') || '—'}</strong></span>
    <span class="grow"></span>
    <button type="button" class="small" onclick={exportCSV} disabled={!shown.length} title="The entries shown, as CSV">Export CSV</button>
    <button type="button" class="small" onclick={exportJSON} title="The log entries shown (sources, flow, action run), with their payload">Export JSON</button>
  </div>

  <div class="filters">
    {#each AUDIT_SOURCES as s (s.id)}
      <label class="check"><input type="checkbox" checked={sources.includes(s.id)} onchange={() => toggleSource(s.id)} /> {s.label} <span class="n">{count(s.id)}</span></label>
    {/each}
    <span class="grow"></span>
    {#if processGroups.length}
      <select bind:value={process} aria-label="Process">
        <option value="">All processes</option>
        {#each processGroups as g (g.processId)}<option value={g.processId}>{g.agent || shortId(g.processId)} · {shortId(g.processId)}</option>{/each}
      </select>
    {/if}
    {#if flows.length}
      <select bind:value={flow} aria-label="Flow">
        <option value="*">All flows</option>
        <option value="">Main flow</option>
        {#each flows as f (f)}<option value={f}>Flow {shortId(f)}</option>{/each}
      </select>
    {/if}
    <input type="search" placeholder="Filter…" aria-label="Filter the audit trail" bind:value={filter} data-no-pin />
    <button type="button" class="small" onclick={() => (newestFirst = !newestFirst)}>{newestFirst ? 'Newest first' : 'Oldest first'}</button>
  </div>
  {#if flowStates.size}
    <div class="legend" aria-label="Flows">
      <span class="flowchip" style={`--c:${colorOf('')}`}>main</span>
      {#each [...flowStates] as [f, st] (f)}
        <button type="button" class="flowchip" class:sel={flow === f} style={`--c:${colorOf(f)}`} title="Show this flow only" onclick={() => (flow = flow === f ? '*' : f)}>
          flow {shortId(f)} <span class="st">{st}</span>
        </button>
      {/each}
    </div>
  {/if}
  {#if process}
    {@const g = processGroups.find((x) => x.processId === process)}
    <p class="runbar">
      {#if g}
        <strong>{g.agent || shortId(process)}</strong>
        {#if g.methodology}· {g.methodology}{g.version ? ` v${g.version}` : ''}{/if}
        {#if g.planner}· planner {g.planner}{/if}
        {#if g.goal}· goal <code>{g.goal}</code>{/if}
        <StatusBadge status={g.status} />
        · {formatInt(g.inputTokens)} → {formatInt(g.outputTokens)} tok
        · {g.modelCalls} model call{g.modelCalls === 1 ? '' : 's'}
        {#if g.toolCalls}· {g.toolCalls} tool call{g.toolCalls === 1 ? '' : 's'}{/if}
        · {g.actions} action{g.actions === 1 ? '' : 's'}
        · {formatDuration(g.durationMs)}
      {:else}
        Process <code>{shortId(process)}</code>
      {/if}
      <button type="button" class="link" onclick={() => (process = '')}>show all</button>
    </p>
  {/if}
  {#if run}
    <p class="runbar">
      Everything done by <strong>{runLabel(runs.get(run)) || shortId(run)}</strong>
      <button type="button" class="link" onclick={() => (run = '')}>show all</button>
    </p>
  {/if}

  {#if error}<div class="alert">{error}</div>{/if}
  {#if loading && !trail.length}<p class="empty">Loading…</p>{/if}

  {#if shown.length}
    <div class="scroll">
      <table>
        <thead><tr><th class="lanes" style={`width:${laneWidth}px`}><span class="sr">Flow</span></th><th>When</th><th>Source</th><th>What</th><th>Subject</th><th>Details</th><th>Flow</th><th>By</th><th>Action run</th></tr></thead>
        <tbody>
          {#each shown as e, i (e.key)}
            {@const isOpen = open.includes(e.key)}
            {@const gr = rowGraph(e, i)}
            <tr class="entry {e.tone}" class:open={isOpen} onclick={() => toggle(e.key)}>
              <td class="lanes" style={`width:${laneWidth}px`} aria-hidden="true">
                {#each gr.segs as s (s.flow)}
                  <span
                    class="line"
                    style={`left:${xOf(s.flow) - 1}px;top:${s.top ?? 0}px;${s.bottom === null ? 'bottom:0' : `height:${Math.max(0, s.bottom - (s.top ?? 0))}px`};background:${colorOf(s.flow)}`}
                  ></span>
                {/each}
                {#if gr.curve}
                  <svg width={laneWidth} height={DOT + BEND + 2} class="curve"><path d={gr.curve} style={`stroke:${colorOf(gr.joined)}`} /></svg>
                {/if}
                {#if e.end}
                  <span class="cross" style={`left:${gr.x - 6}px;top:${DOT - 8}px;color:${colorOf(e.flow)}`}>✕</span>
                {:else}
                  <span
                    class="dot"
                    class:merge={!!e.merge}
                    class:fork={!!e.fork}
                    style={`left:${gr.x - 5}px;top:${DOT - 5}px;border-color:${colorOf(e.flow)};background:${e.source === 'flow' || e.source === 'action' || e.source === 'approval' ? colorOf(e.flow) : 'var(--surface)'}`}
                  ></span>
                {/if}
              </td>
              <td class="nowrap" title={e.at}>{formatDate(e.at)}</td>
              <td><span class="src {e.source}">{e.source}</span></td>
              <td class="nowrap">{e.label}</td>
              <td class="mono">{e.subject}</td>
              <td class="summary">{e.summary}</td>
              <td class="mono">{e.flow ? shortId(e.flow) : 'main'}</td>
              <td>{e.by || '—'}</td>
              <td>
                {#if e.execution && runs.has(e.execution)}
                  <button type="button" class="link" title="Show everything this run did" onclick={(ev) => { ev.stopPropagation(); run = e.execution; }}>{runLabel(runs.get(e.execution))}</button>
                {:else if e.execution}
                  <span class="muted">{shortId(e.execution)}</span>
                {/if}
              </td>
            </tr>
            {#if isOpen}
              <tr class="detail">
                <td class="lanes" style={`width:${laneWidth}px`} aria-hidden="true">
                  {#each gr.segs as s (s.flow)}
                    {#if s.bottom === null}<span class="line" style={`left:${xOf(s.flow) - 1}px;top:0;bottom:0;background:${colorOf(s.flow)}`}></span>{/if}
                  {/each}
                </td>
                <td colspan="8">{@render details(e)}</td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>
  {:else if !loading && !error}
    <p class="empty">{trail.length ? 'No entry matches the filters.' : 'Nothing recorded yet.'}</p>
  {/if}
</div>

{#snippet details(e: AuditEntry)}
  <dl class="meta">
    <dt>At</dt><dd>{e.at}</dd>
    {#if e.by}<dt>By</dt><dd>{e.by}</dd>{/if}
    <dt>Flow</dt><dd class="mono">{e.flow || 'main'}</dd>
    {#if e.execution}
      <dt>Action run</dt>
      <dd>
        <code>{e.execution}</code>
        {#if runs.has(e.execution)}
          {runLabel(runs.get(e.execution))}
          <button type="button" class="link" onclick={() => (run = e.execution)}>show this run</button>
        {/if}
      </dd>
    {/if}
  </dl>
  {#if e.record}
    {@const r = e.record}
    <dl class="meta">
      {#if r.processId}
        <dt>Process</dt>
        <dd>
          <button type="button" class="link mono" onclick={() => onrun?.(r.processId ?? '')}>{shortId(r.processId)}</button>
          {r.agent ?? ''}{r.methodology ? ` · ${r.methodology}${r.methodologyVersion ? ` v${r.methodologyVersion}` : ''}` : ''}{r.planner ? ` · planner ${r.planner}` : ''}{r.goal ? ` · goal ${r.goal}` : ''}
          {#if r.parentProcessId} · sub-agent of <code>{shortId(r.parentProcessId)}</code>{/if}
          <button type="button" class="link" onclick={() => (process = r.processId ?? '')}>show only this process</button>
        </dd>
      {/if}
      {#if r.plan?.length}<dt>Plan</dt><dd>{r.plan.join(' → ')}</dd>{/if}
      {#if r.actionKind}<dt>Action kind</dt><dd>{r.actionKind}{r.specialization ? ` (specialization of ${r.action})` : ''}</dd>{/if}
      {#if r.effectsMet !== undefined}<dt>Effects</dt><dd>{r.effectsMet ? 'met' : 'not met'}</dd>{/if}
      {#if int(r.durationMs)}<dt>Duration</dt><dd>{formatDuration(r.durationMs)}{r.endedAt ? ` (ended ${formatDate(r.endedAt)})` : ''}</dd>{/if}
      {#if int(r.inputTokens) || int(r.outputTokens)}<dt>Tokens</dt><dd>{formatInt(r.inputTokens)} → {formatInt(r.outputTokens)}</dd>{/if}
      {#if r.boardBefore !== undefined}<dt>Blackboard</dt><dd>{r.boardBefore} → {r.boardAfter ?? r.boardBefore} items</dd>{/if}
      {#if r.traceId}<dt>Trace</dt><dd><a href="{JAEGER_URL}/trace/{r.traceId}" target="_blank" rel="noreferrer">{r.traceId}</a></dd>{/if}
    </dl>
    {#if r.before || r.after}
      {@const names = [...new Set([...Object.keys(r.before ?? {}), ...Object.keys(r.after ?? {})])].filter((n) => r.before?.[n] !== r.after?.[n]).sort()}
      {#if names.length && r.after}
        <p class="sub">Conditions changed: {#each names as n (n)}<code class="cond">{n} {r.before?.[n] ? 'true' : 'false'} → {r.after?.[n] ? 'true' : 'false'}</code>{/each}</p>
      {/if}
    {/if}
    <CallsDetail modelCalls={r.modelCalls} toolCalls={r.toolCalls} />
    {#if r.reads?.length}<p class="sub">Read {r.reads.length} node version(s): {#each r.reads as n (`${n.id}@${n.version}`)}<code class="cond">{shortId(n.id)}@{n.version}</code>{/each}</p>{/if}
    {#if r.items?.length}
      <p class="sub">Produced {r.items.length} item(s):
        {#each r.items as it (it)}
          {@const ci = ctx.items.get(it)}
          <code class="cond" class:superseded={ci?.status === 'superseded'}>{shortId(it)} {itemLabel(it)}{ci?.status ? ` · ${ci.status}` : ''}</code>
        {/each}
      </p>
    {/if}
    {#if r.output}<p class="sub">Output</p><pre>{r.output}</pre>{/if}
    {#if r.error}<p class="sub">Error</p><pre class="error">{r.error}</pre>{/if}
  {/if}
  <details>
    <summary>Raw record</summary>
    <pre>{json(e.record ?? e.event ?? e.item ?? change)}</pre>
  </details>
{/snippet}

<style>
  td.lanes,
  th.lanes {
    position: relative;
    padding: 0;
    min-width: 0;
  }
  td.lanes .line {
    position: absolute;
    width: 2px;
  }
  td.lanes .dot {
    position: absolute;
    width: 10px;
    height: 10px;
    border-radius: 50%;
    border: 2px solid;
    box-sizing: border-box;
  }
  td.lanes .dot.merge,
  td.lanes .dot.fork {
    outline: 2px solid var(--surface);
    width: 12px;
    height: 12px;
    margin: -1px 0 0 -1px;
  }
  td.lanes .cross {
    position: absolute;
    font-size: 13px;
    font-weight: 700;
    line-height: 1;
  }
  td.lanes .curve {
    position: absolute;
    left: 0;
    top: 0;
    overflow: visible;
  }
  td.lanes .curve path {
    fill: none;
    stroke-width: 2;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
  }
  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: 0.35rem;
    margin-bottom: 0.5rem;
  }
  .flowchip {
    border: 1px solid var(--c);
    color: var(--c);
    background: none;
    border-radius: 999px;
    padding: 0 0.55rem;
    font-size: 0.8rem;
    font-family: var(--mono);
    cursor: pointer;
  }
  .flowchip.sel {
    background: var(--c);
    color: var(--surface);
  }
  .flowchip .st {
    font-family: inherit;
    opacity: 0.8;
  }
  .stats,
  .filters {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.4rem 0.9rem;
    margin-bottom: 0.5rem;
  }
  .stats {
    color: var(--muted);
  }
  .stats strong {
    color: var(--text);
  }
  .filters input[type='search'] {
    max-width: 200px;
  }
  .filters select {
    width: auto;
  }
  .n {
    color: var(--muted);
    font-size: 0.8em;
  }
  .runbar {
    background: var(--accent-soft);
    border-radius: 6px;
    padding: 0.3rem 0.6rem;
    margin: 0 0 0.5rem;
  }
  .scroll {
    overflow-x: auto;
  }
  .nowrap {
    white-space: nowrap;
  }
  .muted {
    color: var(--muted);
  }
  .summary {
    max-width: 30rem;
  }
  tr.entry {
    cursor: pointer;
  }
  tr.entry:hover td,
  tr.entry.open td {
    background: var(--hover);
  }
  tr.entry.error td:nth-child(2) {
    box-shadow: inset 3px 0 var(--danger);
  }
  tr.entry.warn td:nth-child(2) {
    box-shadow: inset 3px 0 var(--warn);
  }
  tr.entry.ok td:nth-child(2) {
    box-shadow: inset 3px 0 var(--ok);
  }
  tr.detail td {
    background: var(--surface-2);
  }
  .src {
    display: inline-block;
    border-radius: 999px;
    padding: 0 0.5rem;
    font-size: 0.75rem;
    font-family: var(--mono);
    border: 1px solid var(--border);
  }
  .src.action,
  .src.approval {
    border-color: var(--accent);
    color: var(--accent);
  }
  .src.impact {
    border-color: var(--ok);
    color: var(--ok);
  }
  .src.flow {
    border-color: var(--warn);
    color: var(--warn);
  }
  .meta {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.15rem 0.8rem;
    margin: 0 0 0.4rem;
  }
  .meta dt {
    color: var(--muted);
  }
  .meta dd {
    margin: 0;
  }
  .sub {
    margin: 0.5rem 0 0.2rem;
    font-weight: 600;
  }
  .cond {
    margin-left: 0.35rem;
    font-weight: normal;
  }
  .cond.superseded {
    text-decoration: line-through;
    opacity: 0.6;
  }
  pre {
    max-height: 22rem;
    overflow: auto;
    margin: 0.2rem 0 0;
  }
</style>
