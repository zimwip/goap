// Dragging a node (ADR 0178): a link to a node, a result of a query or the title of a node editor carries the node, a
// folder of a project directory takes it. The payload is the node, by id, with the version it was shown at; the drop
// decides whether it keeps that version or follows the node.
import type { GraphNode } from './api';

/** The type of the data a dragged node carries. */
export const NODE_DRAG = 'application/x-goap-node';

/** A node as it is dragged. */
export interface DraggedNode {
  id: string;
  /** the version shown where the drag started (0: the node as a view holds it) */
  version: number;
  key?: string;
  type?: string;
  namespace?: string;
}

/** The handler of a dragstart on something showing a node: the node rides along, its key as text. */
export function dragNode(e: DragEvent, n: Pick<GraphNode, 'id' | 'version' | 'key' | 'type' | 'namespace'> | undefined) {
  if (!e.dataTransfer || !n?.id) return;
  const d: DraggedNode = { id: n.id, version: n.version ?? 0, key: n.key, type: n.type, namespace: n.namespace };
  e.dataTransfer.setData(NODE_DRAG, JSON.stringify(d));
  e.dataTransfer.setData('text/plain', n.key ?? n.id);
  e.dataTransfer.effectAllowed = 'copyMove';
}

/** Whether a drag carries a node (readable during dragover, when only the types are). */
export const carriesNode = (e: DragEvent): boolean => !!e.dataTransfer?.types.includes(NODE_DRAG);

/** The node a drop carries, if any. */
export function droppedNode(e: DragEvent): DraggedNode | undefined {
  const raw = e.dataTransfer?.getData(NODE_DRAG);
  if (!raw) return undefined;
  try {
    const d = JSON.parse(raw) as Partial<DraggedNode>;
    return typeof d.id === 'string' && d.id ? { id: d.id, version: Math.max(0, Number(d.version) || 0), key: d.key, type: d.type, namespace: d.namespace } : undefined;
  } catch {
    return undefined;
  }
}
