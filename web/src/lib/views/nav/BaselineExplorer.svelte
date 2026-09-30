<script lang="ts">
  // Baseline explorer: namespace → branches, open ones first. Selecting a branch drives the Baseline tool's
  // workspace (BaselineWorkspace.svelte) directly, in the editor area. Branch management (open/merge/abandon/
  // describe) is a right-click on the row, not shown inline here or in the workspace.
  import TreeRow from '../TreeRow.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { loadRaw, save } from '../../shell/storage';
  import { graph, errorMessage, formatDate, type Baseline, type Branch } from '../../api';
  import { isEphemeralBranch } from '../../namespace';
  import { loadTypes as loadCatalog, typeCatalog } from '../../stores/types.svelte';
  import { refreshBaselines } from '../../stores/catalog.svelte';
  import { baselineTool, selectNamespace, selectBranch, refreshBranches } from '../../stores/baselineTool.svelte';
  import { openContextMenu, type ContextMenuItem } from '../../shell/contextMenuState.svelte';
  import { openFieldDialog } from '../../shell/fieldDialogState.svelte';
  import { openMergeDialog } from '../../shell/mergeDialogState.svelte';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import { notify } from '../../shell/workbench.svelte';
  import Icon from '../../shell/Icon.svelte';

  // Methodologies are nodes typed by the meta-domain methodology (ADR 0023), authored in their own editors and
  // explorer: not offered here by default.
  const META_NAMESPACES = new Set(['methodology']);
  const CLOSED_KEY = 'goap.ide.baselines.showClosed';

  let namespaces = $state<string[]>([]);
  let branches = $state<Branch[]>([]);
  let baselinesByBranch = $state.raw<Map<string, Baseline[]>>(new Map());
  let baselinesById = $state.raw<Map<string, Baseline>>(new Map());
  let loading = $state(false);
  let error = $state('');
  let reload = $state(0);
  let starting = $state(false);
  let showClosed = $state(!!loadRaw(CLOSED_KEY));

  void loadCatalog();
  /** namespaces holding nodes, plus the ones a domain declares (a namespace without a baseline can be started) */
  const choices = $derived([...new Set([...namespaces, ...typeCatalog.cat.namespaces().filter((n) => !META_NAMESPACES.has(n))])].sort());

  /** Starts a namespace: an empty baseline on main, that changes then fill. */
  async function startNamespace() {
    starting = true;
    error = '';
    try {
      await graph.createBaseline(baselineTool.namespace, `start ${baselineTool.namespace}`);
      void refreshBaselines(baselineTool.namespace);
      reload++;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      starting = false;
    }
  }

  $effect(() => {
    void reload;
    graph
      .listNamespaces()
      .then((r) => {
        namespaces = r.namespaces ?? [];
        const ns = baselineTool.namespace;
        if (!ns || !(namespaces.includes(ns) || typeCatalog.cat.namespaces().includes(ns))) selectNamespace(namespaces.find((n) => !META_NAMESPACES.has(n)) ?? namespaces[0] ?? '');
      })
      .catch((e) => (error = errorMessage(e)));
  });

  const internal = (b: Branch) => !!b.name && isEphemeralBranch(b.name);
  const rank = (b: Branch) => (b.status === 'open' ? 0 : b.status === 'merged' ? 1 : 2);
  const shown = $derived(
    [...branches]
      .filter((b) => !internal(b) && (showClosed || b.status === 'open'))
      .sort((a, b) => rank(a) - rank(b) || (a.name === 'main' ? -1 : b.name === 'main' ? 1 : (a.name ?? '').localeCompare(b.name ?? ''))),
  );

  $effect(() => {
    void reload;
    void baselineTool.reload;
    const ns = baselineTool.namespace;
    if (!ns) return;
    const ctrl = new AbortController();
    loading = true;
    error = '';
    Promise.all([graph.listBranches(ns, ctrl.signal), graph.listBaselines(ns, ctrl.signal)])
      .then(([br, bs]) => {
        let bl = br.branches ?? [];
        if (!bl.some((b) => b.name === 'main')) bl = [{ name: 'main', namespace: ns, status: 'open' }, ...bl];
        branches = bl;
        const byBranch = new Map<string, Baseline[]>();
        const byId = new Map<string, Baseline>();
        for (const b of bs.baselines ?? []) {
          const k = b.branch || 'main';
          byBranch.set(k, [...(byBranch.get(k) ?? []), b]);
          if (b.id) byId.set(b.id, b);
        }
        for (const list of byBranch.values()) list.sort((a, b) => (b.createdAt ?? '').localeCompare(a.createdAt ?? ''));
        baselinesByBranch = byBranch;
        baselinesById = byId;
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
  const countOf = (b: Branch) => baselinesByBranch.get(b.name ?? '')?.length ?? 0;

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

  function menu(e: MouseEvent, b: Branch) {
    const items: ContextMenuItem[] = [{ label: 'Open branch from here…', icon: 'branch', disabled: !headOf(b)?.id, run: () => openFrom(b) }];
    if (b.name !== 'main' && !internal(b)) {
      if (b.status === 'open') {
        for (const target of branches.filter((x) => x.status === 'open' && x.name !== b.name))
          items.push({ label: `Merge into ${target.name}`, icon: 'branch', run: () => openMergeDialog(baselineTool.namespace, b.name ?? '', target.name ?? '') });
        items.push({ label: 'Abandon', icon: 'trash', danger: true, run: () => abandon(b.name ?? '') });
      } else if (b.status === 'abandoned') {
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
      No baselines in {baselineTool.namespace}.
      <button type="button" class="small" disabled={starting} onclick={startNamespace} title="An empty baseline on main: changes then create its nodes">Start {baselineTool.namespace}</button>
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
        active={baselineTool.branch === (b.name ?? '')}
        onselect={() => selectBranch(b.name ?? '')}
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
