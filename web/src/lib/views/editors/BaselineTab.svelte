<script lang="ts">
  // Baseline tab: the node types of a baseline, a paged and filtered list of the nodes of one type, and the
  // neighbour graph of the selected node. Everything is paged on the server: a baseline is never loaded whole.
  import {
    graph,
    errorMessage,
    formatDate,
    nodeTitle,
    shortId,
    type Baseline,
    type GraphNode,
    type Link,
    type TypeCount,
  } from '../../api';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import NodeGraph from '../../components/NodeGraph.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { openNode } from '../../nodeEditors';
  import { indexOf } from '../../graphIndex';
  import { splitType } from '../../stores/types.svelte';
  import { provideActions } from '../../shell/workbench.svelte';

  let { tab }: { tab: Tab } = $props();

  const SIZES = [25, 50, 100, 200];

  let reload = $state(0);
  let baseline = $state<Baseline | undefined>();
  let types = $state<TypeCount[]>([]);
  let nodes = $state<GraphNode[]>([]);
  let total = $state(0);
  let loading = $state(false);
  let error = $state('');
  let filter = $state('');
  let query = $state('');
  let offset = $state(0);
  let size = $state(50);

  const id = $derived(tab.params.id ?? '');
  const type = $derived(tab.params.type ?? '');
  const selected = $derived(tab.params.node ?? '');

  let debounce: ReturnType<typeof setTimeout> | undefined;
  function onFilter(v: string) {
    filter = v;
    clearTimeout(debounce);
    debounce = setTimeout(() => {
      query = filter.trim();
      offset = 0;
    }, 250);
  }

  function pickType(t: string) {
    tab.params.type = t;
    offset = 0;
  }

  // The page of nodes (and the type counts, which follow the text filter).
  $effect(() => {
    void reload;
    const req = { baselineId: id, type, query, offset, limit: size };
    if (!req.baselineId) return;
    const ctrl = new AbortController();
    loading = true;
    error = '';
    graph
      .listBaselineNodes(req, ctrl.signal)
      .then((r) => {
        baseline = r.baseline;
        types = r.types ?? [];
        nodes = r.nodes ?? [];
        total = r.total ?? 0;
        if (baseline?.name && tab.params.name !== baseline.name) tab.params.name = baseline.name;
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) error = errorMessage(e);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) loading = false;
      });
    return () => ctrl.abort();
  });

  const typeTotal = $derived(types.reduce((s, t) => s + (t.count ?? 0), 0));
  const pageCount = $derived(Math.max(1, Math.ceil(total / size)));
  const page = $derived(Math.floor(offset / size) + 1);
  const goto = (p: number) => (offset = (Math.min(Math.max(p, 1), pageCount) - 1) * size);

  // ---- neighbourhood of the selected node -------------------------------------------------------

  let center = $state<GraphNode | undefined>();
  let neighbours = $state<GraphNode[]>([]);
  let links = $state<Link[]>([]);
  let suspect = $state(new Set<string>());
  let nbLoading = $state(false);
  let nbError = $state('');

  $effect(() => {
    void reload;
    const b = id;
    const n = selected;
    center = undefined;
    neighbours = [];
    links = [];
    suspect = new Set();
    nbError = '';
    if (!b || !n) return;
    const ctrl = new AbortController();
    nbLoading = true;
    graph
      .getNodeNeighbourhood(b, n, ctrl.signal)
      .then((r) => {
        center = r.node;
        neighbours = r.nodes ?? [];
        links = r.links ?? [];
        suspect = new Set(r.suspectLinkIds ?? []);
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) nbError = errorMessage(e);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) nbLoading = false;
      });
    return () => ctrl.abort();
  });

  const index = $derived(indexOf(id, center ? [center, ...neighbours] : [], links));

  /** The neighbours with the links that reach them, incoming first. */
  const rows = $derived.by(() => {
    const byId = new Map(neighbours.map((n) => [n.id ?? '', n]));
    return links
      .map((l) => {
        const outgoing = l.from?.id === selected;
        const other = byId.get((outgoing ? l.to?.id : l.from?.id) ?? '');
        return { l, outgoing, other, suspect: suspect.has(l.id ?? '') };
      })
      .sort((a, b) => Number(a.outgoing) - Number(b.outgoing) || (a.other?.key ?? '').localeCompare(b.other?.key ?? ''));
  });

  const inBaseline = (n: GraphNode | undefined) => !!n?.id && !!baseline?.nodes && n.id in baseline.nodes;

  function selectNode(n: GraphNode | undefined) {
    if (!n?.id) return;
    // a neighbour the baseline does not hold (another namespace): open it in its editor
    if (!inBaseline(n)) {
      void openNode(n, { pin: true });
      return;
    }
    tab.params.node = n.id;
  }

  const byNodeId = (nid: string) => (nid === center?.id ? center : neighbours.find((n) => n.id === nid));

  provideActions(
    () => tab.id,
    () => [{ id: 'refresh', label: 'Refresh', icon: 'refresh', disabled: loading, run: () => reload++ }],
  );

  const propRows = (n: GraphNode | undefined) =>
    Object.entries(n?.props ?? {}).map(([k, v]): [string, string] => [k, typeof v === 'string' ? v : JSON.stringify(v)]);
</script>

<div class="editor-page wide">
  <div class="editor-head">
    <Icon name="database" size={18} />
    <h2>{baseline?.name || tab.params.name || shortId(id)}</h2>
  </div>
  {#if error}<div class="alert">{error}</div>{/if}

  {#if baseline}
    <p class="hint">
      <code>{baseline.id}</code>
      {#if baseline.namespace} · namespace <code>{baseline.namespace}</code>{/if}
      {#if baseline.createdAt} · created on {formatDate(baseline.createdAt)}{/if}
      {#if baseline.changeId} · from change <button type="button" class="link mono" onclick={() => openTab({ kind: 'change', params: { id: baseline?.changeId ?? '' } })}>{baseline.changeId.slice(0, 8)}</button>{/if}
      {#if baseline.parentId} · parent <button type="button" class="link mono" onclick={() => openTab({ kind: 'baseline', params: { id: baseline?.parentId ?? '', name: '', type: '', node: '' } })}>{shortId(baseline.parentId)}</button>{/if}
      · {Object.keys(baseline.nodes ?? {}).length} nodes
    </p>
  {/if}

  <div class="browse">
    <nav class="card types" aria-label="Node types">
      <h3>Node types</h3>
      <button type="button" class="type" class:sel={!type} onclick={() => pickType('')}>
        <span class="grow">All types</span><span class="count">{typeTotal}</span>
      </button>
      {#each types as t (t.type)}
        {@const st = splitType(t.type)}
        <button type="button" class="type" class:sel={type === t.type} title={t.type} onclick={() => pickType(t.type ?? '')}>
          <span class="grow">{st.name || t.type}{#if st.namespace && st.namespace !== baseline?.namespace}<span class="ns"> {st.namespace}</span>{/if}</span>
          <span class="count">{t.count ?? 0}</span>
        </button>
      {/each}
      {#if type && !types.some((t) => t.type === type)}
        <p class="empty small">No <code>{type}</code> node {query ? 'matches the filter' : 'in this baseline'}.</p>
      {/if}
    </nav>

    <section class="card list">
      <div class="row head">
        <h3 class="grow">{type ? splitType(type).name || type : 'All nodes'}</h3>
        <input class="filter" type="search" placeholder="Filter key, title, properties…" aria-label="Filter nodes" value={filter} oninput={(e) => onFilter(e.currentTarget.value)} data-no-pin />
      </div>
      {#if nodes.length}
        <div class="scroll">
          <table>
            <thead><tr><th>Key</th>{#if !type}<th>Type</th>{/if}<th>Version</th><th>State</th><th>Title</th></tr></thead>
            <tbody>
              {#each nodes as n (n.id)}
                <tr class:deleted={n.deleted} class:sel={selected === n.id} onclick={() => selectNode(n)} ondblclick={() => void openNode(n, { pin: true })}>
                  <td class="nowrap"><button type="button" class="link mono" title="Show its neighbours (double-click: open in its editor)" onclick={(e) => { e.stopPropagation(); selectNode(n); }}>{n.key}</button></td>
                  {#if !type}<td>{splitType(n.type).name || n.type}</td>{/if}
                  <td>v{n.version ?? 0}</td>
                  <td>{#if n.state}<span class="state">{n.state}</span>{/if}</td>
                  <td>{nodeTitle(n)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {:else if !loading}
        <p class="empty">{query ? 'No matching nodes.' : 'No nodes.'}</p>
      {/if}
      <div class="pager" role="group" aria-label="Pages">
        <button type="button" class="small" disabled={page <= 1 || loading} onclick={() => goto(1)} aria-label="First page">«</button>
        <button type="button" class="small" disabled={page <= 1 || loading} onclick={() => goto(page - 1)} aria-label="Previous page">‹</button>
        <span class="hint">
          {#if total}{offset + 1}–{Math.min(offset + size, total)} of {total}{:else}0 of 0{/if} · page {page}/{pageCount}
        </span>
        <button type="button" class="small" disabled={page >= pageCount || loading} onclick={() => goto(page + 1)} aria-label="Next page">›</button>
        <button type="button" class="small" disabled={page >= pageCount || loading} onclick={() => goto(pageCount)} aria-label="Last page">»</button>
        <span class="grow"></span>
        <label class="hint">
          Per page
          <select
            value={size}
            onchange={(e) => {
              size = Number(e.currentTarget.value);
              offset = 0;
            }}
          >
            {#each SIZES as s (s)}<option value={s}>{s}</option>{/each}
          </select>
        </label>
        {#if loading}<span class="hint">Loading…</span>{/if}
      </div>
    </section>

    <section class="card neighbours">
      {#if !selected}
        <h3>Neighbours</h3>
        <p class="empty">Select a node to see its neighbours.</p>
      {:else if nbError}
        <h3>Neighbours</h3>
        <div class="alert">{nbError}</div>
      {:else if !center}
        <h3>Neighbours</h3>
        <p class="empty">Loading…</p>
      {:else}
        <div class="row head">
          <h3 class="grow">
            <span class="mono">{center.key}</span>
            <span class="hint">{splitType(center.type).name} v{center.version ?? 0}{center.state ? ` · ${center.state}` : ''}</span>
          </h3>
          <button type="button" class="small" onclick={() => center && void openNode(center, { pin: true })}>Open</button>
          <button type="button" class="small" title="Versions and states of the node" onclick={() => center && void openNode(center, { pin: true, generic: true, pane: 'history' })}>History</button>
        </div>
        {#if nodeTitle(center)}<p class="title">{nodeTitle(center)}</p>{/if}
        <NodeGraph {index} center={center.id ?? ''} maxDepth={1} {suspect} onrecenter={(nid) => selectNode(byNodeId(nid))} onopen={(nid) => { const n = byNodeId(nid); if (n) void openNode(n, { pin: true }); }} />
        {#if nbLoading}<p class="hint">Loading…</p>{/if}
        {#if rows.length}
          <table class="links">
            <thead><tr><th></th><th>Link</th><th>Node</th><th>Type</th><th></th></tr></thead>
            <tbody>
              {#each rows as r (r.l.id)}
                <tr class:suspect={r.suspect}>
                  <td class="dir" title={r.outgoing ? 'Outgoing' : 'Incoming'}>{r.outgoing ? '→' : '←'}</td>
                  <td class="mono">{r.l.type}</td>
                  <td>
                    <button type="button" class="link mono" title={inBaseline(r.other) ? 'Show its neighbours' : 'Not in this baseline: open it in its editor'} onclick={() => selectNode(r.other)}>{r.other?.key ?? '?'}</button>
                    {#if nodeTitle(r.other)}<span class="hint"> {nodeTitle(r.other)}</span>{/if}
                  </td>
                  <td>{r.other?.type ?? ''}</td>
                  <td>{#if r.suspect}<span
                        class="tag"
                        title={`A more recent version of ${r.outgoing ? r.other?.key ?? 'the target' : center?.key ?? 'this node'} exists: check whether the link should use it`}>suspect</span
                      >{/if}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
        {#if propRows(center).length}
          <details class="props">
            <summary>Properties</summary>
            <table>
              <tbody>
                {#each propRows(center) as [k, v] (k)}<tr><th>{k}</th><td>{v}</td></tr>{/each}
              </tbody>
            </table>
          </details>
        {/if}
      {/if}
    </section>
  </div>
</div>

<style>
  .wide {
    max-width: 1600px;
  }
  .browse {
    display: grid;
    grid-template-columns: minmax(170px, 220px) minmax(320px, 1fr) minmax(400px, 1.3fr);
    gap: 0.8rem;
    align-items: start;
  }
  @media (max-width: 1100px) {
    .browse {
      grid-template-columns: minmax(160px, 200px) 1fr;
    }
    .neighbours {
      grid-column: 1 / -1;
    }
  }
  .card {
    min-width: 0;
    margin: 0;
  }
  .types {
    display: flex;
    flex-direction: column;
    gap: 1px;
    max-height: 75vh;
    overflow-y: auto;
  }
  .type {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    text-align: left;
    border: none;
    background: none;
    padding: 0.25rem 0.4rem;
    border-radius: 4px;
    cursor: pointer;
    color: inherit;
  }
  .type:hover {
    background: var(--hover);
  }
  .type.sel {
    background: var(--accent-soft);
    font-weight: 600;
  }
  .ns {
    color: var(--muted);
    font-size: 0.8em;
  }
  .count {
    color: var(--muted);
    font-size: 0.85em;
    font-variant-numeric: tabular-nums;
  }
  .head {
    margin-bottom: 0.5rem;
    gap: 0.4rem;
    align-items: center;
  }
  .head h3 {
    margin: 0;
  }
  .filter {
    max-width: 240px;
  }
  .scroll {
    overflow-x: auto;
  }
  tbody tr {
    cursor: pointer;
  }
  tr.sel td {
    background: var(--accent-soft);
  }
  .deleted td {
    text-decoration: line-through;
    color: var(--muted);
  }
  .state {
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 0.5rem;
    font-size: 0.8rem;
    font-family: var(--mono);
  }
  .pager {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    margin-top: 0.5rem;
    flex-wrap: wrap;
  }
  .pager select {
    width: auto;
    margin-left: 0.3rem;
  }
  .title {
    margin: 0 0 0.5rem;
  }
  .links {
    margin-top: 0.6rem;
  }
  .links .dir {
    color: var(--muted);
    width: 1.5rem;
  }
  tr.suspect td {
    background: var(--warn-soft);
  }
  .tag {
    font-size: 0.75rem;
    font-weight: 700;
    color: var(--warn);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .props {
    margin-top: 0.6rem;
  }
  .props th {
    text-align: left;
    font-weight: 600;
    white-space: nowrap;
    vertical-align: top;
  }
  .props td {
    word-break: break-word;
  }
  .nowrap {
    white-space: nowrap;
  }
  .empty.small {
    font-size: 0.85em;
  }
</style>
