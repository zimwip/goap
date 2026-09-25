<script lang="ts">
  // Baseline tab: nodes and links of a baseline (suspect links flagged).
  import { graph, errorMessage, formatDate, nodeTitle, shortId, type Baseline, type GraphNode, type Link } from '../../api';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { provideActions, select, selection } from '../../shell/workbench.svelte';

  let { tab }: { tab: Tab } = $props();

  let reload = $state(0);
  let baseline = $state<Baseline | undefined>();
  let nodes = $state<GraphNode[]>([]);
  let links = $state<Link[]>([]);
  let suspect = $state<Link[]>([]);
  let loading = $state(false);
  let error = $state('');
  let filter = $state('');

  const selected = $derived(tab.params.id ?? '');

  $effect(() => {
    void reload;
    const id = selected;
    if (!id) return;
    const ctrl = new AbortController();
    loading = true;
    error = '';
    graph
      .getBaselineGraph(id, ctrl.signal)
      .then((r) => {
        baseline = r.baseline;
        nodes = r.nodes ?? [];
        links = r.links ?? [];
        suspect = r.suspectLinks ?? [];
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) error = errorMessage(e);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) loading = false;
      });
    return () => ctrl.abort();
  });

  const byId = $derived(new Map(nodes.map((n) => [n.id ?? '', n])));
  const suspectIds = $derived(new Set(suspect.map((l) => l.id ?? '')));

  // Suspect links don't necessarily appear in `links`: merge them in.
  const allLinks = $derived.by(() => {
    const seen = new Set(links.map((l) => l.id ?? ''));
    return [...links, ...suspect.filter((l) => !seen.has(l.id ?? ''))];
  });

  const q = $derived(filter.trim().toLowerCase());
  const shownNodes = $derived(
    q
      ? nodes.filter((n) => `${n.key} ${n.type} ${nodeTitle(n)}`.toLowerCase().includes(q))
      : nodes,
  );

  function keyOf(ref: { id?: string; version?: number } | undefined): string {
    if (!ref?.id) return '?';
    const n = byId.get(ref.id);
    return n?.key ?? ref.id.slice(0, 8);
  }

  provideActions(
    () => tab.id,
    () => [{ id: 'refresh', label: 'Refresh', icon: 'refresh', disabled: loading, run: () => reload++ }],
  );

  function showNode(n: GraphNode) {
    select({
      title: n.key ?? '',
      subtitle: `Node ${n.type ?? ''} v${n.version ?? 0}`,
      rows: [
        ['ID', n.id ?? ''],
        ['Type', n.type ?? ''],
        ['Version', String(n.version ?? 0)],
        ...Object.entries(n.props ?? {}).map(([k, v]): [string, string] => [k, typeof v === 'string' ? v : JSON.stringify(v)]),
        ['Change', n.changeId ?? ''],
        ['Created', formatDate(n.createdAt)],
      ],
    });
  }

  const selectedKey = $derived(selection.current?.subtitle?.startsWith('Node') ? selection.current.title : '');

  function stale(ref: { id?: string; version?: number } | undefined): boolean {
    const n = ref?.id ? byId.get(ref.id) : undefined;
    return !!n && (ref?.version ?? 0) !== (n.version ?? 0);
  }
</script>

<div class="editor-page wide">
<div class="editor-head">
  <Icon name="database" size={18} />
  <h2>{baseline?.name || shortId(selected)}</h2>
</div>
{#if error}<div class="alert">{error}</div>{/if}

{#if baseline}
  <p class="hint">
    <code>{baseline.id}</code>
    {#if baseline.createdAt} · created on {formatDate(baseline.createdAt)}{/if}
    {#if baseline.changeId} · from change <button type="button" class="link mono" onclick={() => openTab({ kind: 'change', params: { id: baseline?.changeId ?? '' } })}>{baseline.changeId.slice(0, 8)}</button>{/if}
    {#if baseline.parentId} · parent <button type="button" class="link mono" onclick={() => openTab({ kind: 'baseline', params: { id: baseline?.parentId ?? '' } })}>{shortId(baseline.parentId)}</button>{/if}
    · {nodes.length} nodes · {allLinks.length} links
    {#if suspect.length}· <span class="suspect-count">{suspect.length} suspect{suspect.length > 1 ? 's' : ''}</span>{/if}
  </p>
{/if}

{#if loading}<p class="empty">Loading…</p>{/if}

{#if selected && !loading && !error}
  <section class="card">
    <div class="row" style="margin-bottom: 0.5rem">
      <h3 class="grow" style="margin: 0">Nodes</h3>
      <input class="filter" type="search" placeholder="Filter…" aria-label="Filter nodes" bind:value={filter} data-no-pin />
    </div>
    {#if shownNodes.length}
      <div class="scroll">
        <table>
          <thead><tr><th>Key</th><th>Type</th><th>Version</th><th>State</th><th>Title</th></tr></thead>
          <tbody>
            {#each shownNodes as n (n.id)}
              <tr class:deleted={n.deleted} class:sel={selectedKey === n.key}>
                <td><button type="button" class="link mono" onclick={() => showNode(n)}>{n.key}</button></td>
                <td>{n.type}</td>
                <td>v{n.version ?? 0}</td>
                <td>{#if n.state}<span class="state">{n.state}</span>{/if}</td>
                <td>{nodeTitle(n)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else}
      <p class="empty">No nodes.</p>
    {/if}
  </section>

  <section class="card">
    <h3>Links</h3>
    {#if allLinks.length}
      <div class="scroll">
        <table>
          <thead><tr><th>Source</th><th>Type</th><th>Target</th><th></th></tr></thead>
          <tbody>
            {#each allLinks as l (l.id)}
              {@const isSuspect = suspectIds.has(l.id ?? '')}
              <tr class:suspect={isSuspect}>
                <td>
                  <code>{keyOf(l.from)}</code>
                  <span class="ver" class:stale={stale(l.from)}>v{l.from?.version ?? 0}</span>
                </td>
                <td><span class="arrow">→ {l.type} →</span></td>
                <td>
                  <code>{keyOf(l.to)}</code>
                  <span class="ver" class:stale={stale(l.to)}>v{l.to?.version ?? 0}</span>
                </td>
                <td>
                  {#if isSuspect}
                    <span class="tag" title="One end of the link has changed since it was created">suspect</span>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else}
      <p class="empty">No links.</p>
    {/if}
  </section>
{/if}
</div>

<style>
  .state {
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 0.5rem;
    font-size: 0.8rem;
    font-family: var(--mono);
  }
  .wide {
    max-width: 1400px;
  }
  tr.sel td {
    background: var(--accent-soft);
  }
  .filter {
    max-width: 220px;
  }
  .scroll {
    overflow-x: auto;
  }
  .deleted td {
    text-decoration: line-through;
    color: var(--muted);
  }
  .arrow {
    color: var(--muted);
    font-size: 0.85rem;
    white-space: nowrap;
  }
  .ver {
    color: var(--muted);
    font-size: 0.78rem;
    margin-left: 0.25rem;
  }
  .ver.stale {
    color: var(--warn);
    font-weight: 600;
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
  .suspect-count {
    color: var(--warn);
    font-weight: 600;
  }
</style>
