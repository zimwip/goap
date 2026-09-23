<script lang="ts">
  // Explorateur des référentiels : baselines → nœuds (par type).
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import { toggle, isOpen, expanded } from './expanded.svelte';
  import { baselines, refreshBaselines } from '../../stores/catalog.svelte';
  import { openTab, tabsState } from '../../shell/tabs.svelte';
  import { select } from '../../shell/workbench.svelte';
  import { graph, errorMessage, formatDate, nodeTitle, shortId, type Baseline, type GraphNode } from '../../api';
  import { SvelteMap } from 'svelte/reactivity';

  let filter = $state('');
  const graphs = new SvelteMap<string, { nodes: GraphNode[]; error: string; loading: boolean }>();

  $effect(() => {
    if (!baselines.loaded) void refreshBaselines();
  });

  async function loadGraph(id: string) {
    if (graphs.get(id)?.nodes.length || graphs.get(id)?.loading) return;
    graphs.set(id, { nodes: [], error: '', loading: true });
    try {
      const r = await graph.getBaselineGraph(id);
      graphs.set(id, { nodes: r.nodes ?? [], error: '', loading: false });
    } catch (e) {
      graphs.set(id, { nodes: [], error: errorMessage(e), loading: false });
    }
  }

  $effect(() => {
    for (const b of baselines.items) if (b.id && expanded[`b:${b.id}`]) void loadGraph(b.id);
  });

  const sorted = $derived([...baselines.items].reverse());
  const q = $derived(filter.trim().toLowerCase());

  function byType(nodes: GraphNode[]): [string, GraphNode[]][] {
    const m = new Map<string, GraphNode[]>();
    for (const n of nodes) {
      if (q && !`${n.key} ${n.type} ${nodeTitle(n)}`.toLowerCase().includes(q)) continue;
      const t = n.type ?? '?';
      m.set(t, [...(m.get(t) ?? []), n]);
    }
    return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  }

  function openBaseline(b: Baseline, pin = false) {
    openTab({ kind: 'baseline', params: { id: b.id ?? '' } }, { pin });
    select({
      title: b.name || shortId(b.id),
      subtitle: 'Référentiel',
      rows: [
        ['Identifiant', b.id ?? ''],
        ['Parent', b.parentId ?? ''],
        ['Changement', b.changeId ?? ''],
        ['Nœuds', String(Object.keys(b.nodes ?? {}).length)],
        ['Créé', formatDate(b.createdAt)],
      ],
    });
  }

  function openNode(b: Baseline, n: GraphNode, pin = false) {
    openTab({ kind: 'baseline', params: { id: b.id ?? '' } }, { pin });
    select({
      title: n.key ?? '',
      subtitle: `Nœud ${n.type ?? ''} v${n.version ?? 0}`,
      rows: [
        ['Identifiant', n.id ?? ''],
        ['Type', n.type ?? ''],
        ['Version', String(n.version ?? 0)],
        ...Object.entries(n.props ?? {}).map(([k, v]): [string, string] => [k, typeof v === 'string' ? v : JSON.stringify(v)]),
        ['Changement', n.changeId ?? ''],
        ['Créé', formatDate(n.createdAt)],
      ],
    });
  }
</script>

<div class="explorer">
  <div class="tools">
    <input type="search" placeholder="Filtrer les nœuds…" aria-label="Filtrer les nœuds" bind:value={filter} data-no-pin />
    <button
      type="button"
      class="ghost small"
      title="Actualiser"
      aria-label="Actualiser"
      disabled={baselines.loading}
      onclick={() => {
        graphs.clear();
        void refreshBaselines();
      }}><Icon name="refresh" size={14} /></button
    >
  </div>
  {#if baselines.error}<div class="alert small">{baselines.error}</div>{/if}
  {#if baselines.loaded && !baselines.items.length && !baselines.error}<p class="empty pad">Aucun référentiel.</p>{/if}
  <div role="tree" aria-label="Référentiels">
    {#each sorted as b (b.id)}
      {@const k = `b:${b.id}`}
      {@const g = graphs.get(b.id ?? '')}
      <TreeRow
        icon="database"
        label={b.name || shortId(b.id)}
        detail={formatDate(b.createdAt)}
        expanded={isOpen(k)}
        active={tabsState.active === `baseline:${b.id}`}
        onselect={() => openBaseline(b)}
        onopen={() => openBaseline(b, true)}
        ontoggle={() => toggle(k)}
      />
      {#if isOpen(k)}
        {#if !g || g.loading}
          <p class="empty pad2">Chargement…</p>
        {:else if g.error}
          <p class="alert small">{g.error}</p>
        {:else}
          {#each byType(g.nodes) as [type, nodes] (type)}
            {@const tk = `bt:${b.id}/${type}`}
            <TreeRow depth={1} icon="folder" label={type} detail={String(nodes.length)} expanded={isOpen(tk, !!q)} ontoggle={() => toggle(tk, !!q)} />
            {#if isOpen(tk, !!q)}
              {#each nodes as n (n.id)}
                <TreeRow
                  depth={2}
                  icon="node"
                  label={n.key ?? ''}
                  detail={nodeTitle(n)}
                  muted={n.deleted}
                  onselect={() => openNode(b, n)}
                  onopen={() => openNode(b, n, true)}
                />
              {/each}
            {/if}
          {:else}
            <p class="empty pad2">Aucun nœud.</p>
          {/each}
        {/if}
      {/if}
    {/each}
  </div>
</div>

<style>
  .explorer {
    padding-bottom: 1rem;
  }
  .tools {
    display: flex;
    gap: 2px;
    padding: 0 0.5rem 0.4rem;
    position: sticky;
    top: 0;
    background: var(--chrome);
    z-index: 1;
  }
  .tools input {
    min-height: 24px;
    height: 24px;
    margin-right: 0.2rem;
  }
  .tools button {
    padding: 0.1rem 0.3rem;
  }
  .pad {
    padding: 0.4rem 0.8rem;
  }
  .pad2 {
    padding: 0.1rem 0 0.1rem 34px;
    margin: 0;
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
</style>
