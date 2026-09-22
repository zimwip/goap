<script lang="ts">
  import { graph, errorMessage, formatDate, nodeTitle, type Baseline, type GraphNode, type Link } from '../api';
  import { nav, go } from '../nav.svelte';

  let baselines = $state<Baseline[]>([]);
  let listError = $state('');

  let baseline = $state<Baseline | undefined>();
  let nodes = $state<GraphNode[]>([]);
  let links = $state<Link[]>([]);
  let suspect = $state<Link[]>([]);
  let loading = $state(false);
  let error = $state('');
  let filter = $state('');

  const selected = $derived(nav.id);

  $effect(() => {
    graph
      .listBaselines()
      .then((r) => {
        baselines = r.baselines ?? [];
        if (nav.tab === 'referentiel' && !nav.id && baselines.length) go('referentiel', baselines[baselines.length - 1].id ?? '');
      })
      .catch((e) => (listError = errorMessage(e)));
  });

  $effect(() => {
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

  // Les liens suspects ne figurent pas forcément dans `links` : on les fusionne.
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

  function stale(ref: { id?: string; version?: number } | undefined): boolean {
    const n = ref?.id ? byId.get(ref.id) : undefined;
    return !!n && (ref?.version ?? 0) !== (n.version ?? 0);
  }
</script>

<div class="row head">
  <h2 class="grow">Référentiel</h2>
  <div class="picker">
    <select value={selected} onchange={(e) => go('referentiel', e.currentTarget.value)} aria-label="Référentiel">
      {#if !selected}<option value="">— choisir —</option>{/if}
      {#each baselines as b (b.id)}
        <option value={b.id}>{b.name || b.id}</option>
      {/each}
    </select>
  </div>
</div>

{#if listError}<div class="alert">{listError}</div>{/if}
{#if error}<div class="alert">{error}</div>{/if}

{#if baseline}
  <p class="hint">
    <code>{baseline.id}</code>
    {#if baseline.createdAt} · créé le {formatDate(baseline.createdAt)}{/if}
    {#if baseline.changeId} · issu du changement <a href="#changement/{baseline.changeId}">{baseline.changeId.slice(0, 8)}</a>{/if}
    · {nodes.length} nœuds · {allLinks.length} liens
    {#if suspect.length}· <span class="suspect-count">{suspect.length} suspect{suspect.length > 1 ? 's' : ''}</span>{/if}
  </p>
{/if}

{#if loading}<p class="empty">Chargement…</p>{/if}

{#if selected && !loading && !error}
  <section class="card">
    <div class="row" style="margin-bottom: 0.5rem">
      <h3 class="grow" style="margin: 0">Nœuds</h3>
      <input class="filter" type="text" placeholder="Filtrer…" bind:value={filter} />
    </div>
    {#if shownNodes.length}
      <div class="scroll">
        <table>
          <thead><tr><th>Clé</th><th>Type</th><th>Version</th><th>Titre</th></tr></thead>
          <tbody>
            {#each shownNodes as n (n.id)}
              <tr class:deleted={n.deleted}>
                <td><code>{n.key}</code></td>
                <td>{n.type}</td>
                <td>v{n.version ?? 0}</td>
                <td>{nodeTitle(n)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else}
      <p class="empty">Aucun nœud.</p>
    {/if}
  </section>

  <section class="card">
    <h3>Liens</h3>
    {#if allLinks.length}
      <div class="scroll">
        <table>
          <thead><tr><th>Source</th><th>Type</th><th>Cible</th><th></th></tr></thead>
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
                    <span class="tag" title="Une extrémité du lien a évolué depuis sa création">suspect</span>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else}
      <p class="empty">Aucun lien.</p>
    {/if}
  </section>
{/if}

<style>
  .head {
    margin-bottom: 0.4rem;
  }
  .head h2 {
    margin: 0;
  }
  .picker {
    min-width: 240px;
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
