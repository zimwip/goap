<script lang="ts">
  // The impact log of a change (ADR 0029): every operation on its change impacts, oldest first, with its caller:
  // who (principal or component), which action run (the journal record: agent, step, action) and which flow.
  import { graph, errorMessage, formatDate, shortId, type Change, type ExecutionRecord, type ImpactEvent, type NodeRef } from '../api';

  let {
    change,
    onjournal,
  }: {
    /** reloaded whenever the change is */
    change: Change;
    /** open the execution journal on a record */
    onjournal?: (record: string) => void;
  } = $props();

  let events = $state<ImpactEvent[]>([]);
  let runs = $state(new Map<string, ExecutionRecord>());
  let error = $state('');
  let filter = $state('');

  $effect(() => {
    const id = change.id ?? '';
    void change;
    if (!id) return;
    const ctrl = new AbortController();
    Promise.all([graph.listChangeEvents(id, ctrl.signal), graph.listExecutions(id, [], ctrl.signal)])
      .then(([e, j]) => {
        events = e.events ?? [];
        runs = new Map((j.records ?? []).map((r) => [r.id ?? '', r]));
        error = '';
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) error = errorMessage(e);
      });
    return () => ctrl.abort();
  });

  // the key of each change impact, from its declaration
  const keys = $derived.by(() => {
    const m = new Map<string, string>();
    for (const cn of change.nodes ?? []) m.set(cn.id ?? '', cn.key ?? '');
    for (const e of events) if (e.state?.key) m.set(e.impactId ?? '', e.state.key);
    return m;
  });

  const v = (r: NodeRef | undefined) => (r ? `v${r.version ?? 0}` : '');

  function what(e: ImpactEvent): string {
    switch (e.op) {
      case 'declared':
      case 'imported':
        return `${e.state?.intent ?? ''}${e.state?.pre ? ` from ${v(e.state.pre)}` : ''}${e.state?.rationale ? ` — ${e.state.rationale}` : ''}`;
      case 'written':
        return v(e.post);
      case 'reviewed':
      case 'discarded':
        return `${e.review?.status ?? ''}${e.review?.comment ? ` — ${e.review.comment}` : ''}`;
      case 'adopted':
        return `flow ${shortId(e.flow)} adopted${e.stale?.length ? `, replaces ${e.stale.length} run${e.stale.length > 1 ? 's' : ''}` : ''}`;
      case 'landed':
        return `landed as ${v(e.landed)}`;
      case 'rebased':
        return `pre moved to ${v(e.pre)}, to re-check`;
    }
    return '';
  }

  function run(e: ImpactEvent): string {
    const r = runs.get(e.execution ?? '');
    if (!r) return e.execution ? `run ${shortId(e.execution)}` : '';
    return [r.agent, r.step !== undefined ? `step ${r.step + 1}` : '', r.specialization || r.action].filter(Boolean).join(' · ');
  }

  const q = $derived(filter.trim().toLowerCase());
  const shown = $derived(
    q ? events.filter((e) => `${e.op} ${keys.get(e.impactId ?? '') ?? ''} ${e.by} ${run(e)} ${what(e)}`.toLowerCase().includes(q)) : events,
  );
</script>

<div class="row head">
  <h3 class="grow">Impact log <span class="count">{events.length}</span></h3>
  {#if events.length}<input class="filter" type="search" placeholder="Filter…" aria-label="Filter the impact log" bind:value={filter} data-no-pin />{/if}
</div>
{#if error}<div class="alert">{error}</div>{/if}
{#if shown.length}
  <div class="scroll">
    <table>
      <thead><tr><th>#</th><th>When</th><th>Operation</th><th>Change impact</th><th>What</th><th>Flow</th><th>By</th><th>Action run</th></tr></thead>
      <tbody>
        {#each shown as e (e.id)}
          <tr class:flow={!!e.flow}>
            <td class="muted">{e.seq}</td>
            <td class="nowrap">{formatDate(e.at)}</td>
            <td><span class="op {e.op}">{e.op}</span></td>
            <td class="mono">{e.impactId ? (keys.get(e.impactId) ?? shortId(e.impactId)) : '—'}</td>
            <td class="what">{what(e)}</td>
            <td class="mono">{e.flow ? shortId(e.flow) : 'main'}</td>
            <td>{e.by || '—'}</td>
            <td>
              {#if e.execution}
                <button type="button" class="link" title="Open the execution journal on this run" onclick={() => onjournal?.(e.execution ?? '')}>{run(e)}</button>
              {:else}
                <span class="muted">—</span>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{:else if !error}
  <p class="empty">{events.length ? 'No matching event.' : 'No event yet.'}</p>
{/if}

<style>
  .head {
    align-items: center;
    gap: 0.4rem;
    margin-bottom: 0.4rem;
  }
  .head h3 {
    margin: 0;
  }
  .filter {
    max-width: 200px;
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
  .what {
    max-width: 28rem;
  }
  tr.flow td {
    background: var(--surface-2);
  }
  .op {
    display: inline-block;
    border-radius: 999px;
    padding: 0 0.5rem;
    font-size: 0.78rem;
    font-family: var(--mono);
    border: 1px solid var(--border);
  }
  .op.declared,
  .op.imported {
    color: var(--accent);
    border-color: var(--accent);
  }
  .op.written,
  .op.landed {
    color: var(--ok);
    border-color: var(--ok);
  }
  .op.reviewed,
  .op.adopted {
    color: var(--text);
  }
  .op.discarded,
  .op.rebased {
    color: var(--warn);
    border-color: var(--warn);
  }
</style>
