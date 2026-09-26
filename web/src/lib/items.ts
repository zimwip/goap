// Formatting of a change's items and change impacts (resolution of node keys).
import type { ChangeItem, GraphNode, JsonValue, NodeRef } from './api';

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

export function refKey(ctx: ItemContext, ref: NodeRef | undefined): string {
  if (!ref?.id) return '';
  const key = ctx.nodes.get(ref.id)?.key ?? ref.id.slice(0, 8);
  return ref.version ? `${key}@v${ref.version}` : key;
}

/** Text of a JSON value for compact display. */
export function show(v: JsonValue | undefined): string {
  if (v === undefined || v === null) return '';
  return typeof v === 'string' ? v : JSON.stringify(v);
}
