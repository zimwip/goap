// Node lifecycles as seen by a change (ADR 0014): the state a node has in the
// change once its transition proposals are counted, and the transitions it can take.
import type { ChangeItem, GraphNode, Lifecycle, LifecycleTransition, NodeRef } from './api';

/** Lifecycle of a node type, from the NodeType nodes of a baseline (extends chain included). */
export function lifecycleResolver(nodes: GraphNode[]): (type: string | undefined) => Lifecycle | undefined {
  const types = new Map<string, { extends: string; lifecycle?: Lifecycle }>();
  for (const n of nodes) {
    if (n.type !== 'NodeType') continue;
    const p = (n.props ?? {}) as Record<string, unknown>;
    const name = typeof p.name === 'string' ? p.name : '';
    if (!name || types.has(name)) continue;
    types.set(name, {
      extends: typeof p.extends === 'string' ? p.extends : '',
      lifecycle: p.lifecycle && typeof p.lifecycle === 'object' ? (p.lifecycle as Lifecycle) : undefined,
    });
  }
  return (type) => {
    const seen = new Set<string>();
    for (let t = type ?? ''; t && !seen.has(t); t = types.get(t)?.extends ?? '') {
      seen.add(t);
      const info = types.get(t);
      if (!info) return undefined;
      if (info.lifecycle) return info.lifecycle;
    }
    return undefined;
  };
}

export interface LifecycleRow {
  node: GraphNode;
  lifecycle: Lifecycle;
  /** stored state ('' : the node has none yet) */
  base: string;
  /** state after the change's transitions */
  effective: string;
  /** states the change moves the node through */
  moves: string[];
  editable: boolean;
  /** transitions available from the effective state */
  transitions: LifecycleTransition[];
}

const editableState = (l: Lifecycle, s: string) => !!l.states?.find((x) => x.name === s)?.editable;

/** Rows of the nodes a change works on (attached ones, plus `extra` ids picked by the user). */
export function lifecycleRows(
  nodes: GraphNode[],
  attached: NodeRef[],
  items: ChangeItem[],
  extra: string[],
): LifecycleRow[] {
  const resolve = lifecycleResolver(nodes);
  const byId = new Map(nodes.map((n) => [n.id ?? '', n]));
  const ids = [...new Set([...attached.map((r) => r.id ?? ''), ...extra])].filter((id) => byId.has(id));
  const rows: LifecycleRow[] = [];
  for (const id of ids) {
    const node = byId.get(id)!;
    const lifecycle = resolve(node.type);
    if (!lifecycle) continue;
    const base = node.state ?? '';
    let cur = base || lifecycle.initial || '';
    const moves: string[] = [];
    for (const it of items) {
      const p = it.proposal;
      if (p?.op !== 'transition_node' || p.node?.base?.id !== id || it.status === 'superseded' || it.status === 'rejected') continue;
      if (p.node?.state) {
        cur = p.node.state;
        moves.push(cur);
      }
    }
    rows.push({
      node,
      lifecycle,
      base,
      effective: cur,
      moves,
      editable: editableState(lifecycle, cur),
      transitions: (lifecycle.transitions ?? []).filter((t) => t.from === cur),
    });
  }
  return rows.sort((a, b) => (a.node.key ?? '').localeCompare(b.node.key ?? ''));
}

/** Is this transition a "reopen": from a state that is not editable into one that is? */
export function isReopen(row: LifecycleRow, t: LifecycleTransition): boolean {
  return !row.editable && editableState(row.lifecycle, t.to ?? '');
}

/** Nodes of the baseline that have a lifecycle and are not in the rows yet. */
export function reopenable(nodes: GraphNode[], rows: LifecycleRow[]): GraphNode[] {
  const resolve = lifecycleResolver(nodes);
  const have = new Set(rows.map((r) => r.node.id));
  return nodes.filter((n) => n.type !== 'NodeType' && !n.deleted && !have.has(n.id) && resolve(n.type));
}
