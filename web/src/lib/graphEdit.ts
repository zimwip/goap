// Editing graph nodes the way the organisation explorer does: one change of a namespace applied on main.
import { graph, type NodeEdit, type GraphNode, type Link, type NodeRef, type Struct } from './api';

export interface HeadGraph {
  baselineId: string;
  nodes: GraphNode[];
  links: Link[];
}

/** The graph at the head of main (the base of every change applied on main). */
export async function headGraph(): Promise<HeadGraph> {
  const b = await graph.getBranch('main');
  const id = b.head?.id ?? b.branch?.head;
  if (!id) throw new Error('main has no baseline yet');
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
