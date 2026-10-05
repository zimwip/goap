// Editing graph nodes the way the organisation explorer does: one change of a namespace applied on main.
import { graph, type NodeEdit, type GraphNode, type Link, type NodeRef, type Struct } from './api';
import { MAIN_BRANCH } from './namespace';

export interface HeadGraph {
  baselineId: string;
  /** the nodes in force, and the links between them */
  nodes: GraphNode[];
  links: Link[];
  /** the nodes their lifecycle retired (never deleted, ADR 0076): an edit of one restores it */
  retired: GraphNode[];
}

/** The state a node no parent holds is retired in, and the one it is restored to (lifecycle config, ADR 0076). */
export const RETIRED = 'retired';
export const ACTIVE = 'active';

/** The graph at the head of a namespace's main (the base of every change applied on it). */
export async function headGraph(namespace: string): Promise<HeadGraph> {
  const b = await graph.getBranch(namespace, MAIN_BRANCH);
  const id = b.head?.id ?? b.branch?.head;
  if (!id) throw new Error(`${namespace} main has no baseline yet`);
  const g = await graph.getBaselineGraph(id);
  const all = g.nodes ?? [];
  const retired = all.filter((n) => n.state === RETIRED);
  const gone = new Set(retired.map((n) => n.id));
  return {
    baselineId: id,
    nodes: all.filter((n) => !gone.has(n.id)),
    links: (g.links ?? []).filter((l) => !gone.has(l.from?.id) && !gone.has(l.to?.id)),
    retired,
  };
}

export const refOf = (n: GraphNode): NodeRef => ({ id: n.id, version: n.version });

/** The node of a key, in force or retired (an edit of a retired node restores it). */
export const findNode = (h: HeadGraph, namespace: string, type: string, key: string): GraphNode | undefined =>
  [...h.nodes, ...h.retired].find((n) => n.namespace === namespace && n.type === type && n.key === key);

/** Commits node edits on main as one change of `namespace`; returns the id of the applied change. */
export async function applyOnMain(namespace: string, title: string, intent: string, baselineId: string, edits: NodeEdit[]): Promise<string> {
  const { changeId } = await graph.commitEdits({ namespace, title, intent, baselineId, edits });
  if (!changeId) throw new Error('change not created');
  return changeId;
}

export const createNodeItem = (key: string, type: string, props: Struct, links: NonNullable<NodeEdit['links']> = []): NodeEdit => ({
  key,
  type,
  props,
  rationale: `Create ${key}`,
  ...(links.length ? { links } : {}),
});

/** Updates a node; a retired one is restored first (ADR 0076). */
export const updateNodeItem = (n: GraphNode, props: Struct): NodeEdit => ({
  pre: refOf(n),
  props,
  rationale: `Update ${n.key}`,
  ...(n.state === RETIRED ? { state: ACTIVE } : {}),
});

/** Retires a node no parent holds (an adapter, a policy, an assignment, an MCP...): it is never deleted, its
 * lifecycle takes it out of force (ADR 0076 §4c). */
export const retireNodeItem = (n: GraphNode): NodeEdit => ({
  pre: refOf(n),
  state: RETIRED,
  rationale: `Retire ${n.key}`,
});

/** The current outgoing link of `type` from `n`, if any (its single parent/membership link, ADR 0040). */
export const currentLink = (h: HeadGraph, n: GraphNode, type: string): Link | undefined =>
  h.links.find((l) => l.type === type && l.from?.id === n.id);

/**
 * Moves `n` to a new parent/organisation: replaces its single outgoing link of `type` (part_of,
 * project_part_of or member_of) with one to `to` in the same edit, so the node is never left with zero or
 * two (ADR 0040's "move" action). `current` is the link found by currentLink, if any — `headGraph` is a
 * baseline snapshot, which can predate a link written outside of any change (EnsureUser's own member_of),
 * so `current` can come back empty even though the node already has one; the commit is refused in that case
 * (the server re-checks the live link count after writing, not just this edit's own declared links) rather
 * than silently leaving two — surface that error and have the caller reload before retrying.
 */
export const moveNodeItem = (n: GraphNode, type: string, current: Link | undefined, to: NodeRef): NodeEdit => ({
  pre: refOf(n),
  rationale: `Move ${n.key}`,
  ...(current?.id ? { removeLinks: [current.id] } : {}),
  links: [{ type, to }],
});
