<script lang="ts">
  // Baseline tool's workspace: replaces the editor area's tabbed view while the Baseline tool is selected
  // (registered as its `editorArea`, shell/EditorArea.svelte), driven by BaselineExplorer's nav (the store
  // baselineTool.svelte). History (git-log-style, BaselineBranchGraph) on the left; node types, a filtered/paged
  // node list, the neighbours graph and BaselineCompare — ported from the retired BaselineTab.svelte — on the
  // right, all for whichever baseline is shown: the selected branch's head, or a specific row clicked in the
  // history graph (own-branch or a fork/merge-source ghost). Branch management (open/merge/abandon/describe) is a
  // right-click on the branch's row in BaselineExplorer, not shown here.
  import {
    graph,
    errorMessage,
    formatDate,
    nodeTitle,
    shortId,
    type Baseline,
    type Branch,
    type GraphNode,
    type Link,
    type TypeCount,
  } from '../../api';
  import Icon from '../../shell/Icon.svelte';
  import NodeGraph from '../../components/NodeGraph.svelte';
  import BaselineCompare from '../../components/BaselineCompare.svelte';
  import BaselineBranchGraph from '../../components/BaselineBranchGraph.svelte';
  import Resizer from '../../shell/Resizer.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { openNode } from '../../nodeEditors';
  import { indexOf } from '../../graphIndex';
  import { splitType } from '../../stores/types.svelte';
  import { MAIN_BRANCH } from '../../namespace';
  import { baselineTool, selectBranch, selectBaseline } from '../../stores/baselineTool.svelte';
  import { loadRaw, save } from '../../shell/storage';

  const SIZES = [25, 50, 100, 200];
  const HW_KEY = 'goap.ide.baseline.historyWidth';
  const CH_KEY = 'goap.ide.baseline.compareHeight';

  const namespace = $derived(baselineTool.namespace);

  let historyWidth = $state(typeof loadRaw(HW_KEY) === 'number' ? (loadRaw(HW_KEY) as number) : 320);
  function resizeHistory(v: number) {
    historyWidth = v;
    save(HW_KEY, v);
  }

  // Compare panel: a third of the page by default, resizable and then remembered
  let pageHeight = $state(0);
  let compareHeightSet = $state(typeof loadRaw(CH_KEY) === 'number' ? (loadRaw(CH_KEY) as number) : 0);
  const compareMax = $derived(Math.max(120, Math.min(600, Math.floor(pageHeight * 0.7))));
  const compareShown = $derived(Math.min(compareHeightSet || Math.round(pageHeight / 3) || 220, compareMax));
  function resizeCompare(v: number) {
    compareHeightSet = v;
    save(CH_KEY, v);
  }

  let reload = $state(0);
  let loading = $state(false);
  let error = $state('');
  let branches = $state<Branch[]>([]);
  let baselines = $state<Baseline[]>([]);

  // A namespace switch fires a new fetch before the previous one may have resolved: abort the stale one so its
  // (possibly later-arriving) response can't overwrite the current namespace's data.
  $effect(() => {
    const ns = namespace;
    void reload;
    void baselineTool.reload;
    if (!ns) return;
    const ctrl = new AbortController();
    loading = true;
    Promise.all([graph.listBranches(ns, ctrl.signal), graph.listBaselines(ns, ctrl.signal)])
      .then(([br, bs]) => {
        branches = br.branches ?? [];
        baselines = bs.baselines ?? [];
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

  const branchOf = (b: Baseline) => b.branch || MAIN_BRANCH;
  const own = $derived(baselines.filter((b) => branchOf(b) === baselineTool.branch));
  const headId = $derived([...own].sort((a, b) => (b.createdAt ?? '').localeCompare(a.createdAt ?? ''))[0]?.id ?? '');
  /** the baseline shown in the panels below: whichever row was clicked in the history graph, or the branch's head */
  const shownId = $derived(baselineTool.baseline || headId);
  /** the branch's fork point: anchors the graph even before it has a baseline of its own */
  const forkBaseline = $derived(branches.find((b) => b.name === baselineTool.branch)?.forkBaseline);

  /** a baseline of the selected branch is just shown (the graph already is the browsing view: no tab); one from
   * another branch (a fork point or merge source) switches to that branch, and shows it too */
  function selectHistory(id: string) {
    const br = branchOf(baselines.find((b) => b.id === id) ?? {});
    if (br !== baselineTool.branch) selectBranch(br);
    selectBaseline(id);
  }

  // ---- node types + filtered/paged node list, for `shownId` --------------------------------------

  let baseline = $state<Baseline | undefined>();
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

  // switching baseline (branch or a specific history row) starts the node browser fresh
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
    const req = { baselineId: shownId, type, query, offset, limit: size };
    if (!req.baselineId) {
      baseline = undefined;
      types = [];
      nodes = [];
      total = 0;
      return;
    }
    const ctrl = new AbortController();
    nodesLoading = true;
    graph
      .listBaselineNodes(req, ctrl.signal)
      .then((r) => {
        baseline = r.baseline;
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

  // browsing the whole baseline's links: only while no node narrows it to its own (unpaginated) links already
  // fetched by the neighbourhood effect below
  $effect(() => {
    void reload;
    if (browseTab !== 'links' || selectedNode) return;
    const req = { baselineId: shownId, type: linkType, query: linkQuery, offset: linkOffset, limit: linkSize };
    if (!req.baselineId) {
      linkTypes = [];
      allLinks = [];
      linkTotal = 0;
      return;
    }
    const ctrl = new AbortController();
    linksLoading = true;
    graph
      .listBaselineLinks(req, ctrl.signal)
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

  // ---- neighbourhood of the selected node -------------------------------------------------------

  let center = $state<GraphNode | undefined>();
  let neighbours = $state<GraphNode[]>([]);
  let links = $state<Link[]>([]);
  let suspect = $state(new Set<string>());
  let nbLoading = $state(false);
  let nbError = $state('');

  $effect(() => {
    void reload;
    const b = shownId;
    const n = selectedNode;
    center = undefined;
    neighbours = [];
    links = [];
    suspect = new Set();
    nbError = '';
    if (!b || !n) return;
    const ctrl = new AbortController();
    nbLoading = true;
    graph
      .getNodeNeighbourhood(b, n, ctrl.signal)
      .then((r) => {
        center = r.node;
        neighbours = r.nodes ?? [];
        links = r.links ?? [];
        suspect = new Set(r.suspectLinkIds ?? []);
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) nbError = errorMessage(e);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) nbLoading = false;
      });
    return () => ctrl.abort();
  });

  const index = $derived(indexOf(shownId, center ? [center, ...neighbours] : [], links));

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

  const inBaseline = (n: GraphNode | undefined) => !!n?.id && !!baseline?.nodes && n.id in baseline.nodes;

  function selectNode(n: GraphNode | undefined) {
    if (!n?.id) return;
    selectedLink = undefined;
    // a neighbour the baseline does not hold (another namespace): open it in its editor
    if (!inBaseline(n)) {
      void openNode(n, { pin: true });
      return;
    }
    selectedNode = n.id;
  }

  function clearSelection() {
    selectedNode = '';
    selectedLink = undefined;
  }

  const byNodeId = (nid: string) => (nid === center?.id ? center : neighbours.find((n) => n.id === nid));

  const propRows = (n: GraphNode | undefined | Link) =>
    Object.entries(n?.props ?? {}).map(([k, v]): [string, string] => [k, typeof v === 'string' ? v : JSON.stringify(v)]);

  // ---- link selection: its own view, centered on both endpoints and their neighbours ------------

  let selectedLink = $state<Link | undefined>();
  let linkFromNode = $state<GraphNode | undefined>();
  let linkToNode = $state<GraphNode | undefined>();
  let linkFromNeighbours = $state<GraphNode[]>([]);
  let linkToNeighbours = $state<GraphNode[]>([]);
  let linkFromLinks = $state<Link[]>([]);
  let linkToLinks = $state<Link[]>([]);
  let linkSuspect = $state(new Set<string>());
  let linkViewLoading = $state(false);
  let linkViewError = $state('');

  function selectLink(l: Link | undefined) {
    if (!l?.id) return;
    selectedLink = l;
  }

  $effect(() => {
    void reload;
    const b = shownId;
    const l = selectedLink;
    linkFromNode = undefined;
    linkToNode = undefined;
    linkFromNeighbours = [];
    linkToNeighbours = [];
    linkFromLinks = [];
    linkToLinks = [];
    linkSuspect = new Set();
    linkViewError = '';
    const fromId = l?.from?.id;
    const toId = l?.to?.id;
    if (!b || !fromId || !toId) return;
    const ctrl = new AbortController();
    linkViewLoading = true;
    Promise.all([graph.getNodeNeighbourhood(b, fromId, ctrl.signal), graph.getNodeNeighbourhood(b, toId, ctrl.signal)])
      .then(([a, c]) => {
        linkFromNode = a.node;
        linkFromNeighbours = a.nodes ?? [];
        linkFromLinks = a.links ?? [];
        linkToNode = c.node;
        linkToNeighbours = c.nodes ?? [];
        linkToLinks = c.links ?? [];
        linkSuspect = new Set([...(a.suspectLinkIds ?? []), ...(c.suspectLinkIds ?? [])]);
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) linkViewError = errorMessage(e);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) linkViewLoading = false;
      });
    return () => ctrl.abort();
  });

  const linkViewIndex = $derived(
    indexOf(
      shownId,
      [...(linkFromNode ? [linkFromNode] : []), ...(linkToNode ? [linkToNode] : []), ...linkFromNeighbours, ...linkToNeighbours],
      [...linkFromLinks, ...linkToLinks],
    ),
  );

  function recenterFromLink(nid: string) {
    const n = linkViewIndex.nodes.get(nid);
    selectedLink = undefined;
    selectNode(n);
  }

  function openFromLink(nid: string) {
    const n = linkViewIndex.nodes.get(nid);
    if (n) void openNode(n, { pin: true });
  }
</script>

<div class="editor-page" bind:clientHeight={pageHeight}>
  <div class="editor-head">
    <Icon name="database" size={18} />
    <h2>Baseline · {namespace}</h2>
    {#if baseline}
      <span class="hint">
        <code>{shortId(baseline.id)}</code>
        {#if baseline.createdAt} · {formatDate(baseline.createdAt)}{/if}
        {#if baseline.changeId} · from change <button type="button" class="link mono" onclick={() => openTab({ kind: 'change', params: { id: baseline?.changeId ?? '' } })}>{baseline.changeId.slice(0, 8)}</button>{/if}
        · {Object.keys(baseline.nodes ?? {}).length} nodes
      </span>
    {/if}
    <span class="grow"></span>
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={loading} onclick={() => reload++}><Icon name="refresh" size={14} /></button>
  </div>
  {#if error}<div class="alert">{error}</div>{/if}

  <div class="workspace">
    <nav class="card history" style:width="{historyWidth}px" aria-label="History">
      <h3>History <span class="hint">{baselineTool.branch}</span></h3>
      <BaselineBranchGraph {baselines} branch={baselineTool.branch} {forkBaseline} selected={shownId} onselect={selectHistory} onopen={selectHistory} />
    </nav>

    <Resizer orientation="vertical" value={historyWidth} min={240} max={520} label="History panel width" onresize={resizeHistory} />

    <div class="browse">
      <div class="node-panel">
        {#if !selectedNode}
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
                  <span class="grow">{st.name || t.type}{#if st.namespace && st.namespace !== baseline?.namespace}<span class="ns"> {st.namespace}</span>{/if}</span>
                  <span class="count">{t.count ?? 0}</span>
                </button>
              {/each}
              {#if type && !types.some((t) => t.type === type)}
                <p class="empty small">No <code>{type}</code> node {query ? 'matches the filter' : 'in this baseline'}.</p>
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
                        <tr class:deleted={n.deleted} onclick={() => selectNode(n)} ondblclick={() => void openNode(n, { pin: true })}>
                          <td class="nowrap"><button type="button" class="link mono" title="Show its neighbours (double-click: open in its editor)" onclick={(e) => { e.stopPropagation(); selectNode(n); }}>{n.key}</button></td>
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
                          <td class="mono">{l.type}</td>
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
        {:else}
          <section class="card list node-links">
            <div class="row head">
              <button type="button" class="back" onclick={clearSelection} title="Back to the node list">
                <Icon name="chevronLeft" size={16} />
                <span class="mono">{center?.key ?? '…'}</span>
              </button>
              {#if center}<span class="hint">{splitType(center.type).name} v{center.version ?? 0}{center.state ? ` · ${center.state}` : ''}</span>{/if}
              <span class="grow"></span>
              <button type="button" class="small" onclick={() => center && void openNode(center, { pin: true })}>Open</button>
              <button type="button" class="small" title="Versions and states of the node" onclick={() => center && void openNode(center, { pin: true, generic: true, pane: 'history' })}>History</button>
            </div>
            {#if nodeTitle(center)}<p class="title">{nodeTitle(center)}</p>{/if}
            {#if nbError}
              <div class="alert">{nbError}</div>
            {:else if !center}
              <p class="empty">Loading…</p>
            {:else}
              {#if rows.length}
                <div class="scroll">
                  <table>
                    <thead><tr><th></th><th>Link</th><th>Node</th><th>Type</th><th></th></tr></thead>
                    <tbody>
                      {#each rows as r (r.l.id)}
                        <tr class:suspect={r.suspect} class:sel={selectedLink?.id === r.l.id}>
                          <td class="dir" title={r.outgoing ? 'Outgoing' : 'Incoming'}>{r.outgoing ? '→' : '←'}</td>
                          <td class="mono"><button type="button" class="link mono" title="Show this link's properties" onclick={() => selectLink(r.l)}>{r.l.type}</button></td>
                          <td>
                            <button type="button" class="link mono" title={inBaseline(r.other) ? 'Show its neighbours' : 'Not in this baseline: open it in its editor'} onclick={() => selectNode(r.other)}>{r.other?.key ?? '?'}</button>
                            {#if nodeTitle(r.other)}<span class="hint"> {nodeTitle(r.other)}</span>{/if}
                          </td>
                          <td>{r.other?.type ?? ''}</td>
                          <td>{#if r.suspect}<span
                                class="tag"
                                title={`A more recent version of ${r.outgoing ? r.other?.key ?? 'the target' : center?.key ?? 'this node'} exists: check whether the link should use it`}>suspect</span
                              >{/if}</td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
              {:else if !nbLoading}
                <p class="empty">No links.</p>
              {/if}
              {#if nbLoading}<p class="hint">Loading…</p>{/if}
            {/if}
          </section>
        {/if}
      </div>

      <section class="card neighbours">
        {#if selectedLink}
          <div class="row head">
            <h3 class="grow">Link <span class="mono">{selectedLink.type}</span></h3>
            <button type="button" class="ghost small" title="Close" aria-label="Close link view" onclick={() => (selectedLink = undefined)}>×</button>
          </div>
          {#if linkViewError}
            <div class="alert">{linkViewError}</div>
          {:else if !linkFromNode || !linkToNode}
            <p class="empty">Loading…</p>
          {:else}
            <p class="hint"><span class="mono">{linkFromNode.key}</span> → <span class="mono">{linkToNode.key}</span></p>
            <NodeGraph
              index={linkViewIndex}
              center={linkFromNode.id ?? ''}
              secondaryCenter={linkToNode.id}
              maxDepth={1}
              suspect={linkSuspect}
              onrecenter={recenterFromLink}
              onopen={openFromLink}
              onLinkSelect={(id) => {
                const l = [...linkFromLinks, ...linkToLinks].find((x) => x.id === id);
                if (l) selectLink(l);
              }}
            />
            {#if linkViewLoading}<p class="hint">Loading…</p>{/if}
            <details class="props" open>
              <summary>Link properties</summary>
              {#if propRows(selectedLink).length}
                <table>
                  <tbody>
                    {#each propRows(selectedLink) as [k, v] (k)}<tr><th>{k}</th><td>{v}</td></tr>{/each}
                  </tbody>
                </table>
              {:else}
                <p class="empty small">No properties.</p>
              {/if}
            </details>
          {/if}
        {:else if !selectedNode}
          <h3>Neighbours</h3>
          <p class="empty">Select a node to see its neighbours.</p>
        {:else if nbError}
          <h3>Neighbours</h3>
          <div class="alert">{nbError}</div>
        {:else if !center}
          <h3>Neighbours</h3>
          <p class="empty">Loading…</p>
        {:else}
          <NodeGraph {index} center={center.id ?? ''} maxDepth={1} {suspect} onrecenter={(nid) => selectNode(byNodeId(nid))} onopen={(nid) => { const n = byNodeId(nid); if (n) void openNode(n, { pin: true }); }} onLinkSelect={(id) => { const l = links.find((x) => x.id === id); if (l) selectLink(l); }} />
          {#if nbLoading}<p class="hint">Loading…</p>{/if}
          {#if propRows(center).length}
            <details class="props" open>
              <summary>Properties</summary>
              <table>
                <tbody>
                  {#each propRows(center) as [k, v] (k)}<tr><th>{k}</th><td>{v}</td></tr>{/each}
                </tbody>
              </table>
            </details>
          {/if}
        {/if}
      </section>
    </div>
  </div>
  {#if baseline}
  <Resizer orientation="horizontal" value={compareShown} min={120} max={compareMax} invert label="Compare panel height" onresize={resizeCompare} />
  <div class="compare-wrap" style:height="{compareShown}px">
    <BaselineCompare {baseline} />
  </div>
{/if}
</div>

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
    grid-template-columns: minmax(500px, 1.1fr) minmax(360px, 1fr);
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
  .back {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    border: none;
    background: none;
    color: inherit;
    font-weight: 600;
    cursor: pointer;
    padding: 0.2rem 0.3rem 0.2rem 0;
  }
  .back:hover {
    color: var(--accent);
  }
  .neighbours {
    overflow-y: auto;
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
  .props {
    margin-top: 0.6rem;
  }
  .props th {
    text-align: left;
    font-weight: 600;
    white-space: nowrap;
    vertical-align: top;
  }
  .props td {
    word-break: break-word;
  }
  .nowrap {
    white-space: nowrap;
  }
  .empty.small {
    font-size: 0.85em;
  }
</style>
