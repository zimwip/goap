<script lang="ts">
  // Comparing two baselines of a namespace: what going from one to the other changes, node by node (added, removed,
  // another version with the properties that differ). The default is the baseline against its parent: what its
  // change landed.
  import { graph, errorMessage, shortId, type Baseline, type BaselineDiff, type JsonValue } from '../api';
  import { openNode } from '../nodeEditors';

  let { baseline }: { baseline: Baseline } = $props();

  let others = $state<Baseline[]>([]);
  let from = $state('');
  let diff = $state<BaselineDiff[] | undefined>();
  let error = $state('');

  $effect(() => {
    const b = baseline;
    from = b.parentId ?? '';
    graph
      .listBaselines(b.namespace ?? '')
      .then((r) => (others = (r.baselines ?? []).filter((x) => x.id !== b.id).reverse()))
      .catch((e) => (error = errorMessage(e)));
  });

  $effect(() => {
    const f = from;
    const to = baseline.id ?? '';
    diff = undefined;
    if (!f || !to) return;
    const ctrl = new AbortController();
    graph
      .diffBaselines(f, to, ctrl.signal)
      .then((r) => ((diff = r.nodes ?? []), (error = '')))
      .catch((e) => {
        if (!ctrl.signal.aborted) error = errorMessage(e);
      });
    return () => ctrl.abort();
  });

  const show = (v: unknown) => (v === undefined || v === null ? '—' : typeof v === 'string' ? v : JSON.stringify(v));
  function changedProps(d: BaselineDiff): string[] {
    const a = (d.from?.props ?? {}) as Record<string, JsonValue>;
    const b = (d.to?.props ?? {}) as Record<string, JsonValue>;
    return [...new Set([...Object.keys(a), ...Object.keys(b)])].filter((k) => JSON.stringify(a[k] ?? null) !== JSON.stringify(b[k] ?? null)).sort();
  }
  const label = (b: Baseline) => `${b.name || shortId(b.id)} (${b.branch || 'main'})`;
</script>

<section class="card compare">
  <div class="head">
    <h3>Compare</h3>
    <label for="cmp-from">from</label>
    <select id="cmp-from" bind:value={from}>
      <option value="">— baseline —</option>
      {#each others as b (b.id)}<option value={b.id}>{label(b)}{b.id === baseline.parentId ? ' · parent' : ''}</option>{/each}
    </select>
    <span>to this baseline</span>
    {#if diff}<span class="hint">{diff.length} node(s) differ</span>{/if}
  </div>
  {#if error}<div class="alert">{error}</div>{/if}
  {#if diff?.length}
    <div class="scroll">
      <table>
        <thead><tr><th>Node</th><th>Change</th><th>Versions</th><th>Properties</th></tr></thead>
        <tbody>
          {#each diff as d (d.node)}
            {@const keys = d.kind === 'changed' ? changedProps(d) : []}
            <tr>
              <td><button type="button" class="link mono" onclick={() => openNode(d.to ?? d.from ?? { id: d.node }, { pin: true })}>{d.key}</button> <span class="hint">{d.type}</span></td>
              <td class="kind {d.kind}">{d.kind}</td>
              <td class="mono">{d.from ? `v${d.from.version}` : '—'} → {d.to ? `v${d.to.version}` : '—'}</td>
              <td>
                {#if d.kind === 'changed'}
                  {#if d.from?.state !== d.to?.state}<div>state: {d.from?.state || '—'} → {d.to?.state || '—'}</div>{/if}
                  {#each keys as k (k)}<div><code>{k}</code>: <span class="muted">{show(d.from?.props?.[k])}</span> → {show(d.to?.props?.[k])}</div>{/each}
                  {#if !keys.length && d.from?.state === d.to?.state}<span class="hint">links only</span>{/if}
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else if diff}
    <p class="empty">The two baselines hold the same versions.</p>
  {/if}
</section>

<style>
  .compare {
    height: 100%;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  .head {
    flex: none;
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
  }
  .head h3 {
    margin: 0;
  }
  .head select {
    width: auto;
    min-width: 14rem;
  }
  .scroll {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    margin-top: 6px;
  }
  table {
    width: 100%;
  }
  thead th {
    position: sticky;
    top: 0;
    background: var(--surface);
  }
  .kind.added {
    color: var(--ok);
  }
  .kind.removed {
    color: var(--danger);
  }
  .kind.changed {
    color: var(--warn);
  }
</style>
