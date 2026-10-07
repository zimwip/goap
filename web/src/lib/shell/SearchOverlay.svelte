<script lang="ts">
  // Node search overlay: hybrid full-text / semantic search over the nodes of the graph (node index, ADR 0026)
  // with facets. It covers the workbench while it is open and closes as soon as a node is chosen.
  import { can } from '../stores/session.svelte';
  import { nodeIndex, errorMessage, type NodeHit, type NodeSearchRequest, type NodeSearchResult, type IndexStatus } from '../api';
  import Icon from './Icon.svelte';
  import { openNode } from '../nodeEditors';
  import { notify } from './workbench.svelte';
  import { searchOverlay, closeSearch } from './searchOverlay.svelte.ts';

  const PAGE = 25;
  const BUILTIN = ['type', 'namespace', 'state', 'branch'] as const;
  type Scope = 'main' | 'all' | 'off';

  let text = $state('');
  let input = $state<HTMLInputElement>();
  let cursor = $state(0);
  let scope = $state<Scope>('main');
  /** selected values per facet (alternatives inside a facet, AND between facets) */
  let selected = $state<Record<string, string[]>>({});
  let declared = $state<string[]>([]); // facets the node types declare, learnt from the hits
  let page = $state(0);
  let result = $state<NodeSearchResult | null>(null);
  let loading = $state(false);
  let error = $state('');
  let status = $state<IndexStatus | null>(null);
  let reindexing = $state(false);

  const admin = $derived(can.administer);

  function request(): NodeSearchRequest {
    const req: NodeSearchRequest = { kinds: ['node'], text: text.trim(), facets: [...BUILTIN, ...declared], limit: PAGE, offset: page * PAGE };
    if (scope === 'main') req.main = true;
    else if (scope === 'off') req.main = false;
    const sel = (n: string) => selected[n] ?? [];
    if (sel('type').length) req.types = sel('type');
    if (sel('namespace').length) req.namespaces = sel('namespace');
    if (sel('state').length) req.states = sel('state');
    if (sel('branch').length) req.branches = sel('branch');
    const ff = declared.filter((n) => sel(n).length).map((n) => ({ name: n, values: sel(n) }));
    if (ff.length) req.facetFilters = ff;
    return req;
  }

  let ctl: AbortController | undefined;
  async function run() {
    ctl?.abort();
    ctl = new AbortController();
    loading = true;
    error = '';
    try {
      const r = await nodeIndex.search(request(), ctl.signal);
      // facets the node types declare show up on the hits: count them from the next request on
      const names = new Set(declared);
      for (const h of r.hits ?? []) for (const k of Object.keys(h.facets ?? {})) names.add(k);
      result = r;
      if (names.size !== declared.length) {
        declared = [...names].sort();
        return run();
      }
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return;
      error = errorMessage(e);
      result = null;
    } finally {
      loading = false;
    }
  }

  async function refreshStatus() {
    try {
      status = await nodeIndex.status();
    } catch {
      status = null;
    }
  }

  // a new query, scope or filter starts again at the first page, after a pause in typing
  let timer: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    if (!searchOverlay.open) return;
    void text;
    void scope;
    void JSON.stringify(selected);
    page = 0;
    clearTimeout(timer);
    timer = setTimeout(run, 250);
    return () => clearTimeout(timer);
  });

  function toggle(facet: string, value: string) {
    const cur = selected[facet] ?? [];
    selected = { ...selected, [facet]: cur.includes(value) ? cur.filter((v) => v !== value) : [...cur, value] };
  }

  function go(p: number) {
    page = p;
    void run();
  }

  async function reindex() {
    reindexing = true;
    try {
      const r = await nodeIndex.reindex();
      notify(`Reindex: ${r.versions ?? 0} node versions published, the index is filling up.`, 'ok');
      setTimeout(() => {
        void refreshStatus();
        void run();
      }, 1500);
    } catch (e) {
      notify(errorMessage(e), 'error');
    } finally {
      reindexing = false;
    }
  }

  /** Choosing a node closes the overlay: what was selected shows in the workbench. */
  function open(h: NodeHit) {
    closeSearch();
    void openNode({ id: h.id, key: h.key, type: h.type, namespace: h.namespace }, { pin: true });
  }

  const total = $derived(result?.total ?? 0);
  const pages = $derived(Math.max(1, Math.ceil(total / PAGE)));
  const facetNames = $derived([...BUILTIN, ...declared].filter((n) => (result?.facets?.find((f) => f.name === n)?.counts ?? []).length > 0));

  // opening starts from a clean slate (the query may come from the header) and focuses the field
  $effect(() => {
    if (!searchOverlay.open) return;
    text = searchOverlay.q;
    selected = {};
    scope = 'main';
    result = null;
    cursor = 0;
    void refreshStatus();
    queueMicrotask(() => input?.focus());
  });

  $effect(() => {
    void result;
    cursor = 0;
  });

  function keydown(e: KeyboardEvent) {
    if (!searchOverlay.open) return;
    const hits = result?.hits ?? [];
    if (e.key === 'Escape') {
      e.preventDefault();
      closeSearch();
    } else if (e.key === 'ArrowDown' && hits.length) {
      e.preventDefault();
      cursor = Math.min(cursor + 1, hits.length - 1);
    } else if (e.key === 'ArrowUp' && hits.length) {
      e.preventDefault();
      cursor = Math.max(cursor - 1, 0);
    } else if (e.key === 'Enter' && hits[cursor] && document.activeElement === input) {
      e.preventDefault();
      open(hits[cursor]);
    }
  }
</script>

<svelte:window onkeydown={keydown} />

{#if searchOverlay.open}
  <div class="backdrop" role="presentation" onmousedown={closeSearch}>
    <div class="panel" role="dialog" aria-modal="true" aria-label="Search nodes" tabindex="-1" onmousedown={(e) => e.stopPropagation()}>
      <div class="bar">
        <Icon name="search" size={20} />
        <input
          class="q"
          type="search"
          bind:this={input}
          bind:value={text}
          placeholder="Words, or a description of what you look for"
          aria-label="Search text"
          autocomplete="off"
        />
        <select bind:value={scope} aria-label="Scope">
          <option value="main">Heads of main</option>
          <option value="all">Every branch</option>
          <option value="off">Not on main (in progress)</option>
        </select>
        {#if admin}
          <button type="button" class="small" onclick={reindex} disabled={reindexing} title="Empty the index and publish the graph again">
            {reindexing ? 'Reindex…' : 'Reindex'}
          </button>
        {/if}
        <button type="button" class="ghost small" aria-label="Close" onclick={closeSearch}><Icon name="x" size={16} /></button>
      </div>
      <p class="hint">
        {#if status}
          {status.semantic ? 'Hybrid search (full text + semantic).' : 'Full-text search only: no embedding model answers (alias “embed”).'}
          · {status.nodesIndexed ?? 0} node events indexed{#if status.errors && status.errors !== '0'}, {status.errors} errors{/if}
        {:else}
          The index service does not answer.
        {/if}
      </p>
      {#if error}<div class="alert small">{error}</div>{/if}

      <div class="cols">
        <aside class="facets" aria-label="Facets">
          {#each facetNames as name (name)}
            <div class="facet">
              <h3>{name}</h3>
              <ul>
                {#each result?.facets?.find((f) => f.name === name)?.counts ?? [] as c (c.value)}
                  <li>
                    <label class:on={(selected[name] ?? []).includes(c.value)}>
                      <input type="checkbox" checked={(selected[name] ?? []).includes(c.value)} onchange={() => toggle(name, c.value)} />
                      <span class="v">{c.value || '—'}</span>
                      <span class="n">{c.count}</span>
                    </label>
                  </li>
                {/each}
              </ul>
            </div>
          {/each}
        </aside>

        <section class="results" aria-busy={loading}>
          <div class="meta muted">
            {total}{result?.truncated ? '+' : ''} result{total === 1 ? '' : 's'}{loading ? ' …' : ''}
          </div>
          {#if !result?.hits?.length && !loading}
            <p class="empty">{text.trim() ? 'No node matches.' : 'Nothing indexed for this scope yet.'}</p>
          {/if}
          <ul role="listbox" aria-label="Results">
            {#each result?.hits ?? [] as h, i (h.id + ':' + h.version)}
              <li role="option" aria-selected={i === cursor}>
                <button type="button" class="hit" class:on={i === cursor} onclick={() => open(h)} onmousemove={() => (cursor = i)}>
                  <span class="k mono">{h.key}</span>
                  <span class="t">{h.type}{h.state ? ` · ${h.state}` : ''}</span>
                  <span class="ns muted">{h.namespace}</span>
                  <span class="b muted">v{h.version}{h.main ? ' · main' : ` · ${h.branch}`}</span>
                  {#each Object.entries(h.facets ?? {}) as [k, v] (k)}<span class="chip">{k}: {v}</span>{/each}
                </button>
              </li>
            {/each}
          </ul>
          {#if pages > 1}
            <div class="pager">
              <button type="button" class="small" disabled={page === 0 || loading} onclick={() => go(page - 1)}>Previous</button>
              <span class="muted">{page + 1} / {pages}</span>
              <button type="button" class="small" disabled={page + 1 >= pages || loading} onclick={() => go(page + 1)}>Next</button>
            </div>
          {/if}
        </section>
      </div>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 96;
    display: grid;
    place-items: start center;
    padding-top: 8vh;
    background: rgba(0, 0, 0, 0.45);
  }
  .panel {
    width: min(960px, calc(100vw - 28px));
    max-height: 84vh;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 0.8rem 1rem 1rem;
    overflow: hidden;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
  }
  .bar {
    display: flex;
    gap: 0.7rem;
    align-items: center;
    padding-bottom: 0.5rem;
    border-bottom: 1px solid var(--border);
  }
  .hint {
    margin: 0;
  }
  .bar input.q {
    flex: 1;
    min-height: 40px;
    padding: 0.3rem 0.2rem;
    font-size: 1.15rem;
    background: transparent;
    border: 0;
    border-radius: 0;
  }
  .bar input.q:focus {
    outline-offset: -2px;
  }
  .bar select {
    flex: none;
    width: auto;
  }
  .cols {
    min-height: 0;
    overflow: auto;
    display: grid;
    grid-template-columns: minmax(11rem, 16rem) 1fr;
    gap: 1rem;
    align-items: start;
  }
  @media (max-width: 720px) {
    .cols {
      grid-template-columns: 1fr;
    }
  }
  .facet h3 {
    margin: 0.2rem 0;
    font-size: 0.78rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--muted, inherit);
  }
  .facet ul,
  .results ul {
    list-style: none;
    margin: 0 0 0.8rem;
    padding: 0;
  }
  .facet label {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    padding: 0.1rem 0;
    cursor: pointer;
  }
  .facet label.on .v {
    font-weight: 600;
  }
  .facet .v {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .facet .n {
    color: var(--muted, inherit);
    font-variant-numeric: tabular-nums;
  }
  .hit {
    display: flex;
    flex-wrap: wrap;
    gap: 0.2rem 0.7rem;
    align-items: baseline;
    width: 100%;
    text-align: left;
    padding: 0.45rem 0.6rem;
    background: transparent;
    border: 0;
    border-bottom: 1px solid var(--border, #8884);
    cursor: pointer;
    color: inherit;
  }
  .hit.on,
  .hit:focus-visible {
    background: var(--hover, #8881);
  }
  .k {
    font-weight: 600;
  }
  .chip {
    font-size: 0.75rem;
    padding: 0 0.4rem;
    border: 1px solid var(--border, #8884);
    border-radius: 999px;
  }
  .meta {
    margin-bottom: 0.4rem;
  }
  .pager {
    display: flex;
    gap: 0.8rem;
    align-items: center;
    justify-content: center;
  }
</style>
