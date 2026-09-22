<script lang="ts">
  import {
    engine,
    graph,
    errorMessage,
    nodeTitle,
    type ChangeItem,
    type GraphNode,
    type ItemInput,
    type Process,
  } from '../api';
  import { makeContext, describeProposal } from '../items';

  let { process, onsubmitted }: { process: Process; onsubmitted: (p: Process) => void } = $props();

  const task = $derived(process.pending);
  const action = $derived(task?.action ?? '');
  const mode = $derived<'select' | 'review' | 'raw'>(
    /select/i.test(action) ? 'select' : /review|revue|valid|approv/i.test(action) ? 'review' : 'raw',
  );

  let nodes = $state<GraphNode[]>([]);
  let items = $state<ChangeItem[]>([]);
  let loadError = $state('');
  let submitting = $state(false);
  let error = $state('');

  // --- sélection des impacts
  let selected = $state<Record<string, boolean>>({});
  let reason = $state('');
  let filter = $state('');

  // --- revue des propositions
  let decisions = $state<Record<string, boolean>>({});

  // --- JSON brut
  let raw = $state('');
  let showRaw = $state(false);

  // Charge le changement et son référentiel de départ quand la tâche change.
  const changeId = $derived(process.changeId);
  const step = $derived(task?.step);
  $effect(() => {
    void step;
    if (!changeId) return;
    const ctrl = new AbortController();
    const id = changeId;
    loadError = '';
    (async () => {
      const change = (await graph.getChange(id, ctrl.signal)).change;
      items = change?.items ?? [];
      nodes = change?.baselineId
        ? ((await graph.getBaselineGraph(change.baselineId, ctrl.signal)).nodes ?? [])
        : [];
      const init: Record<string, boolean> = {};
      for (const it of items) if (it.kind === 'proposal' && it.status === 'proposed' && it.id) init[it.id] = true;
      decisions = init;
    })().catch((e) => {
      if (!ctrl.signal.aborted) loadError = errorMessage(e);
    });
    return () => ctrl.abort();
  });

  const ctx = $derived(makeContext(nodes, items));
  const pendingProposals = $derived(items.filter((i) => i.kind === 'proposal' && i.status === 'proposed'));

  const q = $derived(filter.trim().toLowerCase());
  const shownNodes = $derived(
    nodes
      .filter((n) => !n.deleted)
      .filter((n) => !q || `${n.key} ${n.type} ${nodeTitle(n)}`.toLowerCase().includes(q)),
  );
  const selectedKeys = $derived(Object.keys(selected).filter((k) => selected[k]));

  const built = $derived.by((): ItemInput[] => {
    if (mode === 'select') {
      const r = reason.trim();
      return selectedKeys.map((key) => ({
        kind: 'impact',
        type: 'direct',
        target: key,
        ...(r ? { data: { reason: r } } : {}),
      }));
    }
    if (mode === 'review') {
      return pendingProposals.map((p) => ({
        kind: 'decision',
        decision: { item: `@${p.id}`, accept: decisions[p.id ?? ''] ?? true },
      }));
    }
    return [];
  });

  function openRaw() {
    raw = JSON.stringify(built, null, 2);
    showRaw = true;
  }

  async function send(list: ItemInput[]) {
    if (!process.id) return;
    submitting = true;
    error = '';
    try {
      const res = await engine.submitHumanInput(process.id, list);
      if (res.process) onsubmitted(res.process);
    } catch (e) {
      error = errorMessage(e);
    } finally {
      submitting = false;
    }
  }

  function submitStructured(e: SubmitEvent) {
    e.preventDefault();
    send(built);
  }

  function submitRaw() {
    let parsed: unknown;
    try {
      parsed = JSON.parse(raw || '[]');
    } catch (e) {
      error = `JSON invalide : ${errorMessage(e)}`;
      return;
    }
    const list = Array.isArray(parsed) ? parsed : [parsed];
    send(list as ItemInput[]);
  }
</script>

<section class="card task">
  <div class="row">
    <h3 class="grow" style="margin: 0">Tâche humaine : <code>{action}</code></h3>
  </div>
  {#if task?.description}<p class="desc">{task.description}</p>{/if}
  {#if task?.instructions}<p class="instructions">{task.instructions}</p>{/if}

  {#if loadError}<div class="alert">{loadError}</div>{/if}

  {#if mode === 'select'}
    <form onsubmit={submitStructured}>
      <div class="row" style="margin-bottom: 0.5rem">
        <strong class="grow">Éléments impactés ({selectedKeys.length})</strong>
        <input class="filter" type="text" placeholder="Filtrer…" bind:value={filter} />
      </div>
      <div class="nodes">
        {#each shownNodes as n (n.id)}
          <label class="node">
            <input type="checkbox" bind:checked={selected[n.key ?? '']} />
            <code>{n.key}</code>
            <span class="type">{n.type}</span>
            <span class="title">{nodeTitle(n)}</span>
          </label>
        {:else}
          <p class="empty">Aucun nœud.</p>
        {/each}
      </div>
      <div class="field" style="margin-top: 0.75rem">
        <label for="ht-reason">Raison</label>
        <textarea id="ht-reason" rows="2" bind:value={reason} placeholder="Pourquoi ces éléments sont-ils impactés ?"></textarea>
      </div>
      <div class="row">
        <button class="primary" type="submit" disabled={submitting || selectedKeys.length === 0}>
          {submitting ? 'Envoi…' : 'Envoyer les impacts'}
        </button>
        <button type="button" class="small" onclick={openRaw}>Voir / éditer en JSON</button>
      </div>
    </form>
  {:else if mode === 'review'}
    <form onsubmit={submitStructured}>
      {#if pendingProposals.length}
        <ul class="proposals">
          {#each pendingProposals as p (p.id)}
            {@const id = p.id ?? ''}
            <li>
              <div class="row">
                <span class="grow">{describeProposal(ctx, p)}</span>
                <div class="toggle" role="group" aria-label="Décision">
                  <button
                    type="button"
                    class="small"
                    class:on-accept={decisions[id] !== false}
                    aria-pressed={decisions[id] !== false}
                    onclick={() => (decisions[id] = true)}>Accepter</button
                  >
                  <button
                    type="button"
                    class="small"
                    class:on-reject={decisions[id] === false}
                    aria-pressed={decisions[id] === false}
                    onclick={() => (decisions[id] = false)}>Rejeter</button
                  >
                </div>
              </div>
              {#if p.proposal?.node?.props}
                <details>
                  <summary>Propriétés</summary>
                  <pre>{JSON.stringify(p.proposal.node.props, null, 2)}</pre>
                </details>
              {/if}
            </li>
          {/each}
        </ul>
      {:else}
        <p class="empty">Aucune proposition en attente de décision.</p>
      {/if}
      <div class="row" style="margin-top: 0.75rem">
        <button class="primary" type="submit" disabled={submitting || pendingProposals.length === 0}>
          {submitting ? 'Envoi…' : 'Envoyer les décisions'}
        </button>
        <button type="button" class="small" onclick={openRaw}>Voir / éditer en JSON</button>
      </div>
    </form>
  {/if}

  {#if mode === 'raw' || showRaw}
    <div class="raw">
      <label for="ht-raw">Items (format d'entrée du moteur, tableau JSON)</label>
      <textarea
        id="ht-raw"
        class="mono"
        rows="8"
        bind:value={raw}
        placeholder={'[\n  {"kind": "impact", "type": "direct", "target": "REQ-1", "data": {"reason": "…"}}\n]'}
      ></textarea>
      <div class="row" style="margin-top: 0.5rem">
        <button class="primary" type="button" onclick={submitRaw} disabled={submitting}>
          {submitting ? 'Envoi…' : 'Envoyer le JSON'}
        </button>
        {#if mode !== 'raw'}
          <button type="button" class="small" onclick={() => (showRaw = false)}>Masquer</button>
        {/if}
      </div>
    </div>
  {/if}

  {#if error}<div class="alert" style="margin: 0.75rem 0 0">{error}</div>{/if}
</section>

<style>
  .task {
    border-color: var(--warn);
    box-shadow: 0 0 0 3px var(--warn-soft);
  }
  .desc {
    margin: 0.5rem 0 0.25rem;
  }
  .instructions {
    color: var(--muted);
    font-size: 0.9rem;
  }
  .filter {
    max-width: 200px;
  }
  .nodes {
    max-height: 18rem;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .node {
    display: grid;
    grid-template-columns: auto auto auto 1fr;
    align-items: center;
    gap: 0.6rem;
    margin: 0;
    padding: 0.35rem 0.6rem;
    font-size: 0.9rem;
    font-weight: 400;
    color: var(--text);
    border-bottom: 1px solid var(--border);
    cursor: pointer;
  }
  .node:last-child {
    border-bottom: none;
  }
  .node:hover {
    background: var(--surface-2);
  }
  .type {
    color: var(--muted);
    font-size: 0.8rem;
  }
  .title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .proposals {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .proposals li {
    padding: 0.5rem 0;
    border-bottom: 1px solid var(--border);
  }
  .toggle {
    display: inline-flex;
  }
  .toggle button:first-child {
    border-radius: var(--radius-sm) 0 0 var(--radius-sm);
  }
  .toggle button:last-child {
    border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
    border-left: none;
  }
  .on-accept,
  .on-accept:hover:not(:disabled) {
    background: var(--ok-soft);
    color: var(--ok);
  }
  .on-reject,
  .on-reject:hover:not(:disabled) {
    background: var(--danger-soft);
    color: var(--danger);
  }
  details {
    margin-top: 0.3rem;
  }
  summary {
    cursor: pointer;
    font-size: 0.85rem;
    color: var(--muted);
  }
  .raw {
    margin-top: 1rem;
  }
</style>
