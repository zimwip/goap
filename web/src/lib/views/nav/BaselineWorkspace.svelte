<script lang="ts">
  // Baseline tool's workspace: replaces the editor area's tabbed view while the Baseline tool is selected
  // (registered as its `editorArea`, shell/EditorArea.svelte). Two levels, both driven by BaselineExplorer's
  // nav (the store baselineTool.svelte): the namespace's branches (ADR 0009, ADR 0032 — where each forked from,
  // its head, its status; open/merge/abandon) and, for whichever branch is selected, its change history
  // (BaselineBranchGraph). Drilling into a specific baseline's nodes still opens a 'baseline' tab.
  import { graph, errorMessage, formatDate, shortId, type Baseline, type Branch, type Resolution } from '../../api';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import MergeResolver from '../../components/MergeResolver.svelte';
  import BaselineBranchGraph from '../../components/BaselineBranchGraph.svelte';
  import Resizer from '../../shell/Resizer.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { notify } from '../../shell/workbench.svelte';
  import { refreshBaselines } from '../../stores/catalog.svelte';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import { isEphemeralBranch, MAIN_BRANCH } from '../../namespace';
  import { baselineTool, selectBranch } from '../../stores/baselineTool.svelte';
  import { loadRaw, save } from '../../shell/storage';

  const namespace = $derived(baselineTool.namespace);

  // side panel (Branches / Open / Merge): 1/3 of the workspace by default, resizable and then remembered
  const SIDE_KEY = 'goap.ide.baseline.sideWidth';
  let workspaceWidth = $state(0);
  let sideWidth = $state(typeof loadRaw(SIDE_KEY) === 'number' ? (loadRaw(SIDE_KEY) as number) : 0);
  const sideMax = $derived(Math.max(280, Math.min(720, Math.floor(workspaceWidth * 0.6))));
  const sideShown = $derived(Math.min(sideWidth || Math.round(workspaceWidth / 3) || 380, sideMax));
  function resizeSide(v: number) {
    sideWidth = v;
    save(SIDE_KEY, v);
  }

  let branches = $state<Branch[]>([]);
  let baselines = $state<Baseline[]>([]);
  let loading = $state(false);
  let error = $state('');
  let busy = $state('');
  let showInternal = $state(false);
  // new branch
  let name = $state('');
  let from = $state('');
  let description = $state('');
  let editingDescription = $state('');
  let descriptionDraft = $state('');
  // merge
  let mergeFrom = $state('');
  let mergeInto = $state('main');

  async function load() {
    if (!namespace) return;
    loading = true;
    try {
      const [br, bs] = await Promise.all([graph.listBranches(namespace), graph.listBaselines(namespace)]);
      branches = br.branches ?? [];
      if (!branches.some((b) => b.name === 'main')) branches = [{ name: 'main', namespace, status: 'open' }, ...branches];
      baselines = (bs.baselines ?? []).slice().reverse();
      if (!from) from = baselines[0]?.id ?? '';
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }
  $effect(() => {
    void namespace;
    void load();
  });

  const internal = (b: Branch) => !!b.name && isEphemeralBranch(b.name);
  const shown = $derived(branches.filter((b) => showInternal || !internal(b)));
  const open = $derived(branches.filter((b) => b.status === 'open'));
  const baselineName = (id: string | undefined) => (id ? (baselines.find((b) => b.id === id)?.name || shortId(id)) : '—');
  const openBaseline = (id: string | undefined) => id && openTab({ kind: 'baseline', params: { id, name: baselineName(id) } }, { pin: true });
  const branchOf = (id: string) => baselines.find((b) => b.id === id)?.branch || MAIN_BRANCH;
  /** the branch's fork point: anchors the graph even before it has a baseline of its own */
  const forkBaseline = $derived(branches.find((b) => b.name === baselineTool.branch)?.forkBaseline);

  let selectedHistory = $state<string | undefined>(undefined);

  /** a baseline of the selected branch is just highlighted here (the graph already is the browsing view: no tab);
   * one from another branch (a fork point or merge source) switches to that branch instead */
  function selectHistory(id: string) {
    selectedHistory = id;
    const br = branchOf(id);
    if (br !== baselineTool.branch) selectBranch(br);
  }

  async function act(label: string, fn: () => Promise<unknown>, done: string) {
    busy = label;
    error = '';
    try {
      await fn();
      await load();
      void refreshBaselines(namespace);
      notify(done, 'ok');
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }

  const create = () =>
    act('create', () => graph.createBranch({ namespace, name: name.trim(), fromBaseline: from, description: description.trim() || undefined }), `Branch ${name.trim()} opened.`).then(() => {
      name = '';
      description = '';
    });

  async function abandon(name: string) {
    if (!(await confirmDialog({ message: `Abandon the branch ${name}? It takes no change any more.`, danger: true }))) return;
    void act(`st:${name}`, () => graph.setBranchStatus(namespace, name, 'abandoned'), `Branch ${name} abandoned.`);
  }

  function startDescription(b: Branch) {
    editingDescription = b.name ?? '';
    descriptionDraft = b.description ?? '';
  }

  async function saveDescription(name: string) {
    const value = descriptionDraft.trim();
    editingDescription = '';
    await act(`desc:${name}`, () => graph.setBranchDescription(namespace, name, value), `Description of ${name} saved.`);
  }

  async function merge(resolutions: Record<string, Resolution>): Promise<boolean> {
    const r = await graph.mergeBranch({ namespace, from: mergeFrom, into: mergeInto, resolutions });
    await load();
    void refreshBaselines(namespace);
    notify(`${mergeFrom} merged into ${mergeInto}: baseline ${r.baseline?.name || shortId(r.baseline?.id)}.`, 'ok');
    return true;
  }
</script>

<div class="editor-page">
  <div class="editor-head">
    <Icon name="database" size={18} />
    <h2>Baseline · {namespace}</h2>
    <span class="grow"></span>
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}><Icon name="refresh" size={14} /></button>
  </div>
  {#if error}<div class="alert">{error}</div>{/if}

  <div class="workspace" bind:clientWidth={workspaceWidth}>
    <nav class="card history" aria-label="History">
      <h3>History <span class="hint">{baselineTool.branch}</span></h3>
      <BaselineBranchGraph {baselines} branch={baselineTool.branch} {forkBaseline} selected={selectedHistory} onselect={selectHistory} onopen={openBaseline} />
    </nav>

    <Resizer orientation="vertical" value={sideShown} min={280} max={sideMax} invert label="Branches panel width" onresize={resizeSide} />

    <div class="side" style:width="{sideShown}px">
      <section class="card">
        <div class="head">
          <h3>Branches <span class="count">{shown.length}</span></h3>
          <span class="grow"></span>
          <label class="check"><input type="checkbox" bind:checked={showInternal} /> change and option branches</label>
        </div>
        <div class="scroll">
          <table>
            <thead><tr><th>Branch</th><th>Description</th><th>Forked from</th><th>Head</th><th>Status</th><th></th></tr></thead>
            <tbody>
              {#each shown as b (b.name)}
                <tr class:sel={b.name === baselineTool.branch}>
                  <td><button type="button" class="link mono" onclick={() => selectBranch(b.name ?? MAIN_BRANCH)}>{b.name}</button>{#if b.origin}<span class="hint"> · {b.origin}</span>{/if}</td>
                  <td class="desc">
                    {#if editingDescription === b.name}
                      <input
                        type="text"
                        placeholder="What this branch is for…"
                        bind:value={descriptionDraft}
                        onblur={() => void saveDescription(b.name ?? '')}
                        onkeydown={(e) => {
                          if (e.key === 'Enter') void saveDescription(b.name ?? '');
                          if (e.key === 'Escape') editingDescription = '';
                        }}
                      />
                    {:else}
                      <button type="button" class="link desc-edit" onclick={() => startDescription(b)} title="Edit description">
                        {b.description || '—'}
                      </button>
                    {/if}
                  </td>
                  <td>{#if b.parent}<code>{b.parent}</code> at <button type="button" class="link" onclick={() => openBaseline(b.forkBaseline)}>{baselineName(b.forkBaseline)}</button>{:else}<span class="hint">—</span>{/if}</td>
                  <td>{#if b.head}<button type="button" class="link" onclick={() => openBaseline(b.head)}>{baselineName(b.head)}</button>{:else}<span class="hint">—</span>{/if}</td>
                  <td><StatusBadge status={b.status} />{#if b.createdAt}<span class="hint"> {formatDate(b.createdAt)}</span>{/if}</td>
                  <td class="act">
                    {#if b.name !== 'main' && !internal(b)}
                      {#if b.status === 'open'}
                        <button type="button" class="small" onclick={() => ((mergeFrom = b.name ?? ''), (mergeInto = b.parent || 'main'))}>Merge…</button>
                        <button type="button" class="small danger" disabled={!!busy} onclick={() => abandon(b.name ?? '')}>Abandon</button>
                      {:else if b.status === 'abandoned'}
                        <button type="button" class="small" disabled={!!busy} onclick={() => act(`st:${b.name}`, () => graph.setBranchStatus(namespace, b.name ?? '', 'open'), `Branch ${b.name} reopened.`)}>Reopen</button>
                      {/if}
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </section>

      <section class="card">
        <h3>Open a branch</h3>
        <form class="row" onsubmit={(e) => (e.preventDefault(), create())}>
          <input type="text" placeholder="name (no space)" bind:value={name} />
          <label for="br-from">from</label>
          <select id="br-from" bind:value={from}>
            {#each baselines as b (b.id)}<option value={b.id}>{b.name || shortId(b.id)} ({b.branch || 'main'})</option>{/each}
          </select>
          <input type="text" placeholder="description (optional)" bind:value={description} />
          <button type="submit" disabled={!!busy || !name.trim() || !from}>Open</button>
        </form>
        <p class="hint">A change lands on the branch named at its creation (New change → Lands on); merging the branch brings its changes into another.</p>
      </section>

      <section class="card">
        <h3>Merge a branch</h3>
        <div class="row">
          <select bind:value={mergeFrom} aria-label="Branch to merge">
            <option value="" disabled>— branch —</option>
            {#each open.filter((b) => b.name !== 'main') as b (b.name)}<option value={b.name}>{b.name}</option>{/each}
          </select>
          <span>into</span>
          <select bind:value={mergeInto} aria-label="Target branch">
            {#each open as b (b.name)}<option value={b.name}>{b.name}</option>{/each}
          </select>
        </div>
        {#if mergeFrom && mergeInto && mergeFrom !== mergeInto}
          {#key `${mergeFrom}>${mergeInto}`}
            <MergeResolver {namespace} from={mergeFrom} into={mergeInto} onmerge={merge} />
          {/key}
        {/if}
      </section>
    </div>
  </div>
</div>

<style>
  .editor-page {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }
  .workspace {
    flex: 1;
    min-height: 0;
    display: flex;
    align-items: stretch;
    gap: 0.8rem;
  }
  @media (max-width: 900px) {
    .workspace {
      flex-direction: column;
    }
    .workspace :global(.resizer) {
      display: none;
    }
    .side {
      width: auto !important;
    }
  }
  .history {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    min-height: 0;
  }
  .history h3 {
    margin: 0 0 0.2rem;
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }
  .side {
    flex: none;
    min-width: 260px;
    display: flex;
    flex-direction: column;
    gap: 0.8rem;
    min-height: 0;
    overflow-y: auto;
  }
  .side .scroll {
    overflow-x: auto;
  }
  tr.sel td {
    background: var(--accent-soft);
  }
  .head,
  .row {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
  }
  .row select,
  .row input {
    width: auto;
    min-width: 12rem;
  }
  .check {
    display: inline-flex;
    gap: 4px;
    align-items: center;
  }
  table {
    width: 100%;
  }
  .act {
    text-align: right;
    white-space: nowrap;
  }
  .desc {
    max-width: 16rem;
  }
  .desc input {
    width: 100%;
  }
  .desc-edit {
    color: inherit;
    text-align: left;
    white-space: normal;
  }
</style>
