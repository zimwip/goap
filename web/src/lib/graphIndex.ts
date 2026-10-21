// The graph of a baseline as lookup tables (nodes by id, links by node).
import { graph, type GraphNode, type Link } from './api';
import { MAIN_BRANCH } from './namespace';

export interface GraphIndex {
  baselineId: string;
  nodes: Map<string, GraphNode>;
  out: Map<string, Link[]>;
  in: Map<string, Link[]>;
  list: GraphNode[];
}

export function indexOf(baselineId: string, nodes: GraphNode[], links: Link[]): GraphIndex {
  const byId = new Map(nodes.map((n) => [n.id ?? '', n]));
  const out = new Map<string, Link[]>();
  const inn = new Map<string, Link[]>();
  for (const l of links) {
    const f = l.from?.id ?? '';
    const t = l.to?.id ?? '';
    if (!byId.has(f) || !byId.has(t)) continue;
    out.set(f, [...(out.get(f) ?? []), l]);
    inn.set(t, [...(inn.get(t) ?? []), l]);
  }
  return { baselineId, nodes: byId, out, in: inn, list: nodes };
}

/** The neighbourhood of a node read from an index (the shape of GetNodeNeighbourhood): its links both ways, the nodes
 * at their other ends, and the links naming another version of their target than the index holds (suspect; not a
 * link to a node a change drafted, version 0: the draft is no version yet). */
export function hoodIn(index: GraphIndex, id: string): { node?: GraphNode; nodes: GraphNode[]; links: Link[]; suspectLinkIds: string[] } {
  const node = index.nodes.get(id);
  if (!node) return { nodes: [], links: [], suspectLinkIds: [] };
  const links = [...(index.out.get(id) ?? []), ...(index.in.get(id) ?? [])];
  const ends = new Set(links.map((l) => (l.from?.id === id ? l.to?.id : l.from?.id) ?? '').filter((n) => n && n !== id));
  const suspect = links.filter((l) => {
    const to = index.nodes.get(l.to?.id ?? '');
    return !!to && !!to.version && (l.to?.version ?? 0) !== 0 && l.to?.version !== to.version;
  });
  return { node, nodes: [...ends].flatMap((n) => index.nodes.get(n) ?? []), links, suspectLinkIds: suspect.flatMap((l) => (l.id ? [l.id] : [])) };
}

export async function loadGraph(baselineId: string, signal?: AbortSignal): Promise<GraphIndex> {
  const g = await graph.getBaselineGraph(baselineId, signal);
  return indexOf(baselineId, g.nodes ?? [], g.links ?? []);
}

/** The world a change sees (its fork as it left it, its commits, its drafts), by node: a link names its target's id,
 * whatever version of it (graph.getChangeWorld). `baselineId` is the fork of the change. */
export async function loadChangeWorld(changeId: string, baselineId: string, flow = '', signal?: AbortSignal): Promise<GraphIndex> {
  const g = await graph.getChangeWorld(changeId, flow, signal);
  return indexOf(baselineId, g.nodes ?? [], g.links ?? []);
}

/** The graph at the head of a namespace's main ("current"). */
export async function loadHead(namespace: string, signal?: AbortSignal): Promise<GraphIndex> {
  const b = await graph.getBranch(namespace, MAIN_BRANCH, signal);
  const id = b.head?.id;
  if (!id) return indexOf('', [], []);
  return loadGraph(id, signal);
}
