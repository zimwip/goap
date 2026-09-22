<script lang="ts">
  import {
    graph,
    errorMessage,
    formatDate,
    shortId,
    type Baseline,
    type ChangeItem,
    type ChangeSet,
    type GraphNode,
  } from '../api';
  import { nav, go } from '../nav.svelte';
  import { makeContext, describeProposal, refKey, show } from '../items';
  import StatusBadge from './StatusBadge.svelte';

  let changes = $state<ChangeSet[]>([]);
  let change = $state<ChangeSet | undefined>();
  let nodes = $state<GraphNode[]>([]);
  let loading = $state(false);
  let error = $state('');

  let baselineName = $state('');
  let applying = $state(false);
  let applied = $state<Baseline | undefined>();

  const selected = $derived(nav.id);

  async function loadList() {
    try {
      changes = (await graph.listChanges()).changes ?? [];
    } catch {
      changes = []; // liste facultative : on garde la saisie manuelle
    }
  }

  async function load(id: string, signal?: AbortSignal) {
    loading = true;
    error = '';
    try {
      const c = (await graph.getChange(id, signal)).change;
      change = c;
      if (!baselineName) baselineName = c?.title ? `${c.title}` : `change-${shortId(id)}`;
      nodes = c?.baselineId ? ((await graph.getBaselineGraph(c.baselineId, signal)).nodes ?? []) : [];
    } catch (e) {
      if (!signal?.aborted) error = errorMessage(e);
    } finally {
      if (!signal?.aborted) loading = false;
    }
  }

  $effect(() => {
    loadList();
  });

  $effect(() => {
    const id = selected;
    change = undefined;
    applied = undefined;
    baselineName = '';
    if (!id) return;
    const ctrl = new AbortController();
    load(id, ctrl.signal);
    return () => ctrl.abort();
  });

  const items = $derived(change?.items ?? []);
  const ctx = $derived(makeContext(nodes, items));
  const groups = $derived({
    impact: items.filter((i) => i.kind === 'impact'),
    proposal: items.filter((i) => i.kind === 'proposal'),
    decision: items.filter((i) => i.kind === 'decision'),
    artifact: items.filter((i) => i.kind === 'artifact'),
  });
  const others = $derived(items.filter((i) => !['impact', 'proposal', 'decision', 'artifact'].includes(i.kind ?? '')));

  const isApplied = $derived(change?.status === 'applied');

  async function apply() {
    if (!change?.id) return;
    applying = true;
    error = '';
    try {
      applied = (await graph.applyChange(change.id, baselineName.trim())).baseline;
      await load(change.id);
      loadList();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      applying = false;
    }
  }

  let manualId = $state('');

  function markdownOf(i: ChangeItem): string | undefined {
    const md = i.data?.['markdown'];
    return typeof md === 'string' ? md : undefined;
  }
</script>

<div class="row head">
  <h2 class="grow">Changement</h2>
  <div class="picker">
    {#if changes.length}
      <select value={selected} onchange={(e) => go('changement', e.currentTarget.value)} aria-label="Changement">
        {#if !selected}<option value="">— choisir —</option>{/if}
        {#each changes as c (c.id)}
          <option value={c.id}>{c.title || shortId(c.id)} · {c.status}</option>
        {/each}
      </select>
    {:else}
      <form
        class="row"
        onsubmit={(e) => {
          e.preventDefault();
          if (manualId.trim()) go('changement', manualId.trim());
        }}
      >
        <input class="grow" type="text" placeholder="Identifiant du changement" bind:value={manualId} />
        <button type="submit">Ouvrir</button>
      </form>
    {/if}
  </div>
</div>

{#if error}<div class="alert">{error}</div>{/if}

{#if !selected}
  <p class="empty">Sélectionnez un changement, ou ouvrez-le depuis un processus.</p>
{:else if loading && !change}
  <p class="empty">Chargement…</p>
{/if}

{#if change}
  <section class="card">
    <div class="row">
      <h3 class="grow" style="margin: 0">{change.title || 'Sans titre'}</h3>
      <StatusBadge status={change.status} />
    </div>
    {#if change.intent}<p class="intent">« {change.intent} »</p>{/if}
    <dl class="meta">
      <dt>Identifiant</dt><dd><code>{change.id}</code></dd>
      {#if change.methodology}<dt>Méthodologie</dt><dd>{change.methodology}</dd>{/if}
      {#if change.goal}<dt>Objectif</dt><dd><code>{change.goal}</code></dd>{/if}
      {#if change.baselineId}
        <dt>Référentiel de départ</dt>
        <dd><a href="#referentiel/{change.baselineId}">{shortId(change.baselineId)}</a></dd>
      {/if}
      {#if change.resultBaselineId}
        <dt>Référentiel résultant</dt>
        <dd><a href="#referentiel/{change.resultBaselineId}">{shortId(change.resultBaselineId)}</a></dd>
      {/if}
      {#if change.createdAt}<dt>Créé le</dt><dd>{formatDate(change.createdAt)}</dd>{/if}
    </dl>

    <div class="apply row">
      <div class="grow">
        <label for="bname">Nom du nouveau référentiel</label>
        <input id="bname" type="text" bind:value={baselineName} disabled={isApplied} />
      </div>
      <button class="primary" onclick={apply} disabled={isApplied || applying || !baselineName.trim()}>
        {applying ? 'Application…' : 'Appliquer'}
      </button>
    </div>
    {#if applied}
      <div class="alert ok" style="margin: 0.75rem 0 0">
        Référentiel <a href="#referentiel/{applied.id}">{applied.name || applied.id}</a> créé.
      </div>
    {/if}
  </section>

  <section class="card">
    <h3>Impacts <span class="count">{groups.impact.length}</span></h3>
    {#if groups.impact.length}
      <table>
        <thead><tr><th>Élément</th><th>Type</th><th>Raison</th><th>Produit par</th></tr></thead>
        <tbody>
          {#each groups.impact as i (i.id)}
            <tr>
              <td><code>{refKey(ctx, i.target)}</code></td>
              <td>{i.type}</td>
              <td>{show(i.data?.['reason'])}</td>
              <td class="muted">{i.producedBy}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p class="empty">Aucun impact.</p>
    {/if}
  </section>

  <section class="card">
    <h3>Propositions <span class="count">{groups.proposal.length}</span></h3>
    {#if groups.proposal.length}
      <ul class="list">
        {#each groups.proposal as i (i.id)}
          <li>
            <div class="row">
              <strong class="grow">{describeProposal(ctx, i)}</strong>
              <StatusBadge status={i.status} />
            </div>
            {#if i.proposal?.node?.props}
              <pre>{JSON.stringify(i.proposal.node.props, null, 2)}</pre>
            {/if}
            <div class="hint">{i.producedBy} · <code>{shortId(i.id)}</code></div>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="empty">Aucune proposition.</p>
    {/if}
  </section>

  <section class="card">
    <h3>Décisions <span class="count">{groups.decision.length}</span></h3>
    {#if groups.decision.length}
      <table>
        <thead><tr><th>Proposition</th><th>Décision</th><th>Commentaire</th></tr></thead>
        <tbody>
          {#each groups.decision as i (i.id)}
            <tr>
              <td>{describeProposal(ctx, ctx.items.get(i.decision?.item ?? ''))}</td>
              <td><StatusBadge status={i.decision?.accept ? 'accepted' : 'rejected'} /></td>
              <td>{i.decision?.comment ?? ''}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p class="empty">Aucune décision.</p>
    {/if}
  </section>

  <section class="card">
    <h3>Artefacts <span class="count">{groups.artifact.length}</span></h3>
    {#each groups.artifact as i (i.id)}
      {@const md = markdownOf(i)}
      <article class="artifact">
        <h4>{i.type || 'artefact'} <span class="hint">· {i.producedBy}</span></h4>
        {#if md !== undefined}
          <pre class="md">{md}</pre>
        {:else if i.data}
          <pre>{JSON.stringify(i.data, null, 2)}</pre>
        {/if}
      </article>
    {:else}
      <p class="empty">Aucun artefact.</p>
    {/each}
  </section>

  {#if others.length}
    <section class="card">
      <h3>Autres items</h3>
      <pre>{JSON.stringify(others, null, 2)}</pre>
    </section>
  {/if}
{/if}

<style>
  .head {
    margin-bottom: 0.6rem;
  }
  .head h2 {
    margin: 0;
  }
  .picker {
    min-width: 280px;
  }
  .intent {
    margin: 0.6rem 0;
    font-style: italic;
  }
  .meta {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.2rem 1rem;
    margin: 0.5rem 0 1rem;
    font-size: 0.9rem;
  }
  .meta dt {
    color: var(--muted);
  }
  .meta dd {
    margin: 0;
  }
  .apply {
    align-items: flex-end;
    border-top: 1px solid var(--border);
    padding-top: 0.85rem;
  }
  .count {
    font-size: 0.8rem;
    color: var(--muted);
    font-weight: 500;
    margin-left: 0.3rem;
  }
  .muted {
    color: var(--muted);
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .list li {
    padding: 0.6rem 0;
    border-bottom: 1px solid var(--border);
    display: grid;
    gap: 0.4rem;
  }
  .list li:last-child {
    border-bottom: none;
  }
  .artifact + .artifact {
    margin-top: 1rem;
  }
  .md {
    font-size: 0.88rem;
    line-height: 1.55;
  }
</style>
