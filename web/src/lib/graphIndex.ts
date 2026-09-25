// The graph of a baseline as lookup tables (nodes by id, links by node).
import { graph, type GraphNode, type Link } from './api';

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

export async function loadGraph(baselineId: string, signal?: AbortSignal): Promise<GraphIndex> {
  const g = await graph.getBaselineGraph(baselineId, signal);
  return indexOf(baselineId, g.nodes ?? [], g.links ?? []);
}

/** The graph at the head of main ("current"). */
export async function loadHead(signal?: AbortSignal): Promise<GraphIndex> {
  const b = await graph.getBranch('main', signal);
  const id = b.head?.id;
  if (!id) return indexOf('', [], []);
  return loadGraph(id, signal);
}

/** Key and type of a node, or its short id when unknown. */
export function labelOf(ix: GraphIndex, id: string): string {
  return ix.nodes.get(id)?.key ?? id.slice(0, 8);
}
