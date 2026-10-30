<script lang="ts">
  // Outgoing links of a node version (ADR 0003: they belong to the version). Adding or removing one writes the next
  // version of the node in the working change; incoming links belong to their source nodes and are edited there.
  import type { GraphNode, Link, NodeRef } from '../api';
  import type { GraphIndex } from '../graphIndex';
  import { typeCatalog } from '../stores/types.svelte';
  import { dragNode } from '../nodeDrag';

  let {
    node,
    links,
    total,
    onmore,
    index,
    readonly = false,
    why = '',
    busy = false,
    search,
    onadd,
    onremove,
    onopen,
  }: {
    node: GraphNode;
    /** the outgoing links of the version shown, a page of them when total says more */
    links: Link[];
    /** the number of outgoing links of the version (unset: links holds them all) */
    total?: number;
    /** reads the next page of the links */
    onmore?: () => void;
    /** the graph the targets are picked from (the working change's commit, else the current graph) */
    index?: GraphIndex;
    readonly?: boolean;
    /** why the links cannot be changed now (shown when readonly) */
    why?: string;
    busy?: boolean;
    /** finds the targets matching a text in the graph (the index holds the node's neighbours only); unset: the targets
     * are picked from the index */
    search?: (query: string) => Promise<GraphNode[]>;
    onadd: (type: string, to: NodeRef) => Promise<boolean> | boolean;
    onremove: (link: Link) => Promise<boolean> | boolean;
    onopen: (id: string) => void;
  } = $props();

  let type = $state('');
  let target = $state('');
  let filter = $state('');

  const linkTypes = $derived(typeCatalog.cat.linksFrom(node.type));
  const chosen = $derived(linkTypes.find((l) => l.ref === type));
  /** the nodes the search found for the text typed */
  let found = $state<GraphNode[]>([]);
  $effect(() => {
    const q = filter.trim();
    const find = search;
    if (!find || !type) return;
    let live = true;
    const t = setTimeout(() => {
      find(q)
        .then((r) => {
          if (live) found = r;
        })
        .catch(() => {
          if (live) found = [];
        });
    }, 200);
    return () => {
      live = false;
      clearTimeout(t);
    };
  });
  const targets = $derived(
    (search ? found : (index?.list ?? []))
      .filter((n) => n.id !== node.id && !n.deleted && (!chosen || typeCatalog.cat.linkAccepts(chosen, n.type)))
      .filter((n) => !filter || `${n.key} ${n.type}`.toLowerCase().includes(filter.toLowerCase()))
      .sort((a, b) => (a.key ?? '').localeCompare(b.key ?? ''))
      .slice(0, 200),
  );
  const keyOf = (r: NodeRef | undefined) => index?.nodes.get(r?.id ?? '')?.key ?? r?.id ?? '';
  /** the target moved on since the link was written: it points to an older version */
  const suspect = (l: Link) => {
    const cur = index?.nodes.get(l.to?.id ?? '');
    return !!cur && (cur.version ?? 0) !== (l.to?.version ?? 0);
  };

  async function add() {
    const t = found.find((n) => n.id === target) ?? index?.nodes.get(target);
    if (!type || !t?.id) return;
    if (await onadd(type, { id: t.id, version: t.version })) target = '';
  }
</script>

<section class="card">
  <h3>Links <span class="count">{total ?? links.length}</span></h3>
  <p class="hint">Outgoing links of this version. Adding or removing one writes the next version of the node in the working change.</p>
  {#if links.length}
    <table>
      <thead><tr><th>Type</th><th>To</th><th></th></tr></thead>
      <tbody>
        {#each links as l (l.id)}
          <tr>
            <td title={l.type}>{typeCatalog.cat.linkLabel(l.type, 'out')}</td>
            <td>
              <button type="button" class="link mono" draggable="true" title="Open; drag to put the node in a folder"
                ondragstart={(e) => dragNode(e, { ...index?.nodes.get(l.to?.id ?? ''), id: l.to?.id, version: l.to?.version, key: keyOf(l.to) })}
                onclick={() => onopen(l.to?.id ?? '')}>{keyOf(l.to)}</button>
              <span class="hint">v{l.to?.version}</span>
              {#if suspect(l)}<span class="suspect" title="The target has another version now: check the link still holds">suspect</span>{/if}
            </td>
            <td class="act">
              {#if !readonly}<button type="button" class="small danger" disabled={busy} onclick={() => onremove(l)}>Remove</button>{/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
    {#if total !== undefined && total > links.length && onmore}
      <button type="button" class="small" disabled={busy} onclick={onmore}>Show more ({total - links.length} left)</button>
    {/if}
  {:else}
    <p class="empty">No outgoing link.</p>
  {/if}
  {#if readonly && why}<p class="hint">{why}</p>{/if}
  {#if !readonly}
    <form class="add" onsubmit={(e) => (e.preventDefault(), add())}>
      <select bind:value={type} aria-label="Link type">
        <option value="" disabled>— link type —</option>
        {#each linkTypes as l (l.ref)}<option value={l.ref}>{l.ref}{l.to ? ` → ${l.to}` : ''}</option>{/each}
      </select>
      <input type="search" placeholder="Find a target…" bind:value={filter} />
      <select bind:value={target} aria-label="Link target" disabled={!type}>
        <option value="" disabled>— target —</option>
        {#each targets as n (n.id)}<option value={n.id}>{n.key} ({n.type})</option>{/each}
      </select>
      <button type="submit" disabled={busy || !type || !target}>Add the link</button>
    </form>
    {#if !linkTypes.length}<p class="hint">The type {node.type} declares no outgoing link type.</p>{/if}
  {/if}
</section>

<style>
  table {
    width: 100%;
  }
  .act {
    text-align: right;
  }
  .suspect {
    margin-left: 0.3rem;
    font-size: 0.75rem;
    color: var(--warn);
    border: 1px solid var(--warn);
    border-radius: 999px;
    padding: 0 6px;
    white-space: nowrap;
  }
  .add {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    align-items: center;
    margin-top: 6px;
  }
  .add select,
  .add input {
    width: auto;
    min-width: 12rem;
  }
</style>
