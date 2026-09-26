<script lang="ts">
  import {
    engine,
    graph,
    errorMessage,
    nodeTitle,
    type ChangeImpact,
    type GraphNode,
    type ItemInput,
    type Process,
  } from '../api';
  
  let { process, onsubmitted }: { process: Process; onsubmitted: (p: Process) => void } = $props();

  const task = $derived(process.pending);
  const action = $derived(task?.action ?? '');
  const mode = $derived<'select' | 'review' | 'raw'>(
    /select/i.test(action) ? 'select' : /review|revue|valid|approv/i.test(action) ? 'review' : 'raw',
  );

  let nodes = $state<GraphNode[]>([]);
  let changeImpacts = $state<ChangeImpact[]>([]);
  let loadError = $state('');
  let submitting = $state(false);
  let error = $state('');

  // --- impact selection
  let selected = $state<Record<string, boolean>>({});
  let reason = $state('');
  let filter = $state('');

  // --- change impact review
  let nodeDecisions = $state<Record<string, boolean>>({});
  let comment = $state('');

  // --- raw JSON
  let raw = $state('');
  let showRaw = $state(false);

  // Loads the change and its starting baseline when the task changes.
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
      changeImpacts = change?.nodes ?? [];
      nodes = change?.baselineId
        ? ((await graph.getBaselineGraph(change.baselineId, ctrl.signal)).nodes ?? [])
        : [];
      const initNodes: Record<string, boolean> = {};
      for (const n of changeImpacts) if (isPending(n) && n.id) initNodes[n.id] = true;
      nodeDecisions = initNodes;
    })().catch((e) => {
      if (!ctrl.signal.aborted) loadError = errorMessage(e);
    });
    return () => ctrl.abort();
  });

  /** a change impact awaiting a decision: written directly, on the main flow, not replaced */
  const isPending = (n: ChangeImpact) => n.review === 'proposed' && !n.flow && !n.superseded;
  const pendingNodes = $derived(changeImpacts.filter(isPending));

  const q = $derived(filter.trim().toLowerCase());
  const shownNodes = $derived(
    nodes
      .filter((n) => !n.deleted)
      .filter((n) => !q || `${n.key} ${n.type} ${nodeTitle(n)}`.toLowerCase().includes(q)),
  );
  const selectedKeys = $derived(Object.keys(selected).filter((k) => selected[k]));

  const built = $derived.by((): ItemInput[] => {
    if (mode === 'select') {
      const r = reason.trim() || 'impacted';
      return selectedKeys.map((key) => ({
        kind: 'changeImpact',
        changeImpact: { op: 'declare', intent: 'modified', key, rationale: r },
      }));
    }
    if (mode === 'review') {
      const why = comment.trim();
      return [
        ...pendingNodes.map(
          (n): ItemInput => ({
            kind: 'changeImpact',
            changeImpact: { op: 'review', node: n.key, accept: nodeDecisions[n.id ?? ''] ?? true, comment: why },
          }),
        ),
      ];
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
      error = `Invalid JSON: ${errorMessage(e)}`;
      return;
    }
    const list = Array.isArray(parsed) ? parsed : [parsed];
    send(list as ItemInput[]);
  }
</script>

<section class="card task">
  <div class="row">
    <h3 class="grow" style="margin: 0">Human task: <code>{action}</code></h3>
  </div>
  {#if task?.description}<p class="desc">{task.description}</p>{/if}
  {#if task?.instructions}<p class="instructions">{task.instructions}</p>{/if}

  {#if loadError}<div class="alert">{loadError}</div>{/if}

  {#if mode === 'select'}
    <form onsubmit={submitStructured}>
      <div class="row" style="margin-bottom: 0.5rem">
        <strong class="grow">Impacted elements ({selectedKeys.length})</strong>
        <input class="filter" type="text" placeholder="Filter…" bind:value={filter} />
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
          <p class="empty">No nodes.</p>
        {/each}
      </div>
      <div class="field" style="margin-top: 0.75rem">
        <label for="ht-reason">Reason</label>
        <textarea id="ht-reason" rows="2" bind:value={reason} placeholder="Why are these elements impacted?"></textarea>
      </div>
      <div class="row">
        <button class="primary" type="submit" disabled={submitting || selectedKeys.length === 0}>
          {submitting ? 'Sending…' : 'Send impacts'}
        </button>
        <button type="button" class="small" onclick={openRaw}>View / edit as JSON</button>
      </div>
    </form>
  {:else if mode === 'review'}
    <form onsubmit={submitStructured}>
      {#if pendingNodes.length}
        <ul class="proposals">
          {#each pendingNodes as n (n.id)}
            {@const id = n.id ?? ''}
            <li>
              <div class="row">
                <span class="grow"><code>{n.key}</code> <span class="type">{n.type}</span> · {n.intent}: {n.rationale}</span>
                <div class="toggle" role="group" aria-label="Decision on {n.key}">
                  <button type="button" class="small" class:on-accept={nodeDecisions[id] !== false} aria-pressed={nodeDecisions[id] !== false} onclick={() => (nodeDecisions[id] = true)}>Accept</button>
                  <button type="button" class="small" class:on-reject={nodeDecisions[id] === false} aria-pressed={nodeDecisions[id] === false} onclick={() => (nodeDecisions[id] = false)}>Reject</button>
                </div>
              </div>
              {#if n.post?.id}<div class="hint">written as v{n.post.version}</div>{:else if n.intent === 'created'}<div class="hint">not written yet</div>{/if}
            </li>
          {/each}
        </ul>
        <div class="field" style="margin-top: 0.75rem">
          <label for="ht-comment">Comment (required)</label>
          <textarea id="ht-comment" rows="2" bind:value={comment} placeholder="Why are these decisions taken?"></textarea>
        </div>
      {/if}
      {#if !pendingNodes.length}
        <p class="empty">Nothing awaiting a decision.</p>
      {/if}
      <div class="row" style="margin-top: 0.75rem">
        <button class="primary" type="submit" disabled={submitting || pendingNodes.length === 0 || (pendingNodes.length > 0 && !comment.trim())}>
          {submitting ? 'Sending…' : 'Send decisions'}
        </button>
        <button type="button" class="small" onclick={openRaw}>View / edit as JSON</button>
      </div>
    </form>
  {/if}

  {#if mode === 'raw' || showRaw}
    <div class="raw">
      <label for="ht-raw">Items (engine input format, JSON array)</label>
      <textarea
        id="ht-raw"
        class="mono"
        rows="8"
        bind:value={raw}
        placeholder={'[\n  {"kind": "changeImpact", "changeImpact": {"op": "declare", "intent": "modified", "key": "REQ-1", "rationale": "…"}}\n]'}
      ></textarea>
      <div class="row" style="margin-top: 0.5rem">
        <button class="primary" type="button" onclick={submitRaw} disabled={submitting}>
          {submitting ? 'Sending…' : 'Send JSON'}
        </button>
        {#if mode !== 'raw'}
          <button type="button" class="small" onclick={() => (showRaw = false)}>Hide</button>
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
