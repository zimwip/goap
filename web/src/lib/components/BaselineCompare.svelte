<script lang="ts">
  // Comparing two baselines of a namespace: what going from one to the other changes, node by node (added, removed,
  // another version with the properties that differ). The default is the baseline against its parent: what its
  // change committed.
  import { graph, errorMessage, shortId, type Baseline, type BaselineDiff, type JsonValue, type StackChange } from '../api';
  import { changeLabel } from '../changeStack';
  import { openTab } from '../shell/tabs.svelte';
  import ChangeStackView from './ChangeStackView.svelte';
  import { openNode } from '../nodeEditors';

  /** candidates: the baselines offered as "from" (the history the caller read), newest last */
  let { baseline, candidates = [] }: { baseline: Baseline; candidates?: Baseline[] } = $props();

  const others = $derived(candidates.filter((x) => x.id !== baseline.id).reverse());
  let from = $state('');
  let diff = $state<BaselineDiff[] | undefined>();
  /** the changes that explain the difference (ADR 0133) */
  let added = $state<StackChange[]>([]);
  let removed = $state<StackChange[]>([]);
  let stackOpen = $state(false);
  let error = $state('');

  $effect(() => {
    from = baseline.parentId ?? '';
  });

  $effect(() => {
    const f = from;
    const to = baseline.id ?? '';
    diff = undefined;
    added = [];
    removed = [];
    if (!f || !to) return;
    const ctrl = new AbortController();
    graph
      .diffBaselines(f, to, ctrl.signal)
      .then((r) => ((diff = r.nodes ?? []), (added = r.changesAdded ?? []), (removed = r.changesRemoved ?? []), (error = '')))
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
  {#if added.length || removed.length}
    <p class="explained">
      <span class="hint">Explained by</span>
      {#each added as c (c.id)}<button type="button" class="link added" onclick={() => openTab({ kind: 'change', params: { id: c.id ?? '' } }, { pin: true })}>+ {changeLabel(c)}</button>{/each}
      {#each removed as c (c.id)}<button type="button" class="link removed" onclick={() => openTab({ kind: 'change', params: { id: c.id ?? '' } }, { pin: true })}>− {changeLabel(c)}</button>{/each}
    </p>
  {/if}
  <details ontoggle={(e) => (stackOpen = (e.currentTarget as HTMLDetailsElement).open)}>
    <summary>Change-stack of this baseline</summary>
    {#if stackOpen && baseline.id}<ChangeStackView baselineId={baseline.id} />{/if}
  </details>
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
  .explained {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    margin: 4px 0 0;
  }
  .explained .added {
    color: var(--ok);
  }
  .explained .removed {
    color: var(--danger);
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
