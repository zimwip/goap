// Mise en forme des items d'un changement (résolution des clés de nœuds).
import type { ChangeItem, Endpoint, GraphNode, JsonValue, NodeRef } from './api';

export interface ItemContext {
  /** Nœuds du référentiel de départ, indexés par identifiant. */
  nodes: Map<string, GraphNode>;
  /** Items du changement, indexés par identifiant. */
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

function endpointKey(ctx: ItemContext, ep: Endpoint | undefined): string {
  if (!ep) return '?';
  if (ep.node?.id) return refKey(ctx, ep.node);
  if (ep.item) {
    const it = ctx.items.get(ep.item);
    const key = it?.proposal?.node?.key;
    return key ? `${key} (nouveau)` : `item ${ep.item.slice(0, 8)}`;
  }
  return '?';
}

const OPS: Record<string, string> = {
  create_node: 'Créer',
  update_node: 'Modifier',
  delete_node: 'Supprimer',
  add_link: 'Lier',
  remove_link: 'Délier',
};

export function opLabel(op: string | undefined): string {
  return OPS[op ?? ''] ?? op ?? '?';
}

/** Résumé d'une ligne d'une proposition. */
export function describeProposal(ctx: ItemContext, item: ChangeItem | undefined): string {
  const p = item?.proposal;
  if (!p) return item?.id ? `item ${item.id.slice(0, 8)}` : '?';
  if (p.link) {
    return `${opLabel(p.op)} ${endpointKey(ctx, p.link.from)} → ${p.link.type ?? '?'} → ${endpointKey(ctx, p.link.to)}`;
  }
  const n = p.node;
  const key = n?.key || refKey(ctx, n?.base) || '?';
  const type = n?.type || (n?.base?.id ? ctx.nodes.get(n.base.id)?.type : '') || '';
  return `${opLabel(p.op)} ${key}${type ? ` (${type})` : ''}`;
}

/** Texte d'une valeur JSON pour l'affichage compact. */
export function show(v: JsonValue | undefined): string {
  if (v === undefined || v === null) return '';
  return typeof v === 'string' ? v : JSON.stringify(v);
}
