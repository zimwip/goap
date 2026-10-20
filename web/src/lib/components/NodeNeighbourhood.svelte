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
  // The graph view of one node of a baseline: its direct neighbours (GetNodeNeighbourhood), a click recenters, a
  // double-click opens the node, a link selected shows both of its ends with its properties; below, the node's
  // change-stack and properties. Shared by the Baseline tab and the Graph pane of the node editor.
  import { graph, errorMessage } from '../api';
  import NodeGraph from './NodeGraph.svelte';
  import ChangeStackView from './ChangeStackView.svelte';
  import { indexOf } from '../graphIndex';
  import { openNode } from '../nodeEditors';
  import { typeCatalog } from '../stores/types.svelte';

  let {
    baselineId,
    namespace,
    node = $bindable(''),
    link = $bindable(),
    hood = $bindable(emptyNeighbourhood()),
    reload = 0,
    empty = 'Select a node to see its neighbours.',
  }: {
    baselineId: string;
    /** the namespace the baseline holds: a node of another one opens in its editor instead of recentering */
    namespace: string;
    /** the node at the center */
    node?: string;
    /** the link shown, if any */
    link?: Link;
    hood?: Neighbourhood;
    reload?: number;
    empty?: string;
  } = $props();

  /** the node version whose change-stack is open (ADR 0133) */
  let stackOf = $state('');

  $effect(() => {
    void reload;
    const b = baselineId;
    const n = node;
    if (!b || !n) {
      hood = emptyNeighbourhood();
      return;
    }
    hood = { ...emptyNeighbourhood(), loading: true };
    const ctrl = new AbortController();
    graph
      .getNodeNeighbourhood(b, n, ctrl.signal)
      .then((r) => {
        hood = { center: r.node, neighbours: r.nodes ?? [], links: r.links ?? [], suspect: new Set(r.suspectLinkIds ?? []), loading: false, error: '' };
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) hood = { ...emptyNeighbourhood(), error: errorMessage(e) };
      });
    return () => ctrl.abort();
  });

  const center = $derived(hood.center);
  const index = $derived(indexOf(baselineId, center ? [center, ...hood.neighbours] : [], hood.links));
  const byNodeId = (nid: string) => (nid === center?.id ? center : hood.neighbours.find((n) => n.id === nid));

  function select(n: GraphNode | undefined) {
    if (!n?.id) return;
    link = undefined;
    // a neighbour the baseline does not hold (another namespace): open it in its editor
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
    const b = baselineId;
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
      baselineId,
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
      suspect={linkSuspect}
      onrecenter={recenterFromLink}
      onopen={openFromLink}
      onLinkSelect={(id) => selectLink([...linkFromLinks, ...linkToLinks].find((x) => x.id === id))}
    />
    {#if linkViewLoading}<p class="hint">Loading…</p>{/if}
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
    maxDepth={1}
    suspect={hood.suspect}
    onrecenter={(nid) => select(byNodeId(nid))}
    onopen={(nid) => {
      const n = byNodeId(nid);
      if (n) void openNode(n, { pin: true });
    }}
    onLinkSelect={(id) => selectLink(hood.links.find((x) => x.id === id))}
  />
  {#if hood.loading}<p class="hint">Loading…</p>{/if}
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
