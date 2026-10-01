// Layered left-to-right layout (dagre) for the xyflow canvases: sizes are estimated from the ports because the nodes
// are not measured before their first render.
import dagre from '@dagrejs/dagre';
import type { Edge, Node } from '@xyflow/svelte';

export const NODE_W = 230;

/** Estimated height of a node listing `rows` ports. */
export const nodeHeight = (rows: number) => 38 + 20 * Math.max(1, rows);

/** Positions for the nodes (top-left corner), the edges deciding the ranks. */
export function layered(nodes: { id: string; w?: number; h: number }[], edges: Pick<Edge, 'source' | 'target'>[]): Map<string, { x: number; y: number }> {
  const g = new dagre.graphlib.Graph();
  g.setGraph({ rankdir: 'LR', nodesep: 36, ranksep: 110 });
  g.setDefaultEdgeLabel(() => ({}));
  for (const n of nodes) g.setNode(n.id, { width: n.w ?? NODE_W, height: n.h });
  for (const e of edges) if (e.source !== e.target && g.hasNode(e.source) && g.hasNode(e.target)) g.setEdge(e.source, e.target);
  dagre.layout(g);
  const out = new Map<string, { x: number; y: number }>();
  for (const n of nodes) {
    const p = g.node(n.id);
    out.set(n.id, { x: Math.round(p.x - p.width / 2), y: Math.round(p.y - p.height / 2) });
  }
  return out;
}

export type PositionedNode = Node;
