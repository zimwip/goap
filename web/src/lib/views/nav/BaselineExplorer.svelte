<script lang="ts">
  // Baseline explorer: namespace → baselines → node types → nodes, paged and filtered on the server so that a large
  // graph is never loaded whole. Selecting a node opens the baseline tab on it (neighbour graph).
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import { toggle, isOpen } from './expanded.svelte';
  import { openTab, tabsState } from '../../shell/tabs.svelte';
  import { loadRaw, save } from '../../shell/storage';
  import { openNode } from '../../nodeEditors';
  import { graph, errorMessage, formatDate, nodeTitle, shortId, type Baseline, type GraphNode, type TypeCount } from '../../api';
  import { SvelteMap } from 'svelte/reactivity';
  import { untrack } from 'svelte';
  import { loadTypes as loadCatalog, splitType, typeCatalog } from '../../stores/types.svelte';
  import { refreshBaselines } from '../../stores/catalog.svelte';

  // Methodologies are nodes typed by the meta-domain methodology (ADR 0023), authored in their own editors and
  // explorer: not offered here by default.
  const META_NAMESPACES = new Set(['methodology']);
  const PAGE = 50;
  const NS_KEY = 'goap.ide.baselines.namespace';

  let namespaces = $state<string[]>([]);
  let namespace = $state(typeof loadRaw(NS_KEY) === 'string' ? (loadRaw(NS_KEY) as string) : '');
  let baselines = $state<Baseline[]>([]);
  let loading = $state(false);
  let error = $state('');
  let reload = $state(0);
  let starting = $state(false);

  void loadCatalog();
  /** namespaces holding nodes, plus the ones a domain declares (a namespace without a baseline can be started) */
  const choices = $derived([...new Set([...namespaces, ...typeCatalog.cat.namespaces().filter((n) => !META_NAMESPACES.has(n))])].sort());

  /** Starts a namespace: an empty baseline on main, that changes then fill. */
  async function startNamespace() {
    starting = true;
    error = '';
    try {
      await graph.createBaseline(namespace, `start ${namespace}`);
      void refreshBaselines(namespace);
      reload++;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      starting = false;
    }
  }

  let filter = $state('');
  let query = $state('');
  let debounce: ReturnType<typeof setTimeout> | undefined;
  function onFilter(v: string) {
    filter = v;
    clearTimeout(debounce);
    debounce = setTimeout(() => {
      if (filter.trim() === query) return;
      types.clear();
      pages.clear();
      query = filter.trim();
    }, 250);
  }

  interface Load<T> {
    items: T;
    error: string;
    loading: boolean;
  }
  // type counts per baseline, node pages per baseline + type (for the current query)
  const types = new SvelteMap<string, Load<TypeCount[]>>();
  const pages = new SvelteMap<string, Load<GraphNode[]> & { total: number }>();

  $effect(() => {
    void reload;
    graph
      .listNamespaces()
      .then((r) => {
        namespaces = r.namespaces ?? [];
        if (!namespace || !(namespaces.includes(namespace) || typeCatalog.cat.namespaces().includes(namespace))) namespace = namespaces.find((n) => !META_NAMESPACES.has(n)) ?? namespaces[0] ?? '';
      })
      .catch((e) => (error = errorMessage(e)));
  });

  $effect(() => {
    void reload;
    const ns = namespace;
    if (!ns) return;
    save(NS_KEY, ns);
    const ctrl = new AbortController();
    loading = true;
    error = '';
    graph
      .listBaselines(ns, ctrl.signal)
      .then((r) => (baselines = [...(r.baselines ?? [])].reverse()))
      .catch((e) => {
        if (!ctrl.signal.aborted) error = errorMessage(e);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) loading = false;
      });
    return () => ctrl.abort();
  });

  async function loadTypes(id: string) {
    const k = `${id}|${query}`;
    if (types.has(k)) return;
    types.set(k, { items: [], error: '', loading: true });
    try {
      const r = await graph.listBaselineNodes({ baselineId: id, query, limit: 1 });
      types.set(k, { items: r.types ?? [], error: '', loading: false });
    } catch (e) {
      types.set(k, { items: [], error: errorMessage(e), loading: false });
    }
  }

  async function loadPage(id: string, type: string, more = false) {
    const k = `${id}|${type}|${query}`;
    const cur = pages.get(k);
    if (cur?.loading || (cur && !more)) return;
    const have = cur?.items ?? [];
    pages.set(k, { items: have, total: cur?.total ?? 0, error: '', loading: true });
    try {
      const r = await graph.listBaselineNodes({ baselineId: id, type, query, offset: have.length, limit: PAGE });
      pages.set(k, { items: [...have, ...(r.nodes ?? [])], total: r.total ?? 0, error: '', loading: false });
    } catch (e) {
      pages.set(k, { items: have, total: cur?.total ?? 0, error: errorMessage(e), loading: false });
    }
  }

  const q = $derived(!!query);
  const bKey = (b: Baseline) => `b:${b.id}`;
  const tKey = (b: Baseline, t: string) => `bt:${b.id}/${t}`;

  // Load what the expanded rows show.
  $effect(() => {
    for (const b of baselines) {
      if (!b.id || !isOpen(bKey(b))) continue;
      const id = b.id;
      untrack(() => void loadTypes(id));
      for (const t of types.get(`${id}|${query}`)?.items ?? []) {
        const type = t.type ?? '';
        if (type && isOpen(tKey(b, type), q)) untrack(() => void loadPage(id, type));
      }
    }
  });

  function openBaseline(b: Baseline, pin = false, params: Record<string, string> = {}) {
    openTab({ kind: 'baseline', params: { id: b.id ?? '', name: b.name ?? '', type: '', node: '', ...params } }, { pin });
  }
</script>

<div class="explorer">
  <div class="tools">
    <select aria-label="Namespace" title="Namespace" bind:value={namespace} data-no-pin>
      {#if namespace && !namespaces.includes(namespace)}<option value={namespace}>{namespace}</option>{/if}
      {#each choices as ns (ns)}<option value={ns}>{ns}</option>{/each}
    </select>
    <button type="button" class="ghost small" title="Branches of the namespace" aria-label="Branches" disabled={!namespace} onclick={() => openTab({ kind: 'branches', params: { namespace } }, { pin: true })}
      ><Icon name="branch" size={14} /></button
    >
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={loading} onclick={() => {
        types.clear();
        pages.clear();
        reload++;
      }}
      ><Icon name="refresh" size={14} /></button
    >
  </div>
  <div class="tools">
    <input type="search" placeholder="Filter nodes…" aria-label="Filter nodes" value={filter} oninput={(e) => onFilter(e.currentTarget.value)} data-no-pin />
  </div>
  {#if error}<div class="alert small">{error}</div>{/if}
  {#if !loading && !error && namespace && !baselines.length}
    <div class="empty pad">
      No baselines in {namespace}.
      <button type="button" class="small" disabled={starting} onclick={startNamespace} title="An empty baseline on main: changes then create its nodes">Start {namespace}</button>
    </div>
  {/if}
  {#if !namespace && !error}<p class="empty pad">The graph holds no nodes yet.</p>{/if}
  <div role="tree" aria-label="Baselines">
    {#each baselines as b (b.id)}
      {@const tl = types.get(`${b.id}|${query}`)}
      <TreeRow
        icon="database"
        label={b.name || shortId(b.id)}
        detail={formatDate(b.createdAt)}
        expanded={isOpen(bKey(b))}
        active={tabsState.active === `baseline:${b.id}`}
        onselect={() => openBaseline(b)}
        onopen={() => openBaseline(b, true)}
        ontoggle={() => toggle(bKey(b))}
      />
      {#if isOpen(bKey(b))}
        {#if !tl || tl.loading}
          <p class="empty pad2">Loading…</p>
        {:else if tl.error}
          <p class="alert small">{tl.error}</p>
        {:else}
          {#each tl.items as t (t.type)}
            {@const type = t.type ?? ''}
            {@const pg = pages.get(`${b.id}|${type}|${query}`)}
            {@const st = splitType(type)}
            <TreeRow
              depth={1}
              icon="folder"
              label={st.name || type}
              title={type}
              detail={String(t.count ?? 0)}
              expanded={isOpen(tKey(b, type), q)}
              onselect={() => openBaseline(b, false, { type })}
              onopen={() => openBaseline(b, true, { type })}
              ontoggle={() => toggle(tKey(b, type), q)}
            />
            {#if isOpen(tKey(b, type), q)}
              {#each pg?.items ?? [] as n (n.id)}
                <TreeRow
                  depth={2}
                  icon="node"
                  label={n.key ?? ''}
                  detail={nodeTitle(n)}
                  muted={n.deleted}
                  onselect={() => openBaseline(b, false, { type, node: n.id ?? '' })}
                  onopen={() => void openNode(n, { pin: true })}
                />
              {/each}
              {#if pg?.error}
                <p class="alert small">{pg.error}</p>
              {:else if !pg || pg.loading}
                <p class="empty pad3">Loading…</p>
              {:else if pg.items.length < pg.total}
                <button type="button" class="link small pad3 more" onclick={() => void loadPage(b.id ?? '', type, true)}>
                  Load more ({pg.items.length} of {pg.total})
                </button>
              {/if}
            {/if}
          {:else}
            <p class="empty pad2">{query ? 'No matching nodes.' : 'No nodes.'}</p>
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
  .tools input,
  .tools select {
    min-height: 24px;
    height: 24px;
    margin-right: 0.2rem;
    flex: 1;
    min-width: 0;
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
  .pad3 {
    padding: 0.1rem 0 0.1rem 50px;
    margin: 0;
  }
  .more {
    display: block;
    text-align: left;
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
</style>
