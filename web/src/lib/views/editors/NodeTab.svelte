<script lang="ts">
  // Node editor: one tab for a node, its information organized in panes —
  // details (properties), relations (parents, children, graph), lifecycle,
  // history. Modifications are proposals of a working change (created on demand).
  import {
    graph,
    errorMessage,
    formatDate,
    shortId,
    type Change,
    type GraphNode,
    type LifecycleTransition,
    type NodeRef,
  } from '../../api';
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import EditorPanes, { type Pane } from '../../components/EditorPanes.svelte';
  import NodeTree from '../../components/NodeTree.svelte';
  import NodeGraph from '../../components/NodeGraph.svelte';
  import LifecycleDiagram from '../../components/LifecycleDiagram.svelte';
  import NodeHistory from '../../components/NodeHistory.svelte';
  import NodePropertyForm from '../../components/NodePropertyForm.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { nodeEditor } from '../../shell/registry';
  import { handleOf, openNode, openTarget } from '../../nodeEditors';
  import { loadTypes, typeCatalog } from '../../stores/types.svelte';
  import { provideActions, notify } from '../../shell/workbench.svelte';
  import { changes, refreshChanges } from '../../stores/catalog.svelte';
  import { loadGraph, loadHead, type GraphIndex } from '../../graphIndex';
  import { namespaceOf } from '../../namespace';
  import { declaredProperties, isReopen, lifecycleResolver, lifecycleRows, loadPosts, writeNodeInChange, type LifecycleRow, type PostVersions } from '../../lifecycle';

  let { tab }: { tab: Tab } = $props();

  const id = $derived(tab.params.id ?? '');

  // --- state ------------------------------------------------------------------------
  let pane = $state(untrack(() => tab.params.pane) || 'details');
  $effect(() => {
    tab.params.pane = pane; // remembered with the tab
  });

  let head = $state<GraphIndex | undefined>();
  let versions = $state<GraphNode[]>([]);
  let loading = $state(true);
  let error = $state('');
  let busy = $state('');
  let editing = $state(false);
  let reload = $state(0);
  let centerId = $state('');

  interface Work {
    change: Change;
    index: GraphIndex;
    attached: NodeRef[];
    posts: PostVersions;
  }
  let workId = $state(untrack(() => tab.params.change) ?? '');
  let work = $state<Work | undefined>();

  $effect(() => {
    tab.params.change = workId;
  });

  async function loadNode(signal?: AbortSignal) {
    loading = true;
    error = '';
    try {
      // the node's namespace is only known once its versions come back, so loadHead
      // (namespace-scoped) has to follow rather than run in parallel with it.
      const v = await graph.listNodeVersions(id, signal);
      versions = (v.versions ?? []).slice().sort((a, b) => (a.version ?? 0) - (b.version ?? 0));
      const namespace = namespaceOf(versions.at(-1)?.namespace);
      head = await loadHead(namespace, signal);
      if (!changes.loaded) void refreshChanges();
    } catch (e) {
      if (!signal?.aborted) error = errorMessage(e);
    } finally {
      if (!signal?.aborted) loading = false;
    }
  }

  async function loadWork() {
    if (!workId) {
      work = undefined;
      return;
    }
    try {
      const change = (await graph.getChange(workId)).change;
      if (!change?.baselineId) throw new Error('unknown change');
      const [index, attached] = await Promise.all([loadGraph(change.baselineId), graph.getChangeImpacts(workId)]);
      work = { change, index, attached: attached.nodes ?? [], posts: await loadPosts(change.nodes ?? []) };
    } catch (e) {
      error = errorMessage(e);
      work = undefined;
    }
  }

  $effect(() => {
    void id;
    centerId = id;
    const ctrl = new AbortController();
    void loadNode(ctrl.signal);
    return () => ctrl.abort();
  });

  $effect(() => {
    void workId;
    void loadWork();
  });

  // --- derived ----------------------------------------------------------------------
  const stored = $derived(head?.nodes.get(id) ?? versions.at(-1));
  const typeName = $derived(stored?.type ?? '');
  const headList = $derived(head?.list ?? []);
  void loadTypes();
  const declared = $derived(declaredProperties(typeCatalog.cat, typeName));
  const lifecycleOf = $derived(lifecycleResolver(typeCatalog.cat));
  const lifecycle = $derived(lifecycleOf(typeName));

  /** the node in the working change, or in the current graph when there is none */
  const row = $derived.by<LifecycleRow | undefined>(() => {
    if (work) return lifecycleRows(typeCatalog.cat, work.index.list, work.attached, work.change.nodes ?? [], work.posts, [id]).find((r) => r.node.id === id);
    return lifecycleRows(typeCatalog.cat, headList, [], [], new Map(), [id]).find((r) => r.node.id === id);
  });
  const inChange = $derived(!!work && !!row);
  const nodeProps = $derived((row?.props ?? stored?.props ?? {}) as Record<string, unknown>);
  const storedProps = $derived((stored?.props ?? {}) as Record<string, unknown>);
  const nodeState = $derived(row?.effective ?? stored?.state ?? '');
  const editable = $derived(row ? row.editable : true);
  const removed = $derived(!!row?.removal);
  const reopens = $derived((row?.transitions ?? []).filter((t) => row && isReopen(row, t)));

  const openChanges = $derived(changes.items.filter((c) => (c.status === 'draft' || c.status === 'active') && (!stored?.namespace || c.namespace === stored.namespace)));
  const propertyNames = $derived([...new Set([...declared, ...Object.keys(nodeProps)])]);
  const text = (v: unknown): string => (v === undefined || v === null ? '' : typeof v === 'string' ? v : JSON.stringify(v));

  const panes = $derived<Pane[]>([
    { id: 'details', label: 'Details' },
    { id: 'relations', label: 'Relations', badge: head ? (head.in.get(id)?.length ?? 0) + (head.out.get(id)?.length ?? 0) : undefined },
    { id: 'lifecycle', label: 'Lifecycle', badge: nodeState || undefined, hidden: !lifecycle },
    { id: 'history', label: 'History', badge: versions.length || undefined },
  ]);

  // --- working change ---------------------------------------------------------------
  async function ensureChange(): Promise<string> {
    if (workId) return workId;
    const baselineId = head?.baselineId;
    if (!baselineId) throw new Error('There is no baseline to start a change from.');
    // the change acts on the namespace of the node (ADR 0015 §2)
    const c = (await graph.createChange({ title: `Edit ${stored?.key ?? shortId(id)}`, baselineId, namespace: stored?.namespace })).change;
    if (!c?.id) throw new Error('The change could not be created.');
    workId = c.id;
    await loadWork();
    void refreshChanges();
    notify(`Change “${c.title}” created.`, 'ok');
    return c.id;
  }

  /** Runs a modification: creates the working change if needed, writes the node on its branch, reloads. */
  async function propose(label: string, w: { props?: Record<string, unknown>; state?: string; retire?: boolean }, rationale: string): Promise<boolean> {
    busy = label;
    error = '';
    try {
      const cid = await ensureChange();
      const node = work?.index.nodes.get(id);
      if (!node?.id) throw new Error('This node is not in the baseline of the working change: choose another change.');
      await writeNodeInChange(cid, work?.change.nodes ?? [], { pre: { id: node.id, version: node.version } }, w, rationale);
      await loadWork();
      reload++;
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      busy = '';
    }
  }

  const transition = (t: LifecycleTransition) => propose('move', { state: t.to }, `Move ${stored?.key} to ${t.to}`);

  const saveProps = (patch: Record<string, unknown>) => propose('edit', { props: patch }, `Edit ${stored?.key}`);

  async function remove() {
    if (!confirm(`Delete ${stored?.key} (${typeName}) when the change is applied? Links pointing to it become suspect.`)) return;
    await propose('delete', { retire: true }, `Delete ${stored?.key}`);
  }

  async function undoDelete() {
    const rid = row?.removal?.id;
    if (!rid || !workId) return;
    busy = 'delete';
    error = '';
    try {
      await graph.reviewChangeImpact(workId, rid, false, 'keep the node');
      await loadWork();
      reload++;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }

  function pickChange(value: string) {
    editing = false;
    workId = value;
  }

  const openNeighbour = (nid: string) => openNode(head?.nodes.get(nid) ?? { id: nid }, { pin: true });

  // the editor the node type names (NodeType `editor`): this default editor offers to open the node there
  const typeEditor = $derived(nodeEditor(typeCatalog.cat.editor(typeName)));

  async function openInTypeEditor() {
    if (!typeEditor || !stored) return;
    const target = await Promise.resolve(typeEditor.open(await handleOf(stored))).catch(() => undefined);
    if (target) openTarget(target, { pin: true });
    else notify(`The ${typeEditor.title.toLowerCase()} cannot show ${stored.key ?? 'this node'} (removed element or unknown version).`, 'info');
  }

  const openChange = () => workId && openTab({ kind: 'change', params: { id: workId } }, { pin: true });

  provideActions(
    () => tab.id,
    () => [
      ...(typeEditor ? [{ id: 'type-editor', label: `Open in ${typeEditor.title}`, icon: 'external' as const, title: `${typeName} nodes open in the ${typeEditor.title.toLowerCase()}`, run: openInTypeEditor }] : []),
      { id: 'refresh', label: 'Refresh', icon: 'refresh', disabled: loading, run: async () => { await loadNode(); await loadWork(); reload++; } },
    ],
  );
</script>

<div class="editor-page">
  <div class="editor-head">
    <Icon name="node" size={18} />
    <h2>{stored?.key || tab.params.key || shortId(id)}</h2>
    {#if typeName}<span class="hint">{typeName}</span>{/if}
    {#if nodeState}<span class="state" class:editable={!!lifecycle && !editable}>{nodeState}</span>{/if}
    {#if stored?.version}<span class="hint">v{stored.version}</span>{/if}
    {#if stored?.deleted}<span class="tag bad">deleted</span>{/if}
    {#if removed}<span class="tag bad" title="Deletion proposed in the working change">deleted when applied</span>{/if}
  </div>
  {#if error}<div class="alert">{error}</div>{/if}

  {#if loading && !stored}
    <p class="empty">Loading…</p>
  {:else if !stored}
    <p class="empty">Node not found.</p>
  {:else}
    <EditorPanes {panes} bind:active={pane} label="Node sections">
      {#snippet toolbar()}
        <label class="wc" for="work-change">Working change</label>
        <select id="work-change" value={workId} onchange={(e) => pickChange(e.currentTarget.value)}>
          <option value="">none: created when you edit</option>
          {#each openChanges as c (c.id)}<option value={c.id}>{c.title || shortId(c.id)} ({c.status})</option>{/each}
          {#if workId && !openChanges.some((c) => c.id === workId)}<option value={workId}>{work?.change.title || shortId(workId)}</option>{/if}
        </select>
        {#if workId}<button type="button" class="small" onclick={openChange}>Open change</button>{/if}
      {/snippet}

      {#snippet children(active)}
        {#if workId && work && !row}
          <div class="alert info">This node is not in the baseline of the working change: choose another change, or none.</div>
        {/if}

        {#if active === 'details'}
          <section class="card">
            <div class="head">
              <h3>Properties</h3>
              <span class="grow"></span>
              {#if !editing}
                <button type="button" class="small primary" disabled={!editable || removed || busy !== '' || stored.deleted} onclick={() => (editing = true)} title={editable ? 'Edit in the working change' : 'Reopen the node to edit it'}>Edit</button>
                {#each reopens as t (t.name)}
                  <button type="button" class="small" disabled={busy !== ''} title={`${t.name}: ${t.from} → ${t.to}`} onclick={() => transition(t)}>Reopen → {t.to}</button>
                {/each}
                {#if removed}
                  <button type="button" class="small" disabled={busy !== ''} onclick={undoDelete}>Undo delete</button>
                {:else if !stored.deleted}
                  <button type="button" class="small danger" disabled={!editable || busy !== ''} title={editable ? 'Delete when the change is applied' : 'Reopen the node to delete it'} onclick={remove}>Delete</button>
                {/if}
              {/if}
            </div>
            {#if editing}
              <NodePropertyForm props={nodeProps} {declared} {typeName} busy={busy === 'edit'} onsave={saveProps} oncancel={() => (editing = false)} />
            {:else if propertyNames.length}
              <table class="props">
                <tbody>
                  {#each propertyNames as k (k)}
                    {@const changed = text(nodeProps[k]) !== text(storedProps[k])}
                    <tr class:changed>
                      <th>{k}{#if !declared.includes(k)}<span class="hint" title={`Not declared by ${typeName}`}> *</span>{/if}</th>
                      <td>
                        {#if text(nodeProps[k])}{text(nodeProps[k])}{:else}<span class="hint">—</span>{/if}
                        {#if changed}<span class="hint pending" title="Proposed in the working change"> (was {text(storedProps[k]) || 'empty'})</span>{/if}
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            {:else}
              <p class="empty">No property.</p>
            {/if}
            {#if inChange}<p class="hint">Showing the node as the working change would leave it; modifications are applied when the change is.</p>{/if}
          </section>

          <section class="card">
            <h3>Identity</h3>
            <dl class="meta">
              <dt>Key</dt><dd><code>{stored.key}</code></dd>
              <dt>Type</dt><dd>{typeName}</dd>
              <dt>ID</dt><dd><code>{stored.id}</code></dd>
              <dt>Version</dt><dd>v{stored.version}{#if stored.reason} <span class="hint">({stored.reason})</span>{/if}</dd>
              {#if stored.state}<dt>Stored state</dt><dd><span class="state">{stored.state}</span></dd>{/if}
              <dt>Created</dt><dd>{formatDate(stored.createdAt)}</dd>
              {#if stored.changeId}<dt>Change</dt><dd><button type="button" class="link" onclick={() => openTab({ kind: 'change', params: { id: stored.changeId ?? '' } }, { pin: true })}>{changes.items.find((c) => c.id === stored.changeId)?.title ?? shortId(stored.changeId)}</button></dd>{/if}
            </dl>
          </section>
        {:else if active === 'relations'}
          {#if head && head.nodes.has(id)}
            <div class="rel">
              <div class="trees">
                <section class="card">
                  <h3>Parents <span class="count">{head.in.get(id)?.length ?? 0}</span></h3>
                  <p class="hint">Nodes that link to this one.</p>
                  <NodeTree index={head} root={id} dir="in" onopen={(n) => openNeighbour(n)} />
                </section>
                <section class="card">
                  <h3>Children <span class="count">{head.out.get(id)?.length ?? 0}</span></h3>
                  <p class="hint">Nodes this one links to.</p>
                  <NodeTree index={head} root={id} dir="out" onopen={(n) => openNeighbour(n)} />
                </section>
              </div>
              <section class="card graph">
                <h3>Graph {#if centerId !== id}<button type="button" class="small" onclick={() => (centerId = id)}>Back to {stored.key}</button>{/if}</h3>
                <NodeGraph index={head} center={centerId || id} onrecenter={(n) => (centerId = n)} onopen={(n) => openNeighbour(n)} />
              </section>
            </div>
          {:else}
            <p class="empty">This node is not in the current graph (deleted, or only created by a change).</p>
          {/if}
        {:else if active === 'lifecycle'}
          {#if lifecycle}
            <section class="card">
              <h3>Lifecycle {#if lifecycle.name}<span class="hint">{lifecycle.name}</span>{/if}</h3>
              <p class="hint">
                {nodeState ? `${stored.key} is ${nodeState}${row?.moves.length ? ` in the working change (stored: ${stored.state || 'none'})` : ''}.` : `${stored.key} has no state yet.`}
                Moving a node happens in a change: the transitions below are proposed in the working change.
              </p>
              <LifecycleDiagram {lifecycle} current={nodeState || lifecycle.initial || ''} stored={stored.state ?? ''} busy={busy !== ''} disabled={removed || !!stored.deleted} onmove={transition} />
            </section>
          {/if}
        {:else if active === 'history'}
          <NodeHistory {id} reloadKey={reload} />
        {/if}
      {/snippet}
    </EditorPanes>
  {/if}
</div>

<style>
  .head {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    flex-wrap: wrap;
    margin-bottom: 0.4rem;
  }
  .state {
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 0.5rem;
    font-size: 0.85rem;
    font-family: var(--mono);
  }
  .state.editable {
    border-color: var(--warn);
    color: var(--warn);
  }
  .tag.bad {
    color: var(--danger);
    border: 1px solid var(--danger);
    border-radius: 999px;
    padding: 0 0.45rem;
    font-size: 0.8rem;
  }
  .wc {
    font-size: 0.85rem;
    color: var(--muted);
  }
  #work-change {
    width: auto;
    min-width: 14rem;
  }
  .props th {
    text-align: left;
    width: 14rem;
    font-family: var(--mono);
    font-weight: 500;
    vertical-align: top;
  }
  .props tr.changed td {
    color: var(--ok);
  }
  .pending {
    color: var(--muted);
  }
  .rel {
    display: grid;
    grid-template-columns: minmax(260px, 1fr) minmax(0, 2fr);
    gap: 0.8rem;
    align-items: start;
  }
  .trees {
    display: grid;
    gap: 0;
  }
  @media (max-width: 1000px) {
    .rel {
      grid-template-columns: 1fr;
    }
  }
</style>
