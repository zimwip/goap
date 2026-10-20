<script lang="ts">
  // Baseline explorer: namespace → branches, open ones first. Selecting a branch opens its baseline tab
  // (BaselineTab.svelte), pinned. Branch management (open/merge/abandon/describe) is a right-click on the row, not
  // shown inline here or in the tab.
  import { isMeta } from '../../stores/session.svelte';
  import TreeRow from '../TreeRow.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { loadRaw, save } from '../../shell/storage';
  import { graph, errorMessage, formatDate, type Baseline, type Branch } from '../../api';
  import { changeOfBranch, isChangeBranch, shownBranches } from '../../branchFilter';
  import { activeTab, openTab } from '../../shell/tabs.svelte';
  import { MAIN_BRANCH } from '../../namespace';
  import { loadTypes as loadCatalog, typeCatalog } from '../../stores/types.svelte';
  import { baselineTool, selectNamespace, openBaseline, refreshBranches } from '../../stores/baselineTool.svelte';
  import { openContextMenu, type ContextMenuItem } from '../../shell/contextMenuState.svelte';
  import { openFieldDialog } from '../../shell/fieldDialogState.svelte';
  import { openMergeDialog } from '../../shell/mergeDialogState.svelte';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import { notify } from '../../shell/workbench.svelte';
  import Icon from '../../shell/Icon.svelte';
  import { exportGraph } from '../../graphExport';

  // Methodologies are nodes typed by the meta-domain methodology (ADR 0023), authored in their own editors and
  // explorer: not offered here by default.
  const CLOSED_KEY = 'goap.ide.baselines.showClosed';

  let namespaces = $state<string[]>([]);
  let branches = $state<Branch[]>([]);
  let counts = $state.raw<Record<string, number>>({});
  let baselinesById = $state.raw<Map<string, Baseline>>(new Map());
  let loading = $state(false);
  let error = $state('');
  let reload = $state(0);
  let showClosed = $state(!!loadRaw(CLOSED_KEY));

  void loadCatalog();
  /** namespaces holding nodes, plus the ones a domain declares (a namespace without a baseline can be started) */
  const choices = $derived([...new Set([...namespaces, ...typeCatalog.cat.namespaces().filter((n) => !isMeta(n))])].sort());

  $effect(() => {
    void reload;
    void baselineTool.reload; // a baseline created by anyone may open a namespace
    graph
      .listNamespaces()
      .then((r) => {
        namespaces = r.namespaces ?? [];
        const ns = baselineTool.namespace;
        if (!ns || !(namespaces.includes(ns) || typeCatalog.cat.namespaces().includes(ns))) selectNamespace(namespaces.find((n) => !isMeta(n)) ?? namespaces[0] ?? '');
      })
      .catch((e) => (error = errorMessage(e)));
  });

  /** the baseline tab shown, if any: the explorer follows its namespace and marks its branch */
  const shownTab = $derived.by(() => {
    const t = activeTab();
    return t?.kind === 'baseline' ? t : undefined;
  });
  $effect(() => {
    const ns = shownTab?.params.namespace;
    if (ns && ns !== baselineTool.namespace) selectNamespace(ns);
  });
  const activeBranch = $derived(shownTab && shownTab.params.namespace === baselineTool.namespace ? shownTab.params.branch || MAIN_BRANCH : '');

  const shown = $derived(shownBranches(branches, showClosed, baselinesById.values()));

  $effect(() => {
    void reload;
    void baselineTool.reload;
    const ns = baselineTool.namespace;
    if (!ns) return;
    const ctrl = new AbortController();
    loading = true;
    error = '';
    // the branches with their heads and counts, the merged and abandoned ones only when asked: never every baseline of
    // the namespace (ADR 0140)
    graph
      .listBranches(ns, ctrl.signal, showClosed ? {} : { status: ['open'] })
      .then((br) => {
        let bl = br.branches ?? [];
        if (!bl.some((b) => b.name === 'main')) bl = [{ name: 'main', namespace: ns, status: 'open' }, ...bl];
        branches = bl;
        baselinesById = new Map((br.heads ?? []).map((b) => [b.id ?? '', b]));
        counts = br.counts ?? {};
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) error = errorMessage(e);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) loading = false;
      });
    return () => ctrl.abort();
  });

  const headOf = (b: Branch) => (b.head ? baselinesById.get(b.head) : undefined);
  const countOf = (b: Branch) => counts[b.name || 'main'] ?? 0;

  async function abandon(name: string) {
    if (!(await confirmDialog({ message: `Abandon the branch ${name}? It takes no change any more.`, danger: true }))) return;
    try {
      await graph.setBranchStatus(baselineTool.namespace, name, 'abandoned');
      notify(`Branch ${name} abandoned.`, 'ok');
      refreshBranches();
    } catch (e) {
      error = errorMessage(e);
    }
  }

  async function setStatus(name: string, status: 'open' | 'abandoned') {
    try {
      await graph.setBranchStatus(baselineTool.namespace, name, status);
      notify(`Branch ${name} ${status === 'open' ? 'reopened' : status}.`, 'ok');
      refreshBranches();
    } catch (e) {
      error = errorMessage(e);
    }
  }

  function editDescription(b: Branch) {
    openFieldDialog({
      title: `Description of ${b.name}`,
      submitLabel: 'Save',
      fields: [{ key: 'description', label: 'Description', type: 'textarea', default: b.description ?? '' }],
      onsubmit: async (v) => {
        await graph.setBranchDescription(baselineTool.namespace, b.name ?? '', v.description.trim());
        notify(`Description of ${b.name} saved.`, 'ok');
        refreshBranches();
      },
    });
  }

  function openFrom(b: Branch) {
    const head = headOf(b);
    openFieldDialog({
      title: `Open a branch from ${b.name}`,
      submitLabel: 'Open',
      fields: [
        { key: 'name', label: 'Name', required: true, placeholder: 'no spaces' },
        { key: 'description', label: 'Description' },
      ],
      onsubmit: async (v) => {
        await graph.createBranch({ namespace: baselineTool.namespace, name: v.name.trim(), fromBaseline: head?.id ?? '', description: v.description?.trim() || undefined });
        notify(`Branch ${v.name.trim()} opened.`, 'ok');
        refreshBranches();
      },
    });
  }

  /** Exports the head of a branch of the namespace without history: a JSON snapshot (ADR 0104) or an OKF zip, one
   *  Markdown file per node (ADR 0111); a job (ADR 0149), saved when done. */
  async function exportSnapshot(branch: string, format: 'json' | 'okf' = 'json') {
    const ns = baselineTool.namespace;
    if (!ns) return;
    try {
      await exportGraph({ namespace: ns, branch, format });
    } catch (e) {
      error = errorMessage(e);
    }
  }

  function menu(e: MouseEvent, b: Branch) {
    const items: ContextMenuItem[] = [
      { label: 'Open branch from here…', icon: 'branch', disabled: !headOf(b)?.id, run: () => openFrom(b) },
      { label: 'Export head (JSON snapshot)', icon: 'download', disabled: !headOf(b)?.id, run: () => exportSnapshot(b.name ?? 'main') },
      { label: 'Export head (OKF Markdown)', icon: 'download', disabled: !headOf(b)?.id, run: () => exportSnapshot(b.name ?? 'main', 'okf') },
    ];
    if (isChangeBranch(b)) {
      // a change's branch is merged by its change (IntegrateChange), never by hand
      const change = changeOfBranch(b);
      if (change) items.push({ label: 'Open change', icon: 'branch', run: () => openTab({ kind: 'change', params: { id: change } }, { pin: true }) });
    } else if (b.name !== 'main') {
      if (b.status === 'open') {
        for (const target of branches.filter((x) => x.status === 'open' && x.name !== b.name))
          items.push({ label: `Merge into ${target.name}`, icon: 'branch', run: () => openMergeDialog(baselineTool.namespace, b.name ?? '', target.name ?? '') });
        items.push({ label: 'Abandon', icon: 'trash', danger: true, run: () => abandon(b.name ?? '') });
      } else {
        items.push({ label: 'Reopen', icon: 'refresh', run: () => setStatus(b.name ?? '', 'open') });
      }
      items.push({ label: 'Edit description…', run: () => editDescription(b) });
    }
    openContextMenu(e, items);
  }
</script>

<div class="explorer">
  <div class="tools">
    <select aria-label="Namespace" title="Namespace" value={baselineTool.namespace} onchange={(e) => selectNamespace(e.currentTarget.value)} data-no-pin>
      {#if baselineTool.namespace && !namespaces.includes(baselineTool.namespace)}<option value={baselineTool.namespace}>{baselineTool.namespace}</option>{/if}
      {#each choices as ns (ns)}<option value={ns}>{ns}</option>{/each}
    </select>
    <button
      type="button"
      class="ghost small"
      title="Export the head of {activeBranch || 'main'} as a JSON snapshot (no history)"
      aria-label="Export graph"
      disabled={!baselineTool.namespace}
      onclick={() => exportSnapshot(activeBranch || 'main')}><Icon name="download" size={14} /></button
    >
    <button
      type="button"
      class="ghost small"
      title="Import a graph snapshot (JSON) as a new change"
      aria-label="Import graph"
      onclick={() => openTab({ kind: 'import', params: { what: 'graph' } }, { pin: true })}><Icon name="upload" size={14} /></button
    >
    <button
      type="button"
      class="ghost small"
      title="Import from a source the domain of {baselineTool.namespace || 'the namespace'} declares (a git repository for code)"
      aria-label="Import from a source"
      disabled={!baselineTool.namespace}
      onclick={() => openTab({ kind: 'sources', params: { ns: baselineTool.namespace } }, { pin: true })}><Icon name="database" size={14} /></button
    >
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={loading} onclick={() => reload++}
      ><Icon name="refresh" size={14} /></button
    >
  </div>
  <div class="tools">
    <label class="check"><input type="checkbox" bind:checked={showClosed} onchange={() => save(CLOSED_KEY, showClosed)} /> merged and abandoned</label>
  </div>
  {#if error}<div class="alert small">{error}</div>{/if}
  {#if !loading && !error && baselineTool.namespace && !branches.length}
    <div class="empty pad">
      No baselines in {baselineTool.namespace} yet: the first change that commits creates one.
    </div>
  {/if}
  {#if !baselineTool.namespace && !error}<p class="empty pad">The graph holds no nodes yet.</p>{/if}
  <div role="tree" aria-label="Branches">
    {#each shown as b (b.name)}
      {@const head = headOf(b)}
      <TreeRow
        icon="branch"
        label={b.name || ''}
        detail={head ? formatDate(head.createdAt) : '—'}
        active={activeBranch === (b.name ?? '')}
        onselect={() => openBaseline(baselineTool.namespace, b.name ?? '')}
        oncontextmenu={(e) => menu(e, b)}
      >
        {#snippet trail()}
          <StatusBadge status={b.status} />
          <span class="hint count">{countOf(b)}</span>
        {/snippet}
      </TreeRow>
    {/each}
    {#if baselineTool.namespace && !loading && !shown.length && branches.length}
      <p class="empty pad">No open branches. <button type="button" class="link small" onclick={() => (showClosed = true)}>Show merged and abandoned.</button></p>
    {/if}
  </div>
</div>

<style>
  .explorer {
    padding-bottom: 1rem;
  }
  .tools {
    display: flex;
    gap: 2px;
    padding: 0 0.5rem 0.4rem;
    position: sticky;
    top: 0;
    background: var(--chrome);
    z-index: 1;
    align-items: center;
  }
  .tools select {
    min-height: 24px;
    height: 24px;
    margin-right: 0.2rem;
    flex: 1;
    min-width: 0;
  }
  .tools button {
    padding: 0.1rem 0.3rem;
  }
  .check {
    display: inline-flex;
    gap: 4px;
    align-items: center;
    font-size: 0.85em;
    color: var(--muted);
  }
  .pad {
    padding: 0.4rem 0.8rem;
  }
  .count {
    font-variant-numeric: tabular-nums;
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
</style>
