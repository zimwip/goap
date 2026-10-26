// The diff of one change impact (the Diff button of the Impacts pane): the node as the change holds it (its draft, or
// the version its commit wrote) against the version it was checked out from (ADR 0079), field by field: state, owner,
// properties, and outgoing links (a link is its type and its target node; a draft's links point at exact versions or
// at drafts, so the target's version is not compared). A node the change creates has no base: everything is added.
import { graph, type ChangeImpact, type FieldChange, type GraphNode, type JsonValue, type Link } from './api';

/** One side of the diff: a node and its outgoing links. */
export interface NodeSide {
  node?: GraphNode;
  out?: Link[];
}

/** A value compared by content: objects with their keys sorted. */
function canonical(v: unknown): string {
  if (v === undefined) return 'undefined';
  return JSON.stringify(v, (_k, x) =>
    x && typeof x === 'object' && !Array.isArray(x) ? Object.fromEntries(Object.entries(x as Record<string, unknown>).sort(([a], [b]) => a.localeCompare(b))) : x,
  );
}

const same = (a: unknown, b: unknown) => canonical(a) === canonical(b);
const json = (v: unknown) => v as JsonValue | undefined;
const linkKey = (l: Link) => `${l.type ?? ''}\u0000${l.to?.id ?? ''}`;

/** What the change modifies of the node, from `before` (undefined: the change creates it) to `after`, in a stable
 * order: state, owner, properties by name, links by type then target. `targetKey` names a link's target node. */
export function nodeChanges(before: NodeSide | undefined, after: NodeSide | undefined, targetKey: (id: string) => string = (id) => id): FieldChange[] {
  const out: FieldChange[] = [];
  const b = before?.node;
  const a = after?.node;
  const scalar = (kind: 'state' | 'owner', old?: string, now?: string) => {
    if ((old ?? '') === (now ?? '')) return;
    out.push({ kind, op: !old ? 'added' : !now ? 'removed' : 'changed', old: old || undefined, new: now || undefined });
  };
  scalar('state', b?.state, a?.state);
  // a created node's owner is the change's unit: not a modification worth a line
  if (b) scalar('owner', b.responsible, a?.responsible);

  const bp = (b?.props ?? {}) as Record<string, unknown>;
  const ap = (a?.props ?? {}) as Record<string, unknown>;
  for (const name of [...new Set([...Object.keys(bp), ...Object.keys(ap)])].sort()) {
    const has = (p: Record<string, unknown>) => Object.prototype.hasOwnProperty.call(p, name) && p[name] !== null && p[name] !== undefined;
    if (!has(bp) && has(ap)) out.push({ kind: 'property', name, op: 'added', new: json(ap[name]) });
    else if (has(bp) && !has(ap)) out.push({ kind: 'property', name, op: 'removed', old: json(bp[name]) });
    else if (has(bp) && !same(bp[name], ap[name])) out.push({ kind: 'property', name, op: 'changed', old: json(bp[name]), new: json(ap[name]) });
  }

  const bl = new Map((before?.out ?? []).map((l) => [linkKey(l), l]));
  const al = new Map((after?.out ?? []).map((l) => [linkKey(l), l]));
  const links: FieldChange[] = [];
  for (const k of new Set([...bl.keys(), ...al.keys()])) {
    const o = bl.get(k);
    const n = al.get(k);
    const l = (n ?? o)!;
    const base = { kind: 'link', name: l.type ?? '', target: targetKey(l.to?.id ?? '') };
    if (!o) links.push({ ...base, op: 'added' });
    else if (!n) links.push({ ...base, op: 'removed' });
    else if (!same(o.props ?? {}, n.props ?? {})) links.push({ ...base, op: 'changed', old: json(o.props ?? {}), new: json(n.props ?? {}) });
  }
  links.sort((x, y) => (x.name ?? '').localeCompare(y.name ?? '') || (x.target ?? '').localeCompare(y.target ?? ''));
  return [...out, ...links];
}

/** The diff of an impact, as the dialog shows it. */
export interface ImpactDiffView {
  /** the version the change started from (undefined: the change creates the node) */
  base?: GraphNode;
  /** what the change holds: its draft, or the version its commit wrote (undefined: planned, no draft yet) */
  after?: GraphNode;
  changes: FieldChange[];
}

/** Loads both sides of an impact and compares them. `flow`: the flow the impact is seen from ('main' or an option). */
export async function loadImpactDiff(changeId: string, impact: ChangeImpact, flow = '', signal?: AbortSignal): Promise<ImpactDiffView> {
  const scope = { changeId, flow };
  const pre = impact.pre?.id ? impact.pre : undefined;
  const post = impact.committed?.id ? impact.committed : impact.post?.id ? impact.post : undefined;
  const [before, after] = await Promise.all([
    pre ? graph.getNodeWithLinks(pre, signal).then((r) => r.view) : undefined,
    // a draft is read through the change (ADR 0079); a committed version is read as it is
    post ? graph.getNodeWithLinks(post, signal, post.version ? undefined : scope).then((r) => r.view) : undefined,
  ]);
  if (!after) return { base: before?.node, changes: [] };
  const raw = nodeChanges(before, after);
  // name the targets of the links that changed: a node of the change is read through it
  const ids = [...new Set(raw.filter((c) => c.kind === 'link').map((c) => c.target ?? ''))].filter((id) => id);
  const keys = new Map<string, string>();
  await Promise.all(
    ids.map(async (id) => {
      try {
        const n = (await graph.getNode({ id, version: 0 }, signal, scope)).view?.node;
        if (n?.key) keys.set(id, n.key);
      } catch {
        // an unreadable target keeps its id
      }
    }),
  );
  return { base: before?.node, after: after.node, changes: nodeChanges(before, after, (id) => keys.get(id) ?? id) };
}
