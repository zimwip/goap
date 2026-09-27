<script lang="ts">
  // Change tab: change items and applying to the baseline.
  import {
    graph,
    errorMessage,
    formatDate,
    shortId,
    type Baseline,
    ITEM_SUPERSEDED,
    type ChangeItem,
    type Change,
    type Flow,
    type BoardIssue,
    type GraphNode,
    type LifecycleTransition,
    type NodeRef,
  } from '../../api';
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import { makeContext } from '../../items';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import ChangeLifecycle from '../../components/ChangeLifecycle.svelte';
  import EditorPanes, { type Pane } from '../../components/EditorPanes.svelte';
  import { lifecycleRows, reopenable, nodeTypeNames, lifecycleResolver, loadPosts, writeNodeInChange, type PostVersions, type LifecycleRow } from '../../lifecycle';
  import { loadTypes, typeCatalog } from '../../stores/types.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { openNode } from '../../nodeEditors';
  import { provideActions, notify } from '../../shell/workbench.svelte';
  import { refreshChanges, refreshBaselines } from '../../stores/catalog.svelte';
  import { processes } from '../../stores/live.svelte';
  import FlowGraph from '../../components/FlowGraph.svelte';
  import FlowActions from '../../components/FlowActions.svelte';
  import FlowBranchInfo from '../../components/FlowBranchInfo.svelte';
  import { flowStepNumber } from '../../flowChain';
  import { processOfFlow } from '../../flowDecision';
  import BoardIssueList from '../../components/BoardIssueList.svelte';
  import ChangeImpactList from '../../components/ChangeImpactList.svelte';

  let { tab }: { tab: Tab } = $props();

  let change = $state<Change | undefined>();
  let nodes = $state<GraphNode[]>([]);
  let attached = $state<NodeRef[]>([]);
  let posts = $state<PostVersions>(new Map());
  let extraNodes = $state<string[]>([]);
  let moving = $state('');
  let pane = $state(untrack(() => tab.params.pane) || 'overview');
  $effect(() => {
    tab.params.pane = pane;
  });
  let loading = $state(false);
  let error = $state('');

  let subs = $state<Change[]>([]);
  /** parent chain, the root first */
  let ancestors = $state<Change[]>([]);
  let flows = $state<Flow[]>([]);
  let boardIssues = $state<BoardIssue[] | null>(null);
  let checking = $state(false);
  let checkError = $state('');

  async function checkBoard() {
    if (!selected) return;
    checking = true;
    checkError = '';
    try {
      boardIssues = (await graph.validateBoard(selected)).issues ?? [];
    } catch (e) {
      checkError = errorMessage(e);
    } finally {
      checking = false;
    }
  }
  let splitting = $state(false);
  let merging = $state(false);
  let mergeError = $state('');

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
      attached = (await graph.getChangeImpacts(id, signal)).nodes ?? [];
      posts = await loadPosts(c?.nodes ?? []);
      subs = (await graph.listSubChanges(id, signal)).changes ?? [];
      const chain: Change[] = [];
      for (let p = c?.parentId; p && chain.length < 16; ) {
        const parent = (await graph.getChange(p, signal)).change;
        if (!parent) break;
        chain.unshift(parent);
        p = parent.parentId;
      }
      ancestors = chain;
      flows = (await graph.listFlows(id, signal)).flows ?? [];
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
    subs = [];
    ancestors = [];
    flows = [];
    mergeError = '';
    extraNodes = [];
    applied = undefined;
    baselineName = '';
    if (!id) return;
    const ctrl = new AbortController();
    load(id, ctrl.signal);
    return () => ctrl.abort();
  });

  /** status of an item on the log: base status, then what the flow branches make of it */
  function effectiveStatus(i: ChangeItem): string | undefined {
    const own = i.flow ? flows.find((f) => f.id === i.flow) : undefined;
    if (own?.status === 'open') return 'candidate';
    if (own?.status === 'discarded') return 'rejected';
    for (const f of flows) {
      if (!f.stale?.includes(i.id ?? '')) continue;
      if (f.status === 'adopted') return 'superseded';
      if (f.status === 'open' && (i.status === 'proposed' || i.status === 'accepted')) return 'stale';
    }
    return i.status;
  }
  // flow events are part of the log but not shown as items
  const items = $derived((change?.items ?? []).filter((i) => i.kind !== 'flow').map((i) => ({ ...i, status: effectiveStatus(i) })));
  const flowProcess = (f: Flow) => processOfFlow(f);
  /** the badge of a flow: open flows that compete cannot be adopted any more */
  const flowBadge = (f: Flow) => (f.status === 'open' && f.competesWith?.length ? 'competing' : f.status);
  const ctx = $derived(makeContext(nodes, items));
  const groups = $derived({
    decision: items.filter((i) => i.kind === 'decision'),
    artifact: items.filter((i) => i.kind === 'artifact'),
  });
  const itemLabel = (id: string | undefined) => {
    const it = ctx.items.get(id ?? '');
    return it ? `${it.kind ?? 'item'}${it.type ? ` ${it.type}` : ''}` : shortId(id);
  };
  const others = $derived(items.filter((i) => !['decision', 'artifact'].includes(i.kind ?? '')));

  const isApplied = $derived(change?.status === 'applied');
  const closed = $derived(change?.status === 'applied' || change?.status === 'abandoned');
  void loadTypes();
  const lcRows = $derived(lifecycleRows(typeCatalog.cat, nodes, attached, change?.nodes ?? [], posts, extraNodes));
  // a change creates and modifies the nodes of its namespace (ADR 0015 §2)
  const lcCandidates = $derived(reopenable(nodes, lcRows, change?.namespace ?? ''));
  const typeNames = $derived(nodeTypeNames(typeCatalog.cat, change?.namespace ?? ''));
  const lifecycleOf = $derived(lifecycleResolver(typeCatalog.cat));
  const takenKeys = $derived([
    ...nodes.map((n) => n.key ?? ''),
    ...(change?.nodes ?? []).filter((n) => n.intent === 'created' && !n.superseded && n.review !== 'rejected').map((n) => n.key ?? ''),
  ]);
  const stuckEditable = $derived(lcRows.some((r) => r.lifecycle && r.editable && !r.removal));
  const panes = $derived<Pane[]>([
    { id: 'overview', label: 'Overview', badge: stuckEditable ? '!' : undefined },
    { id: 'impacts', label: 'Impacts', badge: change?.nodes?.length || undefined },
    { id: 'items', label: 'Items', badge: items.length || undefined },
    { id: 'changes', label: 'Changes', badge: subs.length + ancestors.length || undefined },
  ]);

  /** Writes a node in this change through its change impact (the server checks the state). */
  async function write(label: string, target: { pre?: NodeRef; key?: string; type?: string }, w: { props?: Record<string, unknown>; state?: string; retire?: boolean }, rationale: string): Promise<boolean> {
    if (!change?.id) return false;
    moving = label;
    error = '';
    try {
      await writeNodeInChange(change.id, change?.nodes ?? [], target, w, rationale);
      await load(change.id);
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      moving = '';
    }
  }

  /** Writes new values for some properties (and possibly the state) of a node of this change. */
  async function edit(row: LifecycleRow, patch: Record<string, unknown>, state?: string): Promise<boolean> {
    if (row.created) return write(`${row.node.id}:edit`, { key: row.node.key, type: row.node.type }, { props: patch, state }, `edit ${row.node.key}`);
    if (!row.node.id) return false;
    return write(`${row.node.id}:edit`, { pre: { id: row.node.id, version: row.node.version } }, { props: patch }, `edit ${row.node.key}`);
  }

  /** Creates a node: identity and type only. */
  async function createNode(key: string, type: string, state: string): Promise<boolean> {
    const born = lifecycleOf(type) && state && state !== lifecycleOf(type)?.initial ? state : undefined;
    return write('create', { key, type }, { props: {}, state: born }, `create ${key}`);
  }

  /** Retires a node when the change is applied, or discards a node the change creates. */
  async function removeNode(row: LifecycleRow): Promise<boolean> {
    if (!change?.id) return false;
    if (row.created?.id) {
      moving = `${row.node.id}:delete`;
      error = '';
      try {
        await graph.reviewChangeImpact(change.id, row.created.id, false, `discarded ${row.node.key}`);
        await load(change.id);
        return true;
      } catch (e) {
        error = errorMessage(e);
        return false;
      } finally {
        moving = '';
      }
    }
    return write(`${row.node.id}:delete`, { pre: { id: row.node.id, version: row.node.version } }, { retire: true }, `delete ${row.node.key}`);
  }

  /** Withdraws a deletion made by this change. */
  async function undoDelete(row: LifecycleRow): Promise<boolean> {
    if (!change?.id || !row.removal?.id) return false;
    moving = `${row.node.id}:delete`;
    error = '';
    try {
      await graph.reviewChangeImpact(change.id, row.removal.id, false, `keep ${row.node.key}`);
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
    if (!row.node.id) return;
    await write(`${row.node.id}:${t.name}`, { pre: { id: row.node.id, version: row.node.version } }, { state: t.to }, `${t.name} ${row.node.key}`);
  }

  const ownBranch = $derived((change?.branch ?? '').startsWith('change-'));
  const openSubs = $derived(subs.filter((s) => s.status !== 'applied' && s.status !== 'abandoned'));

  async function split() {
    if (!change?.id) return;
    splitting = true;
    error = '';
    try {
      const made = (await graph.splitChange(change.id)).changes ?? [];
      await load(change.id);
      void refreshChanges();
      notify(made.length ? `${made.length} sub-change(s) created.` : 'No new sub-change: every owning unit already has one.', 'ok');
    } catch (e) {
      error = errorMessage(e);
    } finally {
      splitting = false;
    }
  }

  async function merge() {
    if (!change?.id) return;
    merging = true;
    mergeError = '';
    try {
      await graph.mergeChange(change.id);
      await load(change.id);
      void refreshChanges();
      void refreshBaselines();
      notify('Change merged.', 'ok');
    } catch (e) {
      mergeError = errorMessage(e);
    } finally {
      merging = false;
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
  {@const ch = change}
  <EditorPanes {panes} bind:active={pane} label="Change sections">
    {#snippet children(active)}
      {#if active === 'overview'}
      <section class="card">
        <div class="editor-head">
          <Icon name="diff" size={18} />
          <h2>{ch.title || 'Untitled'}</h2>
          <StatusBadge status={ch.status} />
        </div>
        {#if ch.intent}<p class="intent">"{ch.intent}"</p>{/if}
        <dl class="meta">
          <dt>ID</dt><dd><code>{ch.id}</code></dd>
          {#if ch.namespace}<dt>Namespace</dt><dd>{ch.namespace}</dd>{/if}
          {#if ch.ownerOrg}<dt>Owner unit</dt><dd><code>{ch.ownerOrg}</code></dd>{/if}
          {#if ch.parentId}
            <dt>Parent change</dt>
            <dd><button type="button" class="link mono" onclick={() => openTab({ kind: 'change', params: { id: ch.parentId ?? '' } })}>{shortId(ch.parentId)}</button></dd>
          {/if}
          {#if ch.methodology}<dt>Methodology</dt><dd>{ch.methodology}</dd>{/if}
          {#if ch.goal}<dt>Goal</dt><dd><code>{ch.goal}</code></dd>{/if}
          {#if ch.baselineId}
            <dt>Starting baseline</dt>
            <dd><button type="button" class="link mono" onclick={() => openBaseline(ch.baselineId)}>{shortId(ch.baselineId)}</button></dd>
          {/if}
          {#if ch.resultBaselineId}
            <dt>Resulting baseline</dt>
            <dd><button type="button" class="link mono" onclick={() => openBaseline(ch.resultBaselineId)}>{shortId(ch.resultBaselineId)}</button></dd>
          {/if}
          {#if ch.createdAt}<dt>Created on</dt><dd>{formatDate(ch.createdAt)}</dd>{/if}
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

        {#if ch.status === 'merge_pending'}
          <div class="alert warn" style="margin: 0.75rem 0">
            <p>Applied on its own branch; the merge into the parent branch is pending.</p>
            <button class="primary" onclick={merge} disabled={merging}>{merging ? 'Merging…' : 'Merge'}</button>
            {#if mergeError}<pre class="error">{mergeError}</pre>{/if}
          </div>
        {/if}

        <div class="board-check">
          <button onclick={checkBoard} disabled={checking}>{checking ? 'Checking…' : 'Blackboard check'}</button>
          {#if checkError}<span class="error">{checkError}</span>{/if}
          {#if boardIssues && !boardIssues.length}<span class="hint">Consistent</span>{/if}
          {#if boardIssues?.length}
            {@const nWarn = boardIssues.filter((i) => i.severity === 'warning').length}
            {@const nErr = boardIssues.length - nWarn}
            <p class="hint">
              {nErr} error{nErr === 1 ? '' : 's'}, {nWarn} warning{nWarn === 1 ? '' : 's'} on the main flow
            </p>
            <BoardIssueList issues={boardIssues} sections />
          {/if}
        </div>

        {#if flows.length}
          <h3>Flow branches <span class="count">{flows.length}</span></h3>
          <FlowGraph {processes} changeId={selected} {flows} onopen={(pid) => openTab({ kind: 'run', params: { id: pid } })} />
          <ul class="subs flows">
            {#each flows as f (f.id)}
              {@const fp = flowProcess(f)}
              <li>
                <StatusBadge status={flowBadge(f)} />
                <code>{shortId(f.id)}</code>
                from step {flowStepNumber(f, processes)}
                {#if f.reason}<span class="muted">· {f.reason}</span>{/if}
                <span class="hint">· {f.stale?.length ?? 0} stale item(s)</span>
                {#if f.process}<button type="button" class="link mono" onclick={() => openTab({ kind: 'run', params: { id: f.process ?? '' } })}>previous run</button>{/if}
                {#if fp}<button type="button" class="link mono" onclick={() => openTab({ kind: 'run', params: { id: fp.id ?? '' } })}>relaunched run</button>{/if}
                <div class="flow-row"><FlowBranchInfo flow={f} changeBranch={change?.branch ?? ''} /></div>
                <div class="flow-row"><FlowActions flow={f} changeId={selected} ondecided={() => load(selected)} /></div>
              </li>
            {/each}
          </ul>
        {/if}

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
      {:else if active === 'impacts'}
      <section class="card">
        <h3>Change impacts <span class="count">{change?.nodes?.length ?? 0}</span></h3>
        <ChangeImpactList changeId={ch.id ?? ''} nodes={change?.nodes ?? []} {closed} onchange={() => load(selected)} onopennode={(n) => openNode({ id: n.post?.id ?? n.pre?.id ?? '', key: n.key ?? '' }, { pin: true, change: ch.id ?? '' })} />
      </section>

      <section class="card">
        <h3>Node edits <span class="count">{lcRows.length}</span></h3>
      <ChangeLifecycle rows={lcRows} candidates={lcCandidates} disabled={closed} busy={moving} onmove={move} onedit={edit} types={typeNames} {lifecycleOf} keys={takenKeys} oncreate={createNode} onremove={removeNode} onundo={undoDelete} onhistory={(r) => openNode(r.node, { pin: true, generic: true, pane: 'history' })} onopennode={(r) => openNode(r.node, { pin: true, change: ch.id ?? '' })} onadd={(id) => (extraNodes = [...extraNodes, id])} />
      </section>
      {:else if active === 'items'}
      <section class="card">
        <h3>Decisions <span class="count">{groups.decision.length}</span></h3>
        {#if groups.decision.length}
          <table>
            <thead><tr><th>Item</th><th>Decision</th><th>Comment</th></tr></thead>
            <tbody>
              {#each groups.decision as i (i.id)}
                <tr class:superseded={i.status === ITEM_SUPERSEDED}>
                  <td>{itemLabel(i.decision?.item)}</td>
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
              {#if i.type === 'guidance'}<span class="badge-guidance">guidance</span>{:else}{i.type || 'artifact'}{/if}
              <span class="hint">· {@render producer(i)}</span>
              {#if i.status === ITEM_SUPERSEDED || i.status === 'candidate' || i.status === 'stale' || i.status === 'rejected'}<StatusBadge status={i.status} />{/if}
            </h4>
            {#if i.type === 'guidance' && typeof i.data?.text === 'string'}
              <blockquote class="guidance-quote">{i.data.text}</blockquote>
            {:else if md !== undefined}
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
      {:else if active === 'changes'}
      <section class="card">
        <h3>Parent changes <span class="count">{ancestors.length}</span></h3>
        {#if ancestors.length}
          <ul class="subs">
            {#each ancestors as a, i (a.id)}
              <li style="padding-left: {i * 1}rem">
                <button type="button" class="link" onclick={() => openTab({ kind: 'change', params: { id: a.id ?? '' } })}>{a.title || shortId(a.id)}</button>
                {#if a.ownerOrg}<code>{a.ownerOrg}</code>{/if}
                <StatusBadge status={a.status} />
              </li>
            {/each}
          </ul>
        {:else}
          <p class="empty">This change has no parent.</p>
        {/if}
      </section>

      <section class="card">
          <h3>Sub-changes <span class="count">{subs.length}</span></h3>
          {#if subs.length}
            <ul class="subs">
              {#each subs as s (s.id)}
                <li>
                  <button type="button" class="link" onclick={() => openTab({ kind: 'change', params: { id: s.id ?? '' } })}>{s.title || shortId(s.id)}</button>
                  {#if s.ownerOrg}<code>{s.ownerOrg}</code>{/if}
                  <StatusBadge status={s.status} />
                </li>
              {/each}
            </ul>
          {:else}
            <p class="empty">No sub-changes.</p>
          {/if}
          {#if ownBranch && !closed}
            <button type="button" onclick={split} disabled={splitting} title="One sub-change per unit owning the impacted nodes">
              {splitting ? 'Splitting…' : 'Split by owner'}
            </button>
          {/if}
          {#if openSubs.length}<p class="hint">Apply or abandon the {openSubs.length} open sub-change(s) before applying this change.</p>{/if}
      </section>
      {/if}
    {/snippet}
  </EditorPanes>
{/if}
</div>

<style>
  .subs {
    list-style: none;
    margin: 0 0 0.5rem;
    padding: 0;
  }
  .subs li {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.15rem 0;
  }
  .hint {
    color: var(--text-muted, inherit);
    font-size: 0.88em;
  }
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
  .flows > li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }
  .flows .flow-row {
    flex-basis: 100%;
  }
  .badge-guidance {
    border: 1px solid var(--accent);
    color: var(--accent);
    border-radius: 999px;
    padding: 0 8px;
    font-size: 0.85em;
  }
  .guidance-quote {
    margin: 4px 0;
    padding: 4px 10px;
    border-left: 3px solid var(--accent);
    font-style: italic;
    white-space: pre-wrap;
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
