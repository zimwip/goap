<script lang="ts">
  // Node editor: one tab for a node, its information organized in panes —
  // details (properties), relations (parents, children, graph), lifecycle,
  // history. The editor sees the world through the change selected in the context (the working change of the header,
  // stores/workingChange.svelte.ts): the nodes the change created, the versions and drafts it made of the others, the
  // rest as it stands in the fork of the change; with no change selected, the latest released versions. Modifications
  // are proposals of that change (created on demand).
  import EditorActions from '../../shell/EditorActions.svelte';
  import { stopWorking, working, workIn, workInId } from '../../stores/workingChange.svelte';
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
  import NodeNeighbourhood from '../../components/NodeNeighbourhood.svelte';
  import LifecycleDiagram from '../../components/LifecycleDiagram.svelte';
  import NodeHistory from '../../components/NodeHistory.svelte';
  import NodeTags from '../../components/NodeTags.svelte';
  import NodePropertyForm from '../../components/NodePropertyForm.svelte';
  import FormRenderer from '../../forms/FormRenderer.svelte';
  import type { FormValues, NodeSource } from '../../forms/types';
  import { nodeEdit, nodeFormValues, type CurrentLink } from '../../forms/nodeForm';
  import NodeLinks from '../../components/NodeLinks.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { nodeEditor } from '../../shell/registry';
  import { handleOf, openNode, openTarget } from '../../nodeEditors';
  import { loadTypes, typeCatalog } from '../../stores/types.svelte';
  import { provideActions, notify } from '../../shell/workbench.svelte';
  import { changes, refreshChanges } from '../../stores/catalog.svelte';
  import { stamp, keyOf } from '../../flux/signals.svelte';
  import { baselineHoods, changeHoods, hoodIndex, hoodOf, indexOf, type GraphIndex, type Hood } from '../../graphIndex';
  import { MAIN_BRANCH, namespaceOf } from '../../namespace';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import { assistField, registerAssist } from '../../assist/registry.svelte';
  import { diffSummary } from '../../assist/changeScreen';
  import { isCode, orderedAttributes, shownValue } from '../../attributes';
  import CodeView from '../../components/CodeView.svelte';
  import ValueDiff from '../../components/ValueDiff.svelte';
import { declaredProperties, lifecycleResolver, lifecycleRows, loadPosts, writeNodeInChange, removeFromChange, type LifecycleRow, type PostVersions } from '../../lifecycle';

  import NotFound from '../../shell/NotFound.svelte';

  let { tab }: { tab: Tab } = $props();

  const id = $derived(tab.params.id ?? '');

  // --- state ------------------------------------------------------------------------
  let pane = $state(untrack(() => tab.params.pane) || 'details');
  $effect(() => {
    tab.params.pane = pane; // remembered with the tab
  });

  /** the head of the node's namespace as read so far: the neighbourhoods read in it (the node's, then the ones of the
   * nodes unfolded in its trees), never its whole state */
  let headAt = $state<{ id: string; node: string; hoods: Hood[] } | undefined>();
  const head = $derived(headAt ? hoodIndex(headAt.id, headAt.hoods) : undefined);
  let versions = $state<GraphNode[]>([]);
  let loading = $state(true);
  let error = $state('');
  let busy = $state('');
  let editing = $state(false);
  let reload = $state(0);
  /** the Graph pane: the node at its center and the link it shows, in the head baseline (as in the Baseline tab) */
  let graphNode = $state('');
  let graphLink = $state<Link | undefined>();
  /** the draft view of a node no version exists of yet (created by the working change, ADR 0079) */
  let draftNode = $state<GraphNode | undefined>();

  interface Work {
    change: Change;
    /** the name of the flow the change is seen on ('' : the change as a whole) */
    flowName: string;
    /** the fork of the change: the versions its impacts start from */
    base: GraphIndex;
    attached: NodeRef[];
    posts: PostVersions;
  }
  /** the namespace of the node, once read: a change acting on another one does not hold it */
  let nodeNamespace = $state('');
  // the change the editor follows: the one selected in the context, when it acts on the namespace of the node
  const workId = $derived(working.change && (!nodeNamespace || namespaceOf(working.change.namespace) === nodeNamespace) ? working.change.id : '');
  /** the change the tab was opened in (an Edit of an impact, a link), selected in the context when the tab opens it */
  let asked = $state('');
  /** the flow of the change the node is seen and edited on: 'main' or an option (ADR 0032 §6), given with the change
   * the tab was opened in; empty: the change as a whole (its edits go to its active option) */
  let flowAsked = $state({ change: untrack(() => tab.params.change) ?? '', flow: untrack(() => tab.params.flow) ?? '' });
  const workFlow = $derived(workId && flowAsked.change === workId ? flowAsked.flow : '');
  let work = $state<Work | undefined>();
  /** the world of the change as read so far: the neighbourhoods read in it (its fork as it left it, its commits, its
   * drafts; GetNodeNeighbourhood in the change), the node's then the ones of the nodes unfolded */
  let worldHoods = $state<Hood[]>([]);
  const worldIndex = $derived(work ? hoodIndex(work.change.baselineId ?? '', worldHoods) : undefined);
  /** the neighbourhoods to read again when the world moves: the ones read so far, the node's first */
  const readAgain = (hoods: Hood[]) => [...new Set([id, ...hoods.flatMap((h) => (h.node?.id ? [h.node.id] : []))])].slice(0, 20);

  // opened in a change (tab.params.change, set again when the tab is opened anew): that change becomes the one selected
  // in the context, which the editor then follows
  $effect(() => {
    const c = tab.params.change ?? '';
    const f = tab.params.flow ?? '';
    untrack(() => {
      if (!c) return;
      if (flowAsked.change !== c || flowAsked.flow !== f) flowAsked = { change: c, flow: f };
      if (c === workId || c === asked) return;
      asked = c;
      void workInId(c).finally(() => (asked = ''));
    });
  });
  // the tab names what it shows (its URL): the change followed and its flow
  $effect(() => {
    const c = workId;
    const f = workFlow;
    if (asked) return;
    untrack(() => {
      tab.params.change = c;
      tab.params.flow = f;
    });
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
      nodeNamespace = namespace;
      const hid = (await graph.getBranch(namespace, MAIN_BRANCH, signal)).head?.id ?? '';
      const again = headAt?.id === hid && headAt.node === id ? readAgain(headAt.hoods) : [id];
      headAt = { id: hid, node: id, hoods: hid ? await Promise.all(again.map((n) => hoodOf(baselineHoods(hid), n, signal))) : [] };
      if (!changes.loaded) void refreshChanges();
    } catch (e) {
      if (!signal?.aborted) error = errorMessage(e);
    } finally {
      if (!signal?.aborted) loading = false;
    }
  }

  /** the fork of a change (the empty state for the first change of a namespace, ADR 0056) */
  const loadFork = async (baselineId?: string): Promise<GraphIndex> =>
    baselineId ? hoodIndex(baselineId, [await hoodOf(baselineHoods(baselineId), id)]) : indexOf('', [], []);
  /** the neighbourhoods of the world of the change: the node's, and the ones read so far when it is the same change */
  let worldOf = '';
  async function loadWorld(flow = '') {
    const at = `${id} ${workId} ${flow}`;
    const again = worldOf === at ? readAgain(worldHoods) : [id];
    const hoods = await Promise.all(again.map((n) => hoodOf(changeHoods(workId, flow), n)));
    worldOf = at;
    return hoods;
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
        if (!change) throw new Error('unknown change');
        const attached = (change.nodes ?? []).filter((n) => n.pre?.id && !n.superseded).map((n) => n.pre!);
        const flowName = workFlow === 'main' ? 'main flow' : (bb.options?.find((o) => o.id === workFlow)?.option?.name ?? shortId(workFlow));
        const [base, hoods, posts] = await Promise.all([loadFork(change.baselineId), loadWorld(workFlow), loadPosts(change.nodes ?? [], true, { changeId: workId, flow: workFlow })]);
        worldHoods = hoods;
        work = { change, flowName, base, attached, posts };
        return;
      }
      const change = (await graph.getChange(workId)).change;
      if (!change) throw new Error('unknown change');
      const [base, hoods, attached, posts] = await Promise.all([
        loadFork(change.baselineId),
        loadWorld(),
        graph.getChangeImpacts(workId),
        loadPosts(change.nodes ?? [], false, { changeId: workId }),
      ]);
      worldHoods = hoods;
      work = { change, flowName: '', base, attached: attached.nodes ?? [], posts };
    } catch (e) {
      error = errorMessage(e);
      work = undefined;
    }
  }

  $effect(() => {
    void id;
    void stamp(keyOf.node(id)); // a new version of the node, by anyone
    graphNode = id;
    graphLink = undefined;
    const ctrl = new AbortController();
    void loadNode(ctrl.signal);
    return () => ctrl.abort();
  });

  // a node only a change holds (a draft, no version) is read through the change followed
  $effect(() => {
    const w = workId;
    void workFlow;
    untrack(() => {
      if (w && !loading && !versions.length) void loadNode();
    });
  });

  $effect(() => {
    void workId;
    void workFlow;
    void stamp(keyOf.change(workId)); // its impacts or log moved, by anyone
    void loadWork();
  });

  // --- derived ----------------------------------------------------------------------
  const stored = $derived(head?.nodes.get(id) ?? versions.at(-1) ?? (workId ? draftNode : undefined));
  /** the world the editor sees: the change's when one is followed, else the latest released versions */
  const world = $derived(worldIndex ?? head);

  /** reads the neighbourhood of a node unfolded in a tree, in the world seen */
  async function expand(nid: string) {
    try {
      if (work) {
        const [w, f] = [workId, workFlow];
        const h = await hoodOf(changeHoods(w, f), nid);
        if (w === workId && f === workFlow) worldHoods = [...worldHoods, h];
      } else if (headAt?.id) {
        const at = headAt.id;
        const h = await hoodOf(baselineHoods(at), nid);
        if (headAt?.id === at) headAt = { ...headAt, hoods: [...headAt.hoods, h] };
      }
    } catch (e) {
      error = errorMessage(e);
    }
  }

  /** nodes picked from a search (the pickers of links and forms): the world read holds the node's neighbours only */
  let picked = $state<GraphNode[]>([]);
  let foundByKey = new Map<string, GraphNode>();
  /** the nodes matching a text: the ones of the fork of the change (else of the head), and the ones the change creates */
  async function searchNodes(query: string): Promise<GraphNode[]> {
    const baselineId = work ? work.change.baselineId : head?.baselineId;
    const found = baselineId ? ((await graph.listBaselineNodes({ baselineId, query, limit: 200 })).nodes ?? []) : [];
    const q = query.trim().toLowerCase();
    const created = (work?.change.nodes ?? [])
      .filter((c) => c.intent === 'created' && c.post?.id && !c.superseded && (!q || `${c.key} ${c.type}`.toLowerCase().includes(q)))
      .map((c): GraphNode => ({ id: c.post?.id, version: 0, key: c.key, type: c.type, namespace: work?.change.namespace }));
    const out = [...created, ...found.filter((n) => !n.deleted)];
    foundByKey = new Map(out.flatMap((n) => (n.key ? [[n.key, n] as const] : [])));
    return out;
  }
  /** the node in the world seen: as the change has it (a draft is version 0), else its latest released version */
  const inWorld = $derived(world?.nodes.get(id));
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
      return lifecycleRows(typeCatalog.cat, work.base.list, work.attached, work.change.nodes ?? [], work.posts, [id], !!workFlow).find(
        (r) => r.node.id === id || (!!r.created && r.impact?.post?.id === id),
      );
    return lifecycleRows(typeCatalog.cat, headList, [], [], new Map(), [id]).find((r) => r.node.id === id);
  });
  const inChange = $derived(!!work && !!row);
  const nodeProps = $derived((row?.props ?? stored?.props ?? {}) as Record<string, unknown>);
  const storedProps = $derived((stored?.props ?? {}) as Record<string, unknown>);
  const nodeState = $derived(row?.effective ?? stored?.state ?? '');
  // edits are allowed in any state (ADR 0078): a state flagged notCommittable only keeps the change from commit
  const notCommittable = $derived(!!row && !!row.lifecycle && !row.committable);

  const openChanges = $derived(changes.items.filter((c) => (c.status === 'draft' || c.status === 'active') && (!stored?.namespace || c.namespace === stored.namespace)));
  const attrViews = $derived(orderedAttributes(typeCatalog.cat.attributes(typeName)));
  const nodeRules = $derived(typeCatalog.cat.type(typeName)?.nodeValidators ?? []);
  const attrOf = (k: string) => attrViews.find((a) => a.name === k);
  const propertyNames = $derived([...new Set([...attrViews.map((a) => a.name), ...declared, ...Object.keys(nodeProps)])]);
  /** the properties whose diff against the stored value is open */
  let diffOpen = $state<ReadonlySet<string>>(new Set());
  function toggleDiff(k: string) {
    const next = new Set(diffOpen);
    if (!next.delete(k)) next.add(k);
    diffOpen = next;
  }
  const text = (v: unknown): string => (v === undefined || v === null ? '' : typeof v === 'string' ? v : JSON.stringify(v));

  const panes = $derived<Pane[]>([
    { id: 'details', label: 'Details' },
    { id: 'relations', label: 'Relations', badge: world ? (world.in.get(id)?.length ?? 0) + (world.out.get(id)?.length ?? 0) : undefined },
    { id: 'graph', label: 'Graph' },
    { id: 'lifecycle', label: 'Lifecycle', badge: nodeState || undefined, hidden: !lifecycle },
    { id: 'history', label: 'History', badge: versions.at(-1)?.version || undefined },
  ]);

  // opened on the edit of the node (the Edit of an impact, tab.params.edit): the edit starts once the node is read as
  // the change followed has it, never from another state of it
  let editAsked = $state(false);
  $effect(() => {
    if (!tab.params.edit) return;
    untrack(() => {
      delete tab.params.edit;
      pane = 'details';
      editAsked = true;
    });
  });
  $effect(() => {
    if (!editAsked || loading || !stored || asked || (workId && work?.change.id !== workId)) return;
    editAsked = false;
    if (!stored.deleted) editing = true;
  });

  // --- the change followed ------------------------------------------------------------
  async function ensureChange(): Promise<string> {
    if (workId) return workId;
    const baselineId = head?.baselineId;
    if (!baselineId) throw new Error('There is no baseline to start a change from.');
    // the change acts on the namespace of the node (ADR 0015 §2)
    const c = (await graph.createChange({ title: `Edit ${stored?.key ?? shortId(id)}`, baselineId, namespace: stored?.namespace })).change;
    if (!c?.id) throw new Error('The change could not be created.');
    // a change created to edit a node is the change the person works in from now on: the one the editor follows
    if (!(await workIn(c)) || workId !== c.id) throw new Error(`Change “${c.title}” was created, but could not be selected: select it to edit the node.`);
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
      // the version the change starts from: the fork's
      const node = work?.base.nodes.get(id);
      const target = row?.created ? { key: row.node.key, type: row.node.type } : node?.id ? { pre: { id: node.id, version: node.version } } : undefined;
      if (!target) throw new Error('This node is not in the world of the selected change: select another change.');
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
    inChange && row?.impact?.post?.id ? row.impact.post : inWorld?.id ? { id: inWorld.id, version: inWorld.version } : stored?.id ? { id: stored.id, version: stored.version } : undefined,
  );
  let outLinks = $state<Link[]>([]);
  let nodeChanges = $state<Change[]>([]);
  /** the cursor of the older page of the node's changes (ADR 0140: a page at a time); undefined: none */
  let olderChanges = $state<string | undefined>();
  async function loadOlderChanges() {
    if (!olderChanges) return;
    const r = await graph.listNodeChanges(id, undefined, { before: olderChanges }).catch(() => undefined);
    if (!r) return;
    nodeChanges = [...(r.changes ?? []), ...nodeChanges];
    olderChanges = r.next && r.next !== '0' ? r.next : undefined;
  }
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
      .then((r) => ((nodeChanges = r.changes ?? []), (olderChanges = r.next && r.next !== '0' ? r.next : undefined)))
      .catch(() => ((nodeChanges = []), (olderChanges = undefined)));
    return () => ctrl.abort();
  });
  const addLink = (type: string, to: NodeRef) => propose('link', { addLinks: [{ type, to }] }, `Link ${stored?.key} ${type} ${world?.nodes.get(to.id ?? '')?.key ?? ''}`.trim());
  const removeLink = (l: Link) => propose('link', { removeLinks: [l.id ?? ''] }, `Unlink ${stored?.key} ${typeCatalog.cat.linkLabel(l.type, 'out')}`);

  const transition = (t: LifecycleTransition) => propose('move', { state: t.to }, `Move ${stored?.key} to ${t.to}`);

  const saveProps = (patch: Record<string, unknown>) => propose('edit', { props: patch }, `Edit ${stored?.key}`);

  // The form of the node's type (ADR 0115 §10): when the type declares one, the node is edited through it, its link
  // fields standing for the outgoing links of their types.
  const typeForm = $derived(typeName ? typeCatalog.cat.type(typeName)?.form : undefined);
  const formIndex = $derived(world);
  const keyOfId = (nid: string | undefined) =>
    nid ? (formIndex?.nodes.get(nid)?.key ?? head?.nodes.get(nid)?.key ?? picked.find((n) => n.id === nid)?.key) : undefined;
  const currentLinks = $derived<CurrentLink[]>(
    outLinks.flatMap((l) => {
      const to = keyOfId(l.to?.id);
      return l.id && l.type && to ? [{ id: l.id, type: l.type, to }] : [];
    }),
  );
  const formSource = $derived.by((): NodeSource => {
    const cat = typeCatalog.cat;
    return {
      nodes: [...(formIndex?.list ?? []), ...picked.filter((p) => !formIndex?.nodes.has(p.id ?? ''))]
        .filter((n) => n.key && n.id !== id)
        .map((n) => ({ key: n.key!, type: n.type ?? '', state: n.state, namespace: n.namespace }))
        .sort((a, b) => a.key.localeCompare(b.key)),
      isA: (t, a) => t === a || cat.typesOf(t).includes(a),
      linkTypesFrom: () => (typeName ? cat.linksFrom(typeName).map((l) => l.ref ?? '').filter(Boolean) : []),
      linkLabel: (t) => cat.links.find((l) => l.ref === t)?.label ?? '',
      search: async (q) =>
        (await searchNodes(q)).filter((n) => n.key && n.id !== id).map((n) => ({ key: n.key!, type: n.type ?? '', state: n.state, namespace: n.namespace })),
      remember: (o) => {
        const n = foundByKey.get(o.key);
        if (n && !picked.some((p) => p.key === n.key)) picked = [...picked, n];
      },
    };
  });
  let formError = $state('');
  async function saveForm(values: FormValues) {
    if (!typeForm) return;
    formError = '';
    const e = nodeEdit(typeForm, values, nodeProps, currentLinks);
    const refOfKey = (k: string) => [...(formIndex?.list ?? []), ...picked].find((n) => n.key === k);
    const addLinks: LinkWrite[] = [];
    for (const l of e.add) {
      const n = refOfKey(l.to);
      if (!n?.id) return void (formError = `No node ${l.to} to link to.`);
      addLinks.push({ type: l.type, to: { id: n.id, version: n.version ?? 0 } });
    }
    if (!Object.keys(e.props).length && !addLinks.length && !e.remove.length) return void (editing = false);
    const w = { ...(Object.keys(e.props).length ? { props: e.props } : {}), ...(addLinks.length ? { addLinks } : {}), ...(e.remove.length ? { removeLinks: e.remove } : {}) };
    if (await propose('edit', w, `Edit ${stored?.key}`)) editing = false;
    else formError = error;
  }

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

  /** Takes the node out of the working change, at any time (ADR 0102): its draft is dropped, a version it committed is
   * reverted by the next commit. A node is never deleted (ADR 0076). */
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

  /** Selects the change of the context the editor follows ('': none, the latest released versions). */
  function pickChange(value: string) {
    editing = false;
    flowAsked = { change: '', flow: '' }; // another change: seen as a whole
    if (value) void workInId(value);
    else stopWorking();
  }

  const openNeighbour = (nid: string) => openNode(world?.nodes.get(nid) ?? head?.nodes.get(nid) ?? { id: nid }, { pin: true });

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
    {#if nodeState}<span class="state" class:notCommittable title={notCommittable ? 'Not committable: a change cannot commit with the node in this state' : ''}>{nodeState}</span>{/if}
    {#if inChange && isDraft(row?.impact?.post)}
      <span class="hint" title="the working change holds a draft of the node; its version is written when the change commits (ADR 0079). Released: v{stored?.version ?? '—'}">draft in the change</span>
    {:else if (work ? inWorld?.version : undefined) || stored?.version}<span class="hint" title={work && inWorld?.version !== stored?.version ? `as the change has it; released: v${stored?.version ?? '—'}` : undefined}>v{(work ? inWorld?.version : undefined) || stored?.version}</span>{/if}
    {#if inChange && row?.impact}
      <StatusBadge status={row.impact.review} />
      {#if canReview}
        <button type="button" class="small primary" disabled={busy !== ''} title="The edit awaits its review: the change cannot commit before it is reviewed" onclick={() => (reviewing = !reviewing)}>Review…</button>
      {/if}
    {/if}
    {#if stored?.deleted}<span class="tag bad">deleted</span>{/if}
    <EditorActions tabId={tab.id} />
  </div>
  {#if error}<div class="alert">{error}</div>{/if}
  {#if inChange && canReview}
    <div class="alert" role="status">
      This node's impact awaits its review. An edit never reviews on its own: the change cannot commit until someone accepts it.
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
        <label class="wc" for="work-change" title="The editor sees the graph through the change selected in the context (the working change of the header)">Seen through</label>
        <select id="work-change" value={workId} onchange={(e) => pickChange(e.currentTarget.value)}>
          <option value="">the latest released versions</option>
          {#each openChanges as c (c.id)}<option value={c.id}>{c.title || shortId(c.id)} ({c.status})</option>{/each}
          {#if workId && !openChanges.some((c) => c.id === workId)}<option value={workId}>{work?.change.title || shortId(workId)}</option>{/if}
        </select>
        {#if workId && work?.flowName}<span class="hint" title="the node is seen and edited as this flow of the change has it">on {work.flowName}</span>{/if}
        {#if workId}
          <button type="button" class="small" onclick={openChange}>Open change</button>
          <button type="button" class="small ghost" title="Release the selected change: the editor shows the latest released versions (an edit then starts a change)" onclick={() => pickChange('')}>Latest version</button>
        {:else if working.change}
          <span class="hint" title="The selected change acts on another namespace than this node">({working.change.title} acts on {working.change.namespace})</span>
        {/if}
      {/snippet}

      {#snippet children(active)}
        {#if workId && work && !row}
          <div class="alert info">This node is not in the world of the selected change: select another change, or none to see its latest released version.</div>
        {/if}

        {#if active === 'details'}
          <section class="card">
            <div class="head">
              <h3>Properties</h3>
              <span class="grow"></span>
              {#if !editing}
                <button type="button" class="small primary" disabled={busy !== '' || stored.deleted} onclick={() => (editing = true)} title="Edit in the working change">Edit</button>
                {#if inChange && row?.impact?.id && !row.impact.superseded}
                  <button type="button" class="small danger" disabled={busy !== ''} title="Take the node out of the working change, at any time: one committed already is reverted by the next commit" onclick={removeFromWork}>Remove from change</button>
                {/if}
              {/if}
            </div>
            {#if editing && typeForm}
              <FormRenderer form={{ ...typeForm, title: undefined }} source={formSource} initial={nodeFormValues(typeForm, nodeProps, currentLinks)} busy={busy === 'edit'} serverError={formError}
                submitLabel="Save" onsubmit={(v) => void saveForm(v)} assist={{ id: `edit:${stored.key}`, label: `Edit ${stored.key} (${typeName})` }}>
                {#snippet actions()}<button type="button" class="ghost" onclick={() => (editing = false)}>Cancel</button>{/snippet}
              </FormRenderer>
            {:else if editing}
              <NodePropertyForm props={nodeProps} attributes={typeCatalog.cat.attributes(typeName)} {declared} {typeName} open={typeCatalog.cat.open(typeName)} busy={busy === 'edit'} onsave={saveProps} oncancel={() => (editing = false)} />
            {:else if propertyNames.length}
              <table class="props">
                <tbody>
                  {#each propertyNames as k (k)}
                    {@const changed = text(nodeProps[k]) !== text(storedProps[k])}
                    <tr class:changed>
                      <th title={attrOf(k)?.tooltip}>{attrOf(k)?.label ?? k}{#if attrOf(k)?.validators.length}<span class="hint" title={`Checked by ${attrOf(k)?.validators.join(', ')}`}> ✓</span>{/if}{#if !declared.includes(k)}<span class="hint" title={`Not declared by ${typeName}`}> *</span>{/if}</th>
                      <td>
                        {#if isCode(attrOf(k)) && typeof nodeProps[k] === 'string' && nodeProps[k]}
                          <CodeView value={nodeProps[k] as string} label={attrOf(k)?.label} />
                          {#if changed}<span class="hint pending" title="Proposed in the working change">(changed in the working change{text(storedProps[k]) ? '' : ', was empty'})</span>{/if}
                        {:else}
                          {#if text(nodeProps[k])}{shownValue(attrOf(k), nodeProps[k])}{:else}<span class="hint">—</span>{/if}
                          {#if changed}<span class="hint pending" title="Proposed in the working change"> (was {text(storedProps[k]) || 'empty'})</span>{/if}
                        {/if}
                        {#if changed}
                          <button type="button" class="link small" aria-expanded={diffOpen.has(k)} title="What the working change does to the value" onclick={() => toggleDiff(k)}>{diffOpen.has(k) ? 'hide diff' : 'diff'}</button>
                          {#if diffOpen.has(k)}<div class="propdiff"><ValueDiff before={storedProps[k]} after={nodeProps[k]} attribute={attrOf(k)} /></div>{/if}
                        {/if}
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
            index={world}
            search={searchNodes}
            readonly={!!stored.deleted}
            why="The node is deleted."
            busy={busy !== ''}
            onadd={addLink}
            onremove={removeLink}
            onopen={openNeighbour}
          />
          {#if world && inWorld}
            <div class="trees">
              <section class="card">
                <h3>Parents <span class="count">{world.in.get(id)?.length ?? 0}</span></h3>
                <p class="hint">Nodes that link to this one{work ? ', as the change has them' : ''}.</p>
                <NodeTree index={world} root={id} dir="in" onopen={(n) => openNeighbour(n)} loaded={world.loaded} expand={(n) => void expand(n)} />
              </section>
              <section class="card">
                <h3>Children <span class="count">{world.out.get(id)?.length ?? 0}</span></h3>
                <p class="hint">Nodes this one links to{work ? ', as the change has them' : ''}.</p>
                <NodeTree index={world} root={id} dir="out" onopen={(n) => openNeighbour(n)} loaded={world.loaded} expand={(n) => void expand(n)} />
              </section>
            </div>
          {:else}
            <p class="empty">{work ? 'This node is not in the world of the selected change.' : 'This node is not in the current graph (deleted, or only created by a change: select that change to see it).'}</p>
          {/if}
        {:else if active === 'graph'}
          {#if work && inWorld}
            <section class="card neighbourhood">
              {#if graphNode !== id || graphLink}
                <p><button type="button" class="small" onclick={() => ((graphNode = id), (graphLink = undefined))}>Back to {stored.key}</button></p>
              {/if}
              <NodeNeighbourhood baselineId={work.change.baselineId ?? ''} inChange={{ changeId: work.change.id ?? '', flow: workFlow }} namespace={stored.namespace ?? ''} bind:node={graphNode} bind:link={graphLink} details={false} compact fill {reload} />
            </section>
          {:else if !work && head?.baselineId && head.nodes.has(id)}
            <section class="card neighbourhood">
              {#if graphNode !== id || graphLink}
                <p><button type="button" class="small" onclick={() => ((graphNode = id), (graphLink = undefined))}>Back to {stored.key}</button></p>
              {/if}
              <NodeNeighbourhood baselineId={head.baselineId} namespace={stored.namespace ?? ''} bind:node={graphNode} bind:link={graphLink} details={false} compact fill {reload} />
            </section>
          {:else}
            <p class="empty">{work ? 'This node is not in the world of the selected change.' : 'This node is not in the current graph (deleted, or only created by a change: select that change to see it).'}</p>
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
            <h3>Changes <span class="count">{nodeChanges.length}{olderChanges ? '+' : ''}</span></h3>
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
              {#if olderChanges}<button type="button" class="small" onclick={loadOlderChanges}>Older changes</button>{/if}
            {:else}
              <p class="empty">No change acted on this node.</p>
            {/if}
          </section>
          <NodeHistory {id} reloadKey={reload} />
          <NodeTags {id} versions={versions.map((v) => v.version ?? 0).filter((v) => v > 0)} />
        {/if}
      {/snippet}
    </EditorPanes>
  {/if}
</div>

<style>
  .propdiff {
    margin-top: 0.3rem;
  }
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
  .state.notCommittable {
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
  .trees {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 0.8rem;
    align-items: start;
  }
  @media (max-width: 1000px) {
    .trees {
      grid-template-columns: 1fr;
    }
  }
</style>
