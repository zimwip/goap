<script lang="ts" module>
  import type { GraphNode, Link } from '../api';

  /** The neighbourhood the view read for its node: what a host lists beside the graph. */
  export interface Neighbourhood {
    center?: GraphNode;
    neighbours: GraphNode[];
    links: Link[];
    suspect: Set<string>;
    loading: boolean;
    error: string;
  }

  export const emptyNeighbourhood = (): Neighbourhood => ({ neighbours: [], links: [], suspect: new Set(), loading: false, error: '' });
</script>

<script lang="ts">
  // The graph view of one node of a commit: its neighbours (GetNodeNeighbourhood), up to `MAX_LEVELS` levels the
  // reader picks (the deeper ones read on demand: parents of parents, children of children), a click recenters, a
  // double-click opens the node, a link selected shows both of its ends with its properties; below, the node's
  // change-stack and properties. Shared by the Commit tab and the Graph pane of the node editor, which gives the
  // world of its change (`inChange`: the neighbourhoods are read in the world of that change, not in a commit).
  import { graph, errorMessage } from '../api';
  import NodeGraph from './NodeGraph.svelte';
  import ChangeStackView from './ChangeStackView.svelte';
  import { branchView, indexOf } from '../graphIndex';
  import { MAIN_BRANCH } from '../namespace';
  import { openNode } from '../nodeEditors';
  import { typeCatalog } from '../stores/types.svelte';

  let {
    commitId,
    namespace,
    branch = MAIN_BRANCH,
    node = $bindable(''),
    link = $bindable(),
    hood = $bindable(emptyNeighbourhood()),
    reload = 0,
    empty = 'Select a node to see its neighbours.',
    details = true,
    compact = false,
    fill = false,
    inChange,
    keep,
  }: {
    commitId: string;
    /** the namespace the commit holds: a node of another one opens in its editor instead of recentering */
    namespace: string;
    /** the branch of the commit (ADR 0157: always named; main unless said) */
    branch?: string;
    /** the node at the center */
    node?: string;
    /** the link shown, if any */
    link?: Link;
    hood?: Neighbourhood;
    reload?: number;
    empty?: string;
    /** false: the graph only, without the change-stack and the properties */
    details?: boolean;
    /** the graph's drawing: circles with the type's icon and color, details on hover (NodeGraph) */
    compact?: boolean;
    /** the graph takes the height left in the editor (NodeGraph) */
    fill?: boolean;
    /** the change whose world (its fork as it left it, its commits, its drafts) is read in place of the commit */
    inChange?: { changeId: string; flow?: string };
    /** the nodes shown around the center (a filter, a query's answer); unset: every one */
    keep?: (n: GraphNode) => boolean;
  } = $props();

  /** what the neighbourhoods are read from: the world given, else the commit ('' : nothing to read) */
  const source = $derived(inChange ? `change:${inChange.changeId}:${inChange.flow ?? ''}` : commitId);
  const readHood = (b: string, id: string, signal?: AbortSignal): Promise<Read> => graph.getNodeNeighbourhood(inChange ?? branchView(namespace, b, branch), id, signal);

  const MAX_LEVELS = 4;
  /** the nodes read per level beyond the first (the graph shows at most 14 per column anyway) */
  const LEVEL_CAP = 30;

  /** the node version whose change-stack is open (ADR 0133) */
  let stackOf = $state('');

  $effect(() => {
    void reload;
    const b = source;
    const n = node;
    if (!b || !n) {
      hood = emptyNeighbourhood();
      return;
    }
    hood = { ...emptyNeighbourhood(), loading: true };
    const ctrl = new AbortController();
    readHood(b, n, ctrl.signal)
      .then((r) => {
        hood = { center: r.node, neighbours: r.nodes ?? [], links: r.links ?? [], suspect: new Set(r.suspectLinkIds ?? []), loading: false, error: '' };
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) hood = { ...emptyNeighbourhood(), error: errorMessage(e) };
      });
    return () => ctrl.abort();
  });

  const center = $derived(hood.center);

  // ---- deeper levels: the neighbourhoods of the frontier, read as the reader asks for them ---------

  let depth = $state(1);
  type Read = { node?: GraphNode; nodes?: GraphNode[]; links?: Link[]; suspectLinkIds?: string[] };
  /** the neighbourhoods read beyond the center's, by node id, for this commit and reload */
  let reads = $state(new Map<string, Read>());
  let deepLoading = $state(false);

  $effect(() => {
    void reload;
    void source;
    reads = new Map();
  });

  /** the nodes of each level beyond the first, from what was read: parents leftwards, children rightwards */
  function frontiers(levels: number): string[][] {
    const out: string[][] = [];
    if (!center?.id) return out;
    const seen = new Set<string>([center.id, ...hood.neighbours.map((n) => n.id ?? '')]);
    let ins = hood.links.filter((l) => l.to?.id === center.id).map((l) => l.from?.id ?? '');
    let outs = hood.links.filter((l) => l.from?.id === center.id).map((l) => l.to?.id ?? '');
    for (let d = 2; d <= levels; d++) {
      const level = [...new Set([...ins, ...outs])].filter((id) => id && (hood.neighbours.find((n) => n.id === id) ?? nodeOf(id))?.namespace === namespace).slice(0, LEVEL_CAP);
      out.push(level);
      const nextIn: string[] = [];
      const nextOut: string[] = [];
      for (const id of ins) for (const l of reads.get(id)?.links ?? []) if (l.to?.id === id && l.from?.id && !seen.has(l.from.id)) nextIn.push(l.from.id);
      for (const id of outs) for (const l of reads.get(id)?.links ?? []) if (l.from?.id === id && l.to?.id && !seen.has(l.to.id)) nextOut.push(l.to.id);
      [...nextIn, ...nextOut].forEach((id) => seen.add(id));
      ins = nextIn;
      outs = nextOut;
    }
    return out;
  }

  const nodeOf = (id: string): GraphNode | undefined => {
    for (const r of reads.values()) {
      if (r.node?.id === id) return r.node;
      const n = r.nodes?.find((x) => x.id === id);
      if (n) return n;
    }
    return undefined;
  };

  $effect(() => {
    const b = source;
    const missing = b && center?.id && depth > 1 ? frontiers(depth).flat().filter((id) => !reads.has(id)) : [];
    deepLoading = missing.length > 0;
    if (!missing.length) return;
    const ctrl = new AbortController();
    deepLoading = true;
    Promise.all(missing.map((id) => readHood(b, id, ctrl.signal).catch((): Read => ({}))))
      .then((rs) => {
        if (ctrl.signal.aborted) return;
        const next = new Map(reads);
        missing.forEach((id, i) => next.set(id, rs[i]));
        reads = next;
      });
    return () => ctrl.abort();
  });

  const index = $derived.by(() => {
    if (!center) return indexOf(commitId, [], []);
    const nodes = new Map<string, GraphNode>();
    const links = new Map<string, Link>();
    const add = (r: Read) => {
      for (const n of [...(r.node ? [r.node] : []), ...(r.nodes ?? [])]) if (n.id && !nodes.has(n.id)) nodes.set(n.id, n);
      for (const l of r.links ?? []) if (l.id && !links.has(l.id)) links.set(l.id, l);
    };
    add({ node: center, nodes: hood.neighbours, links: hood.links });
    if (depth > 1) for (const id of frontiers(depth).flat()) add(reads.get(id) ?? {});
    if (keep) {
      for (const [id, n] of nodes) if (id !== center.id && !keep(n)) nodes.delete(id);
      for (const [id, l] of links) if (!nodes.has(l.from?.id ?? '') || !nodes.has(l.to?.id ?? '')) links.delete(id);
    }
    return indexOf(commitId, [...nodes.values()], [...links.values()]);
  });
  const suspect = $derived(new Set([...hood.suspect, ...[...reads.values()].flatMap((r) => r.suspectLinkIds ?? [])]));
  const byNodeId = (nid: string) => index.nodes.get(nid);

  function select(n: GraphNode | undefined) {
    if (!n?.id) return;
    link = undefined;
    // a neighbour the commit does not hold (another namespace): open it in its editor
    if ((n.namespace ?? '') !== namespace) {
      void openNode(n, { pin: true });
      return;
    }
    node = n.id;
  }

  function selectLink(l: Link | undefined) {
    if (l?.id) link = l;
  }

  // ---- link selection: its own view, centered on both endpoints and their neighbours ------------

  let linkFromNode = $state<GraphNode | undefined>();
  let linkToNode = $state<GraphNode | undefined>();
  let linkFromNeighbours = $state<GraphNode[]>([]);
  let linkToNeighbours = $state<GraphNode[]>([]);
  let linkFromLinks = $state<Link[]>([]);
  let linkToLinks = $state<Link[]>([]);
  let linkSuspect = $state(new Set<string>());
  let linkViewLoading = $state(false);
  let linkViewError = $state('');

  $effect(() => {
    void reload;
    const b = source;
    const l = link;
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
    Promise.all([readHood(b, fromId, ctrl.signal), readHood(b, toId, ctrl.signal)])
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
      commitId,
      [...(linkFromNode ? [linkFromNode] : []), ...(linkToNode ? [linkToNode] : []), ...linkFromNeighbours, ...linkToNeighbours],
      [...linkFromLinks, ...linkToLinks],
    ),
  );

  function recenterFromLink(nid: string) {
    const n = linkViewIndex.nodes.get(nid);
    link = undefined;
    select(n);
  }

  function openFromLink(nid: string) {
    const n = linkViewIndex.nodes.get(nid);
    if (n) void openNode(n, { pin: true });
  }

  const propRows = (n: GraphNode | undefined | Link) =>
    Object.entries(n?.props ?? {}).map(([k, v]): [string, string] => [k, typeof v === 'string' ? v : JSON.stringify(v)]);
</script>

{#if link}
  <div class="row head">
    <h3 class="grow" title={link.type}>Link <span>{typeCatalog.cat.linkLabel(link.type, 'out')}</span></h3>
    <button type="button" class="ghost small" title="Close" aria-label="Close link view" onclick={() => (link = undefined)}>×</button>
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
      {compact}
      {fill}
      suspect={linkSuspect}
      onrecenter={recenterFromLink}
      onopen={openFromLink}
      onLinkSelect={(id) => selectLink([...linkFromLinks, ...linkToLinks].find((x) => x.id === id))}
    />
    {#if linkViewLoading}<p class="hint">Loading…</p>{/if}
    {#if details}
      <details class="props" open>
        <summary>Link properties</summary>
        {#if propRows(link).length}
          <table>
            <tbody>
              {#each propRows(link) as [k, v] (k)}<tr><th>{k}</th><td>{v}</td></tr>{/each}
            </tbody>
          </table>
        {:else}
          <p class="empty small">No properties.</p>
        {/if}
      </details>
    {/if}
  {/if}
{:else if !node}
  <h3>Neighbours</h3>
  <p class="empty">{empty}</p>
{:else if hood.error}
  <h3>Neighbours</h3>
  <div class="alert">{hood.error}</div>
{:else if !center}
  <h3>Neighbours</h3>
  <p class="empty">Loading…</p>
{:else}
  <NodeGraph
    {index}
    center={center.id ?? ''}
    maxDepth={MAX_LEVELS}
    bind:depth
    {compact}
    {fill}
    {suspect}
    onrecenter={(nid) => select(byNodeId(nid))}
    onopen={(nid) => {
      const n = byNodeId(nid);
      if (n) void openNode(n, { pin: true });
    }}
    onLinkSelect={(id) => selectLink([...index.out.values()].flat().find((x) => x.id === id))}
  />
  {#if hood.loading || deepLoading}<p class="hint">Loading…</p>{/if}
  {#if details}
    <details class="props" ontoggle={(e) => (stackOf = (e.currentTarget as HTMLDetailsElement).open ? `${center?.id}@${center?.version}` : '')}>
      <summary>Change-stack of v{center.version}</summary>
      {#if stackOf === `${center.id}@${center.version}`}<ChangeStackView nodeId={center.id} version={center.version} />{/if}
    </details>
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
{/if}

<style>
  .head {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
    margin-bottom: 0.5rem;
    flex: none;
  }
  .head h3 {
    margin: 0;
  }
  table {
    width: 100%;
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
  .empty.small {
    font-size: 0.85em;
  }
</style>
