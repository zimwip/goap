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
  /** undefined: the node type has no lifecycle (always editable through a change) */
  lifecycle?: Lifecycle;
  /** stored state ('' : the node has none yet) */
  base: string;
  /** state after the change's transitions */
  effective: string;
  /** states the change moves the node through */
  moves: string[];
  editable: boolean;
  /** transitions available from the effective state */
  transitions: LifecycleTransition[];
  /** properties after the change's update proposals */
  props: Record<string, unknown>;
  /** number of update proposals of the change on this node */
  edits: number;
  /** properties declared by the node type (inherited ones included) */
  declared: string[];
  /** the create_node item, for a node the change creates (not stored yet) */
  created?: ChangeItem;
  /** the delete_node proposal standing on this node */
  removal?: ChangeItem;
}

/** Ids of the items a decision rejects (the latest decision on an item wins). */
export function rejectedIds(items: ChangeItem[]): Set<string> {
  const out = new Set<string>();
  for (const it of items) {
    const d = it.decision;
    if (it.kind !== 'decision' || !d?.item) continue;
    if (d.accept) out.delete(d.item);
    else out.add(d.item);
  }
  return out;
}

/** Ids of the items another item replaces. */
export function supersededIds(items: ChangeItem[]): Set<string> {
  return new Set(items.flatMap((i) => i.supersedes ?? []));
}

/** Names of the node types of a baseline (its NodeType nodes). */
export function nodeTypeNames(nodes: GraphNode[]): string[] {
  const names = nodes
    .filter((n) => n.type === 'NodeType')
    .map((n) => (n.props as Record<string, unknown> | undefined)?.name)
    .filter((x): x is string => typeof x === 'string' && !!x);
  return [...new Set(names)].sort();
}

/** The NodeType node of a type name (target of the instanceOf link of its instances). */
export function typeNodeRef(nodes: GraphNode[], name: string): NodeRef | undefined {
  const n = nodes.find((x) => x.type === 'NodeType' && (x.props as Record<string, unknown> | undefined)?.name === name);
  return n?.id ? { id: n.id, version: n.version } : undefined;
}

/** States a new node of the type can be born in: the initial one, or one a transition leads to from it. */
export function birthStates(lifecycle: Lifecycle | undefined): string[] {
  if (!lifecycle) return [];
  const init = lifecycle.initial ?? '';
  return [init, ...(lifecycle.transitions ?? []).filter((t) => t.from === init).map((t) => t.to ?? '')].filter(
    (s, i, all) => s && all.indexOf(s) === i,
  );
}

/** Rows of the nodes the change creates (create_node proposals still standing). */
export function createdRows(nodes: GraphNode[], items: ChangeItem[]): LifecycleRow[] {
  const resolve = lifecycleResolver(nodes);
  const gone = supersededIds(items);
  const rejected = rejectedIds(items);
  const rows: LifecycleRow[] = [];
  for (const it of items) {
    const p = it.proposal;
    if (p?.op !== 'create_node' || !it.id || gone.has(it.id) || rejected.has(it.id) || it.status === 'rejected') continue;
    const lifecycle = resolve(p.node?.type);
    const state = lifecycle ? p.node?.state || lifecycle.initial || '' : '';
    rows.push({
      node: { id: it.id, key: p.node?.key ?? '', type: p.node?.type ?? '', version: 0, state },
      lifecycle,
      base: '',
      effective: state,
      moves: [],
      editable: lifecycle ? editableState(lifecycle, state) : true,
      transitions: [],
      props: { ...((p.node?.props ?? {}) as Record<string, unknown>) },
      edits: 0,
      declared: declaredProperties(nodes, p.node?.type),
      created: it,
    });
  }
  return rows;
}

const editableState = (l: Lifecycle, s: string) => !!l.states?.find((x) => x.name === s)?.editable;

/** Properties declared by a node type, its ancestors' first. */
export function declaredProperties(nodes: GraphNode[], type: string | undefined): string[] {
  const types = new Map<string, { extends: string; properties: string[] }>();
  for (const n of nodes) {
    if (n.type !== 'NodeType') continue;
    const p = (n.props ?? {}) as Record<string, unknown>;
    const name = typeof p.name === 'string' ? p.name : '';
    if (!name || types.has(name)) continue;
    types.set(name, {
      extends: typeof p.extends === 'string' ? p.extends : '',
      properties: Array.isArray(p.properties) ? p.properties.filter((x): x is string => typeof x === 'string') : [],
    });
  }
  const chain: string[][] = [];
  const seen = new Set<string>();
  for (let t = type ?? ''; t && !seen.has(t); t = types.get(t)?.extends ?? '') {
    seen.add(t);
    chain.unshift(types.get(t)?.properties ?? []);
  }
  return [...new Set(chain.flat())];
}

/** Rows of the nodes a change works on (attached ones, plus `extra` ids picked by the user). */
export function lifecycleRows(
  nodes: GraphNode[],
  attached: NodeRef[],
  items: ChangeItem[],
  extra: string[],
): LifecycleRow[] {
  const resolve = lifecycleResolver(nodes);
  const byId = new Map(nodes.map((n) => [n.id ?? '', n]));
  const gone = supersededIds(items);
  const rejected = rejectedIds(items);
  const ids = [...new Set([...attached.map((r) => r.id ?? ''), ...extra])].filter((id) => byId.has(id));
  const rows: LifecycleRow[] = [];
  for (const id of ids) {
    const node = byId.get(id)!;
    const lifecycle = resolve(node.type);
    const base = node.state ?? '';
    let cur = lifecycle ? base || lifecycle.initial || '' : '';
    const moves: string[] = [];
    const props: Record<string, unknown> = { ...((node.props ?? {}) as Record<string, unknown>) };
    let edits = 0;
    let removal: ChangeItem | undefined;
    for (const it of items) {
      const p = it.proposal;
      if (!p || p.node?.base?.id !== id || (it.id && (gone.has(it.id) || rejected.has(it.id))) || it.status === 'superseded' || it.status === 'rejected') continue;
      if (p.op === 'delete_node') {
        removal = it;
      } else if (p.op === 'transition_node' && p.node?.state) {
        cur = p.node.state;
        moves.push(cur);
      } else if (p.op === 'update_node') {
        Object.assign(props, (p.node?.props ?? {}) as Record<string, unknown>);
        edits++;
      }
    }
    rows.push({
      node,
      lifecycle,
      base,
      effective: cur,
      moves,
      editable: lifecycle ? editableState(lifecycle, cur) : true,
      transitions: lifecycle ? (lifecycle.transitions ?? []).filter((t) => t.from === cur) : [],
      props,
      edits,
      declared: declaredProperties(nodes, node.type),
      removal,
    });
  }
  rows.sort((a, b) => (a.node.key ?? '').localeCompare(b.node.key ?? ''));
  return [...rows, ...createdRows(nodes, items)];
}

/** Is this transition a "reopen": from a state that is not editable into one that is? */
export function isReopen(row: LifecycleRow, t: LifecycleTransition): boolean {
  return !!row.lifecycle && !row.editable && editableState(row.lifecycle, t.to ?? '');
}

/** Nodes of the baseline the change could take on (not the metadata layer, nor deleted ones). */
export function reopenable(nodes: GraphNode[], rows: LifecycleRow[]): GraphNode[] {
  const have = new Set(rows.map((r) => r.node.id));
  return nodes.filter((n) => n.type !== 'NodeType' && !n.deleted && !have.has(n.id) && !/^[MD]:/.test(n.key ?? ''));
}
