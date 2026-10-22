// The graph of a baseline as lookup tables (nodes by id, links by node).
import { graph, isNotFound, type Change, type GraphNode, type GraphView, type Link } from './api';
import { MAIN_BRANCH, namespaceOf } from './namespace';

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

/** A neighbourhood as GetNodeNeighbourhood answers it: a node, its neighbours and the links between them. */
export interface Hood {
  node?: GraphNode;
  nodes?: GraphNode[];
  links?: Link[];
  suspectLinkIds?: string[];
}

/** Reads the neighbourhood of a node in one world (a baseline, or the world of a change). */
export type HoodLoader = (id: string, signal?: AbortSignal) => Promise<Hood>;

/** The view of a baseline of a lasting branch (ADR 0157: the branch is always named; main unless said). */
export const branchView = (namespace: string, baselineId = '', branch = MAIN_BRANCH): GraphView => ({ namespace: namespaceOf(namespace), branch, baselineId });

/** The view of the fork of a change: the baseline it started from, on its own branch (read from its header: a change
 * on a branch of its own names that one, not the one it forked from). */
export async function forkView(c: Pick<Change, 'namespace' | 'baselineId'>, signal?: AbortSignal): Promise<GraphView> {
  const ns = namespaceOf(c.namespace ?? '');
  const b = c.baselineId ? (await graph.listBaselines(ns, signal, { ids: [c.baselineId] })).baselines?.[0] : undefined;
  return branchView(ns, c.baselineId ?? '', b?.branch || MAIN_BRANCH);
}

/** The neighbourhoods of a baseline of a branch. */
export const baselineHoods =
  (view: GraphView): HoodLoader =>
  (id, signal) =>
    graph.getNodeNeighbourhood(view, id, signal);

/** The neighbourhoods in the world of a change (its fork as it left it, its commits, its drafts) on a flow. */
export const changeHoods =
  (changeId: string, flow = ''): HoodLoader =>
  (id, signal) =>
    graph.getNodeNeighbourhood({ changeId, flow }, id, signal);

/** The index of the neighbourhoods read so far of one world: the nodes they hold and the links between them. A node
 * whose neighbourhood was read has all its links (`loaded`); the others only the ones to the nodes read. The cost of
 * an index follows what was read, never the size of its world. */
export function hoodIndex(baselineId: string, hoods: Hood[]): GraphIndex & { loaded: Set<string> } {
  const nodes = new Map<string, GraphNode>();
  const links = new Map<string, Link>();
  const loaded = new Set<string>();
  for (const h of hoods) {
    if (h.node?.id) {
      nodes.set(h.node.id, h.node);
      loaded.add(h.node.id);
    }
  }
  for (const h of hoods) {
    for (const n of h.nodes ?? []) if (n.id && !nodes.has(n.id)) nodes.set(n.id, n);
    for (const l of h.links ?? []) links.set(l.id ?? `${l.from?.id} ${l.type} ${l.to?.id}`, l);
  }
  return { ...indexOf(baselineId, [...nodes.values()], [...links.values()]), loaded };
}

/** Reads the neighbourhood of a node, the empty one when the world does not hold it. */
export async function hoodOf(load: HoodLoader, id: string, signal?: AbortSignal): Promise<Hood> {
  try {
    return await load(id, signal);
  } catch (e) {
    if (isNotFound(e)) return {};
    throw e;
  }
}
