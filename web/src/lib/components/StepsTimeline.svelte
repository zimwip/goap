<script lang="ts">
  // The steps of a run, one row per step slot. When a slot was executed several times (retries or
  // incremental progress of the same action, or a relaunched run restarting from that step) the
  // row shows the current run and a "×N runs" chip; a click lists the runs, a click on a run its
  // full detail. Steps of the earlier runs of a relaunch chain are shown as shared history.
  import { formatDate, formatDuration, formatInt, formatTime, int, shortId, type Process, type Step } from '../api';
  import { offsetOf, runKind, type RunKind } from '../flowChain';
  import StatusBadge from './StatusBadge.svelte';

  let {
    steps = [],
    processId = '',
    chain = [],
    onopenprocess,
    liveLogs = [],
    onrelaunch,
  }: {
    /** steps of the run being viewed */
    steps?: Step[];
    processId?: string;
    /** every run of its relaunch chain (the viewed run included) */
    chain?: Process[];
    /** opens another run (a sub-agent, a run of the chain) */
    onopenprocess?: (id: string) => void;
    /** live-received logs, attached to their step */
    liveLogs?: { time?: string; level?: string; message?: string; step?: number }[];
    /** offers a "Relaunch from here" action on finished steps of the viewed run */
    onrelaunch?: (step: number, reason: string) => Promise<void> | void;
  } = $props();

  interface Run {
    key: string;
    procId: string;
    proc?: Process;
    step: Step;
    /** index of the step in its own run */
    own: number;
    /** index of the step in the chain */
    abs: number;
    current: boolean;
    kind: RunKind;
    slot: number;
  }
  interface Slot {
    slot: number;
    runs: Run[];
    primary: Run;
  }

  let relaunching = $state<string | null>(null);
  let reason = $state('');
  let busy = $state(false);
  let relaunchError = $state('');
  let openSlots = $state<Record<number, boolean>>({});
  let openRuns = $state<Record<string, boolean>>({});

  async function relaunch(step: number) {
    busy = true;
    relaunchError = '';
    try {
      await onrelaunch?.(step, reason.trim());
      relaunching = null;
      reason = '';
    } catch (err) {
      relaunchError = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

  function duration(s: Step): string {
    if (!s.startedAt || !s.endedAt) return '';
    const ms = new Date(s.endedAt).getTime() - new Date(s.startedAt).getTime();
    if (!Number.isFinite(ms) || ms < 0) return '';
    return formatDuration(ms);
  }

  function stepState(s: Step): 'error' | 'ok' | 'partial' | 'pending' {
    if (s.error) return 'error';
    if (!s.endedAt) return 'pending';
    return s.effectsMet ? 'ok' : 'partial';
  }

  const LABEL = { error: 'error', ok: 'effects reached', partial: 'effects not reached', pending: 'in progress' };
  const KIND_BADGE: Partial<Record<RunKind, string>> = { open: 'open', adopted: 'adopted', discarded: 'discarded', replaced: 'superseded' };

  function logsOf(r: Run) {
    const own = r.step.logs ?? [];
    if (!r.current) return own;
    const idx = r.step.index ?? r.own;
    const seen = new Set(own.map((l) => `${l.time}|${l.message}`));
    const extra = liveLogs.filter((l) => l.step === idx && !seen.has(`${l.time}|${l.message}`));
    return [...own, ...extra];
  }

  const slots = $derived.by((): Slot[] => {
    const lookup = new Map(chain.map((p) => [p.id ?? '', p]));
    const cur = lookup.get(processId);
    const runs: Run[] = [];
    // repeated consecutive executions of an action share the slot of the first one
    const add = (procId: string, proc: Process | undefined, list: Step[], off: number, current: boolean) => {
      let slot = -1;
      let prev = '';
      list.forEach((st, i) => {
        const abs = off + (st.index ?? i);
        if (st.action !== prev || slot < 0) slot = abs;
        prev = st.action ?? '';
        runs.push({ key: `${procId}:${i}`, procId, proc, step: st, own: i, abs, current, kind: proc ? runKind(proc) : 'main', slot });
      });
    };
    add(processId, cur, steps, cur ? offsetOf(cur, lookup) : 0, true);
    for (const p of chain) {
      if (p.id === processId || p.parentId) continue;
      add(p.id ?? '', p, p.steps ?? [], offsetOf(p, lookup), false);
    }
    const bySlot = new Map<number, Run[]>();
    for (const r of runs) bySlot.set(r.slot, [...(bySlot.get(r.slot) ?? []), r]);
    const rank: Record<RunKind, number> = { main: 3, adopted: 3, open: 2, replaced: 1, discarded: 0 };
    return [...bySlot.entries()]
      .sort((a, b) => a[0] - b[0])
      .map(([slot, rs]) => {
        rs.sort((a, b) => (a.step.startedAt ?? '').localeCompare(b.step.startedAt ?? ''));
        const own = rs.filter((r) => r.current);
        const primary = own.length
          ? own[own.length - 1]
          : [...rs].sort((a, b) => rank[b.kind] - rank[a.kind] || (b.step.startedAt ?? '').localeCompare(a.step.startedAt ?? ''))[0];
        return { slot, runs: rs, primary };
      });
  });

  const runLabel = (r: Run) => (r.current ? 'this run' : r.proc?.title || shortId(r.procId));
</script>

{#snippet detail(r: Run)}
  {@const s = r.step}
  {@const st = stepState(s)}
  {@const logs = logsOf(r)}
  {#if s.error}
    <pre class="error">{s.error}</pre>
  {/if}
  {#if s.childProcessIds?.length}
    <div class="children">
      Sub-agents:
      {#each s.childProcessIds as c (c)}
        <button type="button" class="link mono" onclick={() => onopenprocess?.(c)}>{shortId(c)}</button>
      {/each}
    </div>
  {/if}
  {#if s.llmCalls?.length}
    <details>
      <summary>LLM calls ({s.llmCalls.length})</summary>
      <table class="calls">
        <thead>
          <tr><th>Provider</th><th>Model</th><th class="num">Input</th><th class="num">Output</th><th class="num">Duration</th><th>Error</th></tr>
        </thead>
        <tbody>
          {#each s.llmCalls as c, k (k)}
            <tr class:err={!!c.error}>
              <td>{c.provider}</td>
              <td><code>{c.model}</code></td>
              <td class="num">{formatInt(c.inputTokens)}</td>
              <td class="num">{formatInt(c.outputTokens)}</td>
              <td class="num">{formatDuration(c.durationMs)}</td>
              <td>{c.error ?? ''}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </details>
  {/if}
  {#if s.toolCalls?.length}
    <details>
      <summary>Tool calls ({s.toolCalls.length})</summary>
      <table class="calls">
        <thead><tr><th>Tool</th><th class="num">Duration</th><th>Error</th></tr></thead>
        <tbody>
          {#each s.toolCalls as c, k (k)}
            <tr class:err={!!c.error}>
              <td><code>{c.name}</code></td>
              <td class="num">{formatDuration(c.durationMs)}</td>
              <td>{c.error ?? ''}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </details>
  {/if}
  {#if logs.length}
    <details open={st === 'pending' || st === 'error'}>
      <summary>Log ({logs.length})</summary>
      <ol class="logs">
        {#each logs as l, k (k)}
          <li class="lvl-{l.level || 'info'}">
            <span class="t">{formatTime(l.time)}</span>
            <span class="lv">{l.level || 'info'}</span>
            <span class="m">{l.message}</span>
          </li>
        {/each}
      </ol>
    </details>
  {/if}
  {#if s.output}
    <details>
      <summary>Output</summary>
      <pre>{s.output}</pre>
    </details>
  {/if}
{/snippet}

{#snippet relaunchForm(r: Run)}
  {#if relaunching === r.key}
    <div class="relaunch">
      <p class="hint">The outputs of this step and of what followed are marked stale until you adopt or discard the relaunched flow.</p>
      <input type="text" placeholder="Why relaunch? (optional)" bind:value={reason} />
      {#if relaunchError}<div class="alert">{relaunchError}</div>{/if}
      <div class="row">
        <button type="button" class="primary" disabled={busy} onclick={() => relaunch(r.step.index ?? r.own)}>Relaunch</button>
        <button type="button" disabled={busy} onclick={() => (relaunching = null)}>Cancel</button>
      </div>
    </div>
  {/if}
{/snippet}

{#snippet head(r: Run, extra?: { count: number })}
  {@const s = r.step}
  {@const st = stepState(s)}
  {@const tokensIn = int(s.usage?.inputTokens)}
  {@const tokensOut = int(s.usage?.outputTokens)}
  <span class="idx">#{r.abs + 1}</span>
  <code class="action">{s.action}</code>
  <span class="st">{LABEL[st]}</span>
  {#if extra}<span class="chip-runs" title="This step was executed several times">×{extra.count} runs</span>{/if}
  {#if !r.current}
    <span class="tag" title="Executed by another run of the chain">{runLabel(r)}</span>
  {/if}
  {#if s.sandbox}<span class="tag" title="Execution sandbox">⧉ {s.sandbox}</span>{/if}
  <span class="grow"></span>
  {#if tokensIn || tokensOut}
    <span class="usage" title="Input / output tokens">{formatInt(tokensIn)} → {formatInt(tokensOut)} tok</span>
  {/if}
  {#if s.usage?.llmCalls}<span class="hint">{s.usage.llmCalls} LLM</span>{/if}
  {#if s.usage?.toolCalls}<span class="hint">{s.usage.toolCalls} tool{s.usage.toolCalls > 1 ? 's' : ''}</span>{/if}
  {#if s.approvedBy}<span class="hint">decided by {s.approvedBy}</span>{/if}
  {#if s.items?.length}<span class="hint">{s.items.length} item{s.items.length > 1 ? 's' : ''}</span>{/if}
  <span class="hint" title={formatDate(s.startedAt)}>{duration(s)}</span>
  {#if onrelaunch && r.current && s.endedAt}
    <button type="button" class="link" title="Restart the run from this step on a new flow branch" onclick={() => { relaunching = r.key; relaunchError = ''; }}>Relaunch from here</button>
  {/if}
{/snippet}

{#if slots.length}
  <ol class="timeline">
    {#each slots as sl (sl.slot)}
      {@const r = sl.primary}
      {@const st = stepState(r.step)}
      {@const multi = sl.runs.length > 1}
      <li class="{st} {r.current ? '' : 'shared'}">
        <div class="dot" aria-hidden="true"></div>
        <div class="body">
          {#if multi}
            <div class="row head">
              <button
                type="button"
                class="toggle"
                aria-expanded={!!openSlots[sl.slot]}
                aria-label={openSlots[sl.slot] ? 'Hide the runs of this step' : 'Show the runs of this step'}
                onclick={() => (openSlots[sl.slot] = !openSlots[sl.slot])}
              >{openSlots[sl.slot] ? '▾' : '▸'}</button>
              {@render head(r, { count: sl.runs.length })}
            </div>
            {@render relaunchForm(r)}
            {#if openSlots[sl.slot]}
              <ul class="runs">
                {#each sl.runs as x (x.key)}
                  {@const xs = stepState(x.step)}
                  <li class="run {xs}" class:primary={x === r}>
                    <button type="button" class="runline" aria-expanded={!!openRuns[x.key]} onclick={() => (openRuns[x.key] = !openRuns[x.key])}>
                      <span class="caret">{openRuns[x.key] ? '▾' : '▸'}</span>
                      <span class="rn">{runLabel(x)}</span>
                      {#if KIND_BADGE[x.kind]}<StatusBadge status={KIND_BADGE[x.kind]} />{/if}
                      <code>{x.step.action}</code>
                      <span class="hint">{formatTime(x.step.startedAt)}</span>
                      <span class="hint">{duration(x.step)}</span>
                      <span class="st {xs}">{LABEL[xs]}</span>
                      {#if x.step.items?.length}<span class="hint">{x.step.items.length} item{x.step.items.length > 1 ? 's' : ''}</span>{/if}
                      {#if x.step.error}<span class="err-text" title={x.step.error}>{x.step.error.slice(0, 60)}</span>{/if}
                    </button>
                    {#if !x.current}
                      <button type="button" class="link mono open" onclick={() => onopenprocess?.(x.procId)}>open run</button>
                    {:else if onrelaunch && x.step.endedAt && x !== r}
                      <button type="button" class="link open" onclick={() => { relaunching = x.key; relaunchError = ''; }}>Relaunch from here</button>
                    {/if}
                    {#if x !== r}{@render relaunchForm(x)}{/if}
                    {#if openRuns[x.key]}
                      <div class="rundetail">{@render detail(x)}</div>
                    {/if}
                  </li>
                {/each}
              </ul>
            {/if}
          {:else}
            <div class="row head">
              {@render head(r)}
            </div>
            {@render relaunchForm(r)}
            {@render detail(r)}
          {/if}
        </div>
      </li>
    {/each}
  </ol>
{:else}
  <p class="empty">No steps executed.</p>
{/if}

<style>
  .toggle {
    background: none;
    border: none;
    padding: 0 0.2rem;
    color: var(--muted);
    cursor: pointer;
    font-size: 0.9rem;
  }
  .chip-runs {
    font-size: 0.78rem;
    background: var(--accent-soft);
    color: var(--accent);
    border-radius: 999px;
    padding: 0 0.45rem;
    white-space: nowrap;
  }
  li.shared {
    opacity: 0.75;
  }
  .runs {
    list-style: none;
    margin: 0.3rem 0 0;
    padding: 0 0 0 0.6rem;
    border-left: 2px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
  }
  .runs > li.run {
    display: block;
    padding: 0;
  }
  .runs > li.run::before {
    display: none;
  }
  .runline {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 0.45rem;
    width: 100%;
    background: none;
    border: none;
    text-align: left;
    padding: 0.15rem 0.3rem;
    border-radius: var(--radius-sm);
    cursor: pointer;
    color: inherit;
  }
  .runline:hover {
    background: var(--hover);
  }
  li.run.primary > .runline {
    background: var(--surface-2);
  }
  .caret {
    color: var(--muted);
  }
  .rn {
    font-weight: 600;
    max-width: 14rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .runline .st.ok {
    color: var(--ok);
  }
  .runline .st.partial {
    color: var(--warn);
  }
  .runline .st.error {
    color: var(--danger);
  }
  .err-text {
    color: var(--danger);
    font-size: 0.85em;
  }
  .open {
    margin-left: 1.2rem;
    font-size: 0.85rem;
  }
  .rundetail {
    margin: 0.2rem 0 0.4rem 1.2rem;
  }
  .relaunch {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin: 6px 0;
    padding: 8px;
    border-left: 3px solid var(--warn);
  }
  .timeline {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    display: grid;
    grid-template-columns: 1rem 1fr;
    gap: 0.6rem;
    position: relative;
    padding-bottom: 0.7rem;
  }
  li:not(:last-child)::before {
    content: '';
    position: absolute;
    left: calc(0.5rem - 1px);
    top: 1.1rem;
    bottom: 0;
    width: 2px;
    background: var(--border);
  }
  .dot {
    width: 0.7rem;
    height: 0.7rem;
    margin: 0.3rem 0 0 0.15rem;
    border-radius: 50%;
    background: var(--muted);
  }
  .ok .dot {
    background: var(--ok);
  }
  .partial .dot {
    background: var(--warn);
  }
  .error .dot {
    background: var(--danger);
  }
  .pending .dot {
    background: var(--accent);
    animation: pulse 1.2s ease-in-out infinite;
  }
  @keyframes pulse {
    50% {
      opacity: 0.3;
    }
  }
  .body {
    min-width: 0;
  }
  .head {
    gap: 0.45rem;
  }
  .idx {
    color: var(--muted);
    font-size: 0.85rem;
  }
  .action {
    font-weight: 600;
  }
  .st {
    font-size: 0.82rem;
    color: var(--muted);
  }
  .ok .st {
    color: var(--ok);
  }
  .partial .st {
    color: var(--warn);
  }
  .error .st {
    color: var(--danger);
  }
  .tag {
    font-size: 0.78rem;
    font-family: var(--mono);
    background: var(--info-soft);
    color: var(--info);
    border-radius: 3px;
    padding: 0 0.3rem;
  }
  .usage {
    font-size: 0.82rem;
    font-variant-numeric: tabular-nums;
    color: var(--text);
  }
  pre.error {
    margin-top: 0.3rem;
    color: var(--danger);
    background: var(--danger-soft);
    border-color: transparent;
  }
  .children {
    margin-top: 0.25rem;
    font-size: 0.9em;
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    align-items: baseline;
  }
  details {
    margin-top: 0.25rem;
  }
  summary {
    cursor: pointer;
    font-size: 0.88rem;
    color: var(--muted);
  }
  details pre {
    margin-top: 0.3rem;
    max-height: 22rem;
  }
  .calls {
    margin-top: 0.25rem;
    font-size: 0.9em;
  }
  tr.err td {
    color: var(--danger);
  }
  .logs {
    list-style: none;
    margin: 0.25rem 0 0;
    padding: 0.3rem 0.5rem;
    background: var(--surface-2);
    border-radius: var(--radius-sm);
    font-family: var(--mono);
    font-size: 0.85em;
    max-height: 16rem;
    overflow: auto;
  }
  .logs li {
    display: flex;
    gap: 0.5rem;
    padding: 0;
  }
  .logs li::before {
    display: none;
  }
  .t {
    color: var(--muted);
    flex: none;
  }
  .lv {
    flex: none;
    width: 3.2rem;
    text-transform: uppercase;
    font-size: 0.85em;
    color: var(--muted);
  }
  .lvl-warn .lv,
  .lvl-warn .m {
    color: var(--warn);
  }
  .lvl-error .lv,
  .lvl-error .m {
    color: var(--danger);
  }
  .m {
    white-space: pre-wrap;
    word-break: break-word;
  }
</style>
