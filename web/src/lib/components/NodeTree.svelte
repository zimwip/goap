<script lang="ts">
  // Expandable tree of the nodes related to a node: its children (outgoing
  // links) or its parents (incoming links), followed as deep as wanted.
  import Self from './NodeTree.svelte';
  import type { GraphIndex } from '../graphIndex';

  let {
    index,
    root,
    dir,
    onopen,
    seen = [],
  }: {
    index: GraphIndex;
    root: string;
    dir: 'out' | 'in';
    onopen: (id: string) => void;
    /** ancestors on the path (cycle guard) */
    seen?: string[];
  } = $props();

  let openIds = $state<string[]>([]);

  const entries = $derived(
    (index[dir].get(root) ?? [])
      .map((l) => ({ link: l, id: (dir === 'out' ? l.to?.id : l.from?.id) ?? '' }))
      .filter((e) => index.nodes.has(e.id))
      .sort((a, b) => (index.nodes.get(a.id)?.key ?? '').localeCompare(index.nodes.get(b.id)?.key ?? '')),
  );

  const toggle = (id: string) => (openIds = openIds.includes(id) ? openIds.filter((x) => x !== id) : [...openIds, id]);
  const more = (id: string) => (index[dir].get(id)?.length ?? 0) > 0;
</script>

{#if entries.length}
  <ul class="tree" role={seen.length ? 'group' : 'tree'}>
    {#each entries as e (e.link.id ?? e.id + e.link.type)}
      {@const n = index.nodes.get(e.id)}
      {@const cyc = seen.includes(e.id) || e.id === root}
      <li role="treeitem" aria-selected={false} aria-expanded={more(e.id) && !cyc ? openIds.includes(e.id) : undefined}>
        <div class="row">
          {#if more(e.id) && !cyc}
            <button type="button" class="tog" aria-label={openIds.includes(e.id) ? 'Collapse' : 'Expand'} onclick={() => toggle(e.id)}>{openIds.includes(e.id) ? '▾' : '▸'}</button>
          {:else}
            <span class="tog"></span>
          {/if}
          <span class="lt" title={dir === 'out' ? `${e.link.type} →` : `→ ${e.link.type}`}>{e.link.type}</span>
          <button type="button" class="link mono" title="Open the node" onclick={() => onopen(e.id)}>{n?.key}</button>
          <span class="hint">{n?.type}</span>
          {#if n?.state}<span class="state">{n.state}</span>{/if}
          {#if cyc}<span class="hint" title="Already on this path">↺</span>{/if}
        </div>
        {#if openIds.includes(e.id) && !cyc && seen.length < 8}
          <Self {index} root={e.id} {dir} {onopen} seen={[...seen, root]} />
        {/if}
      </li>
    {/each}
  </ul>
{:else if !seen.length}
  <p class="empty">{dir === 'out' ? 'No child: this node links to nothing.' : 'No parent: nothing links to this node.'}</p>
{/if}

<style>
  .tree {
    list-style: none;
    margin: 0;
    padding: 0 0 0 1rem;
  }
  .tree:first-child {
    padding-left: 0;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0.1rem 0;
    flex-wrap: wrap;
  }
  .tog {
    width: 1.1rem;
    border: none;
    background: none;
    padding: 0;
    min-height: 0;
    color: var(--muted);
  }
  .lt {
    font-size: 0.78rem;
    color: var(--muted);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 0.4rem;
  }
  .link {
    border: none;
    background: none;
    padding: 0;
    min-height: 0;
    color: var(--accent);
    text-decoration: underline;
  }
  .state {
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 0.45rem;
    font-size: 0.75rem;
    font-family: var(--mono);
  }
</style>
