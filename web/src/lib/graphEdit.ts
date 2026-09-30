// Editing graph nodes the way the organisation explorer does: one change of a namespace applied on main.
import { graph, type NodeEdit, type GraphNode, type Link, type NodeRef, type Struct } from './api';
import { MAIN_BRANCH } from './namespace';

export interface HeadGraph {
  baselineId: string;
  nodes: GraphNode[];
  links: Link[];
}

/** The graph at the head of a namespace's main (the base of every change applied on it). */
export async function headGraph(namespace: string): Promise<HeadGraph> {
  const b = await graph.getBranch(namespace, MAIN_BRANCH);
  const id = b.head?.id ?? b.branch?.head;
  if (!id) throw new Error(`${namespace} main has no baseline yet`);
  const g = await graph.getBaselineGraph(id);
  return { baselineId: id, nodes: g.nodes ?? [], links: g.links ?? [] };
}

export const refOf = (n: GraphNode): NodeRef => ({ id: n.id, version: n.version });

export const findNode = (h: HeadGraph, namespace: string, type: string, key: string): GraphNode | undefined =>
  h.nodes.find((n) => n.namespace === namespace && n.type === type && n.key === key);

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

export const updateNodeItem = (n: GraphNode, props: Struct): NodeEdit => ({
  pre: refOf(n),
  props,
  rationale: `Update ${n.key}`,
});

export const deleteNodeItem = (n: GraphNode): NodeEdit => ({
  pre: refOf(n),
  retire: true,
  rationale: `Delete ${n.key}`,
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
