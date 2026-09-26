// Editing graph nodes the way the organisation explorer does: one change of a namespace applied on main.
import { graph, type ChangeItem, type GraphNode, type Link, type NodeRef, type Struct } from './api';

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

/** Applies proposals on main as one change of `namespace`; returns the id of the applied change. */
export async function applyOnMain(namespace: string, title: string, intent: string, baselineId: string, items: ChangeItem[]): Promise<string> {
  const { change } = await graph.createChange({ title, intent, baselineId, namespace });
  if (!change?.id) throw new Error('change not created');
  await graph.addItems(change.id, items);
  await graph.applyChange(change.id, title);
  return change.id;
}

export const createNodeItem = (id: string, key: string, type: string, props: Struct): ChangeItem => ({
  id,
  kind: 'proposal',
  type: 'object',
  proposal: { op: 'create_node', node: { key, type, props } },
});

export const updateNodeItem = (n: GraphNode, props: Struct): ChangeItem => ({
  kind: 'proposal',
  type: 'object',
  proposal: { op: 'update_node', node: { base: refOf(n), props } },
});

export const deleteNodeItem = (n: GraphNode): ChangeItem => ({
  kind: 'proposal',
  type: 'object',
  proposal: { op: 'delete_node', node: { base: refOf(n) } },
});

export const linkItem = (from: string, type: string, to: NodeRef): ChangeItem => ({
  kind: 'proposal',
  type: 'object',
  derivedFrom: [from],
  proposal: { op: 'add_link', link: { type, from: { item: from }, to: { node: to } } },
});
