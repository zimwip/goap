// Formatting of a change's items and change impacts (resolution of node keys).
import type { ChangeItem, GraphNode } from './api';

export interface ItemContext {
  /** Nodes from the starting baseline, indexed by id. */
  nodes: Map<string, GraphNode>;
  /** Items of the change, indexed by id. */
  items: Map<string, ChangeItem>;
}

export function makeContext(nodes: GraphNode[] = [], items: ChangeItem[] = []): ItemContext {
  return {
    nodes: new Map(nodes.map((n) => [n.id ?? '', n])),
    items: new Map(items.map((i) => [i.id ?? '', i])),
  };
}

