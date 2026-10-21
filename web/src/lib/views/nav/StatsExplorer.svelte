<script lang="ts">
  // "Statistics" tool: what every namespace of every graph holds (nodes, versions, links, changes), the totals above,
  // grouped by the graph source holding them (ADR 0154 §9: the platform graph lists the content graph's too); a
  // namespace opens its statistics tab (the shape of the state at the head of a branch, per type, the store).
  import Icon from '../../shell/Icon.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { stamp, keyOf } from '../../flux/signals.svelte';
  import { graph, errorMessage, type NamespaceSummary } from '../../api';
  import { fmt, num, overviewTotals } from '../../graphStats';
  import { bySource } from '../../sources';

  let items = $state<NamespaceSummary[]>([]);
  let loading = $state(false);
  let error = $state('');

  async function load() {
    loading = true;
    try {
      items = ((await graph.graphOverview()).namespaces ?? []).sort(
        (a, b) => num(b.nodes) - num(a.nodes) || (a.namespace ?? '').localeCompare(b.namespace ?? '')
      );
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void stamp(keyOf.baselines);
    void load();
  });

  const totals = $derived(overviewTotals(items));
  const groups = $derived(bySource(items));
  const largest = $derived(Math.max(1, ...items.map((n) => num(n.nodes))));

  function open(namespace: string, pin = false) {
    openTab({ kind: 'graphStats', params: { namespace } }, { pin });
  }
</script>

<div class="explorer">
  <div class="tools">
    <span class="hint">{items.length} namespace{items.length === 1 ? '' : 's'}</span>
    <span class="grow"></span>
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}
      ><Icon name="refresh" size={14} /></button
    >
  </div>
  {#if error}<div class="alert small">{error}</div>{/if}
  {#if items.length}
    <dl class="totals" aria-label="The whole graph">
      <div><dt>Nodes</dt><dd>{fmt(totals.nodes)}</dd></div>
      <div><dt>Versions</dt><dd>{fmt(totals.versions)}</dd></div>
      <div><dt>Link rows</dt><dd>{fmt(totals.linkRows)}</dd></div>
      <div><dt>Changes</dt><dd>{fmt(totals.changes)}</dd></div>
      <div><dt>Baselines</dt><dd>{fmt(totals.baselines)}</dd></div>
      <div><dt>Branches</dt><dd>{fmt(totals.branches)}</dd></div>
    </dl>
  {/if}
  {#each groups as grp (grp.source)}
    {#if groups.length > 1}<div class="source" title="The graph source holding these namespaces">{grp.source}</div>{/if}
    <ul class="list" aria-label={`Namespaces of ${grp.source}`}>
      {#each grp.items as n (n.namespace)}
        <li>
          <button
            type="button"
            class="ns"
            title="Open the statistics of {n.namespace}"
            onclick={() => open(n.namespace ?? '')}
            ondblclick={() => open(n.namespace ?? '', true)}
          >
            <span class="row">
              <span class="name">{n.namespace}</span>
              <span class="count">{fmt(n.nodes)} nodes</span>
            </span>
            <span class="bar" aria-hidden="true"><span style:width="{(100 * num(n.nodes)) / largest}%"></span></span>
            <span class="detail">
              {#if num(n.nodes) || num(n.changes)}
                {fmt(n.types)} types · {fmt(n.versions)} versions · {fmt(n.linkRows)} links · {fmt(n.changes)} changes
              {:else}
                Domain published, no node yet
              {/if}
            </span>
          </button>
        </li>
      {/each}
    </ul>
  {:else}
    {#if !loading && !error}<p class="empty pad">The graph holds no node yet.</p>{/if}
  {/each}
</div>

<style>
  .tools {
    display: flex;
    align-items: center;
    gap: 2px;
    padding: 0 0.5rem 0.4rem 0.8rem;
  }
  .totals {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 0.35rem;
    margin: 0 0.5rem 0.6rem;
  }
  .totals div {
    padding: 0.3rem 0.4rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--chrome);
  }
  .totals dt {
    font-size: 0.72em;
    color: var(--muted);
  }
  .totals dd {
    margin: 0;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .source {
    padding: 0.4rem 0.8rem 0.15rem;
    font-size: 0.75em;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--muted);
  }
  .ns {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    width: 100%;
    padding: 0.35rem 0.8rem;
    border: none;
    border-radius: 0;
    background: transparent;
    color: inherit;
    text-align: left;
    font: inherit;
  }
  .ns:hover {
    background: var(--hover);
  }
  .row {
    display: flex;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .name {
    font-weight: 600;
  }
  .count {
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .bar {
    display: block;
    height: 4px;
    border-radius: 2px;
    background: var(--border);
    overflow: hidden;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .detail {
    font-size: 0.8em;
    color: var(--muted);
  }
  .pad {
    padding: 0.4rem 0.8rem;
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
</style>
