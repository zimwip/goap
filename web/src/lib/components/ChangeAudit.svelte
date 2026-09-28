<script lang="ts">
  // Audit of a change: every event and action, oldest first, from the execution journal, the impact log and the
  // blackboard facts (lib/auditTrail.ts). Each entry expands to its full record; the trail can be filtered by
  // source, flow, action run and text, and exported (CSV, or the raw logs as JSON).
  import {
    graph,
    errorMessage,
    formatDate,
    formatDuration,
    formatInt,
    int,
    shortId,
    JAEGER_URL,
    type Change,
    type ExecutionRecord,
    type ImpactEvent,
  } from '../api';
  import { AUDIT_SOURCES, buildTrail, runLabel, trailCSV, type AuditEntry, type AuditSource } from '../auditTrail';
  import { loadRaw, save } from '../shell/storage';

  let {
    change,
    onjournal,
    onrun,
  }: {
    /** reloaded whenever the change is */
    change: Change;
    /** open the execution journal on a record */
    onjournal?: (record: string) => void;
    /** open a process */
    onrun?: (process: string) => void;
  } = $props();

  let events = $state<ImpactEvent[]>([]);
  let records = $state<ExecutionRecord[]>([]);
  let error = $state('');
  let loading = $state(false);

  $effect(() => {
    const id = change.id ?? '';
    void change;
    if (!id) return;
    const ctrl = new AbortController();
    loading = true;
    Promise.all([graph.listChangeEvents(id, ctrl.signal), graph.listExecutions(id, [], ctrl.signal)])
      .then(([e, j]) => {
        events = e.events ?? [];
        records = j.records ?? [];
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

  const trail = $derived(buildTrail(change, events, records));
  const runs = $derived(new Map(records.map((r) => [r.id ?? '', r])));

  // filters: sources (plans hidden by default, remembered), flow, action run, text
  const KEY = 'goap.ide.audit.sources';
  const stored = loadRaw(KEY);
  let sources = $state<AuditSource[]>(Array.isArray(stored) ? (stored as AuditSource[]) : AUDIT_SOURCES.map((s) => s.id).filter((s) => s !== 'plan'));
  $effect(() => save(KEY, sources));
  let flow = $state('*');
  let run = $state('');
  let filter = $state('');
  let newestFirst = $state(false);
  let open = $state<string[]>([]);

  const flows = $derived([...new Set(trail.map((e) => e.flow).filter(Boolean))]);
  const q = $derived(filter.trim().toLowerCase());
  const shown = $derived.by(() => {
    const out = trail.filter(
      (e) =>
        sources.includes(e.source) &&
        (flow === '*' || e.flow === flow) &&
        (!run || e.execution === run) &&
        (!q || `${e.label} ${e.subject} ${e.summary} ${e.by} ${runLabel(runs.get(e.execution))}`.toLowerCase().includes(q)),
    );
    return newestFirst ? out.reverse() : out;
  });
  const count = (s: AuditSource) => trail.filter((e) => e.source === s).length;

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
    download(`${base}.json`, 'application/json', JSON.stringify({ change, impactEvents: events, journal: records, facts: change.items ?? [] }, null, 2));

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
    <button type="button" class="small" onclick={exportJSON} title="The raw logs of the change: journal, impact log, facts">Export JSON</button>
  </div>

  <div class="filters">
    {#each AUDIT_SOURCES as s (s.id)}
      <label class="check"><input type="checkbox" checked={sources.includes(s.id)} onchange={() => toggleSource(s.id)} /> {s.label} <span class="n">{count(s.id)}</span></label>
    {/each}
    <span class="grow"></span>
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
        <thead><tr><th>When</th><th>Source</th><th>What</th><th>Subject</th><th>Details</th><th>Flow</th><th>By</th><th>Action run</th></tr></thead>
        <tbody>
          {#each shown as e (e.key)}
            {@const isOpen = open.includes(e.key)}
            <tr class="entry {e.tone}" class:open={isOpen} onclick={() => toggle(e.key)}>
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
          <button type="button" class="link" onclick={() => onjournal?.(e.execution)}>open in the journal</button>
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
    {#if r.modelCalls?.length}
      <p class="sub">Model calls</p>
      <table class="calls">
        <thead><tr><th>Provider</th><th>Model</th><th class="num">Input</th><th class="num">Output</th><th class="num">Duration</th><th>Error</th></tr></thead>
        <tbody>
          {#each r.modelCalls as c, k (k)}
            <tr><td>{c.provider}</td><td><code>{c.model}</code></td><td class="num">{formatInt(c.inputTokens)}</td><td class="num">{formatInt(c.outputTokens)}</td><td class="num">{formatDuration(c.durationMs)}</td><td>{c.error ?? ''}</td></tr>
          {/each}
        </tbody>
      </table>
    {/if}
    {#if r.toolCalls?.length}
      <p class="sub">Tool calls</p>
      <table class="calls">
        <thead><tr><th>Tool</th><th class="num">Duration</th><th>Error</th></tr></thead>
        <tbody>
          {#each r.toolCalls as c, k (k)}<tr><td><code>{c.name}</code></td><td class="num">{formatDuration(c.durationMs)}</td><td>{c.error ?? ''}</td></tr>{/each}
        </tbody>
      </table>
    {/if}
    {#if r.reads?.length}<p class="sub">Read {r.reads.length} node version(s): {#each r.reads as n (`${n.id}@${n.version}`)}<code class="cond">{shortId(n.id)}@{n.version}</code>{/each}</p>{/if}
    {#if r.items?.length}<p class="sub">Produced {r.items.length} fact(s): {#each r.items as it (it)}<code class="cond">{shortId(it)}</code>{/each}</p>{/if}
    {#if r.output}<p class="sub">Output</p><pre>{r.output}</pre>{/if}
    {#if r.error}<p class="sub">Error</p><pre class="error">{r.error}</pre>{/if}
  {/if}
  <details>
    <summary>Raw record</summary>
    <pre>{json(e.record ?? e.event ?? e.item ?? change)}</pre>
  </details>
{/snippet}

<style>
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
  tr.entry.error td:first-child {
    box-shadow: inset 3px 0 var(--danger);
  }
  tr.entry.warn td:first-child {
    box-shadow: inset 3px 0 var(--warn);
  }
  tr.entry.ok td:first-child {
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
  .calls {
    width: auto;
  }
  .num {
    text-align: right;
  }
  pre {
    max-height: 22rem;
    overflow: auto;
    margin: 0.2rem 0 0;
  }
</style>
