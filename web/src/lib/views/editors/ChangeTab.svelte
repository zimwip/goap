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
    type Resolution,
  } from '../../api';
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import ChangeAudit from '../../components/ChangeAudit.svelte';
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
  import { namespaceOf } from '../../namespace';
  import { processes } from '../../stores/live.svelte';
  import FlowGraph from '../../components/FlowGraph.svelte';
  import FlowActions from '../../components/FlowActions.svelte';
  import FlowBranchInfo from '../../components/FlowBranchInfo.svelte';
  import { flowStepNumber } from '../../flowChain';
  import { processOfFlow } from '../../flowDecision';
  import BoardIssueList from '../../components/BoardIssueList.svelte';
  import ChangeOptions from '../../components/ChangeOptions.svelte';
  import ChangeDecisions from '../../components/ChangeDecisions.svelte';
  import ScopeBar from '../../components/ScopeBar.svelte';
  import MergeResolver from '../../components/MergeResolver.svelte';
  import { MAIN_SCOPE, candidatesByOption, scopeColor, scopeName, scopeWritable } from '../../changeScope';

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
  // reopening this tab (already mounted) with different params — e.g. from a process's "Audit" link — should still
  // jump there, so also sync the other way
  $effect(() => {
    if (tab.params.pane && tab.params.pane !== pane) pane = tab.params.pane;
  });
  /** the process the Audit pane is restricted to, opened from elsewhere (e.g. a process's "Audit" link) */
  let auditProcess = $state(untrack(() => tab.params.process) || '');
  $effect(() => {
    tab.params.process = auditProcess;
  });
  $effect(() => {
    if (tab.params.process !== undefined && tab.params.process !== auditProcess) auditProcess = tab.params.process;
  });
  /** the action run the Audit pane is restricted to, opened from elsewhere (e.g. an item's producer link) */
  let auditRun = $state(untrack(() => tab.params.run) || '');
  $effect(() => {
    tab.params.run = auditRun;
  });
  $effect(() => {
    if (tab.params.run !== undefined && tab.params.run !== auditRun) auditRun = tab.params.run;
  });
  let loading = $state(false);
  let error = $state('');
  /** the flow the editor looks at: the main flow or one option (ADR 0032 §6); local to the editor, it does not
   * move the active option. The impacts and items panes show the change as that flow sees it. */
  let scope = $state(untrack(() => tab.params.scope) || '');
  $effect(() => {
    tab.params.scope = scope;
  });
  /** the change as the scope sees it: its change impacts (with the posts of that flow) and items */
  let view = $state<Change | undefined>();

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
      boardIssues = (await graph.validateBoard(selected, scope || MAIN_SCOPE)).issues ?? [];
    } catch (e) {
      checkError = errorMessage(e);
    } finally {
      checking = false;
    }
  }
  let splitting = $state(false);
  /** the branch a change on its own branch merges into (its parent branch) */
  let mergeInto = $state('');
  // editing the definition of the change (title, intent) and abandoning it
  let defining = $state(false);
  let defTitle = $state('');
  let defIntent = $state('');
  let defBusy = $state(false);

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
      // no scope yet, or one decided since: the option agents work on, else the main flow
      const opts = flows.filter((f) => f.option);
      if (!scope || (scope !== MAIN_SCOPE && !opts.some((o) => o.id === scope))) scope = opts.find((o) => o.active)?.id ?? MAIN_SCOPE;
      await loadScope(id, scope, signal);
    } catch (e) {
      if (!signal?.aborted) error = errorMessage(e);
    } finally {
      if (!signal?.aborted) loading = false;
    }
  }

  /** Loads the change as the scope sees it. */
  async function loadScope(id: string, sc: string, signal?: AbortSignal) {
    loadedScope = sc;
    const v = (await graph.getBlackboard(id, sc || MAIN_SCOPE, signal)).change;
    const ps = await loadPosts(v?.nodes ?? [], true);
    if (signal?.aborted || sc !== scope) return;
    view = v;
    posts = ps;
    attached = (v?.nodes ?? []).filter((n) => n.pre?.id && !n.superseded).map((n) => n.pre!);
    boardIssues = null;
  }

  // switching the scope reloads what the scoped panes show
  let loadedScope = '';
  $effect(() => {
    const sc = scope;
    const id = untrack(() => change?.id);
    if (!id || !sc || sc === loadedScope) return;
    loadedScope = sc;
    const ctrl = new AbortController();
    loadScope(id, sc, ctrl.signal).catch((e) => {
      if (!ctrl.signal.aborted) error = errorMessage(e);
    });
    return () => ctrl.abort();
  });

  $effect(() => {
    const id = selected;
    change = undefined;
    view = undefined;
    loadedScope = '';
    attached = [];
    subs = [];
    ancestors = [];
    flows = [];
    defining = false;
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
  const items = $derived((view?.items ?? []).filter((i) => i.kind !== 'flow' && i.kind !== 'decision_point').map((i) => ({ ...i, status: effectiveStatus(i) })));
  const flowProcess = (f: Flow) => processOfFlow(f);
  // options are flows opened as hypotheses (ADR 0032 §6): they have a pane of their own
  const options = $derived(flows.filter((f) => f.option));
  const relaunches = $derived(flows.filter((f) => !f.option));
  // decision points that are not decided yet (ADR 0009 §4), replayed from the facts of the main flow
  const pendingDecisions = $derived.by(() => {
    const threshold = new Map<string, number>();
    const decided = new Set<string>();
    for (const it of change?.items ?? []) {
      const e = it.decisionEvent;
      if (it.kind !== 'decision_point' || !e || it.flow) continue;
      if (e.op === 'open') threshold.set(it.id ?? '', e.threshold ?? 0);
      else if (e.op === 'rule' && e.outcome === 'decided' && (e.human || (e.confidence ?? 0) >= (threshold.get(e.point ?? '') ?? 1))) decided.add(e.point ?? '');
      else if (e.op === 'ratify' && e.accept) decided.add(e.point ?? '');
    }
    const opened = [...threshold.keys()];
    return opened.filter((id) => !decided.has(id)).length;
  });
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

  // applied, or applied on its own branch and waiting for its merge: nothing to apply any more
  const isApplied = $derived(change?.status === 'applied' || change?.status === 'merge_pending');
  const closed = $derived(change?.status === 'applied' || change?.status === 'abandoned');
  void loadTypes();
  const lcRows = $derived(lifecycleRows(typeCatalog.cat, nodes, attached, view?.nodes ?? [], posts, extraNodes, true));
  // the scope: what it shows, whether it can be edited, its colour
  const writable = $derived(scopeWritable(options, scope, closed));
  const scopeLabel = $derived(scopeName(options, scope));
  const scopeTint = $derived(scopeColor(options, scope));
  const candidates = $derived(candidatesByOption(change?.nodes ?? []));
  const mainImpacts = $derived((change?.nodes ?? []).filter((n) => !n.flow && !n.superseded).length);
  // a change creates and modifies the nodes of its namespace (ADR 0015 §2)
  const lcCandidates = $derived(reopenable(nodes, lcRows, change?.namespace ?? ''));
  const typeNames = $derived(nodeTypeNames(typeCatalog.cat, change?.namespace ?? ''));
  const lifecycleOf = $derived(lifecycleResolver(typeCatalog.cat));
  const takenKeys = $derived([
    ...nodes.map((n) => n.key ?? ''),
    ...(view?.nodes ?? []).filter((n) => n.intent === 'created' && !n.superseded && n.review !== 'rejected').map((n) => n.key ?? ''),
  ]);
  const stuckEditable = $derived(lcRows.some((r) => r.lifecycle && r.editable && !r.removal));
  const panes = $derived<Pane[]>([
    { id: 'overview', label: 'Overview', badge: stuckEditable ? '!' : undefined },
    { id: 'impacts', label: `${scopeLabel} ▸ Impacts`, badge: view?.nodes?.length || undefined },
    { id: 'items', label: `${scopeLabel} ▸ Items`, badge: items.length || undefined },
    { id: 'compare', label: 'Compare', badge: options.filter((f) => f.status === 'open').length || undefined },
    { id: 'decisions', label: 'Decisions', badge: pendingDecisions || undefined },
    { id: 'changes', label: 'Changes', badge: subs.length + ancestors.length || undefined },
    { id: 'audit', label: 'Audit' },
  ]);

  /** Writes a node in this change through its change impact (the server checks the state). */
  async function write(label: string, target: { pre?: NodeRef; key?: string; type?: string }, w: { props?: Record<string, unknown>; state?: string; retire?: boolean }, rationale: string): Promise<boolean> {
    if (!change?.id) return false;
    moving = label;
    error = '';
    try {
      await writeNodeInChange(change.id, view?.nodes ?? [], target, w, rationale, scope || MAIN_SCOPE);
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

  /** Declares that the change (on the scope) impacts a node of its baseline, and why: a planned change impact. */
  async function addImpact(id: string, rationale = '') {
    const n = nodes.find((x) => x.id === id);
    if (!change?.id || !n?.id) return;
    moving = `${id}:add`;
    error = '';
    try {
      await graph.addChangeImpacts(change.id, [{ intent: 'modified', pre: { id: n.id, version: n.version }, rationale: rationale || `work on ${n.key}`, flow: scope || MAIN_SCOPE }]);
      await load(change.id);
    } catch (e) {
      error = errorMessage(e);
    } finally {
      moving = '';
    }
  }

  /** Accepts or rejects the change impact of a row, on the scope. */
  async function reviewRow(row: LifecycleRow, accept: boolean, comment: string): Promise<boolean> {
    if (!change?.id || !row.impact?.id) return false;
    moving = `${row.node.id}:review`;
    error = '';
    try {
      await graph.reviewChangeImpact(change.id, row.impact.id, accept, comment, scope || MAIN_SCOPE);
      await load(change.id);
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      moving = '';
    }
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
        await graph.reviewChangeImpact(change.id, row.created.id, false, `discarded ${row.node.key}`, scope || MAIN_SCOPE);
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
      await graph.reviewChangeImpact(change.id, row.removal.id, false, `keep ${row.node.key}`, scope || MAIN_SCOPE);
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

  $effect(() => {
    const ch = change;
    mergeInto = '';
    if (ch?.status !== 'merge_pending' || !ch.branch) return;
    graph
      .getBranch(namespaceOf(ch.namespace), ch.branch)
      .then((r) => (mergeInto = r.branch?.parent || 'main'))
      .catch(() => (mergeInto = 'main'));
  });

  async function merge(resolutions: Record<string, Resolution>): Promise<boolean> {
    if (!change?.id) return false;
    await graph.mergeChange(change.id, resolutions);
    await load(change.id);
    void refreshChanges();
    void refreshBaselines(namespaceOf(change?.namespace));
    notify('Change merged.', 'ok');
    return true;
  }

  function startDefine() {
    defTitle = change?.title ?? '';
    defIntent = change?.intent ?? '';
    defining = true;
  }

  async function define(patch: { title?: string; intent?: string; status?: string }, done: string) {
    if (!change?.id) return;
    defBusy = true;
    error = '';
    try {
      await graph.updateChange(change.id, patch);
      defining = false;
      await load(change.id);
      void refreshChanges();
      notify(done, 'ok');
    } catch (e) {
      error = errorMessage(e);
    } finally {
      defBusy = false;
    }
  }

  const abandon = () =>
    confirm(`Abandon “${change?.title}”? Its sub-changes are abandoned too and its branch is closed; nothing it wrote lands.`) &&
    define({ status: 'abandoned' }, 'Change abandoned.');

  async function apply() {
    if (!change?.id) return;
    applying = true;
    error = '';
    try {
      applied = (await graph.applyChange(change.id, baselineName.trim())).baseline;
      await load(change.id);
      void refreshChanges();
      void refreshBaselines(namespaceOf(change?.namespace));
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

  /** Switches to the Audit pane, optionally restricted to one action run. */
  function openAudit(run = '') {
    pane = 'audit';
    auditProcess = '';
    auditRun = run;
  }

  function provenance(i: ChangeItem): string {
    const parts = [i.producedBy ? `Produced by ${i.producedBy}` : 'Unknown producer'];
    if (i.execution) parts.push(`execution ${shortId(i.execution)} — open in the audit trail`);
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
        id: 'audit',
        label: 'Audit',
        icon: 'list',
        disabled: !change,
        title: 'Ticks, actions, model calls, decisions and impacts of this change',
        run: () => openAudit(),
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


{#snippet scopeHead(what: string, count: number)}
  <div class="scope-head">
    <span class="crumb"><span class="dot"></span>{scopeLabel}</span> ▸ <strong>{what}</strong> <span class="count">{count}</span>
    {#if scope && scope !== MAIN_SCOPE}<span class="hint">what this option sees: the main flow, and what it changes</span>{/if}
    {#if !writable && !closed}<span class="hint">· read-only: the option is decided</span>{/if}
  </div>
{/snippet}

{#snippet producer(i: ChangeItem)}
  {#if i.execution}
    <button type="button" class="link" title={provenance(i)} onclick={() => openAudit(i.execution)}>{i.producedBy || shortId(i.execution)}</button>
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
  <ScopeBar changeId={ch.id ?? ''} {options} bind:scope {candidates} {mainImpacts} {closed} onchange={() => load(selected)} oncompare={() => (pane = 'compare')} />
  <EditorPanes {panes} bind:active={pane} label="Change sections">
    {#snippet children(active)}
      {#if active === 'overview'}
      <section class="card">
        <div class="editor-head">
          <Icon name="diff" size={18} />
          <h2>{ch.title || 'Untitled'}</h2>
          <StatusBadge status={ch.status} />
          <span class="grow"></span>
          {#if !closed && !defining}
            <button type="button" class="small" onclick={startDefine}>Edit</button>
            <button type="button" class="small danger" disabled={defBusy} onclick={abandon}>Abandon</button>
          {/if}
        </div>
        {#if defining}
          <form class="define" onsubmit={(e) => (e.preventDefault(), define({ title: defTitle.trim(), intent: defIntent.trim() }, 'Change updated.'))}>
            <label for="def-title">Title</label>
            <input id="def-title" type="text" bind:value={defTitle} />
            <label for="def-intent">Intent</label>
            <textarea id="def-intent" rows="2" bind:value={defIntent}></textarea>
            <div class="row">
              <button type="submit" class="primary small" disabled={defBusy || !defTitle.trim()}>Save</button>
              <button type="button" class="small" onclick={() => (defining = false)}>Cancel</button>
            </div>
          </form>
        {:else if ch.intent}<p class="intent">"{ch.intent}"</p>{/if}
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
          <dt>Audit</dt>
          <dd><button type="button" class="link" onclick={() => openAudit()}>Ticks, actions, model calls, decisions and impacts</button></dd>
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
            <p>Applied on its own branch; the merge into {mergeInto || 'the parent branch'} is pending: resolve the nodes changed on both sides.</p>
            {#if mergeInto}
              <MergeResolver namespace={namespaceOf(ch.namespace)} from={ch.branch ?? ''} into={mergeInto} onmerge={merge} />
            {/if}
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
              {nErr} error{nErr === 1 ? '' : 's'}, {nWarn} warning{nWarn === 1 ? '' : 's'} on {scopeLabel}
            </p>
            <BoardIssueList issues={boardIssues} sections />
          {/if}
        </div>

        {#if relaunches.length}
          <h3>Flow branches <span class="count">{relaunches.length}</span></h3>
          <FlowGraph {processes} changeId={selected} flows={relaunches} onopen={(pid) => openTab({ kind: 'run', params: { id: pid } })} />
          <ul class="subs flows">
            {#each relaunches as f (f.id)}
              {@const fp = flowProcess(f)}
              <li>
                <StatusBadge status={flowBadge(f)} />
                <code>{shortId(f.id)}</code>
                from step {flowStepNumber(f, processes)}
                {#if f.reason}<span class="muted">· {f.reason}</span>{/if}
                <span class="hint">· {f.stale?.length ?? 0} stale item(s)</span>
                {#if f.process}<button type="button" class="link mono" onclick={() => openTab({ kind: 'run', params: { id: f.process ?? '' } })}>previous run</button>{/if}
                {#if fp}<button type="button" class="link mono" onclick={() => openTab({ kind: 'run', params: { id: fp.id ?? '' } })}>relaunched run</button>{/if}
                <div class="flow-row"><FlowBranchInfo flow={f} namespace={namespaceOf(change?.namespace)} changeBranch={change?.branch ?? ''} /></div>
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
      {:else if active === 'compare'}
      <section class="card">
        <h3>Compare the options <span class="count">{options.length}</span></h3>
        <ChangeOptions changeId={ch.id ?? ''} {closed} onchange={() => load(selected)} onview={(id) => ((scope = id), (pane = 'impacts'))} />
      </section>
      {:else if active === 'decisions'}
      <section class="card">
        <h3>Decision points</h3>
        <ChangeDecisions changeId={ch.id ?? ''} {closed} onchange={() => load(selected)} />
      </section>
      {:else if active === 'impacts'}
      <div class="scoped" style="--scope: {scopeTint}">
      {@render scopeHead('Change impacts', lcRows.length)}
      <ChangeLifecycle
        impacts
        scope={scope || MAIN_SCOPE}
        rows={lcRows}
        candidates={lcCandidates}
        disabled={!writable}
        busy={moving}
        onmove={move}
        onedit={edit}
        types={typeNames}
        {lifecycleOf}
        keys={takenKeys}
        oncreate={createNode}
        onremove={removeNode}
        onundo={undoDelete}
        onreview={reviewRow}
        onhistory={(r) => openNode(r.node, { pin: true, generic: true, pane: 'history' })}
        onopennode={(r) => openNode({ id: r.impact?.post?.id ?? r.node.id ?? '', key: r.node.key ?? '' }, { pin: true, change: ch.id ?? '', flow: scope || MAIN_SCOPE })}
        onadd={addImpact}
      />
      </div>
      {:else if active === 'items'}
      <div class="scoped" style="--scope: {scopeTint}">
      {@render scopeHead('Items', items.length)}
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
      </div>
      {:else if active === 'audit'}
      <section class="card">
        <ChangeAudit change={ch} bind:process={auditProcess} bind:run={auditRun} onrun={(pid) => openTab({ kind: 'run', params: { id: pid } })} />
      </section>
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
  .scoped {
    border-left: 3px solid var(--scope);
    padding-left: 8px;
  }
  .scope-head {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
    margin: 4px 0 8px;
  }
  .scope-head .crumb {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    color: var(--scope);
    font-weight: 600;
  }
  .scope-head .dot {
    width: 9px;
    height: 9px;
    border-radius: 50%;
    background: var(--scope);
  }
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
  .define {
    display: flex;
    flex-direction: column;
    gap: 3px;
    margin: 0.6rem 0;
  }
  .define label {
    font-size: 0.8rem;
    color: var(--muted);
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
  .superseded td,
  .superseded h4 {
    text-decoration: line-through;
  }
  .md {
    font-size: 0.88rem;
    line-height: 1.55;
  }
</style>
