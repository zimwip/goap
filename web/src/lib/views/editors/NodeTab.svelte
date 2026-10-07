<script lang="ts">
  // Node editor: one tab for a node, its information organized in panes —
  // details (properties), relations (parents, children, graph), lifecycle,
  // history. Modifications are proposals of a working change (created on demand).
  import {
    graph,
    errorMessage,
    formatDate,
    isDraft,
    shortId,
    type Change,
    type GraphNode,
    type LifecycleTransition,
    type Link,
    type LinkWrite,
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
  import NodeLinks from '../../components/NodeLinks.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { nodeEditor } from '../../shell/registry';
  import { handleOf, openNode, openTarget } from '../../nodeEditors';
  import { loadTypes, typeCatalog } from '../../stores/types.svelte';
  import { provideActions, notify } from '../../shell/workbench.svelte';
  import { changes, refreshChanges } from '../../stores/catalog.svelte';
  import { stamp, keyOf } from '../../flux/signals.svelte';
  import { loadGraph, loadHead, type GraphIndex } from '../../graphIndex';
  import { namespaceOf } from '../../namespace';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import { assistField, registerAssist } from '../../assist/registry.svelte';
  import { diffSummary } from '../../assist/changeScreen';
  import { orderedAttributes, shownValue } from '../../attributes';
import { declaredProperties, lifecycleResolver, lifecycleRows, loadPosts, writeNodeInChange, removeFromChange, type LifecycleRow, type PostVersions } from '../../lifecycle';

  import NotFound from '../../shell/NotFound.svelte';

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
  /** the draft view of a node no version exists of yet (created by the working change, ADR 0079) */
  let draftNode = $state<GraphNode | undefined>();

  interface Work {
    change: Change;
    /** the name of the flow the change is seen on ('' : the change as a whole) */
    flowName: string;
    index: GraphIndex;
    attached: NodeRef[];
    posts: PostVersions;
  }
  let workId = $state(untrack(() => tab.params.change) ?? '');
  /** the flow of the working change the node is seen and edited on: 'main' or an option (ADR 0032 §6); empty: the
   * change as a whole (its edits go to its active option) */
  let workFlow = $state(untrack(() => tab.params.flow) ?? '');
  let work = $state<Work | undefined>();

  $effect(() => {
    tab.params.change = workId;
    tab.params.flow = workFlow;
  });

  async function loadNode(signal?: AbortSignal) {
    loading = true;
    error = '';
    try {
      // the node's namespace is only known once its versions come back, so loadHead
      // (namespace-scoped) has to follow rather than run in parallel with it.
      // a node the working change created has no version yet (ADR 0079): its draft, read through the change, stands in
      draftNode = undefined;
      let list: GraphNode[] = [];
      try {
        list = (await graph.listNodeVersions(id, signal)).versions ?? [];
      } catch (e) {
        if (!workId) throw e;
      }
      if (workId && !list.length) draftNode = (await graph.getNode({ id, version: 0 }, signal, { changeId: workId, flow: workFlow })).view?.node;
      versions = list.slice().sort((a, b) => (a.version ?? 0) - (b.version ?? 0));
      const namespace = namespaceOf(versions.at(-1)?.namespace ?? draftNode?.namespace);
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
      if (workFlow) {
        // the change as the flow sees it: its change impacts with the versions that flow wrote
        const bb = await graph.getBlackboard(workId, workFlow);
        const change = bb.change;
        if (!change?.baselineId) throw new Error('unknown change');
        const attached = (change.nodes ?? []).filter((n) => n.pre?.id && !n.superseded).map((n) => n.pre!);
        const flowName = workFlow === 'main' ? 'main flow' : (bb.options?.find((o) => o.id === workFlow)?.option?.name ?? shortId(workFlow));
        work = { change, flowName, index: await loadGraph(change.baselineId), attached, posts: await loadPosts(change.nodes ?? [], true, { changeId: workId, flow: workFlow }) };
        return;
      }
      const change = (await graph.getChange(workId)).change;
      if (!change?.baselineId) throw new Error('unknown change');
      const [index, attached] = await Promise.all([loadGraph(change.baselineId), graph.getChangeImpacts(workId)]);
      work = { change, flowName: '', index, attached: attached.nodes ?? [], posts: await loadPosts(change.nodes ?? [], false, { changeId: workId }) };
    } catch (e) {
      error = errorMessage(e);
      work = undefined;
    }
  }

  $effect(() => {
    void id;
    void stamp(keyOf.node(id)); // a new version of the node, by anyone
    centerId = id;
    const ctrl = new AbortController();
    void loadNode(ctrl.signal);
    return () => ctrl.abort();
  });

  $effect(() => {
    void workId;
    void stamp(keyOf.change(workId)); // its impacts or log moved, by anyone
    void loadWork();
  });

  // --- derived ----------------------------------------------------------------------
  const stored = $derived(head?.nodes.get(id) ?? versions.at(-1) ?? draftNode);
  const typeName = $derived(stored?.type ?? '');
  const headList = $derived(head?.list ?? []);
  void loadTypes();
  const declared = $derived(declaredProperties(typeCatalog.cat, typeName));
  const lifecycleOf = $derived(lifecycleResolver(typeCatalog.cat));
  const lifecycle = $derived(lifecycleOf(typeName));

  /** the node in the working change, or in the current graph when there is none */
  const row = $derived.by<LifecycleRow | undefined>(() => {
    // a node the change creates is not in its baseline: its row is the one whose written version is this node
    if (work)
      return lifecycleRows(typeCatalog.cat, work.index.list, work.attached, work.change.nodes ?? [], work.posts, [id], !!workFlow).find(
        (r) => r.node.id === id || (!!r.created && r.impact?.post?.id === id),
      );
    return lifecycleRows(typeCatalog.cat, headList, [], [], new Map(), [id]).find((r) => r.node.id === id);
  });
  const inChange = $derived(!!work && !!row);
  const nodeProps = $derived((row?.props ?? stored?.props ?? {}) as Record<string, unknown>);
  const storedProps = $derived((stored?.props ?? {}) as Record<string, unknown>);
  const nodeState = $derived(row?.effective ?? stored?.state ?? '');
  // edits are allowed in any state (ADR 0078): a state flagged notLandable only keeps the change from landing
  const notLandable = $derived(!!row && !!row.lifecycle && !row.landable);

  const openChanges = $derived(changes.items.filter((c) => (c.status === 'draft' || c.status === 'active') && (!stored?.namespace || c.namespace === stored.namespace)));
  const attrViews = $derived(orderedAttributes(typeCatalog.cat.attributes(typeName)));
  const nodeRules = $derived(typeCatalog.cat.type(typeName)?.nodeValidators ?? []);
  const attrOf = (k: string) => attrViews.find((a) => a.name === k);
  const propertyNames = $derived([...new Set([...attrViews.map((a) => a.name), ...declared, ...Object.keys(nodeProps)])]);
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
    notify(`Change “${c.title}” created.`, 'ok');
    return c.id;
  }

  /** Runs a modification: creates the working change if needed, writes the node on its branch, reloads. */
  async function propose(
    label: string,
    w: { props?: Record<string, unknown>; state?: string; addLinks?: LinkWrite[]; removeLinks?: string[] },
    rationale: string,
  ): Promise<boolean> {
    busy = label;
    error = '';
    try {
      const cid = await ensureChange();
      const node = work?.index.nodes.get(id);
      const target = row?.created ? { key: row.node.key, type: row.node.type } : node?.id ? { pre: { id: node.id, version: node.version } } : undefined;
      if (!target) throw new Error('This node is not in the baseline of the working change: choose another change.');
      await writeNodeInChange(cid, work?.change.nodes ?? [], target, w, rationale, workFlow);
      await Promise.all([loadWork(), loadNode()]);
      reload++;
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      busy = '';
    }
  }

  // --- links and changes of the node -------------------------------------------------
  /** what is shown: the draft the working change holds of the node (a draft reference, no version: read through the
   * change, ADR 0079), else the stored version */
  const shownRef = $derived<NodeRef | undefined>(
    inChange && row?.impact?.post?.id ? row.impact.post : stored?.id ? { id: stored.id, version: stored.version } : undefined,
  );
  let outLinks = $state<Link[]>([]);
  let nodeChanges = $state<Change[]>([]);
  $effect(() => {
    const ref = shownRef;
    void reload;
    void stamp(keyOf.node(id));
    if (!ref?.id) return;
    const ctrl = new AbortController();
    graph
      .getNode({ id: ref.id, version: ref.version }, ctrl.signal, isDraft(ref) && workId ? { changeId: workId, flow: workFlow } : undefined)
      .then((r) => (outLinks = r.view?.out ?? []))
      .catch(() => (outLinks = []));
    return () => ctrl.abort();
  });
  $effect(() => {
    void reload;
    void stamp(keyOf.changes);
    if (!id) return;
    const ctrl = new AbortController();
    graph
      .listNodeChanges(id, ctrl.signal)
      .then((r) => (nodeChanges = r.changes ?? []))
      .catch(() => (nodeChanges = []));
    return () => ctrl.abort();
  });
  const addLink = (type: string, to: NodeRef) => propose('link', { addLinks: [{ type, to }] }, `Link ${stored?.key} ${type} ${head?.nodes.get(to.id ?? '')?.key ?? ''}`.trim());
  const removeLink = (l: Link) => propose('link', { removeLinks: [l.id ?? ''] }, `Unlink ${stored?.key} ${l.type}`);

  const transition = (t: LifecycleTransition) => propose('move', { state: t.to }, `Move ${stored?.key} to ${t.to}`);

  const saveProps = (patch: Record<string, unknown>) => propose('edit', { props: patch }, `Edit ${stored?.key}`);

  // The review is an explicit action (ADR 0079): an edit leaves the impact proposed.
  let reviewing = $state(false);
  let reviewComment = $state('');
  const canReview = $derived(!!row?.impact?.id && !row.impact.superseded && !!row.impact.post?.id && (row.impact.review === 'proposed' || !row.impact.review));
  // the impact on screen, with what it edits (names only): context for a review (ADR 0092)
  $effect(() =>
    registerAssist({
      tab: tab.id,
      screen: () => {
        const imp = row?.impact;
        if (!stored || !imp?.id) return { kind: 'node', title: stored?.key };
        return {
          kind: 'node',
          title: stored.key,
          summary: `Node ${stored.key} (${typeName}), state ${nodeState || 'none'}, in change ${workId}: its impact is ${imp.review || 'proposed'}${imp.rationale ? `, “${imp.rationale}”` : ''}. ${diffSummary(row?.node.props, row?.props)}`,
          entities: [{ type: 'impact', id: imp.id, label: stored.key, state: imp.review || 'proposed' }],
        };
      },
      focus: () => ({ element: row?.impact?.id && canReview ? { type: 'impact', id: row.impact.id, label: stored?.key } : undefined, pendingAction: reviewing ? `reviewing the impact of ${stored?.key}` : undefined, errors: error ? [error] : [] }),
    }),
  );
  async function reviewImpact(accept: boolean) {
    if (!row?.impact?.id || !workId) return;
    busy = 'review';
    error = '';
    try {
      await graph.impactNodeReview(workId, row.impact.id, accept, reviewComment.trim(), workFlow);
      reviewing = false;
      reviewComment = '';
      await Promise.all([loadWork(), loadNode()]);
      reload++;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }

  /** Takes the node out of the working change: its draft is dropped; refused once its version has landed (reject it
   * instead). A node is never deleted (ADR 0076). */
  async function removeFromWork() {
    const imp = row?.impact;
    if (!imp?.id || !workId) return;
    if (!(await confirmDialog({ message: `Take ${stored?.key ?? row?.node.key} out of the working change? Its draft is dropped.`, danger: true }))) return;
    busy = 'remove';
    error = '';
    try {
      await removeFromChange(workId, imp, workFlow);
      await Promise.all([loadWork(), loadNode()]);
      reload++;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }

  function pickChange(value: string) {
    editing = false;
    workFlow = ''; // another change: seen as a whole
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
    {#if nodeState}<span class="state" class:notLandable title={notLandable ? 'Not landable: a change cannot land with the node in this state' : ''}>{nodeState}</span>{/if}
    {#if inChange && isDraft(row?.impact?.post)}
      <span class="hint" title="the working change holds a draft of the node; its version is written when the change lands (ADR 0079). Released: v{stored?.version ?? '—'}">draft in the change</span>
    {:else if stored?.version}<span class="hint">v{stored.version}</span>{/if}
    {#if inChange && row?.impact}
      <StatusBadge status={row.impact.review} />
      {#if canReview}
        <button type="button" class="small primary" disabled={busy !== ''} title="The edit awaits its review: the change cannot land before it is reviewed" onclick={() => (reviewing = !reviewing)}>Review…</button>
      {/if}
    {/if}
    {#if stored?.deleted}<span class="tag bad">deleted</span>{/if}
  </div>
  {#if error}<div class="alert">{error}</div>{/if}
  {#if inChange && canReview}
    <div class="alert" role="status">
      This node's impact awaits its review. An edit never reviews on its own: the change cannot land until someone accepts it.
      {#if reviewing}
        <div class="reviewrow">
          <input
            type="text"
            class="grow"
            placeholder="Comment (mandatory)"
            aria-label="Review comment"
            bind:value={reviewComment}
            use:assistField={{ id: 'review_comment', label: 'Review comment', type: 'string', required: true, get: () => reviewComment, set: (v) => (reviewComment = String(v)) }}
          />
          <button type="button" class="small primary" disabled={!reviewComment.trim() || busy !== ''} onclick={() => reviewImpact(true)}>Accept</button>
          <button type="button" class="small danger" disabled={!reviewComment.trim() || busy !== ''} onclick={() => reviewImpact(false)}>Reject</button>
          <button type="button" class="small" onclick={() => (reviewing = false)}>Cancel</button>
        </div>
      {/if}
    </div>
  {/if}

  {#if loading && !stored}
    <p class="empty">Loading…</p>
  {:else if !stored}
    <NotFound {tab} what="Node" />
  {:else}
    <EditorPanes {panes} bind:active={pane} label="Node sections">
      {#snippet toolbar()}
        <label class="wc" for="work-change">Working change</label>
        <select id="work-change" value={workId} onchange={(e) => pickChange(e.currentTarget.value)}>
          <option value="">none: created when you edit</option>
          {#each openChanges as c (c.id)}<option value={c.id}>{c.title || shortId(c.id)} ({c.status})</option>{/each}
          {#if workId && !openChanges.some((c) => c.id === workId)}<option value={workId}>{work?.change.title || shortId(workId)}</option>{/if}
        </select>
        {#if workId && work?.flowName}<span class="hint" title="the node is seen and edited as this flow of the change has it">on {work.flowName}</span>{/if}
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
                <button type="button" class="small primary" disabled={busy !== '' || stored.deleted} onclick={() => (editing = true)} title="Edit in the working change">Edit</button>
                {#if inChange && row?.impact?.id && !row.impact.superseded}
                  <button type="button" class="small danger" disabled={busy !== ''} title="Take the node out of the working change (refused once its version has landed: reject it instead)" onclick={removeFromWork}>Remove from change</button>
                {/if}
              {/if}
            </div>
            {#if editing}
              <NodePropertyForm props={nodeProps} attributes={typeCatalog.cat.attributes(typeName)} {declared} {typeName} open={typeCatalog.cat.open(typeName)} busy={busy === 'edit'} onsave={saveProps} oncancel={() => (editing = false)} />
            {:else if propertyNames.length}
              <table class="props">
                <tbody>
                  {#each propertyNames as k (k)}
                    {@const changed = text(nodeProps[k]) !== text(storedProps[k])}
                    <tr class:changed>
                      <th title={attrOf(k)?.tooltip}>{attrOf(k)?.label ?? k}{#if attrOf(k)?.validators.length}<span class="hint" title={`Checked by ${attrOf(k)?.validators.join(', ')}`}> ✓</span>{/if}{#if !declared.includes(k)}<span class="hint" title={`Not declared by ${typeName}`}> *</span>{/if}</th>
                      <td>
                        {#if text(nodeProps[k])}{shownValue(attrOf(k), nodeProps[k])}{:else}<span class="hint">—</span>{/if}
                        {#if changed}<span class="hint pending" title="Proposed in the working change"> (was {text(storedProps[k]) || 'empty'})</span>{/if}
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
              {#if nodeRules.length}<p class="hint">Checked as a whole by {nodeRules.join(', ')}. ✓ marks a property with its own validators (hover to see them).</p>{:else if attrViews.some((a) => a.validators.length)}<p class="hint">✓ marks a property checked by validators (hover to see them).</p>{/if}
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
              <dt>Version</dt><dd>{stored.draft || !stored.version ? 'draft (no version yet)' : `v${stored.version}`}{#if stored.reason} <span class="hint">({stored.reason})</span>{/if}</dd>
              {#if stored.state}<dt>Stored state</dt><dd><span class="state">{stored.state}</span></dd>{/if}
              <dt>Created</dt><dd>{formatDate(stored.createdAt)}</dd>
              {#if stored.changeId}<dt>Change</dt><dd><button type="button" class="link" onclick={() => openTab({ kind: 'change', params: { id: stored.changeId ?? '' } }, { pin: true })}>{changes.items.find((c) => c.id === stored.changeId)?.title ?? shortId(stored.changeId)}</button></dd>{/if}
            </dl>
          </section>
        {:else if active === 'relations'}
          <NodeLinks
            node={stored}
            links={outLinks}
            index={work?.index ?? head}
            readonly={!!stored.deleted}
            why="The node is deleted."
            busy={busy !== ''}
            onadd={addLink}
            onremove={removeLink}
            onopen={openNeighbour}
          />
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
              <LifecycleDiagram {lifecycle} current={nodeState || lifecycle.initial || ''} stored={stored.state ?? ''} busy={busy !== ''} disabled={!!stored.deleted} onmove={transition} />
            </section>
          {/if}
        {:else if active === 'history'}
          <section class="card">
            <h3>Changes <span class="count">{nodeChanges.length}</span></h3>
            {#if nodeChanges.length}
              <ul class="changes">
                {#each nodeChanges as c (c.id)}
                  <li>
                    <StatusBadge status={c.status} />
                    <button type="button" class="link" onclick={() => openTab({ kind: 'change', params: { id: c.id ?? '' } }, { pin: true })}>{c.title || shortId(c.id)}</button>
                    <span class="hint">{c.branch} · {formatDate(c.createdAt)}</span>
                  </li>
                {/each}
              </ul>
            {:else}
              <p class="empty">No change acted on this node.</p>
            {/if}
          </section>
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
  .state.notLandable {
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
  .reviewrow {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    flex-wrap: wrap;
    margin-top: 0.5rem;
  }
  .reviewrow .grow {
    flex: 1;
    min-width: 14rem;
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
  .changes {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .changes li {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
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
