<script lang="ts">
  // Comparing and deciding the options of a change (ADR 0009 §3, ADR 0032 §6): they are compared on the nodes they
  // changed, evaluated, then one is selected (its versions join the change branch) and the others are rejected.
  // Opening an option, looking at it and working on it (the active option) is the scope bar's.
  import { graph, errorMessage, shortId, type Flow, type OptionComparison, type JsonValue } from '../api';
  import StatusBadge from './StatusBadge.svelte';

  let {
    changeId,
    closed = false,
    onchange,
    onview,
  }: { changeId: string; closed?: boolean; onchange?: () => void; onview?: (option: string) => void } = $props();

  let options = $state<Flow[]>([]);
  let error = $state('');
  let busy = $state('');
  let level = $state<'written' | 'accepted'>('written');
  let cmp = $state<OptionComparison | undefined>();
  let evaluating = $state('');
  let evaluation = $state('');

  const open = $derived(options.filter((o) => o.status === 'open'));
  const nameOf = (id: string) => options.find((o) => o.id === id)?.option?.name ?? shortId(id);

  async function load(signal?: AbortSignal) {
    try {
      const r = await graph.listOptions(changeId, signal);
      options = r.options ?? [];
      cmp = open.length ? await graph.compareOptions(changeId, level, false, signal) : undefined;
      error = '';
    } catch (e) {
      if (!signal?.aborted) error = errorMessage(e);
    }
  }

  $effect(() => {
    void level;
    if (!changeId) return;
    const ctrl = new AbortController();
    load(ctrl.signal);
    return () => ctrl.abort();
  });

  async function act(label: string, fn: () => Promise<unknown>) {
    busy = label;
    error = '';
    try {
      await fn();
      await load();
      onchange?.();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }

  const evaluate = (id: string) =>
    act(`evaluate:${id}`, async () => {
      await graph.evaluateOption(changeId, id, evaluation.trim());
      evaluating = '';
      evaluation = '';
    });

  /** the properties that differ between the sides of a compared node */
  function differing(props: Record<string, Record<string, JsonValue>> | undefined, sides: string[]): string[] {
    const keys = new Set<string>();
    for (const s of sides) for (const k of Object.keys(props?.[s] ?? {})) keys.add(k);
    return [...keys].filter((k) => new Set(sides.map((s) => JSON.stringify(props?.[s]?.[k] ?? null))).size > 1).sort();
  }
  const show = (v: JsonValue | undefined) => (v === undefined || v === null ? '—' : typeof v === 'string' ? v : JSON.stringify(v));
</script>

<div class="options">
  {#if options.length}
    <ul class="list">
      {#each options as o (o.id)}
        <li class:current={o.active}>
          <div class="head">
            <StatusBadge status={o.optionStatus} />
            <strong>{o.option?.name}</strong>
            <code class="muted">{shortId(o.id)}</code>
            {#if o.active}<span class="tag">active</span>{/if}
          </div>
          {#if o.option?.hypothesis}<p class="hyp">{o.option.hypothesis}</p>{/if}
          {#if o.evaluation}<p class="eval"><span class="muted">Evaluation:</span> {o.evaluation}</p>{/if}
          {#if o.status === 'open' && !closed}
            <div class="row">
              <button type="button" onclick={() => onview?.(o.id ?? '')}>View its impacts</button>
              <button type="button" disabled={!!busy} onclick={() => ((evaluating = evaluating === o.id ? '' : (o.id ?? '')), (evaluation = o.evaluation ?? ''))}>Evaluate</button>
              <button type="button" class="primary" disabled={!!busy} title="Its versions join the change branch; the other open options are rejected" onclick={() => act(`select:${o.id}`, () => graph.selectOption(changeId, o.id ?? ''))}>Select</button>
              <button type="button" class="danger" disabled={!!busy} onclick={() => act(`reject:${o.id}`, () => graph.rejectOption(changeId, o.id ?? ''))}>Reject</button>
            </div>
            {#if evaluating === o.id}
              <div class="row">
                <input type="text" class="grow" placeholder="Criteria, scores, rationale" bind:value={evaluation} />
                <button type="button" disabled={!!busy || !evaluation.trim()} onclick={() => evaluate(o.id ?? '')}>Record</button>
              </div>
            {/if}
          {/if}
        </li>
      {/each}
    </ul>
  {:else}
    <p class="hint">No option: open one from the scope bar (+ Option) to explore a hypothesis without touching the main flow of the change.</p>
  {/if}


  {#if cmp && open.length}
    <div class="cmp-head">
      <h4>Comparison</h4>
      <label>
        Level
        <select bind:value={level}>
          <option value="written">written (proposed or accepted)</option>
          <option value="accepted">accepted</option>
        </select>
      </label>
    </div>
    {#if !cmp.nodes?.length}
      <p class="hint">No option changed a node at this level yet.</p>
    {:else}
      {@const sides = ['main', ...(cmp.options ?? []).map((o) => o.id ?? '')]}
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Node</th>
              <th>Property</th>
              {#each sides as s (s)}<th>{s === 'main' ? 'main flow' : nameOf(s)}</th>{/each}
            </tr>
          </thead>
          <tbody>
            {#each cmp.nodes as n (n.node)}
              {@const keys = differing(n.props, sides)}
              {#each keys.length ? keys : [''] as k, i (k)}
                <tr>
                  {#if i === 0}<td rowspan={Math.max(keys.length, 1)}><strong>{n.key}</strong><br /><span class="muted">{n.type}</span></td>{/if}
                  <td>{k || '(presence)'}</td>
                  {#each sides as s (s)}
                    {@const ref = s === 'main' ? n.main : n.options?.[s]}
                    <td class:absent={!ref}>
                      {#if !ref}<span class="muted">absent</span>{:else if k}{show(n.props?.[s]?.[k])}{:else}v{ref.version}{/if}
                    </td>
                  {/each}
                </tr>
              {/each}
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {/if}

  {#if error}<div class="alert">{error}</div>{/if}
</div>

<style>
  .options {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .list li {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 8px 10px;
  }
  .list li.current {
    border-color: var(--accent);
  }
  .head {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }
  .tag {
    font-size: 0.8em;
    color: var(--accent);
  }
  .hyp,
  .eval {
    margin: 4px 0;
  }
  .row,
  .cmp-head {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
  }
  .grow {
    flex: 1;
    min-width: 12em;
  }
  .cmp-head h4 {
    margin: 0;
    flex: 1;
  }
  .table-wrap {
    overflow-x: auto;
  }
  table {
    border-collapse: collapse;
    width: 100%;
  }
  th,
  td {
    border: 1px solid var(--border);
    padding: 4px 6px;
    text-align: left;
    vertical-align: top;
  }
  td.absent {
    background: var(--hover);
  }
  .danger {
    color: var(--danger);
    border-color: var(--danger);
  }
</style>
