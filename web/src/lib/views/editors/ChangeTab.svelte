<script lang="ts">
  // Change tab: change set items and applying to the baseline.
  import {
    graph,
    errorMessage,
    formatDate,
    shortId,
    type Baseline,
    ITEM_SUPERSEDED,
    type ChangeItem,
    type ChangeSet,
    type GraphNode,
    type LifecycleTransition,
    type NodeRef,
    type Struct,
  } from '../../api';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import { makeContext, describeProposal, refKey, show } from '../../items';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import ChangeLifecycle from '../../components/ChangeLifecycle.svelte';
  import { lifecycleRows, reopenable, nodeTypeNames, typeNodeRef, lifecycleResolver, supersededIds, type LifecycleRow } from '../../lifecycle';
  import { openTab } from '../../shell/tabs.svelte';
  import { provideActions, notify } from '../../shell/workbench.svelte';
  import { refreshChanges, refreshBaselines } from '../../stores/catalog.svelte';
  import { processes } from '../../stores/live.svelte';

  let { tab }: { tab: Tab } = $props();

  let change = $state<ChangeSet | undefined>();
  let nodes = $state<GraphNode[]>([]);
  let attached = $state<NodeRef[]>([]);
  let extraNodes = $state<string[]>([]);
  let moving = $state('');
  let loading = $state(false);
  let error = $state('');

  let baselineName = $state('');
  let applying = $state(false);
  let applied = $state<Baseline | undefined>();

  const selected = $derived(tab.params.id ?? '');

  async function load(id: string, signal?: AbortSignal) {
    loading = true;
    error = '';
    try {
      const c = (await graph.getChange(id, signal)).change;
      change = c;
      if (!baselineName) baselineName = c?.title ? `${c.title}` : `change-${shortId(id)}`;
      nodes = c?.baselineId ? ((await graph.getBaselineGraph(c.baselineId, signal)).nodes ?? []) : [];
      attached = (await graph.getChangeNodes(id, signal)).nodes ?? [];
    } catch (e) {
      if (!signal?.aborted) error = errorMessage(e);
    } finally {
      if (!signal?.aborted) loading = false;
    }
  }

  $effect(() => {
    const id = selected;
    change = undefined;
    attached = [];
    extraNodes = [];
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
  const closed = $derived(change?.status === 'applied' || change?.status === 'abandoned');
  const lcRows = $derived(lifecycleRows(nodes, attached, items, extraNodes));
  const lcCandidates = $derived(reopenable(nodes, lcRows));
  const typeNames = $derived(nodeTypeNames(nodes));
  const lifecycleOf = $derived(lifecycleResolver(nodes));
  const takenKeys = $derived([
    ...nodes.map((n) => n.key ?? ''),
    ...items.filter((i) => i.proposal?.op === 'create_node' && !supersededIds(items).has(i.id ?? '')).map((i) => i.proposal?.node?.key ?? ''),
  ]);
  const stuckEditable = $derived(lcRows.some((r) => r.lifecycle && r.editable && !r.removal));

  /** Proposes a transition of a node in this change (the server checks it). */
  /** Proposes new values for some properties of a node (the server checks the state). */
  async function edit(row: LifecycleRow, patch: Record<string, unknown>, state?: string): Promise<boolean> {
    if (row.created) return editCreated(row, patch, state);
    if (!change?.id || !row.node.id) return false;
    moving = `${row.node.id}:edit`;
    error = '';
    try {
      const base: NodeRef = { id: row.node.id, version: row.node.version };
      await graph.addItems(change.id, [{ kind: 'proposal', proposal: { op: 'update_node', node: { base, props: patch as Struct } } }]);
      await load(change.id);
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      moving = '';
    }
  }

  const newId = () => crypto.randomUUID();

  /** create_node proposal (and its instanceOf link to the node type) for a node the change creates. */
  function creationItems(key: string, type: string, props: Struct, state: string, supersedes: string[] = [], oldLinks: string[] = []): ChangeItem[] {
    const id = newId();
    const born = lifecycleOf(type) && state && state !== lifecycleOf(type)?.initial ? state : undefined;
    const out: ChangeItem[] = [
      { id, kind: 'proposal', supersedes, proposal: { op: 'create_node', node: { key, type, props, ...(born ? { state: born } : {}) } } },
    ];
    const typeRef = typeNodeRef(nodes, type);
    if (typeRef) {
      out.push({ id: newId(), kind: 'proposal', type: 'metamodel', supersedes: oldLinks, proposal: { op: 'add_link', link: { type: 'instanceOf', from: { item: id }, to: { node: typeRef } } } });
    }
    return out;
  }

  /** Creates a node: identity and type only. */
  async function createNode(key: string, type: string, state: string): Promise<boolean> {
    if (!change?.id) return false;
    moving = 'create';
    error = '';
    try {
      await graph.addItems(change.id, creationItems(key, type, {}, state));
      await load(change.id);
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      moving = '';
    }
  }

  /** Edits a node the change creates: a new create_node replaces the previous one. */
  async function editCreated(row: LifecycleRow, patch: Record<string, unknown>, state?: string): Promise<boolean> {
    const old = row.created;
    if (!change?.id || !old?.id) return false;
    moving = `${old.id}:edit`;
    error = '';
    try {
      const gone = supersededIds(items);
      const linkIds = items.filter((i) => i.proposal?.op === 'add_link' && i.proposal.link?.from?.item === old.id && !gone.has(i.id ?? '')).map((i) => i.id ?? '');
      // anything else pointing at the pending node would be left dangling
      const dependents = items.filter(
        (i) =>
          !gone.has(i.id ?? '') &&
          !linkIds.includes(i.id ?? '') &&
          (i.proposal?.link?.from?.item === old.id || i.proposal?.link?.to?.item === old.id || i.derivedFrom?.includes(old.id ?? '')),
      );
      if (dependents.length) throw new Error(`${row.node.key} is already linked by other items of the change: it can no longer be edited here.`);
      const props = { ...row.props, ...patch } as Struct;
      await graph.addItems(change.id, creationItems(row.node.key ?? '', row.node.type ?? '', props, state ?? row.effective, [old.id], linkIds));
      await load(change.id);
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      moving = '';
    }
  }

  const reject = (id: string, comment: string): ChangeItem => ({ kind: 'decision', decision: { item: id, accept: false, comment } });

  /** Deletes a node when the change is applied, or discards a node the change creates. */
  async function removeNode(row: LifecycleRow): Promise<boolean> {
    if (!change?.id) return false;
    moving = `${row.node.id}:delete`;
    error = '';
    try {
      if (row.created?.id) {
        const gone = supersededIds(items);
        const own = items.filter((i) => i.proposal?.op === 'add_link' && i.proposal.link?.from?.item === row.created?.id && !gone.has(i.id ?? ''));
        const dependents = items.filter(
          (i) =>
            !gone.has(i.id ?? '') &&
            !own.includes(i) &&
            (i.proposal?.link?.from?.item === row.created?.id || i.proposal?.link?.to?.item === row.created?.id || i.derivedFrom?.includes(row.created?.id ?? '')),
        );
        if (dependents.length) throw new Error(`${row.node.key} is linked by other items of the change: it cannot be discarded here.`);
        await graph.addItems(change.id, [row.created, ...own].map((i) => reject(i.id ?? '', `discarded ${row.node.key}`)));
      } else {
        const base: NodeRef = { id: row.node.id, version: row.node.version };
        await graph.addItems(change.id, [{ kind: 'proposal', proposal: { op: 'delete_node', node: { base } } }]);
      }
      await load(change.id);
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      moving = '';
    }
  }

  /** Withdraws a deletion proposed by this change. */
  async function undoDelete(row: LifecycleRow): Promise<boolean> {
    if (!change?.id || !row.removal?.id) return false;
    moving = `${row.node.id}:delete`;
    error = '';
    try {
      await graph.addItems(change.id, [reject(row.removal.id, `keep ${row.node.key}`)]);
      await load(change.id);
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      moving = '';
    }
  }

  async function move(row: LifecycleRow, t: LifecycleTransition) {
    if (!change?.id || !row.node.id) return;
    moving = `${row.node.id}:${t.name}`;
    error = '';
    try {
      const base: NodeRef = { id: row.node.id, version: row.node.version };
      await graph.addItems(change.id, [{ kind: 'proposal', proposal: { op: 'transition_node', node: { base, state: t.to } } }]);
      await load(change.id);
    } catch (e) {
      error = errorMessage(e);
    } finally {
      moving = '';
    }
  }

  async function apply() {
    if (!change?.id) return;
    applying = true;
    error = '';
    try {
      applied = (await graph.applyChange(change.id, baselineName.trim())).baseline;
      await load(change.id);
      void refreshChanges();
      void refreshBaselines();
      notify(`Baseline ${applied?.name || shortId(applied?.id)} created.`, 'ok');
    } catch (e) {
      error = errorMessage(e);
    } finally {
      applying = false;
    }
  }

  function markdownOf(i: ChangeItem): string | undefined {
    const md = i.data?.['markdown'];
    return typeof md === 'string' ? md : undefined;
  }

  const related = $derived([...processes.values()].filter((p) => p.changeId && p.changeId === selected));

  /** Execution journal of the change, optionally centered on a record. */
  function openJournal(record = '') {
    if (change?.id) openTab({ kind: 'journal', params: { id: change.id, process: '', record } }, { pin: true });
  }

  function provenance(i: ChangeItem): string {
    const parts = [i.producedBy ? `Produced by ${i.producedBy}` : 'Unknown producer'];
    if (i.execution) parts.push(`execution ${shortId(i.execution)} — open in the execution journal`);
    if (i.supersedes?.length) parts.push(`supersedes ${i.supersedes.map(shortId).join(', ')}`);
    if (i.status === ITEM_SUPERSEDED) parts.push('superseded by a more recent item');
    return parts.join(' · ');
  }

  function openBaseline(id: string | undefined) {
    if (id) openTab({ kind: 'baseline', params: { id } });
  }

  provideActions(
    () => tab.id,
    () => [
      { id: 'refresh', label: 'Refresh', icon: 'refresh', disabled: loading, run: () => load(selected) },
      {
        id: 'journal',
        label: "Execution journal",
        icon: 'list',
        disabled: !change,
        title: 'Ticks, actions, model calls and decisions of this change\'s processes',
        run: () => openJournal(),
      },
      {
        id: 'apply',
        label: applying ? 'Applying…' : 'Apply',
        icon: 'check',
        primary: true,
        disabled: !change || isApplied || applying || !baselineName.trim() || stuckEditable,
        title: stuckEditable ? 'Move the nodes out of their editable state first' : 'Create a new baseline from the change',
        run: apply,
      },
    ],
  );
</script>


{#snippet producer(i: ChangeItem)}
  {#if i.execution}
    <button type="button" class="link" title={provenance(i)} onclick={() => openJournal(i.execution)}>{i.producedBy || shortId(i.execution)}</button>
  {:else}
    <span title={provenance(i)}>{i.producedBy}</span>
  {/if}
  {#if i.supersedes?.length}<span class="hint" title={provenance(i)}> (supersedes {i.supersedes.length})</span>{/if}
{/snippet}

<div class="editor-page">
{#if error}<div class="alert">{error}</div>{/if}

{#if loading && !change}
  <p class="empty">Loading…</p>
{/if}

{#if change}
  <section class="card">
    <div class="editor-head">
      <Icon name="diff" size={18} />
      <h2>{change.title || 'Untitled'}</h2>
      <StatusBadge status={change.status} />
    </div>
    {#if change.intent}<p class="intent">"{change.intent}"</p>{/if}
    <dl class="meta">
      <dt>ID</dt><dd><code>{change.id}</code></dd>
      {#if change.methodology}<dt>Methodology</dt><dd>{change.methodology}</dd>{/if}
      {#if change.goal}<dt>Goal</dt><dd><code>{change.goal}</code></dd>{/if}
      {#if change.baselineId}
        <dt>Starting baseline</dt>
        <dd><button type="button" class="link mono" onclick={() => openBaseline(change?.baselineId)}>{shortId(change.baselineId)}</button></dd>
      {/if}
      {#if change.resultBaselineId}
        <dt>Resulting baseline</dt>
        <dd><button type="button" class="link mono" onclick={() => openBaseline(change?.resultBaselineId)}>{shortId(change.resultBaselineId)}</button></dd>
      {/if}
      {#if change.createdAt}<dt>Created on</dt><dd>{formatDate(change.createdAt)}</dd>{/if}
      <dt>Journal</dt>
      <dd><button type="button" class="link" onclick={() => openJournal()}>Execution journal</button></dd>
      {#if related.length}
        <dt>Executions</dt>
        <dd class="runs">
          {#each related as p (p.id)}
            <button type="button" class="link" onclick={() => openTab({ kind: 'run', params: { id: p.id ?? '' } })}>{p.agent || shortId(p.id)}</button>
            <StatusBadge status={p.status} />
          {/each}
        </dd>
      {/if}
    </dl>

    <div class="apply row">
      <div class="grow">
        <label for="bname">Name of the new baseline</label>
        <input id="bname" type="text" bind:value={baselineName} disabled={isApplied} />
      </div>
      <button class="primary" onclick={apply} disabled={isApplied || applying || !baselineName.trim() || stuckEditable} title={stuckEditable ? 'Move the nodes out of their editable state first' : ''}>
        {applying ? 'Applying…' : 'Apply'}
      </button>
    </div>
    {#if applied}
      <div class="alert ok" style="margin: 0.75rem 0 0">
        Baseline <button type="button" class="link" onclick={() => openBaseline(applied?.id)}>{applied.name || applied.id}</button> created.
      </div>
    {/if}
  </section>

  <ChangeLifecycle rows={lcRows} candidates={lcCandidates} disabled={closed} busy={moving} onmove={move} onedit={edit} types={typeNames} {lifecycleOf} keys={takenKeys} oncreate={createNode} onremove={removeNode} onundo={undoDelete} onadd={(id) => (extraNodes = [...extraNodes, id])} />

  <section class="card">
    <h3>Impacts <span class="count">{groups.impact.length}</span></h3>
    {#if groups.impact.length}
      <table>
        <thead><tr><th>Item</th><th>Type</th><th>Reason</th><th>Produced by</th></tr></thead>
        <tbody>
          {#each groups.impact as i (i.id)}
            <tr class:superseded={i.status === ITEM_SUPERSEDED}>
              <td><code>{refKey(ctx, i.target)}</code></td>
              <td>{i.type}</td>
              <td>{show(i.data?.['reason'])}</td>
              <td class="muted">{@render producer(i)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p class="empty">No impacts.</p>
    {/if}
  </section>

  <section class="card">
    <h3>Proposals <span class="count">{groups.proposal.length}</span></h3>
    {#if groups.proposal.length}
      <ul class="list">
        {#each groups.proposal as i (i.id)}
          <li class:superseded={i.status === ITEM_SUPERSEDED}>
            <div class="row">
              <strong class="grow">{describeProposal(ctx, i)}</strong>
              <StatusBadge status={i.status} />
            </div>
            {#if i.proposal?.node?.props}
              <pre>{JSON.stringify(i.proposal.node.props, null, 2)}</pre>
            {/if}
            <div class="hint">{@render producer(i)} · <code>{shortId(i.id)}</code></div>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="empty">No proposals.</p>
    {/if}
  </section>

  <section class="card">
    <h3>Decisions <span class="count">{groups.decision.length}</span></h3>
    {#if groups.decision.length}
      <table>
        <thead><tr><th>Proposal</th><th>Decision</th><th>Comment</th></tr></thead>
        <tbody>
          {#each groups.decision as i (i.id)}
            <tr class:superseded={i.status === ITEM_SUPERSEDED}>
              <td>{describeProposal(ctx, ctx.items.get(i.decision?.item ?? ''))}</td>
              <td><StatusBadge status={i.decision?.accept ? 'accepted' : 'rejected'} /></td>
              <td>{i.decision?.comment ?? ''}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p class="empty">No decisions.</p>
    {/if}
  </section>

  <section class="card">
    <h3>Artifacts <span class="count">{groups.artifact.length}</span></h3>
    {#each groups.artifact as i (i.id)}
      {@const md = markdownOf(i)}
      <article class="artifact" class:superseded={i.status === ITEM_SUPERSEDED}>
        <h4>
          {i.type || 'artifact'} <span class="hint">· {@render producer(i)}</span>
          {#if i.status === ITEM_SUPERSEDED}<StatusBadge status={i.status} />{/if}
        </h4>
        {#if md !== undefined}
          <pre class="md">{md}</pre>
        {:else if i.data}
          <pre>{JSON.stringify(i.data, null, 2)}</pre>
        {/if}
      </article>
    {:else}
      <p class="empty">No artifacts.</p>
    {/each}
  </section>

  {#if others.length}
    <section class="card">
      <h3>Other items</h3>
      <pre>{JSON.stringify(others, null, 2)}</pre>
    </section>
  {/if}
{/if}
</div>

<style>
  .runs {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem 0.5rem;
    align-items: center;
  }
  .intent {
    margin: 0.6rem 0;
    font-style: italic;
  }
  .meta {
    margin: 0.5rem 0 0.8rem;
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
  .superseded {
    opacity: 0.55;
  }
  .superseded strong,
  .superseded td,
  .superseded h4 {
    text-decoration: line-through;
  }
  .md {
    font-size: 0.88rem;
    line-height: 1.55;
  }
</style>
