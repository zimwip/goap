<script lang="ts">
  // Commit tab: the history of one branch of a namespace (git-log-style, CommitBranchGraph) on the left; node
  // types, a filtered/paged node list, the detail of the node or link selected (properties, links; its
  // neighbourhood graph in a modal, on demand) and CommitCompare, all for whichever commit is shown: the branch's
  // head, or a specific row clicked in the history graph (`commit` param), of this branch or another one (a row's
  // context menu opens the tab of its branch). One tab per branch, opened pinned from HistoryExplorer
  // (stores/commitTool.svelte) or a link elsewhere. Branch management (open/merge/abandon/describe) is a right-click
  // on the branch's row in HistoryExplorer, not shown here.
  import {
    graph,
    errorMessage,
    formatDate,
    nodeTitle,
    shortId,
    type Commit,
    type Branch,
    type GraphNode,
    type Link,
    type TypeCount,
  } from '../../api';
  import { exportGraph } from '../../graphExport';
  import Icon from '../../shell/Icon.svelte';
  import { emptyNeighbourhood, type Neighbourhood } from '../../components/NodeNeighbourhood.svelte';
  import NeighbourhoodDialog from '../../components/NeighbourhoodDialog.svelte';
  import ChangeStackView from '../../components/ChangeStackView.svelte';
  import { branchView } from '../../graphIndex';
  import CommitCompare from '../../components/CommitCompare.svelte';
  import CommitBranchGraph from '../../components/CommitBranchGraph.svelte';
  import { branchLabel, isOpenHead } from '../../commitHistory';
  import { stamp, keyOf } from '../../flux/signals.svelte';
  import Resizer from '../../shell/Resizer.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { openNode } from '../../nodeEditors';
  import { splitType, typeCatalog } from '../../stores/types.svelte';
  import { MAIN_BRANCH } from '../../namespace';
  import { commitTool, openCommit } from '../../stores/commitTool.svelte';
  import type { Tab } from '../../shell/types';
  import { loadRaw, save } from '../../shell/storage';
  import { openContextMenu, type ContextMenuItem } from '../../shell/contextMenuState.svelte';

  const SIZES = [25, 50, 100, 200];
  const HW_KEY = 'goap.ide.commit.historyWidth';
  const LANES_KEY = 'goap.ide.commit.historyLanes';
  const CH_KEY = 'goap.ide.commit.compareHeight';

  let { tab }: { tab: Tab } = $props();

  const namespace = $derived(tab.params.namespace ?? '');
  const branchName = $derived(tab.params.branch || MAIN_BRANCH);
  let historyWidth = $state(typeof loadRaw(HW_KEY) === 'number' ? (loadRaw(HW_KEY) as number) : 320);
  /** the text the history's rows are matched against (the others dimmed), and whether the changes' lanes show */
  let historyFilter = $state('');
  let lanes = $state(loadRaw(LANES_KEY) !== false);
  function toggleLanes() {
    lanes = !lanes;
    save(LANES_KEY, lanes);
  }
  /** the commit the shown one is compared with (CommitCompare: its parent unless picked from the history) */
  let compareFrom = $state('');
  function resizeHistory(v: number) {
    historyWidth = v;
    save(HW_KEY, v);
  }

  // Compare panel: a third of the page by default, resizable up to all but the page's head and then remembered;
  // enlarged to the whole page or shrunk to its head, and back, from its own buttons
  let pageHeight = $state(0);
  let compareHeightSet = $state(typeof loadRaw(CH_KEY) === 'number' ? (loadRaw(CH_KEY) as number) : 0);
  const compareMax = $derived(Math.max(120, pageHeight - 160));
  const compareShown = $derived(Math.min(compareHeightSet || Math.round(pageHeight / 3) || 220, compareMax));
  /** normal: its height; max: the whole page below the head; min: its head only */
  let compareSize = $state<'normal' | 'max' | 'min'>('normal');
  function resizeCompare(v: number) {
    compareHeightSet = v;
    compareSize = 'normal';
    save(CH_KEY, v);
  }
  const toggleCompare = (to: 'max' | 'min') => (compareSize = compareSize === to ? 'normal' : to);

  let reload = $state(0);
  let loading = $state(false);
  let error = $state('');
  let branches = $state<Branch[]>([]);
  /** the history of the branch read so far, a page at a time (ADR 0140), and what draws it */
  let history = $state<Commit[]>([]);
  let related = $state<Commit[]>([]);
  let next = $state('');
  let older = $state(false);
  const commits = $derived.by(() => {
    const byId = new Map<string, Commit>();
    for (const b of [...history, ...related]) if (b.id && !byId.has(b.id)) byId.set(b.id, b);
    return [...byId.values()];
  });
  const PAGE = 100;

  // A namespace switch fires a new fetch before the previous one may have resolved: abort the stale one so its
  // (possibly later-arriving) response can't overwrite the current namespace's data.
  $effect(() => {
    const ns = namespace;
    const branch = branchName;
    void reload;
    void commitTool.reload;
    // a commit, a merge, a branch opened by a change (its first edit, ADR 0102): the lanes follow
    void stamp(keyOf.commits);
    void stamp(keyOf.changes);
    if (!ns) return;
    const ctrl = new AbortController();
    loading = true;
    // the newest page of the branch's history: never every commit of the namespace (ADR 0140)
    // the open branches draw their lanes; the branch shown is read too when it is merged or abandoned
    Promise.all([
      graph.listBranches(ns, ctrl.signal, { status: ['open'] }),
      graph.listCommits(ns, ctrl.signal, { branch, limit: PAGE }),
      graph.getBranch(ns, branch, ctrl.signal).catch(() => undefined),
    ])
      .then(([br, bs, shown]) => {
        branches = br.branches ?? [];
        if (shown?.branch && !branches.some((b) => b.name === shown.branch?.name)) branches = [...branches, shown.branch];
        history = bs.commits ?? [];
        related = bs.related ?? [];
        next = bs.next && bs.next !== '0' ? bs.next : '';
        error = '';
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) error = errorMessage(e);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) loading = false;
      });
    return () => ctrl.abort();
  });

  /** Reads the older page of the branch's history. */
  async function loadOlder() {
    const ns = namespace;
    if (!ns || !next || older) return;
    older = true;
    try {
      const r = await graph.listCommits(ns, undefined, { branch: branchName, before: next, limit: PAGE });
      history = [...(r.commits ?? []), ...history];
      related = [...related, ...(r.related ?? [])];
      next = r.next && r.next !== '0' ? r.next : '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      older = false;
    }
  }

  const branchOf = (b: Commit) => b.branch || MAIN_BRANCH;
  const own = $derived(commits.filter((b) => branchOf(b) === branchName));
  const headId = $derived([...own].sort((a, b) => (b.createdAt ?? '').localeCompare(a.createdAt ?? ''))[0]?.id ?? '');
  /** the commit shown in the panels below: whichever row was clicked in the history graph, or the branch's head */
  const shownId = $derived(tab.params.commit || headId);
  /** the branch of the commit shown (a row of a forked lane is of its own branch): a read names it (ADR 0157) */
  const shownBranch = $derived(commits.find((b) => b.id === shownId)?.branch || branchName);
  /** the branch's fork point: anchors the graph even before it has a commit of its own */
  const forkCommit = $derived(branches.find((b) => b.name === branchName)?.forkCommit);

  /** any row is shown in this tab, whatever its branch (a fork point, a change's commit, a merge source): the reads
   *  name the branch of the commit shown; the open head of a branch with no commit shows the commit it forked from.
   *  Opening the tab of another branch is an explicit choice, from the row's context menu. */
  function selectHistory(id: string) {
    if (isOpenHead(id)) {
      const fork = branches.find((b) => b.name === id.slice('open:'.length))?.forkCommit;
      if (fork) tab.params.commit = fork;
      return;
    }
    tab.params.commit = id;
  }

  function historyMenu(id: string, e: MouseEvent) {
    const open = isOpenHead(id);
    const c = commits.find((b) => b.id === id);
    const br = open ? id.slice('open:'.length) : branchOf(c ?? {});
    const changeId = c?.changeId || (open ? branches.find((b) => b.name === br)?.origin?.replace(/^change:/, '') : '');
    const items: ContextMenuItem[] = [{ label: open ? 'Show its fork point here' : 'Show here', icon: 'database', run: () => selectHistory(id) }];
    if (br !== branchName) items.push({ label: `Open in its branch (${branchLabel(br)})`, icon: 'branch', run: () => openCommit(namespace, br, open ? undefined : id) });
    if (changeId) items.push({ label: 'Open change', icon: 'external', run: () => openTab({ kind: 'change', params: { id: changeId } }, { pin: true }) });
    if (!open && id !== shownId) items.push({ label: 'Compare with the commit shown', icon: 'diff', run: () => (compareFrom = id) });
    if (!open) items.push({ label: 'Copy commit id', icon: 'copy', run: () => void navigator.clipboard?.writeText(id) });
    openContextMenu(e, items);
  }

  /** Exports the state of the shown commit, without history: a JSON snapshot (ADR 0104) or an OKF zip, one Markdown
   *  file per node with a YAML front matter (ADR 0111); a job (ADR 0149), saved when done. */
  let exporting = $state(false);
  async function exportCommit(format: 'json' | 'okf') {
    const id = shownId;
    if (!namespace || !id || isOpenHead(id)) return;
    exporting = true;
    try {
      await exportGraph({ namespace, commitId: id, format });
    } catch (e) {
      error = errorMessage(e);
    } finally {
      exporting = false;
    }
  }

  // ---- node types + filtered/paged node list, for `shownId` --------------------------------------

  let commit = $state<Commit | undefined>();
  let types = $state<TypeCount[]>([]);
  let nodes = $state<GraphNode[]>([]);
  let total = $state(0);
  let nodesLoading = $state(false);
  let type = $state('');
  let selectedNode = $state('');
  let filter = $state('');
  let query = $state('');
  let offset = $state(0);
  let size = $state(50);

  // switching commit (branch or a specific history row) starts the node browser fresh
  $effect(() => {
    void shownId;
    type = '';
    selectedNode = '';
    filter = '';
    query = '';
    offset = 0;
    selectedLink = undefined;
  });

  let debounce: ReturnType<typeof setTimeout> | undefined;
  function onFilter(v: string) {
    filter = v;
    clearTimeout(debounce);
    debounce = setTimeout(() => {
      query = filter.trim();
      offset = 0;
    }, 250);
  }

  function pickType(t: string) {
    type = t;
    offset = 0;
  }

  $effect(() => {
    void reload;
    const req = { view: { namespace, branch: shownBranch, commitId: shownId }, type, query, offset, limit: size };
    if (!shownId) {
      commit = undefined;
      types = [];
      nodes = [];
      total = 0;
      return;
    }
    const ctrl = new AbortController();
    nodesLoading = true;
    graph
      .listCommitNodes(req, ctrl.signal)
      .then((r) => {
        commit = r.commit;
        types = r.types ?? [];
        nodes = r.nodes ?? [];
        total = r.total ?? 0;
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) error = errorMessage(e);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) nodesLoading = false;
      });
    return () => ctrl.abort();
  });

  const typeTotal = $derived(types.reduce((s, t) => s + (t.count ?? 0), 0));
  const pageCount = $derived(Math.max(1, Math.ceil(total / size)));
  const page = $derived(Math.floor(offset / size) + 1);
  const goto = (p: number) => (offset = (Math.min(Math.max(p, 1), pageCount) - 1) * size);

  // ---- All Nodes / All Links tab ----------------------------------------------------------------

  let browseTab = $state<'nodes' | 'links'>('nodes');
  let linkType = $state('');
  let linkTypes = $state<TypeCount[]>([]);
  let allLinks = $state<Link[]>([]);
  let linkTotal = $state(0);
  let linksLoading = $state(false);
  let linkFilter = $state('');
  let linkQuery = $state('');
  let linkOffset = $state(0);
  let linkSize = $state(50);

  $effect(() => {
    void shownId;
    linkType = '';
    linkFilter = '';
    linkQuery = '';
    linkOffset = 0;
  });

  let linkDebounce: ReturnType<typeof setTimeout> | undefined;
  function onLinkFilter(v: string) {
    linkFilter = v;
    clearTimeout(linkDebounce);
    linkDebounce = setTimeout(() => {
      linkQuery = linkFilter.trim();
      linkOffset = 0;
    }, 250);
  }

  function pickLinkType(t: string) {
    linkType = t;
    linkOffset = 0;
  }

  // browsing the whole commit's links, a page at a time
  $effect(() => {
    void reload;
    if (browseTab !== 'links') return;
    const req = { commitId: shownId, type: linkType, query: linkQuery, offset: linkOffset, limit: linkSize };
    if (!req.commitId) {
      linkTypes = [];
      allLinks = [];
      linkTotal = 0;
      return;
    }
    const ctrl = new AbortController();
    linksLoading = true;
    graph
      .listCommitLinks(req, ctrl.signal)
      .then((r) => {
        linkTypes = r.types ?? [];
        allLinks = r.links ?? [];
        linkTotal = r.total ?? 0;
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) error = errorMessage(e);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) linksLoading = false;
      });
    return () => ctrl.abort();
  });

  const linkTypeTotal = $derived(linkTypes.reduce((s, t) => s + (t.count ?? 0), 0));
  const linkPageCount = $derived(Math.max(1, Math.ceil(linkTotal / linkSize)));
  const linkPage = $derived(Math.floor(linkOffset / linkSize) + 1);
  const gotoLink = (p: number) => (linkOffset = (Math.min(Math.max(p, 1), linkPageCount) - 1) * linkSize);

  // ---- the selected node: its detail on the right (properties, links), its neighbourhood in a modal ------

  let hood = $state<Neighbourhood>(emptyNeighbourhood());
  /** the neighbourhood graph is open (a modal, on demand) */
  let showHood = $state(false);
  /** the node version whose change-stack is open (ADR 0133) */
  let stackOf = $state('');

  $effect(() => {
    void reload;
    const id = selectedNode;
    const view = branchView(namespace, shownId, shownBranch);
    if (!id || !shownId) {
      hood = emptyNeighbourhood();
      return;
    }
    hood = { ...emptyNeighbourhood(), loading: true };
    const ctrl = new AbortController();
    graph
      .getNodeNeighbourhood(view, id, ctrl.signal)
      .then((r) => {
        hood = { center: r.node, neighbours: r.nodes ?? [], links: r.links ?? [], suspect: new Set(r.suspectLinkIds ?? []), loading: false, error: '' };
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) hood = { ...emptyNeighbourhood(), error: errorMessage(e) };
      });
    return () => ctrl.abort();
  });
  const center = $derived(hood.center);
  const neighbours = $derived(hood.neighbours);
  const links = $derived(hood.links);
  const suspect = $derived(hood.suspect);
  const nbLoading = $derived(hood.loading);
  const nbError = $derived(hood.error);

  /** The neighbours with the links that reach them, incoming first. */
  const rows = $derived.by(() => {
    const byId = new Map(neighbours.map((n) => [n.id ?? '', n]));
    return links
      .map((l) => {
        const outgoing = l.from?.id === selectedNode;
        const other = byId.get((outgoing ? l.to?.id : l.from?.id) ?? '');
        return { l, outgoing, other, suspect: suspect.has(l.id ?? '') };
      })
      .sort((a, b) => Number(a.outgoing) - Number(b.outgoing) || (a.other?.key ?? '').localeCompare(b.other?.key ?? ''));
  });

  // the commit holds the nodes of its namespace (its header is sent without its nodes, ADR 0140)
  const inCommit = (n: GraphNode | undefined) => !!n?.id && !!commit && (n.namespace ?? '') === (commit.namespace ?? '');

  function selectNode(n: GraphNode | undefined) {
    if (!n?.id) return;
    selectedLink = undefined;
    // a neighbour the commit does not hold (another namespace): open it in its editor
    if (!inCommit(n)) {
      void openNode(n, { pin: true });
      return;
    }
    selectedNode = n.id;
  }

  let selectedLink = $state<Link | undefined>();

  /** a link of the commit: its detail on the right, in place of the node's */
  function selectLink(l: Link | undefined) {
    if (!l?.id) return;
    selectedNode = '';
    selectedLink = l;
  }

  /** an end of the link shown: that node's detail */
  function selectEnd(id: string | undefined) {
    if (!id) return;
    selectedLink = undefined;
    selectedNode = id;
  }

  const propRows = (n: GraphNode | Link | undefined) =>
    Object.entries(n?.props ?? {}).map(([k, v]): [string, string] => [k, typeof v === 'string' ? v : JSON.stringify(v)]);
</script>

<div class="editor-page" bind:clientHeight={pageHeight}>
  <div class="editor-head">
    <Icon name="database" size={18} />
    <h2>Commit · {namespace}</h2>
    {#if commit}
      <span class="hint">
        <code>{shortId(commit.id)}</code>
        {#if shownBranch !== branchName} · on <strong title={shownBranch}>{branchLabel(shownBranch)}</strong>{/if}
        {#if tab.params.commit && tab.params.commit !== headId} · <button type="button" class="link" title="Show the head of {branchName}" onclick={() => (tab.params.commit = '')}>back to the head</button>{/if}
        {#if commit.createdAt} · {formatDate(commit.createdAt)}{/if}
        {#if commit.changeId} · from change <button type="button" class="link mono" onclick={() => openTab({ kind: 'change', params: { id: commit?.changeId ?? '' } })}>{commit.changeId.slice(0, 8)}</button>{/if}
        {#if !query} · {typeTotal} nodes{/if}
      </span>
    {/if}
    <span class="grow"></span>
    <button
      type="button"
      class="ghost small"
      title="Export this commit as an OKF tree: a zip of one Markdown file per node (YAML front matter, Markdown body)"
      disabled={!commit || exporting}
      onclick={() => exportCommit('okf')}><Icon name="download" size={14} /> OKF</button
    >
    <button
      type="button"
      class="ghost small"
      title="Export this commit as a JSON snapshot (no history)"
      disabled={!commit || exporting}
      onclick={() => exportCommit('json')}><Icon name="download" size={14} /> JSON</button
    >
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={loading} onclick={() => reload++}><Icon name="refresh" size={14} /></button>
  </div>
  {#if error}<div class="alert">{error}</div>{/if}

  <div class="workspace" hidden={!!commit && compareSize === 'max'}>
    <nav class="card history" style:width="{historyWidth}px" aria-label="History">
      <h3>History <span class="hint">{branchName}</span></h3>
      <div class="htools">
        <input type="search" placeholder="Filter title, change, date…" aria-label="Filter the history" bind:value={historyFilter} data-no-pin />
        <label class="hint" title="Show the branch of each change that committed here and the open branches forked from it"><input type="checkbox" checked={lanes} onchange={toggleLanes} /> Changes</label>
      </div>
      <CommitBranchGraph {commits} {branches} branch={branchName} {forkCommit} selected={shownId} filter={historyFilter} {lanes} onselect={selectHistory} onopen={selectHistory} oncontextmenu={historyMenu} />
      {#if next}<button type="button" class="link small older" disabled={older} onclick={loadOlder}>{older ? 'Loading…' : 'Older commits'}</button>{/if}
    </nav>

    <Resizer orientation="vertical" value={historyWidth} min={240} max={520} label="History panel width" onresize={resizeHistory} />

    <div class="browse">
      <div class="node-panel">
          <nav class="card types" aria-label={browseTab === 'nodes' ? 'Node types' : 'Link types'}>
            <div class="tabs" role="group" aria-label="Browse">
              <button type="button" class="small" class:primary={browseTab === 'nodes'} aria-pressed={browseTab === 'nodes'} onclick={() => (browseTab = 'nodes')}>All Nodes</button>
              <button type="button" class="small" class:primary={browseTab === 'links'} aria-pressed={browseTab === 'links'} onclick={() => (browseTab = 'links')}>All Links</button>
            </div>
            {#if browseTab === 'nodes'}
              <button type="button" class="type" class:sel={!type} onclick={() => pickType('')}>
                <span class="grow">All types</span><span class="count">{typeTotal}</span>
              </button>
              {#each types as t (t.type)}
                {@const st = splitType(t.type)}
                <button type="button" class="type" class:sel={type === t.type} title={t.type} onclick={() => pickType(t.type ?? '')}>
                  <span class="grow">{st.name || t.type}{#if st.namespace && st.namespace !== commit?.namespace}<span class="ns"> {st.namespace}</span>{/if}</span>
                  <span class="count">{t.count ?? 0}</span>
                </button>
              {/each}
              {#if type && !types.some((t) => t.type === type)}
                <p class="empty small">No <code>{type}</code> node {query ? 'matches the filter' : 'in this commit'}.</p>
              {/if}
            {:else}
              <button type="button" class="type" class:sel={!linkType} onclick={() => pickLinkType('')}>
                <span class="grow">All types</span><span class="count">{linkTypeTotal}</span>
              </button>
              {#each linkTypes as t (t.type)}
                {@const st = splitType(t.type)}
                <button type="button" class="type" class:sel={linkType === t.type} title={t.type} onclick={() => pickLinkType(t.type ?? '')}>
                  <span class="grow">{st.name || t.type}</span>
                  <span class="count">{t.count ?? 0}</span>
                </button>
              {/each}
            {/if}
          </nav>

          {#if browseTab === 'nodes'}
            <section class="card list">
              <div class="row head">
                <h3 class="grow">{type ? splitType(type).name || type : 'All nodes'}</h3>
                <input class="filter" type="search" placeholder="Filter key, title, properties…" aria-label="Filter nodes" value={filter} oninput={(e) => onFilter(e.currentTarget.value)} data-no-pin />
              </div>
              {#if nodes.length}
                <div class="scroll">
                  <table>
                    <thead><tr><th>Key</th>{#if !type}<th>Type</th>{/if}<th>Version</th><th>State</th><th>Title</th></tr></thead>
                    <tbody>
                      {#each nodes as n (n.id)}
                        <tr class:deleted={n.deleted} class:sel={selectedNode === n.id} title="Click: its detail · double-click: open it in its editor" onclick={() => selectNode(n)} ondblclick={() => void openNode(n, { pin: true })}>
                          <td class="nowrap mono key">{n.key}</td>
                          {#if !type}<td>{splitType(n.type).name || n.type}</td>{/if}
                          <td>v{n.version ?? 0}</td>
                          <td>{#if n.state}<span class="state">{n.state}</span>{/if}</td>
                          <td>{nodeTitle(n)}</td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
              {:else if !nodesLoading}
                <p class="empty">{shownId ? (query ? 'No matching nodes.' : 'No nodes.') : 'No history.'}</p>
              {/if}
              <div class="pager" role="group" aria-label="Pages">
                <button type="button" class="small" disabled={page <= 1 || nodesLoading} onclick={() => goto(1)} aria-label="First page">«</button>
                <button type="button" class="small" disabled={page <= 1 || nodesLoading} onclick={() => goto(page - 1)} aria-label="Previous page">‹</button>
                <span class="hint">
                  {#if total}{offset + 1}–{Math.min(offset + size, total)} of {total}{:else}0 of 0{/if} · page {page}/{pageCount}
                </span>
                <button type="button" class="small" disabled={page >= pageCount || nodesLoading} onclick={() => goto(page + 1)} aria-label="Next page">›</button>
                <button type="button" class="small" disabled={page >= pageCount || nodesLoading} onclick={() => goto(pageCount)} aria-label="Last page">»</button>
                <span class="grow"></span>
                <label class="hint">
                  Per page
                  <select
                    value={size}
                    onchange={(e) => {
                      size = Number(e.currentTarget.value);
                      offset = 0;
                    }}
                  >
                    {#each SIZES as s (s)}<option value={s}>{s}</option>{/each}
                  </select>
                </label>
                {#if nodesLoading}<span class="hint">Loading…</span>{/if}
              </div>
            </section>
          {:else}
            <section class="card list">
              <div class="row head">
                <h3 class="grow">{linkType ? splitType(linkType).name || linkType : 'All links'}</h3>
                <input class="filter" type="search" placeholder="Filter type, properties…" aria-label="Filter links" value={linkFilter} oninput={(e) => onLinkFilter(e.currentTarget.value)} data-no-pin />
              </div>
              {#if allLinks.length}
                <div class="scroll">
                  <table>
                    <thead><tr><th>Type</th><th>From</th><th>To</th></tr></thead>
                    <tbody>
                      {#each allLinks as l (l.id)}
                        <tr class:sel={selectedLink?.id === l.id} onclick={() => selectLink(l)}>
                          <td title={l.type}>{typeCatalog.cat.linkLabel(l.type, 'out')}</td>
                          <td class="mono" title={l.from?.id}>{shortId(l.from?.id ?? '')}</td>
                          <td class="mono" title={l.to?.id}>{shortId(l.to?.id ?? '')}</td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
              {:else if !linksLoading}
                <p class="empty">{shownId ? (linkQuery ? 'No matching links.' : 'No links.') : 'No history.'}</p>
              {/if}
              <div class="pager" role="group" aria-label="Pages">
                <button type="button" class="small" disabled={linkPage <= 1 || linksLoading} onclick={() => gotoLink(1)} aria-label="First page">«</button>
                <button type="button" class="small" disabled={linkPage <= 1 || linksLoading} onclick={() => gotoLink(linkPage - 1)} aria-label="Previous page">‹</button>
                <span class="hint">
                  {#if linkTotal}{linkOffset + 1}–{Math.min(linkOffset + linkSize, linkTotal)} of {linkTotal}{:else}0 of 0{/if} · page {linkPage}/{linkPageCount}
                </span>
                <button type="button" class="small" disabled={linkPage >= linkPageCount || linksLoading} onclick={() => gotoLink(linkPage + 1)} aria-label="Next page">›</button>
                <button type="button" class="small" disabled={linkPage >= linkPageCount || linksLoading} onclick={() => gotoLink(linkPageCount)} aria-label="Last page">»</button>
                <span class="grow"></span>
                <label class="hint">
                  Per page
                  <select
                    value={linkSize}
                    onchange={(e) => {
                      linkSize = Number(e.currentTarget.value);
                      linkOffset = 0;
                    }}
                  >
                    {#each SIZES as s (s)}<option value={s}>{s}</option>{/each}
                  </select>
                </label>
                {#if linksLoading}<span class="hint">Loading…</span>{/if}
              </div>
            </section>
          {/if}
      </div>

      <section class="card detail" aria-label="Detail">
        {#if selectedLink}
          {@const l = selectedLink}
          <div class="row head">
            <h3 class="grow" title={l.type}>Link · {typeCatalog.cat.linkLabel(l.type, 'out')}</h3>
            <button type="button" class="small" title="Both ends of the link and their neighbours" onclick={() => (showHood = true)}><Icon name="network" size={14} /> Neighbourhood</button>
          </div>
          <p class="ends">
            <button type="button" class="link mono" title="Show this node" onclick={() => selectEnd(l.from?.id)}>{shortId(l.from?.id ?? '')}</button>
            →
            {#if l.to?.id}<button type="button" class="link mono" title="Show this node" onclick={() => selectEnd(l.to?.id)}>{shortId(l.to.id)}</button>{:else}<span class="mono">{l.target ?? '?'}</span>{/if}
          </p>
          <h4>Properties</h4>
          {#if propRows(l).length}
            <table class="props"><tbody>{#each propRows(l) as [k, v] (k)}<tr><th>{k}</th><td>{v}</td></tr>{/each}</tbody></table>
          {:else}
            <p class="empty small">No properties.</p>
          {/if}
        {:else if !selectedNode}
          <h3>Detail</h3>
          <p class="empty">Select a node to see its properties (double-click: open it in its editor).</p>
        {:else if nbError}
          <div class="alert">{nbError}</div>
        {:else if !center}
          <p class="empty">Loading…</p>
        {:else}
          <div class="row head">
            <h3 class="grow mono" title={center.key}>{center.key}</h3>
            <button type="button" class="small" title="The graph of its neighbours" onclick={() => (showHood = true)}><Icon name="network" size={14} /> Neighbourhood</button>
            <button type="button" class="small primary" title="Open the node in its editor (or double-click it)" onclick={() => center && void openNode(center, { pin: true })}><Icon name="external" size={14} /> Open</button>
          </div>
          <p class="hint">
            {splitType(center.type).name || center.type} · v{center.version ?? 0}{#if center.state} · <span class="state">{center.state}</span>{/if}
            · <button type="button" class="link" title="Versions and states of the node" onclick={() => center && void openNode(center, { pin: true, generic: true, pane: 'history' })}>history</button>
          </p>
          {#if nodeTitle(center)}<p class="title">{nodeTitle(center)}</p>{/if}
          <div class="scroll">
            <h4>Properties</h4>
            {#if propRows(center).length}
              <table class="props"><tbody>{#each propRows(center) as [k, v] (k)}<tr><th>{k}</th><td>{v}</td></tr>{/each}</tbody></table>
            {:else}
              <p class="empty small">No properties.</p>
            {/if}
            <details class="more" open={rows.length > 0 && rows.length <= 20}>
              <summary>Links <span class="count">{nbLoading ? '…' : rows.length}</span></summary>
              {#if rows.length}
                <table>
                  <tbody>
                    {#each rows as r (r.l.id)}
                      <tr class:suspect={r.suspect}>
                        <td class="dir" title={r.outgoing ? 'Outgoing' : 'Incoming'}>{r.outgoing ? '→' : '←'}</td>
                        <td title={r.l.type}>{typeCatalog.cat.linkLabel(r.l.type, r.outgoing ? 'out' : 'in')}</td>
                        <td>
                          <button type="button" class="link mono" title={inCommit(r.other) ? 'Show this node' : 'Not in this commit: open it in its editor'} onclick={() => selectNode(r.other)}>{r.other?.key ?? '?'}</button>
                          {#if nodeTitle(r.other)}<span class="hint"> {nodeTitle(r.other)}</span>{/if}
                          {#if r.suspect}<span class="tag" title="A more recent version of one of its ends exists: check whether the link should use it">suspect</span>{/if}
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              {:else if !nbLoading}
                <p class="empty small">No links.</p>
              {/if}
            </details>
            <details class="more" ontoggle={(e) => (stackOf = (e.currentTarget as HTMLDetailsElement).open ? `${center?.id}@${center?.version}` : '')}>
              <summary>Change-stack of v{center.version}</summary>
              {#if stackOf === `${center.id}@${center.version}`}<ChangeStackView nodeId={center.id} version={center.version} />{/if}
            </details>
          </div>
        {/if}
      </section>
    </div>
  </div>
  {#if commit}
  {#if compareSize === 'normal'}
    <Resizer orientation="horizontal" value={compareShown} min={120} max={compareMax} invert label="Compare panel height" onresize={resizeCompare} />
  {/if}
  <div class="compare-wrap" class:max={compareSize === 'max'} style:height={compareSize === 'normal' ? `${compareShown}px` : null}>
    <CommitCompare {commit} candidates={commits} bind:from={compareFrom} collapsed={compareSize === 'min'}>
      {#snippet actions()}
        <button
          type="button"
          class="ghost small"
          title={compareSize === 'min' ? 'Restore the Compare panel' : 'Shrink the Compare panel to its head'}
          aria-label={compareSize === 'min' ? 'Restore the Compare panel' : 'Shrink the Compare panel'}
          aria-pressed={compareSize === 'min'}
          onclick={() => toggleCompare('min')}><Icon name={compareSize === 'min' ? 'chevronUp' : 'chevronDown'} size={14} /></button>
        <button
          type="button"
          class="ghost small"
          title={compareSize === 'max' ? 'Restore the Compare panel' : 'Enlarge the Compare panel to the whole page'}
          aria-label={compareSize === 'max' ? 'Restore the Compare panel' : 'Enlarge the Compare panel'}
          aria-pressed={compareSize === 'max'}
          onclick={() => toggleCompare('max')}><Icon name={compareSize === 'max' ? 'minimize' : 'maximize'} size={14} /></button>
      {/snippet}
    </CommitCompare>
  </div>
{/if}
</div>

{#if showHood && (selectedNode || selectedLink)}
  <NeighbourhoodDialog commitId={shownId} branch={shownBranch} namespace={commit?.namespace ?? namespace} bind:node={selectedNode} bind:link={selectedLink} {reload} onclose={() => (showHood = false)} />
{/if}

<style>
  .editor-page {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
    padding-bottom: 0.9rem;
  }
  .editor-head {
    flex-wrap: wrap;
  }
  .compare-wrap {
    flex: none;
    min-height: 0;
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }
  .compare-wrap.max {
    flex: 1;
  }
  .workspace[hidden] {
    display: none;
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
    .history {
      width: auto !important;
    }
  }
  .history {
    flex: none;
    min-width: 0;
    display: flex;
    flex-direction: column;
    min-height: 0;
  }
  .history .older {
    align-self: center;
    margin: 0.4rem 0;
  }
  .htools {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.4rem;
    flex: none;
  }
  .htools input[type='search'] {
    flex: 1;
    min-width: 0;
  }
  .htools label {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    white-space: nowrap;
  }
  .htools input[type='checkbox'] {
    width: auto;
    margin: 0;
  }
  .history h3 {
    margin: 0 0 0.2rem;
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }
  .browse {
    flex: 1;
    min-width: 0;
    min-height: 0;
    overflow-x: auto;
    display: grid;
    grid-template-columns: minmax(500px, 1.6fr) minmax(320px, 1fr);
    gap: 0.8rem;
    align-items: stretch;
  }
  @media (max-width: 1300px) {
    .browse {
      grid-template-columns: 1fr;
    }
  }
  .card {
    min-width: 0;
    min-height: 0;
    margin: 0;
  }
  .node-panel {
    display: flex;
    gap: 0.8rem;
    min-width: 0;
    min-height: 0;
    align-items: stretch;
  }
  .types {
    flex: 0 0 200px;
    display: flex;
    flex-direction: column;
    gap: 1px;
    overflow-y: auto;
  }
  .tabs {
    display: flex;
    gap: 0.3rem;
    margin-bottom: 0.4rem;
    flex: none;
  }
  .tabs button {
    flex: 1;
  }
  .list {
    display: flex;
    flex-direction: column;
    min-height: 0;
    flex: 1;
  }
  .detail {
    display: flex;
    flex-direction: column;
    justify-content: flex-start;
    min-height: 0;
  }
  .detail h3 {
    margin: 0;
  }
  .detail .head h3 {
    flex: 1 1 0;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .detail .head {
    flex-wrap: nowrap;
  }
  .detail .head button {
    flex: none;
    white-space: nowrap;
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
  }
  .detail h4 {
    margin: 0.2rem 0 0.3rem;
    font-size: 0.85em;
    color: var(--muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .detail .hint {
    margin: 0 0 0.4rem;
  }
  .props th {
    text-align: left;
    font-weight: 600;
    white-space: nowrap;
    vertical-align: top;
    width: 1%;
  }
  .props td {
    word-break: break-word;
  }
  .props tr {
    cursor: default;
  }
  .more {
    margin-top: 0.6rem;
  }
  .more summary {
    cursor: pointer;
    font-weight: 600;
  }
  .detail > .empty {
    margin: 0.3rem 0 0;
  }
  .ends {
    margin: 0 0 0.5rem;
  }
  .key {
    color: var(--accent);
  }
  .type {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    text-align: left;
    border: none;
    background: none;
    padding: 0.25rem 0.4rem;
    border-radius: 4px;
    cursor: pointer;
    color: inherit;
  }
  .type:hover {
    background: var(--hover);
  }
  .type.sel {
    background: var(--accent-soft);
    font-weight: 600;
  }
  .ns {
    color: var(--muted);
    font-size: 0.8em;
  }
  .count {
    color: var(--muted);
    font-size: 0.85em;
    font-variant-numeric: tabular-nums;
  }
  .head,
  .row {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
  }
  .head {
    margin-bottom: 0.5rem;
    flex: none;
  }
  .head h3 {
    margin: 0;
  }
  .filter {
    max-width: 240px;
  }
  .scroll {
    flex: 1;
    min-height: 0;
    overflow: auto;
  }
  table {
    width: 100%;
  }
  tbody tr {
    cursor: pointer;
  }
  tr.sel td {
    background: var(--accent-soft);
  }
  .deleted td {
    text-decoration: line-through;
    color: var(--muted);
  }
  .state {
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 0.5rem;
    font-size: 0.8rem;
    font-family: var(--mono);
  }
  .pager {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    margin-top: 0.5rem;
    flex-wrap: wrap;
  }
  .pager select {
    width: auto;
    margin-left: 0.3rem;
  }
  .title {
    margin: 0 0 0.5rem;
  }
  .dir {
    color: var(--muted);
    width: 1.5rem;
  }
  tr.suspect td {
    background: var(--warn-soft);
  }
  .tag {
    font-size: 0.75rem;
    font-weight: 700;
    color: var(--warn);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .nowrap {
    white-space: nowrap;
  }
  .empty.small {
    font-size: 0.85em;
  }
</style>
